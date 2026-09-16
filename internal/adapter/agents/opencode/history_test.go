package agents_opencode_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/domain"

	_ "modernc.org/sqlite"
)

// seedHistoryDB creates a temp opencode.db with the subset of the
// opencode schema the adapter reads and returns its path.
func seedHistoryDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, title TEXT, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	sessions := []struct {
		id        string
		directory string
		title     string
		updated   int64
	}{
		{"s_old", "/Users/me/old", "old", 1000},
		{"s_new", "/Users/me/new", "new task", 2000},
	}
	for _, s := range sessions {
		if _, err := db.Exec(
			`INSERT INTO session (id, directory, title, time_updated) VALUES (?, ?, ?, ?)`,
			s.id, s.directory, s.title, s.updated,
		); err != nil {
			t.Fatal(err)
		}
	}

	messages := []struct {
		id      string
		session string
		created int64
		role    string
	}{
		{"m1", "s_new", 10, "user"},
		{"m2", "s_new", 20, "assistant"},
	}
	for _, m := range messages {
		data, _ := json.Marshal(map[string]any{"role": m.role})
		if _, err := db.Exec(
			`INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`,
			m.id, m.session, m.created, string(data),
		); err != nil {
			t.Fatal(err)
		}
	}

	parts := []struct {
		id      string
		message string
		created int64
		body    string
	}{
		{"p1", "m1", 11, `{"type":"text","text":"hola"}`},
		{"p2", "m2", 21, `{"type":"text","text":"listo"}`},
		{"p3", "m2", 22, `{"type":"tool","tool":"bash"}`},
	}
	for _, p := range parts {
		if _, err := db.Exec(
			`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`,
			p.id, p.message, "s_new", p.created, p.body,
		); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestHistoryLocateReturnsFreshestSession(t *testing.T) {
	history, err := agents_opencode.OpenHistory(seedHistoryDB(t))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer history.Close()

	sess, err := history.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "s_new" {
		t.Fatalf("session id = %q, want s_new", sess.SessionID)
	}
	if sess.Directory != "/Users/me/new" {
		t.Fatalf("directory = %q, want /Users/me/new", sess.Directory)
	}
	if sess.Project != "new" {
		t.Fatalf("project = %q, want new", sess.Project)
	}
	if sess.Title != "new task" {
		t.Fatalf("title = %q, want new task", sess.Title)
	}
	if sess.Preview != "listo" {
		t.Fatalf("preview = %q, want listo", sess.Preview)
	}
	if sess.Kind != domain.AgentOpenCode {
		t.Fatalf("kind = %q, want opencode", sess.Kind)
	}
}

func TestHistoryListMessagesOrdersRolesAndParts(t *testing.T) {
	history, err := agents_opencode.OpenHistory(seedHistoryDB(t))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer history.Close()

	msgs, err := history.ListMessages(context.Background(), "s_new")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Info.Role != "user" || msgs[0].Parts[0].Text != "hola" {
		t.Fatalf("message 0 = %#v", msgs[0])
	}
	if msgs[1].Info.Role != "assistant" {
		t.Fatalf("message 1 role = %q, want assistant", msgs[1].Info.Role)
	}
	if len(msgs[1].Parts) != 2 {
		t.Fatalf("message 1 has %d parts, want 2", len(msgs[1].Parts))
	}
	if msgs[1].Parts[0].Text != "listo" {
		t.Fatalf("message 1 part 0 = %#v", msgs[1].Parts[0])
	}
	if msgs[1].Parts[1].Type != "tool" {
		t.Fatalf("message 1 part 1 type = %q, want tool", msgs[1].Parts[1].Type)
	}
}

func TestHistoryLocateEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, title TEXT, time_updated INTEGER)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	history, err := agents_opencode.OpenHistory(path)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer history.Close()

	_, err = history.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

// TestClientListMessagesPrefersHistory proves the SQLite reader wins when
// the store has rows, so a locally-launched opencode without an HTTP
// server still yields message history.
func TestClientListMessagesPrefersHistory(t *testing.T) {
	history, err := agents_opencode.OpenHistory(seedHistoryDB(t))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer history.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := agents_opencode.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.SetHistory(history)

	msgs, err := client.ListMessages(context.Background(), "s_new")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
}

// TestClientListMessagesFallsBackToHTTP proves the HTTP path is still
// used when the store has no rows for the session.
func TestClientListMessagesFallsBackToHTTP(t *testing.T) {
	history, err := agents_opencode.OpenHistory(seedHistoryDB(t))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer history.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"info":  map[string]any{"id": "x", "sessionID": "s_missing", "role": "assistant"},
				"parts": []map[string]any{{"type": "text", "text": "from http"}},
			},
		})
	}))
	defer srv.Close()

	client, err := agents_opencode.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.SetHistory(history)

	msgs, err := client.ListMessages(context.Background(), "s_missing")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Parts[0].Text != "from http" {
		t.Fatalf("unexpected messages: %#v", msgs)
	}
}
