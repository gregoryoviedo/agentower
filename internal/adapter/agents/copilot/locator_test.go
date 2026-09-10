package copilot_test

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

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// writeFakeCopilotSQLite creates a session-store.db under
// <root>/session-store.db with the supplied sessions and turns.
// The schema matches the real VS Code Copilot Chat storage
// (verified against the live install on 2026-09-10): a `sessions`
// table with id/cwd/summary/created_at/updated_at plus a `turns`
// table joined by session_id.
func writeFakeCopilotSQLite(t *testing.T, root string, sessions []map[string]any, turns []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "session-store.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			cwd TEXT,
			summary TEXT,
			created_at TEXT,
			updated_at TEXT
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE turns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			turn_index INTEGER NOT NULL,
			user_message TEXT,
			assistant_response TEXT,
			timestamp TEXT
		)
	`); err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if _, err := db.Exec(`INSERT INTO sessions (id,cwd,summary,created_at,updated_at) VALUES (?,?,?,?,?)`,
			s["id"], s["cwd"], s["summary"], s["created_at"], s["updated_at"]); err != nil {
			t.Fatal(err)
		}
	}
	for _, turn := range turns {
		if _, err := db.Exec(`INSERT INTO turns (session_id,turn_index,user_message,assistant_response,timestamp) VALUES (?,?,?,?,?)`,
			turn["session_id"], turn["turn_index"], turn["user_message"], turn["assistant_response"], turn["timestamp"]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionLocatorPicksFreshestFromSQLite(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	writeFakeCopilotSQLite(t, root,
		[]map[string]any{
			{
				"id":         "ses-old",
				"cwd":        "/Users/me/old-proj",
				"summary":    "old chat",
				"created_at": now.Add(-3 * time.Hour).Format(time.RFC3339Nano),
				"updated_at": now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			},
			{
				"id":         "ses-fresh",
				"cwd":        "/Users/me/new-proj",
				"summary":    "fresh chat",
				"created_at": now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
				"updated_at": now.Add(-30 * time.Second).Format(time.RFC3339Nano),
			},
		},
		[]map[string]any{
			{
				"session_id":         "ses-fresh",
				"turn_index":         0,
				"user_message":       "summarize this project",
				"assistant_response": "Here is a summary of the project...",
				"timestamp":          now.Add(-30 * time.Second).Format(time.RFC3339Nano),
			},
		},
	)
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "ses-fresh" {
		t.Fatalf("session id = %q, want ses-fresh", sess.SessionID)
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
	if sess.Preview != "Here is a summary of the project..." {
		t.Fatalf("preview = %q, want assistant turn text", sess.Preview)
	}
	if sess.Source != "sqlite" {
		t.Fatalf("source = %q, want sqlite", sess.Source)
	}
	if !sess.TouchedAt.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, now.Add(-30*time.Second))
	}
}

func TestSessionLocatorPicksLatestTurn(t *testing.T) {
	// The most recent assistant_response is the preview, not
	// the first one. The locator must ORDER BY timestamp DESC.
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	writeFakeCopilotSQLite(t, root,
		[]map[string]any{
			{
				"id":         "ses-multi",
				"cwd":        "/Users/me/proj",
				"summary":    "multi-turn",
				"created_at": now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				"updated_at": now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
			},
		},
		[]map[string]any{
			{
				"session_id":         "ses-multi",
				"turn_index":         0,
				"user_message":       "first prompt",
				"assistant_response": "first answer",
				"timestamp":          now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
			},
			{
				"session_id":         "ses-multi",
				"turn_index":         1,
				"user_message":       "second prompt",
				"assistant_response": "second answer (newest)",
				"timestamp":          now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
			},
		},
	)
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.Preview != "second answer (newest)" {
		t.Fatalf("preview = %q, want second answer (newest)", sess.Preview)
	}
}

func TestSessionLocatorFallsBackToLegacyJSON(t *testing.T) {
	// Older VS Code builds stored one .json per session. The
	// locator must keep working on those installs.
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	doc := map[string]any{
		"sessionId":       "legacy-id",
		"workspacePath":   "/Users/me/legacy",
		"title":           "legacy chat",
		"lastResponse":    "legacy answer",
		"lastMessageDate": now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "legacy-id.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "legacy-id.json"), now.Add(-2*time.Minute), now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "legacy-id" {
		t.Fatalf("session id = %q, want legacy-id", sess.SessionID)
	}
	if sess.Source != "fs" {
		t.Fatalf("source = %q, want fs", sess.Source)
	}
	if sess.Preview != "legacy answer" {
		t.Fatalf("preview = %q, want legacy answer", sess.Preview)
	}
}

func TestSessionLocatorNoStoreReturnsSentinel(t *testing.T) {
	root := t.TempDir()
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func TestSessionLocatorMissingDirIsSentinel(t *testing.T) {
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: "/path/that/does/not/exist/anywhere"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for missing dir, got %v", err)
	}
}

func TestSessionLocatorEmptySQLiteFallsThrough(t *testing.T) {
	// SQLite file exists but has no sessions; the legacy
	// scanner finds nothing either; the locator surfaces
	// ErrNoActiveSession (not an error).
	root := t.TempDir()
	dbPath := filepath.Join(root, "session-store.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, cwd TEXT, summary TEXT, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for empty store, got %v", err)
	}
}

func TestSessionLocatorKind(t *testing.T) {
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if loc.Kind() != domain.AgentCopilot {
		t.Fatalf("Kind = %q, want copilot", loc.Kind())
	}
}

func TestSessionLocatorSetStateDir(t *testing.T) {
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	other := t.TempDir()
	writeFakeCopilotSQLite(t, other,
		[]map[string]any{{
			"id":         "after-swap",
			"cwd":        "/Users/me/other",
			"summary":    "after swap",
			"created_at": now.Format(time.RFC3339Nano),
			"updated_at": now.Format(time.RFC3339Nano),
		}}, nil)
	loc.SetStateDir(other)
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate after swap: %v", err)
	}
	if sess.SessionID != "after-swap" {
		t.Fatalf("session id = %q, want after-swap", sess.SessionID)
	}
}
