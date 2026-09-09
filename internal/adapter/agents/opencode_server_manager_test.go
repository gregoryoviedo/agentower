package agents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// buildFakeOpenCodeBin compiles the same stand-in the opencode
// manager tests use so the wrapper tests can exercise the real
// subprocess path without depending on the user's opencode install.
func buildFakeOpenCodeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-opencode")
	src, err := os.ReadFile("opencode/testdata/fakebin.go")
	if err != nil {
		t.Fatalf("read fakebin source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakebin: %v\n%s", err, out)
	}
	return bin
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// portFromURL extracts the port an httptest server is listening on.
func portFromURL(t *testing.T, rawURL string) int {
	t.Helper()
	idx := strings.LastIndex(rawURL, ":")
	if idx < 0 {
		t.Fatalf("cannot extract port from %q", rawURL)
	}
	port, err := strconv.Atoi(rawURL[idx+1:])
	if err != nil {
		t.Fatalf("parse port from %q: %v", rawURL, err)
	}
	return port
}

// reserveFreePort asks the kernel for an unused TCP port and returns
// it. There is a small race window between Close and the subprocess
// binding, but in practice tests run on hosts that don't recycle ports
// that fast.
func reserveFreePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// TestOpenCodeServerManagerStartAcceptsOnlyOpenCode verifies the
// per-kind gating: calling Start with any agent kind other than
// opencode must return ErrAgentUnavailable without touching the
// underlying manager.
func TestOpenCodeServerManagerStartAcceptsOnlyOpenCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"version":"x"}`))
	}))
	defer srv.Close()
	mgr := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin: buildFakeOpenCodeBin(t), Port: portFromURL(t, srv.URL), Logger: discardLogger(),
	})
	sm := NewOpenCodeServerManager(mgr)
	for _, kind := range []domain.AgentKind{
		domain.AgentClaude, domain.AgentCodex,
		domain.AgentKiro, domain.AgentCopilot,
	} {
		if err := sm.Start(context.Background(), kind, t.TempDir()); !errors.Is(err, domain.ErrAgentUnavailable) {
			t.Errorf("Start(%s) = %v, want ErrAgentUnavailable", kind, err)
		}
	}
}

// TestOpenCodeServerManagerDelegatesToUnderlyingManager ensures the
// wrapper passes through working dir, context and the opencode kind
// unchanged. We exercise it against a real Manager backed by the
// fakebin so the call actually spawns, then assert via the public
// WorkingDir/StartedSubprocess surface.
func TestOpenCodeServerManagerDelegatesToUnderlyingManager(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"version":"x"}`))
	}))
	defer srv.Close()
	mgr := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin: buildFakeOpenCodeBin(t), Port: portFromURL(t, srv.URL), Logger: discardLogger(),
	})
	sm := NewOpenCodeServerManager(mgr)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sm.Start(ctx, domain.AgentOpenCode, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sm.StartedSubprocess(domain.AgentOpenCode) {
		t.Fatal("StartedSubprocess(OpenCode) = false after a successful Start")
	}
	if sm.StartedSubprocess(domain.AgentClaude) {
		t.Fatal("StartedSubprocess(Claude) = true; wrapper must only report opencode")
	}
	if sm.WorkingDir(domain.AgentOpenCode) == "" {
		t.Fatal("WorkingDir(OpenCode) is empty after Start")
	}
	if got := sm.WorkingDir(domain.AgentCodex); got != "" {
		t.Fatalf("WorkingDir(Codex) = %q, want empty", got)
	}
	sm.Stop(domain.AgentOpenCode)
	if sm.StartedSubprocess(domain.AgentOpenCode) {
		t.Fatal("StartedSubprocess(OpenCode) = true after Stop")
	}
	// Stop on a non-opencode kind must be a no-op (already stopped).
	sm.Stop(domain.AgentClaude)
	sm.StopAll()
}

// TestOpenCodeServerManagerPropagatesStartError covers the failure
// path: when the underlying manager returns an error (here, empty
// working directory), the wrapper must surface it unchanged. The bot
// handler relies on this to render a clear failure message back to
// the user.
func TestOpenCodeServerManagerPropagatesStartError(t *testing.T) {
	mgr := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin: buildFakeOpenCodeBin(t), Port: 4096, Logger: discardLogger(),
	})
	sm := NewOpenCodeServerManager(mgr)
	err := sm.Start(context.Background(), domain.AgentOpenCode, "")
	if err == nil {
		t.Fatal("Start with empty working dir returned nil; want error")
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("err = %v, want one mentioning working directory", err)
	}
}

// TestOpenCodeServerManagerOwnsSubprocessReflectsUnderlying is the
// contract the macOS wrapper depends on: only the opencode slot may
// report "owning" a subprocess; other kinds must always return false
// because the wrapper does not manage them. With no external server
// running the manager must own the subprocess it spawned.
func TestOpenCodeServerManagerOwnsSubprocessReflectsUnderlying(t *testing.T) {
	// Reserve a free port for fakebin to bind to so the manager's
	// adoption probe fails and the subprocess path runs.
	port := reserveFreePort(t)
	mgr := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin: buildFakeOpenCodeBin(t), Port: port, Logger: discardLogger(),
	})
	sm := NewOpenCodeServerManager(mgr)
	if sm.OwnsSubprocess(domain.AgentOpenCode) {
		t.Fatal("OwnsSubprocess(OpenCode) = true before Start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sm.Start(ctx, domain.AgentOpenCode, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !sm.OwnsSubprocess(domain.AgentOpenCode) {
		t.Fatal("OwnsSubprocess(OpenCode) = false after Start")
	}
	if sm.OwnsSubprocess(domain.AgentClaude) {
		t.Fatal("OwnsSubprocess(Claude) = true; wrapper does not own other slots")
	}
}

// TestOpenCodeServerManagerDoesNotOwnAdoptedServer covers the
// adoption path: when an `opencode serve` is already answering on
// the loopback port, the wrapper must report StartedSubprocess=true
// but OwnsSubprocess=false so a subsequent Stop does not kill the
// user's own server.
func TestOpenCodeServerManagerDoesNotOwnAdoptedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"version":"x"}`))
	}))
	defer srv.Close()
	mgr := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin: buildFakeOpenCodeBin(t), Port: portFromURL(t, srv.URL), Logger: discardLogger(),
	})
	sm := NewOpenCodeServerManager(mgr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sm.Start(ctx, domain.AgentOpenCode, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sm.StartedSubprocess(domain.AgentOpenCode) {
		t.Fatal("StartedSubprocess(OpenCode) = false after adopting")
	}
	if sm.OwnsSubprocess(domain.AgentOpenCode) {
		t.Fatal("OwnsSubprocess(OpenCode) = true; manager should not own adopted server")
	}
}
