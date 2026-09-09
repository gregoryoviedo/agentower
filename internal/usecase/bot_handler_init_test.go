package usecase_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// recordingServer satisfies domain.AgentServerManager and remembers every
// working directory Start was invoked with.
type recordingServer struct {
	started   bool
	startedAt []string
}

func (r *recordingServer) Start(_ context.Context, _ domain.AgentKind, workingDir string) error {
	r.started = true
	r.startedAt = append(r.startedAt, workingDir)
	return nil
}
func (r *recordingServer) Stop(_ domain.AgentKind) { r.started = false }
func (r *recordingServer) StopAll()                { r.started = false }
func (r *recordingServer) StartedSubprocess(_ domain.AgentKind) bool {
	return r.started
}
func (r *recordingServer) OwnsSubprocess(_ domain.AgentKind) bool { return r.started }
func (r *recordingServer) WorkingDir(_ domain.AgentKind) string {
	if len(r.startedAt) == 0 {
		return ""
	}
	return r.startedAt[len(r.startedAt)-1]
}

// newInitFixture creates a workspace with the requested layout, a Handler
// backed by an in-memory SQLite store and a recordingServer. Returns the
// workspace root, the handler and the recorder.
func newInitFixture(t *testing.T, layout []string) (string, *usecase.Handler, *recordingServer) {
	t.Helper()
	root := t.TempDir()
	// On macOS, /tmp and /var/folders are symlinks; the WorkspaceBrowser
	// resolves the root via EvalSymlinks, so we mirror that here to keep
	// path comparisons deterministic across platforms.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	for _, dir := range layout {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &recordingServer{}
	handler := usecase.NewHandler(nil, store, nilRegistry{}, server, browser)
	return root, handler, server
}

// nilRegistry is a stub AgentRegistry used by tests that don't exercise
// the agent flow (only the server-manager wiring).
type nilRegistry struct{}

func (nilRegistry) Descriptors() []domain.AgentDescriptor { return nil }
func (nilRegistry) Available() []domain.AgentDescriptor   { return nil }
func (nilRegistry) DescriptorFor(domain.AgentKind) (domain.AgentDescriptor, bool) {
	return domain.AgentDescriptor{}, false
}
func (nilRegistry) Get(domain.AgentKind) (domain.AgentAdapter, error) {
	return nil, domain.ErrNoActiveAgent
}
func (nilRegistry) Active(int64) (domain.AgentKind, error)  { return "", nil }
func (nilRegistry) SetActive(int64, domain.AgentKind) error { return nil }

func TestInitWithRelativePathStartsServerInsideWorkspace(t *testing.T) {
	root, handler, server := newInitFixture(t, []string{"work", "work/sub"})

	resp, err := handler.HandleCommand(context.Background(), 42, "/init", []string{"work/sub"})
	if err != nil {
		t.Fatalf("/init returned err: %v", err)
	}
	if !server.started {
		t.Fatalf("/init did not start the server; response=%q", resp.Text)
	}
	want := filepath.Join(root, "work", "sub")
	if server.WorkingDir(domain.AgentOpenCode) != want {
		t.Fatalf("server cwd = %q, want %q", server.WorkingDir(domain.AgentOpenCode), want)
	}
	if !strings.Contains(resp.Text, want) {
		t.Fatalf("/init response should mention the working dir, got %q", resp.Text)
	}
}

func TestInitRejectsAbsolutePaths(t *testing.T) {
	_, handler, server := newInitFixture(t, nil)

	for _, arg := range []string{"/etc", "/tmp", "/Users/someone/else"} {
		resp, err := handler.HandleCommand(context.Background(), 42, "/init", []string{arg})
		if err != nil {
			t.Fatalf("/init %q err: %v", arg, err)
		}
		if server.started {
			t.Fatalf("/init %q should not start the server", arg)
		}
		if !strings.Contains(resp.Text, "outside") {
			t.Errorf("/init %q response should mention workspace, got %q", arg, resp.Text)
		}
	}
}

func TestInitRejectsPathEscapesViaDotDot(t *testing.T) {
	_, handler, server := newInitFixture(t, []string{"inside"})

	for _, arg := range []string{"..", "../etc", "inside/../../etc"} {
		resp, err := handler.HandleCommand(context.Background(), 42, "/init", []string{arg})
		if err != nil {
			t.Fatalf("/init %q err: %v", arg, err)
		}
		if server.started {
			t.Fatalf("/init %q must not escape the workspace; got cwd=%q", arg, server.WorkingDir(domain.AgentOpenCode))
		}
		if resp.Text == "" {
			t.Fatalf("/init %q returned empty response", arg)
		}
	}
}

func TestInitWithoutArgsUsesSavedRuntimeState(t *testing.T) {
	root, handler, server := newInitFixture(t, []string{"work/proj"})

	ctx := context.Background()
	if err := handler.StateForTest().SaveRuntimeState(ctx, domain.RuntimeState{
		WorkspaceRoot: root,
		RelativePath:  "work/proj",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := handler.HandleCommand(ctx, 42, "/init", nil); err != nil {
		t.Fatal(err)
	}
	if !server.started {
		t.Fatal("/init should have started the server")
	}
	want := filepath.Join(root, "work", "proj")
	if server.WorkingDir(domain.AgentOpenCode) != want {
		t.Fatalf("server cwd = %q, want %q", server.WorkingDir(domain.AgentOpenCode), want)
	}
}

func TestInitSavesRelativePathNotBasename(t *testing.T) {
	root, handler, _ := newInitFixture(t, []string{"work/proj"})

	ctx := context.Background()
	if _, err := handler.HandleCommand(ctx, 42, "/init", []string{"work/proj"}); err != nil {
		t.Fatal(err)
	}
	state, err := handler.StateForTest().LoadRuntimeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.RelativePath != "work/proj" {
		t.Fatalf("RelativePath = %q, want %q (full path must be preserved)", state.RelativePath, "work/proj")
	}
	if state.WorkspaceRoot == "" {
		t.Fatal("WorkspaceRoot should be populated")
	}
	if got, _ := filepath.Abs(filepath.Join(state.WorkspaceRoot, state.RelativePath)); got != filepath.Join(root, "work", "proj") {
		t.Fatalf("joined path = %q, want %q", got, filepath.Join(root, "work", "proj"))
	}
}
