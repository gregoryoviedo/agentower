package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeCodex compiles the testdata/fakecodex binary once and returns
// its path.
func buildFakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakecodex")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakecodex.go")
	cmd.Dir = mustPackageDir(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakecodex: %v\n%s", err, out)
	}
	return bin
}

func mustPackageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestManagerSendPromptCreatesAndResumes(t *testing.T) {
	bin := buildFakeCodex(t)
	workdir := t.TempDir()
	mgr := NewManager(bin, 4101)
	mgr.MarkStarted(workdir)

	sessionID := mgr.NewSessionID()
	reply, err := mgr.SendPrompt(context.Background(), sessionID, "hola")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "fakecodex reply to: hola") {
		t.Fatalf("unexpected reply %q", reply)
	}
	if !mgr.HasSession(sessionID) {
		t.Fatalf("expected session %q to be tracked", sessionID)
	}
	mgr.mu.Lock()
	thread := mgr.threads[sessionID]
	mgr.mu.Unlock()
	if thread == "" {
		t.Fatalf("expected real thread id to be recorded")
	}
	if got := mgr.PublicSessionID(thread); got != sessionID {
		t.Fatalf("PublicSessionID = %q, want %q", got, sessionID)
	}

	// Second prompt resumes the same thread.
	reply, err = mgr.SendPrompt(context.Background(), sessionID, "otra vez")
	if err != nil {
		t.Fatalf("resume SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "otra vez") {
		t.Fatalf("unexpected resumed reply %q", reply)
	}
}

func TestManagerRequiresWorkdir(t *testing.T) {
	mgr := NewManager("codex", 4101)
	if _, err := mgr.SendPrompt(context.Background(), mgr.NewSessionID(), "x"); err == nil {
		t.Fatal("expected error when no working directory is set")
	}
}

func TestBuildArgv(t *testing.T) {
	argv := buildArgv("codex", DefaultExecArgs, "")
	want := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Fatalf("new argv = %v, want %v", argv, want)
	}
	argv = buildArgv("codex", DefaultExecArgs, "abc")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "exec resume") || !strings.HasSuffix(joined, " abc -") {
		t.Fatalf("resume argv = %v", argv)
	}
}
