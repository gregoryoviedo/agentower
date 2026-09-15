package kiro

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildFakeKiro compiles the fake ACP server fixture the same way the
// adapter tests do.
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

func TestSendPromptReturnsAssistantReply(t *testing.T) {
	m := NewManager(buildFakeKiro(t), 4099)
	m.MarkStarted(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reply, err := m.SendPrompt(ctx, "session-1", "hola")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if want := "fakekiro reply: hola"; reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

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

func TestHasSessionReflectsCreatedSessions(t *testing.T) {
	m := NewManager(buildFakeKiro(t), 4099)
	dir := t.TempDir()
	m.MarkStarted(dir)
	if m.HasSession("ghost") {
		t.Fatal("HasSession(ghost) = true on a fresh manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := m.CreateSession(ctx, dir)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id == "" {
		t.Fatal("CreateSession returned empty id")
	}
	if !m.HasSession(id) {
		t.Fatal("HasSession(id) = false after CreateSession")
	}
}

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
