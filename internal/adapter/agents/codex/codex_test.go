package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRollout writes a rollout file under
// <root>/sessions/2026/01/15/rollout-<id>.jsonl with a session_meta
// line and the supplied response_item lines.
func writeRollout(t *testing.T, sessionsDir, id, cwd string, lines []string) {
	t.Helper()
	dir := filepath.Join(sessionsDir, "2026", "01", "15")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n"
	for _, l := range lines {
		body += l + "\n"
	}
	path := filepath.Join(dir, "rollout-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const assistantLine = `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hola desde codex"}]}}`

func TestLocatorPicksFreshest(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	writeRollout(t, sessions, "old-thread", "/tmp/old", nil)
	writeRollout(t, sessions, "new-thread", "/tmp/new", []string{assistantLine})

	loc, err := NewSessionLocator(SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	as, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if as.SessionID != "new-thread" {
		t.Fatalf("SessionID = %q, want new-thread", as.SessionID)
	}
	if as.Preview != "hola desde codex" {
		t.Fatalf("Preview = %q", as.Preview)
	}
}

func TestLocatorNoSessions(t *testing.T) {
	root := t.TempDir()
	loc, err := NewSessionLocator(SessionLocatorOptions{StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loc.Locate(context.Background()); err == nil {
		t.Fatal("expected ErrNoActiveSession")
	}
}

func TestAdapterListMessages(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	writeRollout(t, sessions, "thread-1", "/tmp/proj", []string{assistantLine})

	mgr := NewManager("codex", 4101)
	mgr.MarkStarted("/tmp/proj")
	adapter := NewAdapter(mgr)
	adapter.SetStateDir(root)

	msgs, err := adapter.ListMessages(context.Background(), "thread-1")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Info.Role != "assistant" {
		t.Fatalf("unexpected messages %+v", msgs)
	}
	if msgs[0].Parts[0].Text != "hola desde codex" {
		t.Fatalf("unexpected part text %q", msgs[0].Parts[0].Text)
	}
}

func TestAdapterCreateSessionIsSynthetic(t *testing.T) {
	adapter := NewAdapter(NewManager("codex", 4101))
	s, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !isSynthetic(s.ID) {
		t.Fatalf("expected synthetic id, got %q", s.ID)
	}
}

// TestBuildArgvResumeUsesThreadID pins the resume contract: the locator
// reads the real thread id from the rollout session_meta, and the
// manager must pass that same id to `codex exec resume`.
func TestBuildArgvResumeUsesThreadID(t *testing.T) {
	argv := buildArgv("codex", DefaultExecArgs, "thread-abc")
	if argv[0] != "codex" || argv[1] != "exec" || argv[2] != "resume" {
		t.Fatalf("argv prefix = %v, want codex exec resume", argv[:3])
	}
	if len(argv) < 2 || argv[len(argv)-2] != "thread-abc" || argv[len(argv)-1] != "-" {
		t.Fatalf("argv tail = %v, want <thread-abc> -", argv)
	}
	if !strings.Contains(strings.Join(argv, " "), "--json") {
		t.Fatalf("argv = %v, want the exec flags preserved", argv)
	}
}
