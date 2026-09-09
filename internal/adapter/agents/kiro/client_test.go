package kiro_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func buildFakeKiro(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakekiro")
	src, err := os.ReadFile("testdata/fakekiro.go")
	if err != nil {
		t.Fatalf("read fakekiro source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakekiro: %v\n%s", err, out)
	}
	return bin
}

func TestAdapterKindAndDisplayName(t *testing.T) {
	mgr := kiro.NewManager(buildFakeKiro(t), 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	if adapter.Kind() != domain.AgentKiro {
		t.Fatalf("Kind()=%s", adapter.Kind())
	}
	if adapter.DisplayName() != "Kiro" {
		t.Fatalf("DisplayName()=%s", adapter.DisplayName())
	}
}

func TestAdapterSendPromptRoundTrip(t *testing.T) {
	mgr := kiro.NewManager(buildFakeKiro(t), 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	session, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := adapter.SendPrompt(ctx, session.ID, "hola kiro")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if !strings.Contains(reply, "hola kiro") {
		t.Fatalf("reply = %q, expected echo of the prompt", reply)
	}
	if !strings.Contains(reply, "fakekiro reply") {
		t.Fatalf("reply = %q, missing fakekiro prefix", reply)
	}
}

func TestAdapterRevertReturnsCapabilitiesLimited(t *testing.T) {
	mgr := kiro.NewManager(buildFakeKiro(t), 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	err := adapter.Revert(context.Background(), "x")
	if err == nil {
		t.Fatal("Revert must return an error")
	}
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("Revert error = %v, expected ErrAgentCapabilitiesLimited", err)
	}
}

func TestAdapterFileStatusReturnsCapabilitiesLimited(t *testing.T) {
	mgr := kiro.NewManager(buildFakeKiro(t), 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	_, err := adapter.FileStatus(context.Background(), "x")
	if !errors.Is(err, domain.ErrAgentCapabilitiesLimited) {
		t.Fatalf("FileStatus err = %v, expected ErrAgentCapabilitiesLimited", err)
	}
}
