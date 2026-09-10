package kiro_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// writeFakeKiroDB creates a state.vscdb at <root>/User/globalStorage/
// kiro.kiroagent/default/state.vscdb with the supplied index blob
// and per-session blobs. The function fails the test on any I/O
// or SQL error.
func writeFakeKiroDB(t *testing.T, root string, index map[string]any, perSession map[string]map[string]any) {
	t.Helper()
	dbDir := filepath.Join(root, "User", "globalStorage", "kiro.kiroagent", "default")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	indexBlob, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "chat.ChatSessionStore.index", indexBlob); err != nil {
		t.Fatal(err)
	}
	for id, doc := range perSession {
		blob, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "chat.ChatSessionStore."+id, blob); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionLocatorPicksFreshestFromIndex(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	index := map[string]any{
		"version": 1,
		"entries": map[string]any{
			"sess_old": map[string]any{
				"title":           "old chat",
				"workspacePath":   "/Users/me/old-proj",
				"lastMessageDate": now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			},
			"sess_fresh": map[string]any{
				"title":           "fresh chat",
				"workspacePath":   "/Users/me/new-proj",
				"lastMessageDate": now.Add(-30 * time.Second).Format(time.RFC3339Nano),
			},
		},
	}
	writeFakeKiroDB(t, root, index, nil)

	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "sess_fresh" {
		t.Fatalf("session id = %q, want sess_fresh", sess.SessionID)
	}
	if sess.Project != "new-proj" {
		t.Fatalf("project = %q, want new-proj", sess.Project)
	}
	if sess.Directory != "/Users/me/new-proj" {
		t.Fatalf("directory = %q, want /Users/me/new-proj", sess.Directory)
	}
	if sess.Title != "fresh chat" {
		t.Fatalf("title = %q, want fresh chat", sess.Title)
	}
	if sess.Source != "sqlite" {
		t.Fatalf("source = %q, want sqlite", sess.Source)
	}
	if !sess.TouchedAt.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, now.Add(-30*time.Second))
	}
}

func TestSessionLocatorEnrichesFromPerSessionBlob(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	index := map[string]any{
		"version": 1,
		"entries": map[string]any{
			"sess_a": map[string]any{
				"title":           "stale title in index",
				"workspacePath":   "/Users/me/proj",
				"lastMessageDate": now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
			},
		},
	}
	perSession := map[string]map[string]any{
		"sess_a": {
			"title":           "fresh title from per-session blob",
			"preview":         "snippets help /resume show the right card",
			"lastMessageDate": now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		},
	}
	writeFakeKiroDB(t, root, index, perSession)

	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.Title != "fresh title from per-session blob" {
		t.Fatalf("title = %q, want fresh title from per-session blob", sess.Title)
	}
	if sess.Preview != "snippets help /resume show the right card" {
		t.Fatalf("preview = %q, want snippet text", sess.Preview)
	}
}

func TestSessionLocatorNoIndexReturnsSentinel(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "User", "globalStorage", "kiro.kiroagent", "default")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	// Empty db; no index.

	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func TestSessionLocatorMissingDirIsSentinel(t *testing.T) {
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: "/path/that/does/not/exist"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for missing dir, got %v", err)
	}
}

func TestSessionLocatorMalformedIndexFallsBack(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "User", "globalStorage", "kiro.kiroagent", "default")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "chat.ChatSessionStore.index", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed index, got nil")
	}
	if errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("malformed index should not collapse to sentinel: %v", err)
	}
}

func TestSessionLocatorKind(t *testing.T) {
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if loc.Kind() != domain.AgentKiro {
		t.Fatalf("Kind = %q, want kiro", loc.Kind())
	}
}

func TestSessionLocatorSetStateDir(t *testing.T) {
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// A second fake db at a different root, then swap.
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	other := t.TempDir()
	writeFakeKiroDB(t, other, map[string]any{
		"version": 1,
		"entries": map[string]any{
			"sess_other": map[string]any{
				"title":           "after swap",
				"lastMessageDate": now.Format(time.RFC3339Nano),
			},
		},
	}, nil)
	loc.SetStateDir(other)
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate after swap: %v", err)
	}
	if sess.SessionID != "sess_other" {
		t.Fatalf("session id = %q, want sess_other", sess.SessionID)
	}
}
