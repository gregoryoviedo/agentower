package antigravity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeAgy compiles the testdata/fakeagy binary once.
func buildFakeAgy(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fakeagy")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakeagy.go")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakeagy: %v\n%s", err, out)
	}
	return bin
}

func TestManagerSendPromptCreatesAndResumes(t *testing.T) {
	bin := buildFakeAgy(t)
	mgr := NewManager(bin, 4102)
	mgr.MarkStarted(t.TempDir())

	sessionID := mgr.NewSessionID()
	reply, err := mgr.SendPrompt(context.Background(), sessionID, "hola")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "fakeagy reply to: hola") {
		t.Fatalf("unexpected reply %q", reply)
	}
	mgr.mu.Lock()
	convo := mgr.convos[sessionID]
	mgr.mu.Unlock()
	if convo == "" {
		t.Fatal("expected conversation id to be recorded")
	}
	if got := mgr.PublicSessionID(convo); got != sessionID {
		t.Fatalf("PublicSessionID = %q, want %q", got, sessionID)
	}
}

func TestManagerRequiresWorkdir(t *testing.T) {
	mgr := NewManager("agy", 4102)
	if _, err := mgr.SendPrompt(context.Background(), mgr.NewSessionID(), "x"); err == nil {
		t.Fatal("expected error when no working directory is set")
	}
}

func TestBuildArgv(t *testing.T) {
	argv := buildArgv("agy", DefaultExecArgs, "", "hola mundo")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-p") || !strings.Contains(joined, "hola mundo") {
		t.Fatalf("new argv = %v", argv)
	}
	if !strings.Contains(joined, "--output-format stream-json") {
		t.Fatalf("missing stream-json: %v", argv)
	}
	argv = buildArgv("agy", DefaultExecArgs, "conv-1", "hola")
	if !strings.Contains(strings.Join(argv, " "), "--conversation conv-1") {
		t.Fatalf("resume argv = %v", argv)
	}
}
