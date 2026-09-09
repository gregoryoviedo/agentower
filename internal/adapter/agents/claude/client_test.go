package claude_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/claude"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// buildFakeClaude compiles a stand-in for the `claude` binary that
// emits the documented stream-json protocol. Used by the adapter
// tests so they don't require a real Claude Code install in CI.
func buildFakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeclaude")
	src, err := os.ReadFile("testdata/fakeclaude.go")
	if err != nil {
		t.Fatalf("read fakeclaude source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude: %v\n%s", err, out)
	}
	return bin
}

func TestAdapterKindAndDisplayName(t *testing.T) {
	bin := buildFakeClaude(t)
	mgr := claude.NewManager(bin, 4097)
	mgr.MarkStarted(t.TempDir())
	adapter := claude.NewAdapter(mgr)
	if adapter.Kind() != domain.AgentClaude {
		t.Fatalf("Kind()=%s", adapter.Kind())
	}
	if adapter.DisplayName() != "Claude" {
		t.Fatalf("DisplayName()=%s", adapter.DisplayName())
	}
}

func TestAdapterHealthReportsRunning(t *testing.T) {
	bin := buildFakeClaude(t)
	mgr := claude.NewManager(bin, 4097)
	mgr.MarkStarted(t.TempDir())
	adapter := claude.NewAdapter(mgr)
	health, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !health.Healthy {
		t.Fatal("expected healthy=true after MarkStarted")
	}
}

func TestAdapterSendPromptRoundTrip(t *testing.T) {
	bin := buildFakeClaude(t)
	dir := t.TempDir()
	mgr := claude.NewManager(bin, 4097)
	mgr.MarkStarted(dir)
	adapter := claude.NewAdapter(mgr)
	session, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := adapter.SendPrompt(ctx, session.ID, "hola claude")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "hola claude") {
		t.Fatalf("reply = %q, expected echo of the prompt", reply)
	}
	// The fakeclaude echo uses "fakeclaude reply to: <prompt>".
	if !strings.Contains(reply, "fakeclaude reply") {
		t.Fatalf("reply = %q, missing fakeclaude prefix", reply)
	}
}

func TestAdapterRevertReturnsCapabilitiesLimited(t *testing.T) {
	bin := buildFakeClaude(t)
	mgr := claude.NewManager(bin, 4097)
	mgr.MarkStarted(t.TempDir())
	adapter := claude.NewAdapter(mgr)
	err := adapter.Revert(context.Background(), "x")
	if err == nil {
		t.Fatal("Revert must return an error")
	}
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("Revert error = %v, expected ErrAgentCapabilitiesLimited", err)
	}
}
