package usecase

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// fakeLocator is a minimal SessionLocator used to exercise the
// directory resolution without touching the real agent stores.
type fakeLocator struct {
	kind      domain.AgentKind
	active    domain.ActiveSession
	err       error
	locateCtx context.Context
}

func (f *fakeLocator) Kind() domain.AgentKind { return f.kind }

func (f *fakeLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	f.locateCtx = ctx
	return f.active, f.err
}

func TestEnsureAgentStartedResolvesSessionDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &localFakeServer{}
	handler := &Handler{manager: manager, workspaceRoot: root}

	state := domain.RuntimeState{WorkspaceRoot: root, RelativePath: "proj"}
	if err := handler.ensureAgentStarted(context.Background(), domain.AgentOpenCode, state); err != nil {
		t.Fatalf("ensureAgentStarted err=%v", err)
	}
	if !manager.started || manager.cwd != project {
		t.Fatalf("started=%v cwd=%q, want started in %q", manager.started, manager.cwd, project)
	}
}

func TestEnsureAgentStartedFallsBackToWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	manager := &localFakeServer{}
	handler := &Handler{manager: manager, workspaceRoot: root}

	state := domain.RuntimeState{WorkspaceRoot: root, RelativePath: "gone"}
	if err := handler.ensureAgentStarted(context.Background(), domain.AgentKiro, state); err != nil {
		t.Fatalf("ensureAgentStarted err=%v", err)
	}
	if manager.cwd != root {
		t.Fatalf("cwd=%q, want the workspace root %q", manager.cwd, root)
	}
}

func TestEnsureAgentStartedSkipsAlreadyRunningAgent(t *testing.T) {
	manager := &localFakeServer{started: true, cwd: "already"}
	handler := &Handler{manager: manager, workspaceRoot: "/tmp"}

	if err := handler.ensureAgentStarted(context.Background(), domain.AgentKiro, domain.RuntimeState{}); err != nil {
		t.Fatalf("ensureAgentStarted err=%v", err)
	}
	if manager.cwd != "already" {
		t.Fatalf("cwd=%q, want the manager to keep its original directory", manager.cwd)
	}
}

func TestDirectoryForSessionUsesLocator(t *testing.T) {
	registry := NewActiveLocatorRegistry()
	registry.Add(&fakeLocator{kind: domain.AgentKiro, active: domain.ActiveSession{
		Kind:      domain.AgentKiro,
		SessionID: "sess_1",
		Directory: "/Users/me/proj",
	}})
	handler := &Handler{locators: registry}

	if got := handler.directoryForSession(context.Background(), domain.AgentKiro, "sess_1"); got != "/Users/me/proj" {
		t.Fatalf("directoryForSession = %q, want the located directory", got)
	}
	if got := handler.directoryForSession(context.Background(), domain.AgentKiro, "other"); got != "" {
		t.Fatalf("directoryForSession(mismatch) = %q, want empty", got)
	}
}
