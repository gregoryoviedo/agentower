package usecase_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// fakeServer satisfies domain.AgentServerManager without spawning anything.
type fakeServer struct {
	started bool
	cwd     string
}

func (f *fakeServer) Start(_ context.Context, _ domain.AgentKind, workingDir string) error {
	f.started = true
	f.cwd = workingDir
	return nil
}
func (f *fakeServer) Stop(_ domain.AgentKind) { f.started = false; f.cwd = "" }
func (f *fakeServer) StopAll()                { f.started = false; f.cwd = "" }
func (f *fakeServer) StartedSubprocess(_ domain.AgentKind) bool {
	return f.started
}
func (f *fakeServer) OwnsSubprocess(_ domain.AgentKind) bool { return f.started }
func (f *fakeServer) WorkingDir(_ domain.AgentKind) string  { return f.cwd }

// fakeRegistry wraps an opencode client as the single Available agent.
type fakeRegistry struct{ client domain.AgentAdapter }

func (r *fakeRegistry) Descriptors() []domain.AgentDescriptor {
	return []domain.AgentDescriptor{{
		Kind: domain.AgentOpenCode, DisplayName: "opencode", Bin: "opencode",
		Detected: true, Available: true, Port: 4096,
		Capabilities: domain.AgentCapabilities{
			Health: true, ListProjects: true, ListSessions: true, CreateSession: true,
			SendPrompt: true, Revert: true, FileStatus: true, ListMessages: true,
		},
	}}
}
func (r *fakeRegistry) Available() []domain.AgentDescriptor { return r.Descriptors() }
func (r *fakeRegistry) DescriptorFor(k domain.AgentKind) (domain.AgentDescriptor, bool) {
	for _, d := range r.Descriptors() {
		if d.Kind == k {
			return d, true
		}
	}
	return domain.AgentDescriptor{}, false
}
func (r *fakeRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != domain.AgentOpenCode {
		return nil, domain.ErrAgentUnavailable
	}
	return r.client, nil
}
func (r *fakeRegistry) Active(_ int64) (domain.AgentKind, error) { return domain.AgentOpenCode, nil }
func (r *fakeRegistry) SetActive(_ int64, _ domain.AgentKind) error {
	return nil
}

func TestEndToEndSelectsProjectThenPrompt(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"work", "work/work1", "work/work2", "personal"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
		case r.URL.Path == "/project":
			fmt.Fprint(w, `[{"id":"work","worktree":"`+root+`/work","time":{"updated":1700000000000}}]`)
		case r.URL.Path == "/session" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/message") && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"info":{"id":"m1","parts":[{"type":"text","text":"ok"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer opencodeServer.Close()

	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	opencodeClient, err := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeServer{started: true}
	navigation := usecase.NewNavigationService(browser, store)
	handler := usecase.NewHandler(navigation, store, &fakeRegistry{client: opencodeClient}, fake, browser)
	ctx := context.Background()

	state, entries, err := navigation.Start(ctx, 42)
	if err != nil || len(entries) != 2 {
		t.Fatalf("navigation start failed: entries=%#v err=%v", entries, err)
	}
	state, entries, err = navigation.Enter(ctx, state.ID, 42, "work")
	if err != nil || len(entries) != 2 {
		t.Fatalf("enter work: entries=%#v err=%v", entries, err)
	}
	project, err := navigation.Select(ctx, state.ID, 42, "work/work1")
	if err != nil || project.RelativePath != "work/work1" {
		t.Fatalf("select project: project=%#v err=%v", project, err)
	}

	runtime, err := store.LoadRuntimeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ProjectID = "work/work1"
	runtime.RelativePath = project.RelativePath
	runtime.WorkspaceRoot = project.WorkspaceRoot
	runtime.SessionID = "ses_does_not_matter"
	if err := store.SaveRuntimeState(ctx, runtime); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleText(ctx, 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if resp.Text == "" {
		t.Fatal("HandleText returned empty response")
	}
}

func TestHandlerIgnoresForeignChat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
	}))
	defer opencodeServer.Close()

	browser, _ := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})

	navigation := usecase.NewNavigationService(browser, store)
	handler := usecase.NewHandler(navigation, store, &fakeRegistry{client: client}, &fakeServer{}, browser)
	ctx := context.Background()

	state, _, err := navigation.Start(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := navigation.Enter(ctx, state.ID, 99, "work"); err == nil {
		t.Fatal("Enter accepted foreign chat")
	}

	resp, err := handler.HandleCommand(ctx, 99, "/status", nil)
	if err != nil {
		t.Fatalf("status handler: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("status returned empty body")
	}

	runtime := domain.RuntimeState{WorkspaceRoot: root}
	_ = store.SaveRuntimeState(ctx, runtime)
}
