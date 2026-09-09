package usecase_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

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

	browser, _ := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(t.TempDir() + "/state.db")
	defer store.Close()
	client, _ := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})

	handler := usecase.NewHandler(usecase.NewNavigationService(browser, store), store, &fakeRegistry{client: client}, &fakeServer{started: true}, browser)
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
