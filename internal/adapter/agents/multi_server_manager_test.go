package agents

import (
	"context"
	"errors"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/antigravity"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/claude"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/codex"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// TestMultiServerManagerRejectsNilSlots verifies that undetected
// agents (nil managers) are reported unavailable and Start fails with
// ErrAgentUnavailable.
func TestMultiServerManagerRejectsNilSlots(t *testing.T) {
	sm := NewMultiServerManager(MultiServerManagerOptions{})
	for _, kind := range domain.AllAgentKinds() {
		if sm.StartedSubprocess(kind) {
			t.Errorf("StartedSubprocess(%s) = true with no managers wired", kind)
		}
		if got := sm.WorkingDir(kind); got != "" {
			t.Errorf("WorkingDir(%s) = %q, want empty", kind, got)
		}
		if err := sm.Start(context.Background(), kind, t.TempDir()); !errors.Is(err, domain.ErrAgentUnavailable) {
			t.Errorf("Start(%s) = %v, want ErrAgentUnavailable", kind, err)
		}
	}
}

// TestMultiServerManagerStartsStdioAgents verifies that the stdio
// managers (claude/kiro/copilot) are marked started with the working
// directory and that the OnWorkdir hook fires with the right values.
func TestMultiServerManagerStartsStdioAgents(t *testing.T) {
	dir := t.TempDir()
	var hookKinds []domain.AgentKind
	var hookDirs []string
	sm := NewMultiServerManager(MultiServerManagerOptions{
		Claude:      claude.NewManager("claude", 4097),
		Kiro:        kiro.NewManager("kiro", 4099),
		Copilot:     copilot.NewManager(copilot.LaunchConfig{}),
		Codex:       codex.NewManager("codex", 4101),
		Antigravity: antigravity.NewManager("agy", 4102),
		OnWorkdir: func(kind domain.AgentKind, workdir string) {
			hookKinds = append(hookKinds, kind)
			hookDirs = append(hookDirs, workdir)
		},
	})

	for _, kind := range []domain.AgentKind{domain.AgentClaude, domain.AgentKiro, domain.AgentCopilot, domain.AgentCodex, domain.AgentAntigravity} {
		if err := sm.Start(context.Background(), kind, dir); err != nil {
			t.Fatalf("Start(%s) = %v", kind, err)
		}
		if !sm.StartedSubprocess(kind) {
			t.Errorf("StartedSubprocess(%s) = false after Start", kind)
		}
		if !sm.OwnsSubprocess(kind) {
			t.Errorf("OwnsSubprocess(%s) = false after Start", kind)
		}
		if got := sm.WorkingDir(kind); got != dir {
			t.Errorf("WorkingDir(%s) = %q, want %q", kind, got, dir)
		}
	}
	if len(hookKinds) != 5 {
		t.Fatalf("OnWorkdir fired %d times, want 5", len(hookKinds))
	}
	for i, kind := range []domain.AgentKind{domain.AgentClaude, domain.AgentKiro, domain.AgentCopilot, domain.AgentCodex, domain.AgentAntigravity} {
		if hookKinds[i] != kind {
			t.Errorf("OnWorkdir kind[%d] = %s, want %s", i, hookKinds[i], kind)
		}
		if hookDirs[i] != dir {
			t.Errorf("OnWorkdir dir[%d] = %q, want %q", i, hookDirs[i], dir)
		}
	}
}

// TestMultiServerManagerStdioStartRejectsBadWorkdir verifies the stdio
// managers validate the working directory before marking themselves
// started.
func TestMultiServerManagerStdioStartRejectsBadWorkdir(t *testing.T) {
	sm := NewMultiServerManager(MultiServerManagerOptions{
		Claude: claude.NewManager("claude", 4097),
	})
	if err := sm.Start(context.Background(), domain.AgentClaude, ""); err == nil {
		t.Fatal("Start(claude, empty workdir) returned nil, want error")
	}
	if sm.StartedSubprocess(domain.AgentClaude) {
		t.Fatal("StartedSubprocess(claude) = true after a failed Start")
	}
	if sm.Start(context.Background(), domain.AgentClaude, t.TempDir()+"/missing") == nil {
		t.Fatal("Start(claude, nonexistent workdir) returned nil, want error")
	}
	if sm.StartedSubprocess(domain.AgentClaude) {
		t.Fatal("StartedSubprocess(claude) = true after a failed Start")
	}
}

// TestMultiServerManagerStopAndStopAll verifies Stop clears the per-kind
// running flag and StopAll clears every slot.
func TestMultiServerManagerStopAndStopAll(t *testing.T) {
	sm := NewMultiServerManager(MultiServerManagerOptions{
		Claude: claude.NewManager("claude", 4097),
		Kiro:   kiro.NewManager("kiro", 4099),
	})
	dir := t.TempDir()
	_ = sm.Start(context.Background(), domain.AgentClaude, dir)
	_ = sm.Start(context.Background(), domain.AgentKiro, dir)

	sm.Stop(domain.AgentClaude)
	if sm.StartedSubprocess(domain.AgentClaude) {
		t.Error("StartedSubprocess(claude) = true after Stop")
	}
	if !sm.StartedSubprocess(domain.AgentKiro) {
		t.Error("StartedSubprocess(kiro) = false; Stop must not touch other slots")
	}

	_ = sm.Start(context.Background(), domain.AgentClaude, dir)
	sm.StopAll()
	for _, kind := range []domain.AgentKind{domain.AgentClaude, domain.AgentKiro} {
		if sm.StartedSubprocess(kind) {
			t.Errorf("StartedSubprocess(%s) = true after StopAll", kind)
		}
	}
}
