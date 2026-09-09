package usecase

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// recordingNotifier counts how many times NotifyTyping was called.
type recordingNotifier struct {
	count atomic.Int32
}

func (r *recordingNotifier) NotifyTyping(_ context.Context, _ int64) error {
	r.count.Add(1)
	return nil
}

func (r *recordingNotifier) SendMessage(_ context.Context, _ int64, _ string) error {
	return nil
}

func (r *recordingNotifier) SendResponse(_ context.Context, _ int64, _ domain.BotResponse) error {
	return nil
}

// localFakeServer is a minimal OpenCodeServerManager used by tests inside
// the usecase package (bot_handler_e2e_test.go defines its own copy inside
// the _test external package and cannot be shared).
type localFakeServer struct {
	started bool
	cwd     string
}

func (f *localFakeServer) Start(_ context.Context, workingDir string) error {
	f.started = true
	f.cwd = workingDir
	return nil
}
func (f *localFakeServer) Stop()                   { f.started = false; f.cwd = "" }
func (f *localFakeServer) StartedSubprocess() bool { return f.started }
func (f *localFakeServer) WorkingDir() string      { return f.cwd }

func TestHandleTextEmitsTypingWhilePromptRuns(t *testing.T) {
	root := t.TempDir()

	var promptDelay time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
		case r.URL.Path == "/session/s1/message" && r.Method == http.MethodPost:
			if promptDelay > 0 {
				time.Sleep(promptDelay)
			}
			fmt.Fprint(w, `{"info":{"id":"m1","role":"assistant"},"parts":[{"type":"text","text":"done"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	browser, err := NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(statePathForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	notifier := &recordingNotifier{}
	handler := NewHandler(NewNavigationService(browser, store), store, client, &localFakeServer{started: true}, browser)
	handler.SetNotifier(notifier)

	if err := store.SaveRuntimeState(context.Background(), domain.RuntimeState{WorkspaceRoot: root, SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}

	// Force the prompt to take long enough for the ticker to refresh at
	// least once (the indicator refresh interval is 4s, so we sleep 5s).
	promptDelay = 5 * time.Second

	start := time.Now()
	resp, err := handler.HandleText(context.Background(), 42, "hola")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if !strings.Contains(resp.Text, "done") {
		t.Fatalf("response text = %q, want it to contain %q", resp.Text, "done")
	}
	if elapsed < promptDelay {
		t.Fatalf("HandleText returned in %s; expected it to wait for the prompt (%s)", elapsed, promptDelay)
	}
	got := notifier.count.Load()
	if got < 2 {
		t.Fatalf("expected at least 2 typing notifications (initial + refresh), got %d", got)
	}
}

func TestHandleTextWithoutNotifierIsSafe(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/global/health" {
			fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
			return
		}
		if r.URL.Path == "/session/s1/message" {
			fmt.Fprint(w, `{"info":{"id":"m1","role":"assistant"},"parts":[{"type":"text","text":"ok"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	browser, _ := NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	store, _ := sqlite.Open(statePathForTest(t))
	defer store.Close()
	client, _ := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})

	handler := NewHandler(NewNavigationService(browser, store), store, client, &localFakeServer{started: true}, browser)
	// Intentionally do NOT call SetNotifier — the handler must remain
	// safe to use without a typing notifier wired up.
	_ = store.SaveRuntimeState(context.Background(), domain.RuntimeState{WorkspaceRoot: root, SessionID: "s1"})

	resp, err := handler.HandleText(context.Background(), 42, "hola")
	if err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("response = %q", resp.Text)
	}
}

func statePathForTest(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/state.db"
}
