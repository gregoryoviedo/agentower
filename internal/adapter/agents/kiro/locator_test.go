package kiro_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// writeFakeKiroState lays down a minimal ~/.kiro/ tree under
// <root>. The sessionIndex is written to
// <root>/session-index/<workspaceHash>.jsonl; the matching
// session.json + messages.jsonl go under
// <root>/sessions/<workspaceHash>/<sessionID>/.
//
// The function returns the workspace hash the tests used so the
// assertion side can build the same key.
func writeFakeKiroState(t *testing.T, root, workspaceHash, sessionID string, session map[string]any, messages []map[string]any, indexLines []map[string]any) {
	t.Helper()
	indexDir := filepath.Join(root, "session-index")
	sessionDir := filepath.Join(root, "sessions", workspaceHash, sessionID)
	for _, d := range []string{indexDir, sessionDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if session != nil {
		raw, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if messages != nil {
		lines := make([][]byte, 0, len(messages))
		for _, m := range messages {
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, raw)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "messages.jsonl"), joinLines(lines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if indexLines != nil {
		lines := make([][]byte, 0, len(indexLines))
		for _, l := range indexLines {
			raw, err := json.Marshal(l)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, raw)
		}
		if err := os.WriteFile(filepath.Join(indexDir, workspaceHash+".jsonl"), joinLines(lines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func joinLines(lines [][]byte) []byte {
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	out := make([]byte, 0, total)
	for _, l := range lines {
		out = append(out, l...)
		out = append(out, '\n')
	}
	return out
}

func TestSessionLocatorPicksFreshestFromIndex(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	hashA := "ws-aaaa"
	hashB := "ws-bbbb"

	// Two workspaces, two sessions each; the freshest in time
	// wins regardless of which workspace it lives in.
	writeFakeKiroState(t, root, hashA, "sess-old", map[string]any{
		"id":             "sess-old",
		"title":          "old chat",
		"workspacePaths": []string{"/Users/me/old-proj"},
		"createdAt":      now.Add(-3 * time.Hour).Format(time.RFC3339Nano),
		"lastModifiedAt": now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
	}, nil, []map[string]any{
		{"op": "add", "sessionPath": hashA + "/sess-old", "at": now.Add(-2 * time.Hour).UnixMilli()},
	})
	writeFakeKiroState(t, root, hashB, "sess-fresh", map[string]any{
		"id":             "sess-fresh",
		"title":          "fresh chat",
		"workspacePaths": []string{"/Users/me/new-proj"},
		"createdAt":      now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		"lastModifiedAt": now.Add(-30 * time.Second).Format(time.RFC3339Nano),
	}, nil, []map[string]any{
		{"op": "add", "sessionPath": hashB + "/sess-fresh", "at": now.Add(-30 * time.Second).UnixMilli()},
	})

	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "sess-fresh" {
		t.Fatalf("session id = %q, want sess-fresh", sess.SessionID)
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
	if sess.Source != "jsonl" {
		t.Fatalf("source = %q, want jsonl", sess.Source)
	}
	if !sess.TouchedAt.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, now.Add(-30*time.Second))
	}
}

func TestSessionLocatorPeeksAssistantPreview(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	hash := "ws-cccc"
	messages := []map[string]any{
		{
			"id":        "tool-call-1",
			"timestamp": now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
			"payload": map[string]any{
				"type":     "tool_call",
				"toolName": "fetch_cloud_config",
			},
		},
		{
			"id":        "user-1",
			"timestamp": now.Add(-50 * time.Second).Format(time.RFC3339Nano),
			"payload": map[string]any{
				"type":    "user",
				"content": "summarize this project",
			},
		},
		{
			"id":        "assistant-1",
			"timestamp": now.Add(-30 * time.Second).Format(time.RFC3339Nano),
			"payload": map[string]any{
				"type": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "Project overview: an agent tower."},
				},
			},
		},
	}
	writeFakeKiroState(t, root, hash, "sess-peek", map[string]any{
		"id":             "sess-peek",
		"title":          "summarize this project",
		"workspacePaths": []string{"/Users/me/proj"},
		"lastModifiedAt": now.Add(-30 * time.Second).Format(time.RFC3339Nano),
	}, messages, []map[string]any{
		{"op": "add", "sessionPath": hash + "/sess-peek", "at": now.Add(-30 * time.Second).UnixMilli()},
	})

	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.Preview != "Project overview: an agent tower." {
		t.Fatalf("preview = %q, want assistant text", sess.Preview)
	}
}

func TestSessionLocatorSkipsNonAddOps(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	hash := "ws-dddd"
	// Index has an unknown op plus a non-add one. The locator
	// must ignore them and only consider the `add`.
	writeFakeKiroState(t, root, hash, "sess-real", map[string]any{
		"id":             "sess-real",
		"title":          "real",
		"workspacePaths": []string{"/Users/me/proj"},
		"lastModifiedAt": now.Format(time.RFC3339Nano),
	}, nil, []map[string]any{
		{"op": "remove", "sessionPath": hash + "/sess-gone", "at": now.UnixMilli()},
		{"op": "garbage", "sessionPath": hash + "/sess-also-gone", "at": now.UnixMilli()},
		{"op": "add", "sessionPath": hash + "/sess-real", "at": now.UnixMilli()},
	})
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "sess-real" {
		t.Fatalf("session id = %q, want sess-real", sess.SessionID)
	}
}

func TestSessionLocatorNoIndexReturnsSentinel(t *testing.T) {
	root := t.TempDir()
	// Even an empty ~/.kiro/ is OK; only the missing index
	// should produce ErrNoActiveSession.
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
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: "/path/that/does/not/exist/anywhere"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for missing dir, got %v", err)
	}
}

func TestSessionLocatorMalformedIndexFallsThrough(t *testing.T) {
	root := t.TempDir()
	indexDir := filepath.Join(root, "session-index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "ws-eeee.jsonl"), []byte("not json\nalso not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loc.Locate(context.Background())
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession for malformed index, got %v", err)
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
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	other := t.TempDir()
	writeFakeKiroState(t, other, "ws-ffff", "sess-other", map[string]any{
		"id":             "sess-other",
		"title":          "after swap",
		"workspacePaths": []string{"/Users/me/other"},
		"lastModifiedAt": now.Format(time.RFC3339Nano),
	}, nil, []map[string]any{
		{"op": "add", "sessionPath": "ws-ffff/sess-other", "at": now.UnixMilli()},
	})
	loc.SetStateDir(other)
	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate after swap: %v", err)
	}
	if sess.SessionID != "sess-other" {
		t.Fatalf("session id = %q, want sess-other", sess.SessionID)
	}
}
