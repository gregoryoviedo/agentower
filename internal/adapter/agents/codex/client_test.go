package codex_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/codex"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// buildFakeCodex compiles a stand-in for the `codex` CLI that emits
// the documented --json stream so the adapter can be exercised in
// CI without depending on a real codex install.
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

func TestAdapterKindAndDisplayName(t *testing.T) {
	bin := buildFakeCodex(t)
	mgr := codex.NewManager(bin, 4098)
	mgr.MarkStarted(t.TempDir())
	adapter := codex.NewAdapter(mgr)
	if adapter.Kind() != domain.AgentCodex {
		t.Fatalf("Kind()=%s", adapter.Kind())
	}
	if adapter.DisplayName() != "Codex" {
		t.Fatalf("DisplayName()=%s", adapter.DisplayName())
	}
}

func TestAdapterHealthReportsRunning(t *testing.T) {
	bin := buildFakeCodex(t)
	mgr := codex.NewManager(bin, 4098)
	mgr.MarkStarted(t.TempDir())
	adapter := codex.NewAdapter(mgr)
	health, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !health.Healthy {
		t.Fatal("expected healthy=true after MarkStarted")
	}
}

func TestAdapterSendPromptRoundTrip(t *testing.T) {
	bin := buildFakeCodex(t)
	mgr := codex.NewManager(bin, 4098)
	mgr.MarkStarted(t.TempDir())
	adapter := codex.NewAdapter(mgr)
	session, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := adapter.SendPrompt(ctx, session.ID, "hola codex")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "hola codex") {
		t.Fatalf("reply = %q, expected echo of the prompt", reply)
	}
	if !strings.Contains(reply, "fakecodex reply") {
		t.Fatalf("reply = %q, missing fakecodex prefix", reply)
	}
}

func TestAdapterRevertReturnsCapabilitiesLimited(t *testing.T) {
	bin := buildFakeCodex(t)
	mgr := codex.NewManager(bin, 4098)
	mgr.MarkStarted(t.TempDir())
	adapter := codex.NewAdapter(mgr)
	err := adapter.Revert(context.Background(), "x")
	if err == nil {
		t.Fatal("Revert must return an error")
	}
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("Revert error = %v, expected ErrAgentCapabilitiesLimited", err)
	}
}
