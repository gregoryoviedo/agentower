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

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
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
func (f *fakeServer) WorkingDir(_ domain.AgentKind) string   { return f.cwd }

// fakeRegistry wraps an opencode client as the single Available agent.
type fakeRegistry struct {
	client domain.AgentAdapter
	active domain.AgentKind
}

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
func (r *fakeRegistry) Active(_ int64) (domain.AgentKind, error) {
	if r.active != "" {
		return r.active, nil
	}
	return domain.AgentOpenCode, nil
}
func (r *fakeRegistry) SetActive(_ int64, _ domain.AgentKind) error {
	return nil
}

// TestRepliesToTheFollowedSession is the companion happy path: the bot
// has a session to follow (set by /continue or a "Continuar sesión"
// tap) and a free-text message is forwarded to that session.
func TestRepliesToTheFollowedSession(t *testing.T) {
	root := t.TempDir()

	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
		case strings.HasSuffix(r.URL.Path, "/message") && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"info":{"id":"m1","role":"assistant"},"parts":[{"type":"text","text":"ok"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer opencodeServer.Close()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	opencodeClient, err := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	handler := usecase.NewHandler(store, &fakeRegistry{client: opencodeClient}, &fakeServer{started: true}, root)
	ctx := context.Background()
	if err := store.SaveRuntimeState(ctx, domain.RuntimeState{WorkspaceRoot: root, SessionID: "ses_1", AgentKind: domain.AgentOpenCode}); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleText(ctx, 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("HandleText text = %q, want it to contain the agent reply", resp.Text)
	}
}

func TestStatusReportsFollowedSession(t *testing.T) {
	root := t.TempDir()
	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
	}))
	defer opencodeServer.Close()

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})

	handler := usecase.NewHandler(store, &fakeRegistry{client: client}, &fakeServer{}, root)
	ctx := context.Background()
	if err := store.SaveRuntimeState(ctx, domain.RuntimeState{WorkspaceRoot: root, RelativePath: "work/proj", SessionID: "ses_1", AgentKind: domain.AgentOpenCode}); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleCommand(ctx, 42, "/status", nil)
	if err != nil {
		t.Fatalf("status handler: %v", err)
	}
	if !strings.Contains(resp.Text, "ses_1") || !strings.Contains(resp.Text, "work/proj") {
		t.Fatalf("status text = %q, want the followed session and project", resp.Text)
	}
}

// TestFreeTextUsesTheSessionsAgentNotTheDefault guards the companion
// routing rule: a continuation is sent to the agent that owns the
// followed session, not to whatever the registry considers the chat's
// default agent.
func TestFreeTextUsesTheSessionsAgentNotTheDefault(t *testing.T) {
	root := t.TempDir()
	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/message") && r.Method == http.MethodPost {
			fmt.Fprint(w, `{"info":{"id":"m1","role":"assistant"},"parts":[{"type":"text","text":"ok"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer opencodeServer.Close()

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})

	// The registry's default agent is Kiro, but the followed session
	// belongs to opencode; the message must go to opencode.
	handler := usecase.NewHandler(store, &fakeRegistry{client: client, active: domain.AgentKiro}, &fakeServer{}, root)
	ctx := context.Background()
	if err := store.SaveRuntimeState(ctx, domain.RuntimeState{WorkspaceRoot: root, SessionID: "ses_1", AgentKind: domain.AgentOpenCode}); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleText(ctx, 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("HandleText text = %q, want the reply from the session's agent", resp.Text)
	}
}

// TestHandleTextStartsTheAgentOnFirstPrompt guards the lazy autostart:
// the manager is never started at boot, so the first free-text message
// after reactivating a session must bring the agent up in the session's
// project directory before forwarding the prompt.
func TestHandleTextStartsTheAgentOnFirstPrompt(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/message") && r.Method == http.MethodPost {
			fmt.Fprint(w, `{"info":{"id":"m1","role":"assistant"},"parts":[{"type":"text","text":"ok"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer opencodeServer.Close()

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})

	server := &fakeServer{}
	handler := usecase.NewHandler(store, &fakeRegistry{client: client}, server, root)
	ctx := context.Background()
	if err := store.SaveRuntimeState(ctx, domain.RuntimeState{WorkspaceRoot: root, RelativePath: "proj", SessionID: "ses_1", AgentKind: domain.AgentOpenCode}); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleText(ctx, 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("HandleText text = %q, want the agent reply", resp.Text)
	}
	if !server.started || server.cwd != project {
		t.Fatalf("manager started=%v cwd=%q, want it started in %q", server.started, server.cwd, project)
	}
}

func TestHandleTextWithoutSessionPromptsToContinue(t *testing.T) {
	root := t.TempDir()
	opencodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
	}))
	defer opencodeServer.Close()

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient(opencodeServer.URL, &http.Client{Timeout: time.Second})

	handler := usecase.NewHandler(store, &fakeRegistry{client: client}, &fakeServer{}, root)
	resp, err := handler.HandleText(context.Background(), 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if !strings.Contains(resp.Text, "/continue") {
		t.Fatalf("HandleText text = %q, want it to point the user at /continue", resp.Text)
	}
}
