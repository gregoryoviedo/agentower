package telegram_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/telegram"
)

type handler struct{}

func (handler) HandleCommand(context.Context, int64, string, []string) (telegram.Response, error) {
	return telegram.Response{Text: "ok"}, nil
}
func (handler) HandleText(context.Context, int64, string) (telegram.Response, error) {
	return telegram.Response{Text: "ok"}, nil
}
func (handler) HandleCallback(context.Context, int64, string) (telegram.Response, error) {
	return telegram.Response{Text: "ok"}, nil
}

func TestBotRejectsInvalidConfiguration(t *testing.T) {
	if _, err := telegram.New(telegram.Config{}, handler{}, slog.Default()); err == nil {
		t.Fatal("New accepted an empty configuration")
	}
}

func TestBotRegistersCommandsOnStart(t *testing.T) {
	var capturedCommands []map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "setMyCommands"):
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Commands []map[string]string `json:"commands"`
			}
			_ = json.Unmarshal(body, &payload)
			capturedCommands = payload.Commands
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.Contains(r.URL.Path, "getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"x","username":"x"}}`))
		case strings.Contains(r.URL.Path, "deleteWebhook"):
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.Contains(r.URL.Path, "logOut"):
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.Contains(r.URL.Path, "close"):
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer server.Close()

	_, err := telegram.New(telegram.Config{
		Token:         "123:abc",
		AllowedChatID: 42,
		APIRoot:       server.URL,
	}, handler{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if len(capturedCommands) < 6 {
		t.Fatalf("expected at least 6 commands, got %d", len(capturedCommands))
	}
	want := map[string]bool{
		"start": false, "help": false, "status": false, "projects": false,
		"sessions": false, "diff": false, "changes": false, "undo": false,
	}
	for _, command := range capturedCommands {
		want[command["command"]] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing command %q in setMyCommands payload", name)
		}
	}
}
