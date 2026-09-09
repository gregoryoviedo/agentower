package usecase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// recordingCompletionPublisher records every PublishCompletion call so the
// watcher test can assert the bot-side flow without spinning up the HTTP
// control surface.
type recordingCompletionPublisher struct {
	count atomic.Int32
	last  atomic.Value // domain.CompletedSession
}

func (r *recordingCompletionPublisher) PublishCompletion(snap domain.CompletedSession) {
	r.count.Add(1)
	r.last.Store(snap)
}

func TestSessionWatcherMarksIdleSessionAsCompleted(t *testing.T) {
	var messageCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
		case r.URL.Path == "/session" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"ses1","projectID":"p1","title":"Main","directory":"/tmp/work"}]`))
		case r.URL.Path == "/session/ses1/message" && r.Method == http.MethodGet:
			count := messageCount.Add(1)
			// Tick 1: 2 messages, last is assistant with a text part.
			// Tick 2: 3 messages (a new user message arrives), last is
			// user — the watcher must reset stability and NOT record yet.
			// Tick 3+: 4 messages with a fresh assistant text part so the
			// heuristic finally accepts the window.
			switch count {
			case 1:
				body := `[
					{"info":{"id":"u1","role":"user"},"parts":[{"type":"text","text":"hola"}]},
					{"info":{"id":"a1","role":"assistant"},"parts":[{"type":"text","text":"hola humano"}]}
				]`
				_, _ = w.Write([]byte(body))
			case 2:
				body := `[
					{"info":{"id":"u1","role":"user"},"parts":[{"type":"text","text":"hola"}]},
					{"info":{"id":"a1","role":"assistant"},"parts":[{"type":"text","text":"hola humano"}]},
					{"info":{"id":"u2","role":"user"},"parts":[{"type":"text","text":"y ahora?"}]}
				]`
				_, _ = w.Write([]byte(body))
			default:
				body := `[
					{"info":{"id":"u1","role":"user"},"parts":[{"type":"text","text":"hola"}]},
					{"info":{"id":"a1","role":"assistant"},"parts":[{"type":"text","text":"hola humano"}]},
					{"info":{"id":"u2","role":"user"},"parts":[{"type":"text","text":"y ahora?"}]},
					{"info":{"id":"a2","role":"assistant"},"parts":[{"type":"text","text":"ahora dime tu"}]}
				]`
				_, _ = w.Write([]byte(body))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	client, err := opencode.NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	pub := &recordingCompletionPublisher{}
	clock := time.Now
	watcher := NewSessionWatcher(client, store, store, pub, SessionWatcherOptions{
		PollInterval:  10 * time.Millisecond,
		IdleInterval:  10 * time.Millisecond,
		IdleThreshold: 50 * time.Millisecond,
		Clock:         clock,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.Run(ctx)
	}()

	watcher.Watch(42, "ses1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub.count.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if got := pub.count.Load(); got < 1 {
		t.Fatalf("publisher count = %d, want >= 1", got)
	}
	lastRaw := pub.last.Load()
	last, ok := lastRaw.(domain.CompletedSession)
	if !ok {
		t.Fatalf("publisher last = %T, want domain.CompletedSession", lastRaw)
	}
	if last.SessionID != "ses1" {
		t.Fatalf("last.SessionID = %q, want ses1", last.SessionID)
	}
	if last.Preview != "ahora dime tu" {
		t.Fatalf("preview = %q, want %q", last.Preview, "ahora dime tu")
	}

	snap, ok, err := store.LoadCompletedSession(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected persisted completion snapshot")
	}
	if snap.SessionID != "ses1" {
		t.Fatalf("snap.SessionID = %q, want ses1", snap.SessionID)
	}
}

// TestSessionWatcherDoesNotRecordWhileStreaming ensures the heuristic
// gates the recording on the latest assistant message carrying a text
// part: an assistant message with only step-start parts means the model
// is still working and the watcher must stay quiet.
func TestSessionWatcherDoesNotRecordWhileStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
		case "/session/ses1/message":
			body := `[
				{"info":{"id":"u1","role":"user"},"parts":[{"type":"text","text":"hola"}]},
				{"info":{"id":"a1","role":"assistant"},"parts":[{"type":"step-start","text":""}]}
			]`
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	client, _ := opencode.NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
	pub := &recordingCompletionPublisher{}
	watcher := NewSessionWatcher(client, store, store, pub, SessionWatcherOptions{
		PollInterval:  10 * time.Millisecond,
		IdleInterval:  10 * time.Millisecond,
		IdleThreshold: 30 * time.Millisecond,
		Clock:         time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.Run(ctx)
	}()
	watcher.Watch(42, "ses1")

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if got := pub.count.Load(); got != 0 {
		t.Fatalf("publisher count = %d, want 0 (streaming should not record)", got)
	}
}
