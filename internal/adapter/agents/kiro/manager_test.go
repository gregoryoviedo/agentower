package kiro

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildFakeKiro compiles the fixture binary the same way the adapter
// tests do. The fixture emits a single assistant turn then exits so
// the manager's parser can be exercised end-to-end.
func buildFakeKiro(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakekiro")
	src, err := os.ReadFile("testdata/fakekiro.go")
	if err != nil {
		t.Fatalf("read fakekiro source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakekiro: %v\n%s", err, out)
	}
	return bin
}

// TestSendPromptReturnsAssistantReply is the happy path the rest of
// the suite relies on: a single round-trip must return the assistant
// text in the form the parser accumulates (one event with one text
// part) without losing the framing.
func TestSendPromptReturnsAssistantReply(t *testing.T) {
	m := NewManager(buildFakeKiro(t), 4099)
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
	if want := "fakekiro reply: hola"; reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

// TestEnsureSessionRespawnsAfterSubprocessExits documents the
// subprocess lifecycle: the Kiro CLI consumes one prompt per
// subprocess because the adapter closes stdin after writing. A
// second prompt for the same session id must therefore trigger a
// fresh spawn and succeed.
func TestEnsureSessionRespawnsAfterSubprocessExits(t *testing.T) {
	m := NewManager(buildFakeKiro(t), 4099)
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
	m := NewManager(buildFakeKiro(t), 4099)
	_, err := m.SendPrompt(context.Background(), "session-1", "hola")
	if err == nil {
		t.Fatal("SendPrompt on an unstarted manager returned nil")
	}
	if !contains(err.Error(), "working directory") {
		t.Fatalf("err = %v, want one mentioning working directory", err)
	}
}

// TestNewSessionIDIsHexShape verifies the manager hands out
// UUID-shaped ids. The wire format expects hex-only strings.
func TestNewSessionIDIsHexShape(t *testing.T) {
	m := NewManager("kiro", 4099)
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
	m := NewManager(buildFakeKiro(t), 4099)
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
	m := NewManager("kiro", 4099)
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

// TestAdapterListMessagesReturnsCapabilitiesLimited mirrors the
// documented limitation: Kiro does not expose a session-history
// format today, so the adapter returns ErrAgentCapabilitiesLimited so
// the Telegram UI hides the /messages button.
func TestAdapterListMessagesReturnsCapabilitiesLimited(t *testing.T) {
	m := NewManager("kiro", 4099)
	m.MarkStarted(t.TempDir())
	adapter := NewAdapter(m)
	_, err := adapter.ListMessages(context.Background(), "x")
	if err == nil {
		t.Fatal("ListMessages returned nil; want ErrAgentCapabilitiesLimited")
	}
	if !contains(err.Error(), "session history") {
		t.Fatalf("err = %v, want one mentioning session history", err)
	}
}

// TestAdapterFileStatusReturnsCapabilitiesLimited covers the same
// limitation for the file diff path.
func TestAdapterFileStatusReturnsCapabilitiesLimited(t *testing.T) {
	m := NewManager("kiro", 4099)
	m.MarkStarted(t.TempDir())
	adapter := NewAdapter(m)
	_, err := adapter.FileStatus(context.Background(), "x")
	if err == nil {
		t.Fatal("FileStatus returned nil; want ErrAgentCapabilitiesLimited")
	}
	if !contains(err.Error(), "file diff") {
		t.Fatalf("err = %v, want one mentioning file diff", err)
	}
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
