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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

func TestAdapterListMessagesReadsDisk(t *testing.T) {
	root := t.TempDir()
	sid := "sess_test123"
	msgPath := filepath.Join(root, "sessions", "ws", sid, "messages.jsonl")
	if err := os.MkdirAll(filepath.Dir(msgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"payload":{"type":"user","content":"hola"}}`,
		`{"payload":{"type":"assistant","content":"respuesta"}}`,
	}
	if err := os.WriteFile(msgPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := kiro.NewManager("kiro", 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	adapter.SetStateDir(root)
	msgs, err := adapter.ListMessages(context.Background(), sid)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Info.Role != "user" || msgs[0].Parts[0].Text != "hola" {
		t.Fatalf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Info.Role != "assistant" || msgs[1].Parts[0].Text != "respuesta" {
		t.Fatalf("msg[1] = %+v", msgs[1])
	}
}

func TestAdapterListMessagesMissingSession(t *testing.T) {
	mgr := kiro.NewManager("kiro", 4099)
	mgr.MarkStarted(t.TempDir())
	adapter := kiro.NewAdapter(mgr)
	adapter.SetStateDir(t.TempDir())
	_, err := adapter.ListMessages(context.Background(), "ghost")
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("err = %v, want ErrNoActiveSession", err)
	}
}
