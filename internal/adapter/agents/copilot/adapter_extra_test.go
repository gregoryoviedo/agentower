package copilot_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
)

// requireGitCopilot skips the test if git is not installed or does not
// actually run (e.g. an unaccepted Xcode license breaks /usr/bin/git).
func requireGitCopilot(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if out, err := exec.Command("git", "--version").CombinedOutput(); err != nil {
		t.Skipf("git unusable: %v (%s)", err, out)
	}
}

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

func gitCommitCopilot(t *testing.T, dir, msg string) {
	t.Helper()
	requireGitCopilot(t)
	c := exec.Command("git", "commit", "-m", msg, "--allow-empty")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

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
// used when Copilot does not expose a native file diff endpoint.
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
		t.Errorf("untracked.txt should not appear in --name-status output")
	}
}

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

// TestAdapterListMessagesReadsDisk seeds a session-store.db with a
// turns row and asserts the adapter surfaces the user/assistant pair.
func TestAdapterListMessagesReadsDisk(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "session-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE turns (
		id INTEGER PRIMARY KEY,
		session_id TEXT NOT NULL,
		turn_index INTEGER,
		user_message TEXT,
		assistant_response TEXT,
		timestamp TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO turns (session_id, turn_index, user_message, assistant_response, timestamp) VALUES (?, ?, ?, ?, ?)`,
		"sess-1", 0, "hola copilot", "respuesta copilot", "2026-09-15T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: "copilot"})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	adapter.SetStateDir(root)
	msgs, err := adapter.ListMessages(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Info.Role != "user" || msgs[0].Parts[0].Text != "hola copilot" {
		t.Fatalf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Info.Role != "assistant" || msgs[1].Parts[0].Text != "respuesta copilot" {
		t.Fatalf("msg[1] = %+v", msgs[1])
	}
}
