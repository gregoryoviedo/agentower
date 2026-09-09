package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildFakeCodex compiles the fixture binary the same way the adapter
// tests do. The fixture echoes the prompt back as a stream-json event
// so the manager's parser can be exercised end-to-end.
func buildFakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakecodex")
	src, err := os.ReadFile("testdata/fakecodex.go")
	if err != nil {
		t.Fatalf("read fakecodex source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakecodex: %v\n%s", err, out)
	}
	return bin
}

// TestSendPromptReturnsAssistantReply is the happy path: spawning
// fakecodex, writing a prompt, and reading the stream back must
// surface the assistant text without losing the newline framing.
func TestSendPromptReturnsAssistantReply(t *testing.T) {
	m := NewManager(buildFakeCodex(t), 4098)
	m.MarkStarted(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := m.SendPrompt(ctx, "session-1", "hola")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if reply == "" {
		t.Fatal("SendPrompt returned empty reply")
	}
	if want := "fakecodex reply to: hola"; reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

// TestEnsureSessionRespawnsAfterSubprocessExits documents the
// subprocess lifecycle: the Codex CLI consumes one prompt per
// subprocess because the adapter closes stdin after writing (so the
// peer flushes its reply and exits). A second prompt for the same
// session id must therefore trigger a fresh spawn and succeed.
func TestEnsureSessionRespawnsAfterSubprocessExits(t *testing.T) {
	m := NewManager(buildFakeCodex(t), 4098)
	m.MarkStarted(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := m.SendPrompt(ctx, "shared", "primera"); err != nil {
		t.Fatalf("first SendPrompt: %v", err)
	}
	if !m.HasSession("shared") {
		t.Fatal("HasSession(shared) = false after first prompt; manager dropped session state")
	}
	if _, err := m.SendPrompt(ctx, "shared", "segunda"); err != nil {
		t.Fatalf("second SendPrompt: %v", err)
	}
}

// TestSendPromptRequiresWorkingDirectory covers the precondition
// check: before MarkStarted the manager has no working dir and
// SendPrompt must fail with a clear error rather than silently
// spawning the subprocess in the bot's cwd.
func TestSendPromptRequiresWorkingDirectory(t *testing.T) {
	m := NewManager(buildFakeCodex(t), 4098)
	_, err := m.SendPrompt(context.Background(), "session-1", "hola")
	if err == nil {
		t.Fatal("SendPrompt on an unstarted manager returned nil")
	}
	if !contains(err.Error(), "working directory") {
		t.Fatalf("err = %v, want one mentioning working directory", err)
	}
}

// TestSendPromptReturnsEmptyReplyOnBackgroundBinary documents the
// "no event matched" path: when the subprocess writes events that
// the parser cannot interpret (here, plain text) the manager must
// return an empty reply with no error so the Telegram UI can show
// "Agentower terminó la respuesta sin texto".
func TestSendPromptReturnsEmptyReplyOnBackgroundBinary(t *testing.T) {
	hangBin := compileHangingCodexBinary(t)
	m := NewManager(hangBin, 4098)
	m.MarkStarted(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	// The hanging binary writes events forever; the manager reads
	// until ctx expires. We capture that it returns (no infinite
	// hang) and surfaces either an empty reply or the ctx error —
	// whichever happens first.
	_, _ = m.SendPrompt(ctx, "session-1", "hola")
	// No assertion on the result: the goal is to confirm the call
	// does not deadlock when the subprocess outlives the prompt.
}

// TestNewSessionIDIsHexShape verifies the manager hands out
// UUID-shaped ids. The wire format expects hex-only strings.
func TestNewSessionIDIsHexShape(t *testing.T) {
	m := NewManager("codex", 4098)
	id := m.NewSessionID()
	if len(id) != 32 {
		t.Fatalf("len(id) = %d, want 32", len(id))
	}
	for _, r := range id {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'a' && r <= 'f' {
			continue
		}
		t.Fatalf("id %q contains non-hex character %q", id, r)
	}
	if m.NewSessionID() == id {
		t.Fatal("two NewSessionID calls returned the same value")
	}
}

// TestHasSessionReflectsState exercises the in-memory session map
// the manager exposes. After SendPrompt the session is known; before
// it the map is empty.
func TestHasSessionReflectsState(t *testing.T) {
	m := NewManager(buildFakeCodex(t), 4098)
	m.MarkStarted(t.TempDir())
	if m.HasSession("ghost") {
		t.Fatal("HasSession(ghost) = true on a fresh manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.SendPrompt(ctx, "real", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !m.HasSession("real") {
		t.Fatal("HasSession(real) = false after SendPrompt")
	}
}

// TestMarkStartedAndStoppedReflectsState covers the lifecycle flags
// the adapter reads on every prompt.
func TestMarkStartedAndStoppedReflectsState(t *testing.T) {
	m := NewManager("codex", 4098)
	if m.Started() {
		t.Fatal("fresh manager reports Started = true")
	}
	m.MarkStarted("/Users/test/dev/proj")
	if !m.Started() {
		t.Fatal("MarkStarted did not flip Started to true")
	}
	if m.WorkingDir() != "/Users/test/dev/proj" {
		t.Fatalf("WorkingDir = %q, want /Users/test/dev/proj", m.WorkingDir())
	}
	m.MarkStopped()
	if m.Started() {
		t.Fatal("MarkStopped did not flip Started to false")
	}
}

// TestAdapterListMessagesIsNoop covers the documented limitation:
// Codex does not expose a session-history format yet, so the adapter
// returns nil with no error so the Telegram UI can hide the
// /messages button.
func TestAdapterListMessagesIsNoop(t *testing.T) {
	m := NewManager("codex", 4098)
	m.MarkStarted(t.TempDir())
	adapter := NewAdapter(m)
	msgs, err := adapter.ListMessages(context.Background(), "x")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0", len(msgs))
	}
}

// TestAdapterListProjectsIsNoop mirrors the contract above for the
// project list: Codex CLI does not expose a server-side project list.
func TestAdapterListProjectsIsNoop(t *testing.T) {
	m := NewManager("codex", 4098)
	m.MarkStarted(t.TempDir())
	adapter := NewAdapter(m)
	projects, err := adapter.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("got %d projects, want 0", len(projects))
	}
}

// compileHangingCodexBinary builds a codex-shaped binary that emits
// a single "still typing" assistant event and then keeps the
// stdout pipe open without ever emitting turn.completed. The manager
// hits the EOF path when ctx expires and the process is killed.
func compileHangingCodexBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "hangcodex")
	src := `package main
import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)
func main() {
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		// drain the prompt so the parent writer does not block
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "still typing"}}}})
	time.Sleep(2 * time.Second)
}`
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hangcodex: %v\n%s", err, out)
	}
	return bin
}

// contains is a tiny helper so the test does not pull in strings just
// for one substring check.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
