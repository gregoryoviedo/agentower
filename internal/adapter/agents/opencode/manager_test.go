package agents_opencode_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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

// buildFakeBin compiles a stand-in for the `opencode` binary so the manager
// tests don't require the real CLI in $PATH (CI runners don't ship it).
func buildFakeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-opencode")
	src, err := os.ReadFile("testdata/fakebin.go")
	if err != nil {
		t.Fatalf("read fakebin source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatalf("write fakebin source: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakebin: %v\n%s", err, out)
	}
	return bin
}

func TestManagerStartInWorkingDirAndStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/global/health") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"healthy":true,"version":"x"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	bin := buildFakeBin(t)
	manager := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin:    bin,
		Port:   portFromURL(t, srv.URL),
		Logger: newDiscardLogger(),
	})
	dir := t.TempDir()

	if err := manager.Start(context.Background(), dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !manager.StartedSubprocess() {
		t.Fatal("StartedSubprocess=false after Start")
	}
	if got := manager.WorkingDir(); got != dir {
		t.Fatalf("WorkingDir=%q, want %q", got, dir)
	}

	if err := manager.Start(context.Background(), dir); err != nil {
		t.Fatalf("Start (re-issue): %v", err)
	}
	if !manager.StartedSubprocess() {
		t.Fatal("StartedSubprocess=false after second Start")
	}

	manager.Stop()
	if manager.StartedSubprocess() {
		t.Fatal("StartedSubprocess=true after Stop")
	}
	if got := manager.WorkingDir(); got != "" {
		t.Fatalf("WorkingDir=%q, want empty", got)
	}
}

func TestManagerStartRejectsEmptyWorkingDir(t *testing.T) {
	bin := buildFakeBin(t)
	manager := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin:    bin,
		Port:   4096,
		Logger: newDiscardLogger(),
	})
	if err := manager.Start(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty working dir")
	}
}

func TestManagerStartRejectsNonDirectory(t *testing.T) {
	bin := buildFakeBin(t)
	manager := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin:    bin,
		Port:   4096,
		Logger: newDiscardLogger(),
	})
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), path); err == nil {
		t.Fatal("expected error for file-as-working-dir")
	}
}

// TestManagerAdoptsAlreadyRunningServer verifies that Start prefers an
// external `opencode serve` over spawning its own subprocess. The fakebin
// exits as soon as the manager tries to launch it, so a successful Start
// without ever executing the binary proves we adopted the httptest server.
func TestManagerAdoptsAlreadyRunningServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/global/health") {
			_, _ = io.WriteString(w, `{"healthy":true,"version":"x"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Point the fakebin at a path that exists but cannot be executed so any
	// attempt to spawn would surface immediately as an error. The adopt
	// path must short-circuit before reaching exec.LookPath.
	missingBin := filepath.Join(t.TempDir(), "does-not-exist")
	manager := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin:    missingBin,
		Port:   portFromURL(t, srv.URL),
		Logger: newDiscardLogger(),
	})

	dir := t.TempDir()
	if err := manager.Start(context.Background(), dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !manager.StartedSubprocess() {
		t.Fatal("StartedSubprocess=false after adopting")
	}
	if manager.OwnsSubprocess() {
		t.Fatal("OwnsSubprocess=true; manager should NOT own the adopted server")
	}
	if got := manager.WorkingDir(); got != dir {
		t.Fatalf("WorkingDir=%q, want %q", got, dir)
	}

	// Stop must not panic and must clear the adopted flag.
	manager.Stop()
	if manager.StartedSubprocess() {
		t.Fatal("StartedSubprocess=true after Stop")
	}
	if manager.OwnsSubprocess() {
		t.Fatal("OwnsSubprocess=true after Stop")
	}
}
