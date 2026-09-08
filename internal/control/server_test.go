package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gregoryoviedo/opencode-telegram-remote/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/opencode-telegram-remote/internal/control"
	"github.com/gregoryoviedo/opencode-telegram-remote/internal/domain"
)

// recordingNotifier implements domain.ChatNotifier for the control server
// tests so we can assert the notification flow without standing up a real
// Telegram bot.
type recordingNotifier struct {
	count    atomic.Int32
	lastText atomic.Value // string
	lastResp atomic.Value // domain.BotResponse
}

func (r *recordingNotifier) NotifyTyping(_ context.Context, _ int64) error { return nil }
func (r *recordingNotifier) SendMessage(_ context.Context, _ int64, text string) error {
	r.count.Add(1)
	r.lastText.Store(text)
	return nil
}
func (r *recordingNotifier) SendResponse(_ context.Context, _ int64, resp domain.BotResponse) error {
	r.count.Add(1)
	r.lastText.Store(resp.Text)
	r.lastResp.Store(resp)
	return nil
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newStore(t *testing.T) *sqlite.Repository {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPublisherTracksActiveAndCompletion(t *testing.T) {
	p := control.NewPublisher()
	p.SetActive(42, "work/proj", "ses_1")
	snap := p.Snapshot()
	if snap.ChatID != 42 || snap.ActiveProject != "work/proj" || snap.ActiveSession != "ses_1" {
		t.Fatalf("snapshot after SetActive = %+v", snap)
	}

	p.PublishCompletion(domain.CompletedSession{
		ChatID:      42,
		SessionID:   "ses_1",
		ProjectName: "proj",
		CompletedAt: time.Now().UTC(),
		Preview:     "listo",
	})
	snap = p.Snapshot()
	if snap.LastCompleted == nil || snap.LastCompleted.SessionID != "ses_1" {
		t.Fatalf("expected LastCompleted.SessionID=ses_1, got %+v", snap.LastCompleted)
	}
}

func TestServerStateAndNotifyFlow(t *testing.T) {
	store := newStore(t)
	publisher := control.NewPublisher()
	publisher.SetActive(7, "work/foo", "ses_9")
	publisher.PublishCompletion(domain.CompletedSession{
		ChatID:      7,
		SessionID:   "ses_9",
		ProjectName: "foo",
		Preview:     "preview",
		CompletedAt: time.Now().UTC(),
	})
	publisher.RequestNotification(7)

	notifier := &recordingNotifier{}
	srv := control.NewServer(publisher, store, publisher, notifier, newDiscardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// GET /state
	resp, err := http.Get("http://" + srv.Addr() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /state status = %d, body = %s", resp.StatusCode, body)
	}
	var snap domain.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("decode /state: %v", err)
	}
	if snap.PendingNotifChat != 7 {
		t.Fatalf("PendingNotifChat = %d, want 7", snap.PendingNotifChat)
	}
	if snap.LastCompleted == nil || snap.LastCompleted.SessionID != "ses_9" {
		t.Fatalf("LastCompleted = %+v", snap.LastCompleted)
	}

	// POST /notify consumes the pending notification.
	notifyBody := bytes.NewBufferString(`{"chat_id":7}`)
	resp, err = http.Post("http://"+srv.Addr()+"/notify", "application/json", notifyBody)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /notify status = %d, body = %s", resp.StatusCode, body)
	}
	if notifier.count.Load() != 1 {
		t.Fatalf("notifier count = %d, want 1", notifier.count.Load())
	}
	if last, _ := notifier.lastText.Load().(string); last == "" {
		t.Fatal("notifier last message empty")
	}
	notifRaw := notifier.lastResp.Load()
	notifResp, ok := notifRaw.(domain.BotResponse)
	if !ok {
		t.Fatalf("notifier last resp = %T", notifRaw)
	}
	if len(notifResp.Buttons) == 0 {
		t.Fatal("expected buttons on completion response")
	}
	foundContinue, foundDiff := false, false
	for _, row := range notifResp.Buttons {
		for _, btn := range row {
			if btn.Text == "▶️ Continuar sesión" {
				foundContinue = true
			}
			if btn.Text == "📝 Ver cambios" {
				foundDiff = true
			}
		}
	}
	if !foundContinue || !foundDiff {
		t.Fatalf("expected both buttons, got continue=%v diff=%v", foundContinue, foundDiff)
	}

	// A second POST /notify for the same chat must fail because the
	// notification has already been consumed.
	resp, err = http.Post("http://"+srv.Addr()+"/notify", "application/json", bytes.NewBufferString(`{"chat_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second POST /notify status = %d, want 409", resp.StatusCode)
	}

	// POST /cancel is a no-op now (pending already consumed) but should not error.
	resp, err = http.Post("http://"+srv.Addr()+"/cancel?chat_id=7", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /cancel status = %d, want 204", resp.StatusCode)
	}
}

func TestServerRejectsUnknownChatForNotify(t *testing.T) {
	store := newStore(t)
	publisher := control.NewPublisher()
	srv := control.NewServer(publisher, store, publisher, nil, newDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// No completion published, so /notify must refuse.
	resp, err := http.Post("http://"+srv.Addr()+"/notify", "application/json",
		bytes.NewBufferString(`{"chat_id":99}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /notify without completion status = %d, want 404 or 409", resp.StatusCode)
	}
}
