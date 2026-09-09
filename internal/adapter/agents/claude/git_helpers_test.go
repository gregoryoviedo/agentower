package claude

import (
	"os/exec"
	"testing"
)

// requireGit skips the test if git is not installed (CI runners in
// slim containers sometimes lack it).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

// initGitRepo creates a fresh git repo with one commit so subsequent
// `git diff --name-status` has something to compare against.
func initGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
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

// gitCommit commits everything currently staged with the given
// message so the next `git diff --name-status` can detect changes.
func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	requireGit(t)
	c := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// gitAdd stages the given path so the next commit picks it up.
func gitAdd(t *testing.T, dir, path string) {
	t.Helper()
	requireGit(t)
	c := exec.Command("git", "add", path)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
}
