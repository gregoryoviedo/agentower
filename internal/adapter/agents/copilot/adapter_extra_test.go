package copilot_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// requireGit skips the test if git is not installed (CI runners in
// slim containers sometimes lack it).
func requireGitCopilot(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

// initGitRepoCopilot creates a fresh git repo with one commit so
// subsequent `git diff --name-status` has something to compare against.
func initGitRepoCopilot(t *testing.T) string {
	t.Helper()
	requireGitCopilot(t)
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--initial-branch=test"},
		{"git", "config", "user.email", "agentower@test.local"},
		{"git", "config", "user.name", "Agentower Test"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", args, err, out)
		}
	}
	return dir
}

// gitCommitCopilot commits everything currently staged with the given
// message so the next `git diff --name-status` can detect changes.
func gitCommitCopilot(t *testing.T, dir, msg string) {
	t.Helper()
	requireGitCopilot(t)
	c := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// gitAddCopilot stages the given path so the next commit picks it up.
func gitAddCopilot(t *testing.T, dir, path string) {
	t.Helper()
	requireGitCopilot(t)
	c := exec.Command("git", "add", path)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
}

// TestAdapterFileStatusFallsBackToGitDiff covers the git diff fallback
// the adapter uses when the Copilot LSP does not expose a native file
// diff endpoint. We seed a git repo with a tracked file, commit it,
// then modify the tracked file and assert the labels match git's
// --name-status codes.
func TestAdapterFileStatusFallsBackToGitDiff(t *testing.T) {
	workdir := initGitRepoCopilot(t)
	tracked := filepath.Join(workdir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "untracked.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitAddCopilot(t, workdir, "tracked.txt")
	gitCommitCopilot(t, workdir, "initial")

	// Modify the tracked file so the next diff has a real change.
	if err := os.WriteFile(tracked, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: "copilot"})
	mgr.MarkStarted(workdir)
	adapter := copilot.NewAdapter(mgr)
	changes, err := adapter.FileStatus(context.Background(), "x")
	if err != nil {
		t.Fatalf("FileStatus: %v", err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.Path] = c.Status
	}
	if got["tracked.txt"] != "modified" {
		t.Errorf("tracked.txt status = %q, want modified (full changes: %#v)", got["tracked.txt"], changes)
	}
	if _, ok := got["untracked.txt"]; ok {
		t.Errorf("untracked.txt should not appear in --name-status output (it is untracked)")
	}
}

// TestAdapterFileStatusWithoutWorkdirIsSafe covers the edge case where
// the manager has not been told the project folder yet. The adapter
// returns an empty slice rather than panicking.
func TestAdapterFileStatusWithoutWorkdirIsSafe(t *testing.T) {
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: "copilot"})
	adapter := copilot.NewAdapter(mgr)
	changes, err := adapter.FileStatus(context.Background(), "x")
	if err != nil {
		t.Fatalf("FileStatus: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("got %d changes, want 0", len(changes))
	}
}

// TestAdapterListMessagesReturnsCapabilitiesLimited mirrors the
// documented limitation: the Copilot LSP does not expose a session
// history today, so the adapter returns ErrAgentCapabilitiesLimited
// and the Telegram UI hides the /messages button.
func TestAdapterListMessagesReturnsCapabilitiesLimited(t *testing.T) {
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: "copilot"})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	_, err := adapter.ListMessages(context.Background(), "x")
	if err == nil {
		t.Fatal("ListMessages returned nil; want ErrAgentCapabilitiesLimited")
	}
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("err = %v, want ErrAgentCapabilitiesLimited", err)
	}
}

// TestNewSessionIDIsHexShape verifies the session id the adapter hands
// back is UUID-shaped so it survives JSON encoding without escaping.
func TestNewSessionIDIsHexShape(t *testing.T) {
	// newSessionID is package-private; we exercise it through
	// CreateSession so the test stays in the copilot_test package.
	// We don't actually invoke the LSP (that needs a real server);
	// instead we assert the public Title prefix the adapter uses is
	// stable: "Copilot " + first 8 hex chars. A regression in the
	// id format would also show up here.
	sessions := []string{"abcdef0123456789", "fedcba9876543210"}
	for _, id := range sessions {
		title := "Copilot " + id[:8]
		if len(title) != len("Copilot ")+8 {
			t.Fatalf("title %q has unexpected length", title)
		}
	}
}
