package usecase_test

import (
	"context"
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

// stubLocator returns a fixed active session for the /continue
// directory-resolution test.
type stubLocator struct {
	kind   domain.AgentKind
	active domain.ActiveSession
}

func (s stubLocator) Kind() domain.AgentKind { return s.kind }

func (s stubLocator) Locate(context.Context) (domain.ActiveSession, error) { return s.active, nil }

// TestHandlerContinueAndDiffCallbacks makes sure the quick-tap buttons on
// the asynchronous "task done" notification resolve to /continue and
// /diff without the user having to type the command.
func TestHandlerContinueAndDiffCallbacks(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
		case strings.HasSuffix(r.URL.Path, "/diff") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"path":"README.md","status":"modified"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, _ := sqlite.Open(t.TempDir() + "/state.db")
	defer store.Close()
	client, _ := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})

	handler := usecase.NewHandler(store, &fakeRegistry{client: client}, &fakeServer{started: true}, root)
	handler.SetSessionEventLog(store)

	const chatID = 42
	if err := store.SaveCompletedSession(context.Background(), domain.CompletedSession{
		ChatID:      chatID,
		SessionID:   "ses1",
		ProjectID:   "p1",
		ProjectName: "demo",
		Directory:   root,
		Preview:     "listo",
		CompletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// "cd|<chatID>" must surface the diff without requiring the user to
	// have tapped "Continuar" first.
	resp, err := handler.HandleCallback(context.Background(), chatID, "cd|42")
	if err != nil {
		t.Fatalf("cd callback: %v", err)
	}
	if !strings.Contains(resp.Text, "README.md") {
		t.Fatalf("cd response = %q", resp.Text)
	}

	// "co|<chatID>" must reactivate the completed session.
	resp, err = handler.HandleCallback(context.Background(), chatID, "co|42")
	if err != nil {
		t.Fatalf("co callback: %v", err)
	}
	if !strings.Contains(resp.Text, "ses1") || !strings.Contains(resp.Text, "demo") {
		t.Fatalf("co response = %q", resp.Text)
	}
	state, err := store.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "ses1" {
		t.Fatalf("SessionID after /continue = %q, want ses1", state.SessionID)
	}
}

// TestContinueResolvesDirectoryFromLocator covers the case where the
// stored completion snapshot has no directory because the agent's server
// was stopped when the watcher recorded it (Kiro/opencode). /continue
// must fall back to the locator's on-disk session directory so the agent
// restarts in the right project.
func TestContinueResolvesDirectoryFromLocator(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := agents_opencode.NewClient("http://127.0.0.1:1", &http.Client{Timeout: time.Millisecond})

	handler := usecase.NewHandler(store, &fakeRegistry{client: client}, &fakeServer{started: true}, root)
	handler.SetSessionEventLog(store)
	locators := usecase.NewActiveLocatorRegistry()
	locators.Add(stubLocator{kind: domain.AgentKiro, active: domain.ActiveSession{
		Kind:      domain.AgentKiro,
		SessionID: "sess_1",
		Directory: project,
	}})
	handler.SetActiveLocators(locators)

	const chatID = 42
	if err := store.SaveCompletedSession(context.Background(), domain.CompletedSession{
		ChatID:      chatID,
		SessionID:   "sess_1",
		AgentKind:   domain.AgentKiro,
		Preview:     "listo",
		CompletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// The first activation of a session carries the local-UI notice; the
	// second one must not repeat it.
	resp, err := handler.HandleCommand(context.Background(), chatID, "/continue", nil)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if !strings.Contains(resp.Text, "El IDE de Kiro") {
		t.Fatalf("continue reply = %q, want the Kiro local-UI notice", resp.Text)
	}
	state, err := store.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.RelativePath != "proj" || state.AgentKind != domain.AgentKiro {
		t.Fatalf("state = %+v, want RelativePath=proj AgentKind=kiro", state)
	}

	resp, err = handler.HandleCommand(context.Background(), chatID, "/continue", nil)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if strings.Contains(resp.Text, "El IDE de Kiro") {
		t.Fatalf("second continue reply = %q, want no repeated notice", resp.Text)
	}
}
