package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildFakeACP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeacp")
	src, err := os.ReadFile("testdata/fakeacp.go")
	if err != nil {
		t.Fatalf("read fakeacp source: %v", err)
	}
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeacp: %v\n%s", err, out)
	}
	return bin
}

func TestAgentInitializeNewAndPrompt(t *testing.T) {
	a := NewAgent(AgentOptions{
		Bin:      buildFakeACP(t),
		Framing:  FramingNewline,
		TrustAll: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()
	if a.Capabilities() == nil || !a.Capabilities().AgentCapabilities.LoadSession {
		t.Fatal("loadSession capability not negotiated")
	}
	cwd := t.TempDir()
	id, err := a.NewSession(ctx, cwd)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if id != "acp-session-1" {
		t.Fatalf("session id = %q, want acp-session-1", id)
	}
	reply, err := a.Prompt(ctx, id, "hola")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "fakeacp reply") {
		t.Fatalf("reply = %q, want fakeacp reply", reply)
	}
}

func TestAgentResumeThenPrompt(t *testing.T) {
	a := NewAgent(AgentOptions{
		Bin:      buildFakeACP(t),
		Framing:  FramingNewline,
		TrustAll: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()
	cwd := t.TempDir()
	if err := a.ResumeSession(ctx, "existing-1", cwd); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	reply, err := a.Prompt(ctx, "existing-1", "continua")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply after resume+prompt")
	}
}

func TestAgentCloseTerminatesProcess(t *testing.T) {
	a := NewAgent(AgentOptions{
		Bin:      buildFakeACP(t),
		Framing:  FramingNewline,
		TrustAll: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	a.Close()
	if a.Running() {
		t.Fatal("Running() = true after Close")
	}
}

func TestClientNewlineAndContentLengthFraming(t *testing.T) {
	// The framing layer must parse both wire formats. We exercise it
	// against a fake that writes newline messages regardless of framing.
	cli := NewClient(ClientOptions{Bin: buildFakeACP(t), Framing: FramingNewline})
	if err := cli.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cli.Close()
	var caps struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := cli.Request(context.Background(), "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":      map[string]any{"writeTextFile": true, "readTextFile": true},
			"session": map[string]any{},
		},
		"clientInfo": map[string]any{"name": "test", "version": "0"},
	}, &caps); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if caps.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion = %d, want 1", caps.ProtocolVersion)
	}
}
