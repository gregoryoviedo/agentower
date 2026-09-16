package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/claude"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestSessionLocatorReadsFreshestJSONL(t *testing.T) {
	root := t.TempDir()
	// Mirror the Claude Code convention: <base>/projects/<sanitized-cwd>/<id>.jsonl.
	stateDir := filepath.Join(root, ".claude")
	cwd := "/Users/me/proj"
	sanitized := "-Users-me-proj"
	historyDir := filepath.Join(stateDir, "projects", sanitized)
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	oldPath := filepath.Join(historyDir, "old.jsonl")
	midPath := filepath.Join(historyDir, "mid.jsonl")
	newPath := filepath.Join(historyDir, "new.jsonl")

	writeJSONL(t, oldPath, []map[string]any{
		{
			"type":      "user",
			"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "old"}}},
		},
	})
	writeJSONL(t, midPath, []map[string]any{
		{
			"type":      "assistant",
			"timestamp": now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "mid answer"}}},
		},
	})
	writeJSONL(t, newPath, []map[string]any{
		{
			"type":      "user",
			"timestamp": now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "build me x"}}},
		},
		{
			"type":      "assistant",
			"timestamp": now.Add(-90 * time.Second).Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "new answer"}}},
		},
		{
			"type":      "result",
			"timestamp": now.Add(-60 * time.Second).Format(time.RFC3339Nano),
		},
	})
	if err := os.Chtimes(oldPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(midPath, now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, now.Add(-60*time.Second), now.Add(-60*time.Second)); err != nil {
		t.Fatal(err)
	}

	loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
		Workdir:  cwd,
		StateDir: stateDir,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "new" {
		t.Fatalf("session id = %q, want new", sess.SessionID)
	}
	if sess.Preview != "new answer" {
		t.Fatalf("preview = %q, want new answer", sess.Preview)
	}
	if !sess.TouchedAt.Equal(now.Add(-60 * time.Second)) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, now.Add(-60*time.Second))
	}
	if sess.Project != "proj" {
		t.Fatalf("project = %q, want proj", sess.Project)
	}
}

func TestSessionLocatorNoHistory(t *testing.T) {
	stateDir := t.TempDir()
	loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
		Workdir:  "/Users/nobody/nowhere",
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func TestSessionLocatorRequiresWorkdir(t *testing.T) {
	if _, err := claude.NewSessionLocator(claude.SessionLocatorOptions{}); err == nil {
		t.Fatal("expected error for empty workdir")
	}
}

func TestSessionLocatorKindMatches(t *testing.T) {
	loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{Workdir: "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if loc.Kind() != domain.AgentClaude {
		t.Fatalf("Kind = %q, want claude", loc.Kind())
	}
}

// TestSessionLocatorGlobalScansEveryProject proves the global locator
// follows a session living in a project folder other than the
// configured workdir, and recovers the real cwd from the JSONL.
func TestSessionLocatorGlobalScansEveryProject(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".claude")
	now := time.Now()

	otherCwd := "/Users/me/other"
	otherDir := filepath.Join(stateDir, "projects", "-Users-me-other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, filepath.Join(otherDir, "fresh.jsonl"), []map[string]any{
		{"type": "attachment", "cwd": otherCwd, "timestamp": now.Format(time.RFC3339Nano)},
		{
			"type":      "assistant",
			"timestamp": now.Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "done elsewhere"}}},
		},
	})

	scopedDir := filepath.Join(stateDir, "projects", "-Users-me-proj")
	if err := os.MkdirAll(scopedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(scopedDir, "stale.jsonl")
	writeJSONL(t, stale, []map[string]any{
		{
			"type":      "assistant",
			"cwd":       "/Users/me/proj",
			"timestamp": now.Add(-3 * time.Hour).Format(time.RFC3339Nano),
			"message":   map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "old"}}},
		},
	})
	old := now.Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
		Workdir:  "/Users/me/proj",
		StateDir: stateDir,
		Global:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "fresh" {
		t.Fatalf("session id = %q, want fresh", sess.SessionID)
	}
	if sess.Directory != otherCwd {
		t.Fatalf("directory = %q, want %q", sess.Directory, otherCwd)
	}
	if sess.Project != "other" {
		t.Fatalf("project = %q, want other", sess.Project)
	}
}

// TestSessionLocatorGlobalAllowsEmptyWorkdir documents that global mode
// no longer requires a workdir.
func TestSessionLocatorGlobalAllowsEmptyWorkdir(t *testing.T) {
	if _, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
		StateDir: t.TempDir(),
		Global:   true,
	}); err != nil {
		t.Fatalf("global locator with empty workdir: %v", err)
	}
}

// TestSessionLocatorGlobalNoProjects ensures the global locator returns
// the sentinel when ~/.claude/projects does not exist yet.
func TestSessionLocatorGlobalNoProjects(t *testing.T) {
	loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
		StateDir: t.TempDir(),
		Global:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func writeJSONL(t *testing.T, path string, events []map[string]any) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
