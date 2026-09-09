package usecase_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// TestAgentCommandShowsPickerWithoutActiveAgent verifies /agent renders
// the picker even when the chat has never picked one.
func TestAgentCommandShowsPickerWithoutActiveAgent(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
	}))
	defer server.Close()

	browser, _ := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(t.TempDir() + "/state.db")
	defer store.Close()
	client, _ := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})

	handler := usecase.NewHandler(usecase.NewNavigationService(browser, store), store,
		&fakeRegistry{client: client}, &fakeServer{started: true}, browser)

	resp, err := handler.HandleCommand(context.Background(), 42, "/agent", nil)
	if err != nil {
		t.Fatalf("/agent: %v", err)
	}
	if len(resp.Buttons) == 0 {
		t.Fatal("picker should have buttons")
	}
	foundContinue := false
	for _, row := range resp.Buttons {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Continuar con") {
				foundContinue = true
			}
		}
	}
	if !foundContinue {
		t.Fatalf("expected a 'Continuar con' button, got %#v", resp.Buttons)
	}
}

// TestAgentsCommandShowsAllKnownAgents verifies /agents lists every
// descriptor, available or not, and surfaces the "próximamente" hint
// for the ones whose adapter hasn't shipped.
func TestAgentsCommandShowsAllKnownAgents(t *testing.T) {
	root := t.TempDir()
	browser, _ := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(t.TempDir() + "/state.db")
	defer store.Close()

	reg := &multiRegistry{}
	handler := usecase.NewHandler(usecase.NewNavigationService(browser, store), store, reg, &fakeServer{}, browser)

	resp, err := handler.HandleCommand(context.Background(), 42, "/agents", nil)
	if err != nil {
		t.Fatalf("/agents: %v", err)
	}
	for _, name := range []string{"opencode", "Claude", "Codex", "Kiro", "GitHub Copilot"} {
		if !strings.Contains(resp.Text, name) {
			t.Errorf("/agents output should mention %s, got %q", name, resp.Text)
		}
	}
	if !strings.Contains(resp.Text, "próximamente") {
		t.Errorf("/agents should label not-yet-available adapters as próximamente, got %q", resp.Text)
	}
}

// TestAgentsMigrateReportsNothingToDoWhenColumnPopulated verifies that
// /agents migrate with no legacy rows reports a clean answer rather
// than asking the user to confirm an empty migration.
func TestAgentsMigrateReportsNothingToDoWhenColumnPopulated(t *testing.T) {
	root := t.TempDir()
	browser, _ := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(t.TempDir() + "/state.db")
	defer store.Close()

	handler := usecase.NewHandler(usecase.NewNavigationService(browser, store), store,
		&fakeRegistry{}, &fakeServer{}, browser)

	resp, err := handler.HandleCommand(context.Background(), 42, "/agents", []string{"migrate"})
	if err != nil {
		t.Fatalf("/agents migrate: %v", err)
	}
	if !strings.Contains(resp.Text, "No hay sesiones que migrar") {
		t.Errorf("expected 'nothing to migrate' message, got %q", resp.Text)
	}
}

// multiRegistry exposes all 5 known agents but marks only opencode as
// Available, mirroring PR-1's posture.
type multiRegistry struct{}

func (r *multiRegistry) Descriptors() []domain.AgentDescriptor {
	return []domain.AgentDescriptor{
		{Kind: domain.AgentOpenCode, DisplayName: "opencode", Detected: true, Available: true},
		{Kind: domain.AgentClaude, DisplayName: "Claude", Detected: true, Available: false, Reason: "Próximamente"},
		{Kind: domain.AgentCodex, DisplayName: "Codex", Detected: true, Available: false, Reason: "Próximamente"},
		{Kind: domain.AgentKiro, DisplayName: "Kiro", Detected: true, Available: false, Reason: "Próximamente"},
		{Kind: domain.AgentCopilot, DisplayName: "GitHub Copilot", Detected: true, Available: false, Reason: "Próximamente"},
	}
}
func (r *multiRegistry) Available() []domain.AgentDescriptor {
	out := []domain.AgentDescriptor{}
	for _, d := range r.Descriptors() {
		if d.Available {
			out = append(out, d)
		}
	}
	return out
}
func (r *multiRegistry) DescriptorFor(k domain.AgentKind) (domain.AgentDescriptor, bool) {
	for _, d := range r.Descriptors() {
		if d.Kind == k {
			return d, true
		}
	}
	return domain.AgentDescriptor{}, false
}
func (r *multiRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != domain.AgentOpenCode {
		return nil, domain.ErrAgentUnavailable
	}
	return stub{}, nil
}
func (r *multiRegistry) Active(_ int64) (domain.AgentKind, error) { return domain.AgentOpenCode, nil }
func (r *multiRegistry) SetActive(_ int64, _ domain.AgentKind) error {
	return nil
}

type stub struct{}

func (stub) Kind() domain.AgentKind { return domain.AgentOpenCode }
func (stub) DisplayName() string    { return "opencode" }
func (stub) Health(context.Context) (domain.HealthStatus, error) {
	return domain.HealthStatus{Healthy: true}, nil
}
func (stub) ListProjects(context.Context) ([]domain.Project, error) { return nil, nil }
func (stub) ListSessions(context.Context) ([]domain.Session, error) { return nil, nil }
func (stub) CreateSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (stub) SendPrompt(context.Context, string, string) (string, error) {
	return "", nil
}
func (stub) Revert(context.Context, string) error { return nil }
func (stub) FileStatus(context.Context, string) ([]domain.FileChange, error) {
	return nil, nil
}
func (stub) ListMessages(context.Context, string) ([]domain.Message, error) {
	return nil, nil
}
