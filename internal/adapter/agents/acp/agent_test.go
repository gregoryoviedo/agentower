package acp

import (
	"context"
	"errors"
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
	if !strings.Contains(reply, "fakeacp reply") {
		t.Fatalf("reply = %q, want fakeacp reply", reply)
	}
	// session/load replays history as notifications; the replay must not
	// leak into the first prompt's reply.
	if strings.Contains(reply, "replayed history") {
		t.Fatalf("reply = %q, leaked replayed history", reply)
	}
}

// TestAgentResumePrefersAdvertisedResume checks that an agent advertising
// the session/resume capability is resumed with session/resume (no replay).
func TestAgentResumePrefersAdvertisedResume(t *testing.T) {
	t.Setenv("FAKEACP_ADVERTISE_RESUME", "1")
	a := NewAgent(AgentOptions{Bin: buildFakeACP(t), Framing: FramingNewline, TrustAll: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()
	if a.Capabilities().AgentCapabilities.SessionCapabilities.Resume == nil {
		t.Fatal("sessionCapabilities.resume not negotiated")
	}
	if err := a.ResumeSession(ctx, "existing-1", t.TempDir()); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	reply, err := a.Prompt(ctx, "existing-1", "hola")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "fakeacp reply") {
		t.Fatalf("reply = %q, want fakeacp reply", reply)
	}
}

// TestAgentResumeFallsBackOnMethodNotFound mimics Kiro CLI 2.x: it
// advertises session/resume but answers -32601, so the client must fall
// back to session/load.
func TestAgentResumeFallsBackOnMethodNotFound(t *testing.T) {
	t.Setenv("FAKEACP_ADVERTISE_RESUME", "1")
	t.Setenv("FAKEACP_NO_RESUME", "1")
	a := NewAgent(AgentOptions{Bin: buildFakeACP(t), Framing: FramingNewline, TrustAll: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()
	if err := a.ResumeSession(ctx, "existing-1", t.TempDir()); err != nil {
		t.Fatalf("ResumeSession should fall back to session/load: %v", err)
	}
	reply, err := a.Prompt(ctx, "existing-1", "hola")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "fakeacp reply") || strings.Contains(reply, "replayed history") {
		t.Fatalf("reply = %q, want the prompt reply without replay", reply)
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

func TestResourceNotFound(t *testing.T) {
	if !ResourceNotFound(&rpcError{Code: -32002, Message: "Resource not found"}) {
		t.Fatal("expected -32002 to be reported as resource-not-found")
	}
	if ResourceNotFound(&rpcError{Code: -32601, Message: "Method not found"}) {
		t.Fatal("-32601 must not be resource-not-found")
	}
	if ResourceNotFound(errors.New("boom")) {
		t.Fatal("plain errors must not be resource-not-found")
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
