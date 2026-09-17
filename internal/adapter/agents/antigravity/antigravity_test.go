package antigravity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// writeTranscript writes a transcript_full.jsonl for a conversation.
func writeTranscript(t *testing.T, root, id string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, "brain", id, ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript_full.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeHistory appends a history.jsonl prompt recall log.
func writeHistory(t *testing.T, root string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocatorPicksFreshestAcrossRoots(t *testing.T) {
	base := t.TempDir()
	cli := filepath.Join(base, "antigravity-cli")
	ide := filepath.Join(base, "antigravity")

	writeHistory(t, cli, []string{
		`{"display":"viejo","timestamp":1000,"workspace":"/tmp/cli","conversationId":"conv-cli"}`,
	})
	writeHistory(t, ide, []string{
		`{"display":"nuevo","timestamp":2000,"workspace":"/tmp/ide","conversationId":"conv-ide"}`,
	})
	writeTranscript(t, ide, "conv-ide", []string{
		`{"step_index":1,"type":"PLANNER_RESPONSE","source":"model","status":"DONE","content":"respuesta del IDE"}`,
	})

	loc, err := NewSessionLocator(SessionLocatorOptions{StateDir: base})
	if err != nil {
		t.Fatal(err)
	}
	as, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if as.SessionID != "conv-ide" {
		t.Fatalf("SessionID = %q, want conv-ide", as.SessionID)
	}
	if as.Preview != "respuesta del IDE" {
		t.Fatalf("Preview = %q", as.Preview)
	}
}

func TestLocatorNoSessions(t *testing.T) {
	loc, err := NewSessionLocator(SessionLocatorOptions{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loc.Locate(context.Background()); err == nil {
		t.Fatal("expected ErrNoActiveSession")
	}
}

func TestAdapterListMessages(t *testing.T) {
	base := t.TempDir()
	cli := filepath.Join(base, "antigravity-cli")
	writeTranscript(t, cli, "conv-1", []string{
		`{"step_index":0,"type":"USER_INPUT","source":"user","content":"hola"}`,
		`{"step_index":3,"type":"PLANNER_RESPONSE","source":"model","content":"qué tal"}`,
		`{"step_index":4,"type":"CONVERSATION_HISTORY","source":"system"}`,
	})

	mgr := NewManager("agy", 4102)
	mgr.MarkStarted("/tmp/proj")
	adapter := NewAdapter(mgr)
	adapter.SetStateDir(base)

	msgs, err := adapter.ListMessages(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Info.Role != "user" || msgs[1].Info.Role != "assistant" {
		t.Fatalf("unexpected roles %q/%q", msgs[0].Info.Role, msgs[1].Info.Role)
	}
}

func TestAdapterCreateSessionIsSynthetic(t *testing.T) {
	adapter := NewAdapter(NewManager("agy", 4102))
	s, err := adapter.CreateSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !isSynthetic(s.ID) {
		t.Fatalf("expected synthetic id, got %q", s.ID)
	}
}

// TestLocatorTagsSourceByProductRoot makes sure the locator reports
// whether the freshest conversation came from the CLI (resumable by
// `agy`) or the IDE (read-only for the bot).
func TestLocatorTagsSourceByProductRoot(t *testing.T) {
	base := t.TempDir()
	cli := filepath.Join(base, "antigravity-cli")
	ide := filepath.Join(base, "antigravity")
	writeHistory(t, cli, []string{
		`{"display":"cli","timestamp":1000,"workspace":"/tmp/cli","conversationId":"conv-cli"}`,
	})
	writeHistory(t, ide, []string{
		`{"display":"ide","timestamp":2000,"workspace":"/tmp/ide","conversationId":"conv-ide"}`,
	})

	loc, err := NewSessionLocator(SessionLocatorOptions{StateDir: base})
	if err != nil {
		t.Fatal(err)
	}
	as, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if as.Source != "ide" {
		t.Fatalf("source = %q, want ide", as.Source)
	}
}

// TestAdapterRejectsIDESession guards the fail-fast path: a
// conversation that only exists under the IDE root cannot be resumed by
// `agy --conversation`, so SendPrompt must return the sentinel without
// spawning the CLI.
func TestAdapterRejectsIDESession(t *testing.T) {
	base := t.TempDir()
	ide := filepath.Join(base, "antigravity")
	writeTranscript(t, ide, "conv-ide", []string{
		`{"step_index":1,"type":"PLANNER_RESPONSE","source":"model","status":"DONE","content":"hola"}`,
	})

	mgr := NewManager("agy-does-not-exist", 4102)
	mgr.MarkStarted(t.TempDir())
	adapter := NewAdapter(mgr)
	adapter.SetStateDir(base)

	_, err := adapter.SendPrompt(context.Background(), "conv-ide", "hola")
	if !errors.Is(err, domain.ErrSessionNotResumable) {
		t.Fatalf("err = %v, want ErrSessionNotResumable", err)
	}
}

// TestConversationInCLIIgnoresIDERoot checks the CLI-root probe used by
// the resume guard.
func TestConversationInCLIIgnoresIDERoot(t *testing.T) {
	base := t.TempDir()
	cli := filepath.Join(base, "antigravity-cli")
	ide := filepath.Join(base, "antigravity")
	writeTranscript(t, cli, "conv-cli", nil)
	writeTranscript(t, ide, "conv-ide", nil)

	adapter := NewAdapter(NewManager("agy", 4102))
	adapter.SetStateDir(base)
	if !adapter.conversationInCLI("conv-cli") {
		t.Fatal("CLI conversation should be resumable")
	}
	if adapter.conversationInCLI("conv-ide") {
		t.Fatal("IDE conversation must not be reported as resumable")
	}
}
