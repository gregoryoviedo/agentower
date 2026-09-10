package copilot_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestSessionLocatorPicksFreshest(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	older := now.Add(-2 * time.Hour)
	fresh := now.Add(-2 * time.Minute)
	olderPath := filepath.Join(dir, "old.json")
	freshPath := filepath.Join(dir, "new.json")
	writeSession(t, olderPath, map[string]any{
		"sessionId":       "ses_old",
		"workspacePath":   "/Users/me/repo-a",
		"title":           "old chat",
		"lastResponse":    "older answer",
		"lastMessageDate": older.Format(time.RFC3339Nano),
	})
	writeSession(t, freshPath, map[string]any{
		"sessionId":       "ses_fresh",
		"workspacePath":   "/Users/me/repo-b",
		"title":           "fresh chat",
		"lastResponse":    "freshest answer",
		"lastMessageDate": fresh.Format(time.RFC3339Nano),
	})
	if err := os.Chtimes(olderPath, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(freshPath, fresh, fresh); err != nil {
		t.Fatal(err)
	}

	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "ses_fresh" {
		t.Fatalf("session id = %q, want ses_fresh", sess.SessionID)
	}
	if sess.Directory != "/Users/me/repo-b" {
		t.Fatalf("directory = %q, want /Users/me/repo-b", sess.Directory)
	}
	if sess.Project != "repo-b" {
		t.Fatalf("project = %q, want repo-b", sess.Project)
	}
	if sess.Title != "fresh chat" {
		t.Fatalf("title = %q, want fresh chat", sess.Title)
	}
	if sess.Preview != "freshest answer" {
		t.Fatalf("preview = %q, want freshest answer", sess.Preview)
	}
	if !sess.TouchedAt.Equal(fresh) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, fresh)
	}
}

func TestSessionLocatorFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc-123.json")
	writeSession(t, path, map[string]any{
		"workspacePath": "/Users/me/other",
		"title":         "untitled",
	})
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "abc-123" {
		t.Fatalf("session id = %q, want abc-123", sess.SessionID)
	}
}

func TestSessionLocatorSkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lock"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), []byte(`{"sessionId":"x","workspacePath":"/a","title":"t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "x" {
		t.Fatalf("session id = %q, want x", sess.SessionID)
	}
}

func TestSessionLocatorEmptyDir(t *testing.T) {
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func TestSessionLocatorMissingDir(t *testing.T) {
	loc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: "/path/that/does/not/exist/anywhere"})
	if err != nil {
		// NewSessionLocator swallows the missing-dir error so the
		// bot can boot on machines without VS Code; we still want
		// the test to cover that path. Fall through and expect the
		// missing-dir sentinel at Locate time instead.
		_ = loc
	}
	loc, _ = copilot.NewSessionLocator(copilot.SessionLocatorOptions{StateDir: "/path/that/does/not/exist/anywhere"})
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for missing dir, got %v", err)
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

func writeSession(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
