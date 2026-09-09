package copilot_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// buildFakeCopilot compiles the JSON-RPC fixture that speaks the
// subset of LSP the adapter needs (initialize / initialized /
// didOpen / didChange / inlineCompletion).
func buildFakeCopilot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakecopilot")
	src, err := os.ReadFile("testdata/fakecopilot.go")
	if err != nil {
		t.Fatalf("read fakecopilot source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakecopilot: %v\n%s", err, out)
	}
	return bin
}

func TestAdapterKindAndDisplayName(t *testing.T) {
	bin := buildFakeCopilot(t)
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: bin})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	if adapter.Kind() != domain.AgentCopilot {
		t.Fatalf("Kind()=%s", adapter.Kind())
	}
	if adapter.DisplayName() != "GitHub Copilot" {
		t.Fatalf("DisplayName()=%s", adapter.DisplayName())
	}
}

func TestAdapterHealthReportsRunning(t *testing.T) {
	bin := buildFakeCopilot(t)
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: bin})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	health, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !health.Healthy {
		t.Fatal("expected healthy=true after MarkStarted")
	}
}

func TestAdapterSendPromptRoundTrip(t *testing.T) {
	bin := buildFakeCopilot(t)
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: bin})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	session, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := adapter.SendPrompt(ctx, session.ID, "hola copilot")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if reply != "fakecopilot reply" {
		t.Fatalf("reply = %q, want %q", reply, "fakecopilot reply")
	}
}

func TestAdapterRevertReturnsCapabilitiesLimited(t *testing.T) {
	bin := buildFakeCopilot(t)
	mgr := copilot.NewManager(copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: bin})
	mgr.MarkStarted(t.TempDir())
	adapter := copilot.NewAdapter(mgr)
	err := adapter.Revert(context.Background(), "x")
	if err == nil {
		t.Fatal("Revert must return an error")
	}
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("Revert error = %v, expected ErrAgentCapabilitiesLimited", err)
	}
}
