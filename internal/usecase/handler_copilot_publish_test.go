package usecase_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// capturingPublisher records every PublishCompletion call so the
// handler test for non-streaming agents (Copilot) can assert the
// snapshot the handler publishes after SendPrompt returns.
type capturingPublisher struct {
	count atomic.Int32
	last  atomic.Value // domain.CompletedSession
}

func (c *capturingPublisher) PublishCompletion(snap domain.CompletedSession) {
	c.count.Add(1)
	c.last.Store(snap)
}

func (c *capturingPublisher) Last() (domain.CompletedSession, bool) {
	raw := c.last.Load()
	if raw == nil {
		return domain.CompletedSession{}, false
	}
	snap, ok := raw.(domain.CompletedSession)
	return snap, ok
}

// copilotAdapter is a minimal AgentAdapter used by the handler test
// to verify that the handler publishes a CompletedSession when the
// non-streaming agent's SendPrompt returns.
type copilotAdapter struct {
	reply  string
	err    error
	called atomic.Int32
}

func (c *copilotAdapter) Kind() domain.AgentKind { return domain.AgentCopilot }
func (c *copilotAdapter) DisplayName() string    { return "GitHub Copilot" }
func (c *copilotAdapter) Health(context.Context) (domain.HealthStatus, error) {
	return domain.HealthStatus{Healthy: true}, nil
}
func (c *copilotAdapter) ListProjects(context.Context) ([]domain.Project, error) {
	return nil, nil
}
func (c *copilotAdapter) ListSessions(context.Context) ([]domain.Session, error) {
	return nil, nil
}
func (c *copilotAdapter) CreateSession(context.Context, string) (domain.Session, error) {
	return domain.Session{ID: "copilot_ses"}, nil
}
func (c *copilotAdapter) SendPrompt(_ context.Context, _, _ string) (string, error) {
	c.called.Add(1)
	return c.reply, c.err
}
func (c *copilotAdapter) Revert(context.Context, string) error {
	return errors.New("not supported")
}
func (c *copilotAdapter) FileStatus(context.Context, string) ([]domain.FileChange, error) {
	return nil, nil
}
func (c *copilotAdapter) ListMessages(context.Context, string) ([]domain.Message, error) {
	return nil, nil
}

// copilotRegistry wraps a single Copilot adapter as the only
// Available kind so the handler treats HandleText as a Copilot
// flow. The OpenCode adapter in fakeRegistry is unreachable
// because the registry rejects non-Copilot kinds.
type copilotRegistry struct{ adapter domain.AgentAdapter }

func (r *copilotRegistry) Descriptors() []domain.AgentDescriptor {
	return []domain.AgentDescriptor{{Kind: domain.AgentCopilot, Available: true}}
}
func (r *copilotRegistry) Available() []domain.AgentDescriptor { return r.Descriptors() }
func (r *copilotRegistry) DescriptorFor(k domain.AgentKind) (domain.AgentDescriptor, bool) {
	if k != domain.AgentCopilot {
		return domain.AgentDescriptor{}, false
	}
	return r.Descriptors()[0], true
}
func (r *copilotRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != domain.AgentCopilot {
		return nil, domain.ErrAgentUnavailable
	}
	return r.adapter, nil
}
func (r *copilotRegistry) Active(int64) (domain.AgentKind, error)  { return domain.AgentCopilot, nil }
func (r *copilotRegistry) SetActive(int64, domain.AgentKind) error { return nil }

// nopSnapshotPublisher satisfies domain.SnapshotPublisher without
// keeping any state — the HandleText path does not depend on the
// snapshot, only on the completion publisher.
type nopSnapshotPublisher struct{}

func (n *nopSnapshotPublisher) Snapshot() domain.Snapshot       { return domain.Snapshot{} }
func (n *nopSnapshotPublisher) SetActive(int64, string, string) {}
func (n *nopSnapshotPublisher) RequestNotification(int64)       {}
func (n *nopSnapshotPublisher) CancelPending(int64)             {}

// newCopilotHandler builds a handler whose only registered agent
// is Copilot. A real workspace browser is provided so NewHandler
// can resolve Root(); the navigation service is left nil because
// the test never exercises /projects.
func newCopilotHandler(t *testing.T, adapter *copilotAdapter, cwd string, now time.Time) (*usecase.Handler, *sqlite.Repository, *capturingPublisher) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, cwd)
	if err != nil {
		t.Fatal(err)
	}

	pub := &capturingPublisher{}
	handler := usecase.NewHandler(
		nil,
		store,
		&copilotRegistry{adapter: adapter},
		&fakeServer{started: true, cwd: cwd},
		browser,
	)
	handler.SetCompletionPublisher(pub)
	handler.SetSessionEventLog(store)
	handler.SetSnapshotPublisher(&nopSnapshotPublisher{})
	handler.SetActiveLocators(usecase.NewActiveLocatorRegistry())
	handler.SetClock(func() time.Time { return now })
	return handler, store, pub
}

func TestHandlerPublishesCompletionForCopilot(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	adapter := &copilotAdapter{reply: "all done from copilot"}
	handler, store, pub := newCopilotHandler(t, adapter, workspace, now)

	if err := store.SaveRuntimeState(context.Background(), domain.RuntimeState{
		WorkspaceRoot: workspace,
		SessionID:     "copilot_ses",
		AgentKind:     domain.AgentCopilot,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := handler.HandleText(context.Background(), 42, "build me x"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if adapter.called.Load() != 1 {
		t.Fatalf("SendPrompt calls = %d, want 1", adapter.called.Load())
	}
	if got := pub.count.Load(); got != 1 {
		t.Fatalf("publisher count = %d, want 1", got)
	}
	snap, ok := pub.Last()
	if !ok {
		t.Fatal("expected publisher last to be set")
	}
	if snap.AgentKind != domain.AgentCopilot {
		t.Fatalf("AgentKind = %q, want copilot", snap.AgentKind)
	}
	if snap.SessionID != "copilot_ses" {
		t.Fatalf("SessionID = %q, want copilot_ses", snap.SessionID)
	}
	if snap.Preview != "all done from copilot" {
		t.Fatalf("Preview = %q, want %q", snap.Preview, "all done from copilot")
	}

	// The completion log must also be persisted so /continue and
	// /continuar can rehydrate the session after a restart.
	persisted, ok, err := store.LoadCompletedSession(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected persisted completion")
	}
	if persisted.AgentKind != domain.AgentCopilot {
		t.Fatalf("persisted AgentKind = %q, want copilot", persisted.AgentKind)
	}
	if persisted.Preview != "all done from copilot" {
		t.Fatalf("persisted Preview = %q, want %q", persisted.Preview, "all done from copilot")
	}
}

func TestHandlerSkipsPublishWhenCopilotReplyEmpty(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	adapter := &copilotAdapter{reply: ""}
	handler, store, pub := newCopilotHandler(t, adapter, workspace, now)
	if err := store.SaveRuntimeState(context.Background(), domain.RuntimeState{
		WorkspaceRoot: workspace, SessionID: "copilot_ses", AgentKind: domain.AgentCopilot,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.HandleText(context.Background(), 42, "build me x"); err != nil {
		t.Fatal(err)
	}
	if got := pub.count.Load(); got != 0 {
		t.Fatalf("publisher count = %d, want 0 (empty reply should not publish)", got)
	}
}
