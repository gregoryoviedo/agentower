package usecase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
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

// singleAdapterRegistry satisfies domain.AgentRegistry with a single
// opencode adapter. The session watcher test only cares about
// dispatch, so the other methods return minimal stubs.
type singleAdapterRegistry struct {
	adapter domain.AgentAdapter
}

func (r *singleAdapterRegistry) Descriptors() []domain.AgentDescriptor {
	return []domain.AgentDescriptor{{Kind: domain.AgentOpenCode, Available: true}}
}
func (r *singleAdapterRegistry) Available() []domain.AgentDescriptor { return r.Descriptors() }
func (r *singleAdapterRegistry) DescriptorFor(k domain.AgentKind) (domain.AgentDescriptor, bool) {
	if k != domain.AgentOpenCode {
		return domain.AgentDescriptor{}, false
	}
	return r.Descriptors()[0], true
}
func (r *singleAdapterRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != domain.AgentOpenCode {
		return nil, domain.ErrAgentUnavailable
	}
	return r.adapter, nil
}
func (r *singleAdapterRegistry) Active(_ int64) (domain.AgentKind, error) {
	return domain.AgentOpenCode, nil
}
func (r *singleAdapterRegistry) SetActive(_ int64, _ domain.AgentKind) error { return nil }

func TestSessionWatcherMarksIdleSessionAsCompleted(t *testing.T) {
	var messageCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
		case "/session":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[{"id":"ses1","projectID":"p1","title":"Main","directory":"/tmp/work"}]`))
			}
		case "/session/ses1/message":
			if r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
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

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	pub := &recordingCompletionPublisher{}
	watcher := NewSessionWatcher(&singleAdapterRegistry{adapter: client}, store, store, pub, SessionWatcherOptions{
		PollInterval:  10 * time.Millisecond,
		IdleInterval:  10 * time.Millisecond,
		IdleThreshold: 50 * time.Millisecond,
		Clock:         time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.Run(ctx)
	}()

	watcher.Watch(42, domain.AgentOpenCode, "ses1")

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
	if last.AgentKind != domain.AgentOpenCode {
		t.Fatalf("AgentKind = %q, want opencode", last.AgentKind)
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
	if snap.AgentKind != domain.AgentOpenCode {
		t.Fatalf("persisted AgentKind = %q, want opencode", snap.AgentKind)
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
	client, _ := agents_opencode.NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
	pub := &recordingCompletionPublisher{}
	watcher := NewSessionWatcher(&singleAdapterRegistry{adapter: client}, store, store, pub, SessionWatcherOptions{
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
	watcher.Watch(42, domain.AgentOpenCode, "ses1")

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if got := pub.count.Load(); got != 0 {
		t.Fatalf("publisher count = %d, want 0 (streaming should not record)", got)
	}
}

// TestSessionWatcherSkipsCopilot guards the registry-lookup path for
// an agent the bot does not have an adapter for: the tick must not
// panic or record anything when the registry has no adapter for the
// watched kind.
func TestSessionWatcherSkipsCopilot(t *testing.T) {
	store, _ := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	pub := &recordingCompletionPublisher{}
	// singleAdapterRegistry returns ErrAgentUnavailable for
	// non-opencode kinds, so the watcher's tick would short-circuit
	// on the registry lookup anyway. The point of this test is to
	// prove the watcher never reaches the adapter: it must short
	// before Get().
	watcher := NewSessionWatcher(&singleAdapterRegistry{adapter: nil}, store, store, pub, SessionWatcherOptions{
		PollInterval:  5 * time.Millisecond,
		IdleInterval:  5 * time.Millisecond,
		IdleThreshold: 5 * time.Millisecond,
		Clock:         time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.Run(ctx)
	}()
	watcher.Watch(42, domain.AgentCopilot, "ses_copilot")

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if got := pub.count.Load(); got != 0 {
		t.Fatalf("publisher count = %d, want 0 (copilot has no stream)", got)
	}
}

// staticAdapter is a minimal domain.AgentAdapter for watcher tests: it
// serves a fixed ListMessages result and returns empty everywhere else.
type staticAdapter struct {
	msgs []domain.Message
}

func (s *staticAdapter) Kind() domain.AgentKind { return domain.AgentKiro }
func (s *staticAdapter) DisplayName() string    { return "Kiro" }
func (s *staticAdapter) Health(context.Context) (domain.HealthStatus, error) {
	return domain.HealthStatus{Healthy: true}, nil
}
func (s *staticAdapter) ListProjects(context.Context) ([]domain.Project, error) { return nil, nil }
func (s *staticAdapter) ListSessions(context.Context) ([]domain.Session, error) { return nil, nil }
func (s *staticAdapter) CreateSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (s *staticAdapter) SendPrompt(context.Context, string, string) (string, error) { return "", nil }
func (s *staticAdapter) Revert(context.Context, string) error                       { return nil }
func (s *staticAdapter) FileStatus(context.Context, string) ([]domain.FileChange, error) {
	return nil, nil
}
func (s *staticAdapter) ListMessages(context.Context, string) ([]domain.Message, error) {
	return s.msgs, nil
}

// staticLocator serves a fixed active session for the IDE watcher.
type staticLocator struct {
	kind domain.AgentKind
	as   domain.ActiveSession
	err  error
}

func (l *staticLocator) Kind() domain.AgentKind { return l.kind }
func (l *staticLocator) Locate(context.Context) (domain.ActiveSession, error) {
	return l.as, l.err
}

// kindAdapterRegistry serves the adapter for a specific agent kind.
type kindAdapterRegistry struct {
	kind    domain.AgentKind
	adapter domain.AgentAdapter
}

func (r *kindAdapterRegistry) Descriptors() []domain.AgentDescriptor {
	return []domain.AgentDescriptor{{Kind: r.kind, Available: true}}
}
func (r *kindAdapterRegistry) Available() []domain.AgentDescriptor { return r.Descriptors() }
func (r *kindAdapterRegistry) DescriptorFor(k domain.AgentKind) (domain.AgentDescriptor, bool) {
	if k != r.kind {
		return domain.AgentDescriptor{}, false
	}
	return r.Descriptors()[0], true
}
func (r *kindAdapterRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != r.kind {
		return nil, domain.ErrAgentUnavailable
	}
	return r.adapter, nil
}
func (r *kindAdapterRegistry) Active(_ int64) (domain.AgentKind, error) { return r.kind, nil }
func (r *kindAdapterRegistry) SetActive(_ int64, _ domain.AgentKind) error {
	return nil
}

// TestSessionWatcherWatchIDERecordsAndRequestsNotification covers the
// IDE auto-follow path used for Copilot/Kiro: the watcher arms itself
// on the locator's freshest session and, once stable, persists the
// completion AND marks the notification pending so the macOS wrapper
// can push the Telegram "Completado" message after idle.
func TestSessionWatcherWatchIDERecordsAndRequestsNotification(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	msgs := []domain.Message{
		{Info: domain.MessageInfo{ID: "u1", Role: "user"}, Parts: []domain.MessagePart{{Type: "text", Text: "hola"}}},
		{Info: domain.MessageInfo{ID: "a1", Role: "assistant"}, Parts: []domain.MessagePart{{Type: "text", Text: "respuesta"}}},
	}
	adapter := &staticAdapter{msgs: msgs}
	reg := &kindAdapterRegistry{kind: domain.AgentKiro, adapter: adapter}

	pub := &recordingCompletionPublisher{}
	var notified []int64
	watcher := NewSessionWatcher(reg, store, store, pub, SessionWatcherOptions{
		PollInterval:  10 * time.Millisecond,
		IdleInterval:  10 * time.Millisecond,
		IdleThreshold: 30 * time.Millisecond,
		Clock:         time.Now,
	})
	watcher.SetRequestNotifier(func(chatID int64) {
		notified = append(notified, chatID)
	})

	loc := &staticLocator{
		kind: domain.AgentKiro,
		as:   domain.ActiveSession{Kind: domain.AgentKiro, SessionID: "ide-1", Directory: "/tmp/work"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.Run(ctx)
	}()
	watcher.WatchIDE(42, domain.AgentKiro, loc)

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
	if len(notified) == 0 {
		t.Fatal("RequestNotification never called after completion")
	}
	if notified[0] != 42 {
		t.Fatalf("notified chat = %d, want 42", notified[0])
	}
	lastRaw := pub.last.Load()
	last, ok := lastRaw.(domain.CompletedSession)
	if !ok {
		t.Fatalf("last = %T, want CompletedSession", lastRaw)
	}
	if last.SessionID != "ide-1" || last.AgentKind != domain.AgentKiro {
		t.Fatalf("completed = %+v, want ide-1/kiro", last)
	}
	if last.Preview != "respuesta" {
		t.Fatalf("preview = %q, want respuesta", last.Preview)
	}
}
