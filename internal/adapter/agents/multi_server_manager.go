package agents

import (
	"context"
	"fmt"
	"os"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/claude"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// MultiServerManagerOptions bundles the per-agent managers the
// multi-agent server manager dispatches to. A nil manager means the
// agent's binary was not detected at boot and its slot stays
// unavailable.
type MultiServerManagerOptions struct {
	OpenCode *agents_opencode.Manager
	Claude   *claude.Manager
	Kiro     *kiro.Manager
	Copilot  *copilot.Manager

	// OnWorkdir, when set, is invoked with (kind, workingDir) after a
	// successful Start so the composition root can keep auxiliary
	// components (e.g. the Claude session locator) in sync with the
	// manager's working directory.
	OnWorkdir func(kind domain.AgentKind, workingDir string)
}

// multiServerManager implements domain.AgentServerManager across every
// agent kind. opencode goes through its HTTP server manager; the
// stdio-based agents (Claude, Kiro, Copilot) are lazy: Start only
// records the working directory via MarkStarted and the subprocess is
// spawned on first use.
type multiServerManager struct {
	opts MultiServerManagerOptions
}

// NewMultiServerManager wires one manager per agent kind. Pass nil for
// a slot whose binary was not detected.
func NewMultiServerManager(opts MultiServerManagerOptions) domain.AgentServerManager {
	return &multiServerManager{opts: opts}
}

// Start activates the manager for the given kind. opencode spawns (or
// adopts) its HTTP server; the stdio agents are marked started with the
// working directory and lazily spawn their subprocess on first prompt.
func (m *multiServerManager) Start(ctx context.Context, kind domain.AgentKind, workingDir string) error {
	switch kind {
	case domain.AgentOpenCode:
		if m.opts.OpenCode == nil {
			return domain.ErrAgentUnavailable
		}
		if err := m.opts.OpenCode.Start(ctx, workingDir); err != nil {
			return err
		}
	case domain.AgentClaude:
		if m.opts.Claude == nil {
			return domain.ErrAgentUnavailable
		}
		if err := validateWorkdir(workingDir); err != nil {
			return err
		}
		m.opts.Claude.MarkStarted(workingDir)
	case domain.AgentKiro:
		if m.opts.Kiro == nil {
			return domain.ErrAgentUnavailable
		}
		if err := validateWorkdir(workingDir); err != nil {
			return err
		}
		m.opts.Kiro.MarkStarted(workingDir)
	case domain.AgentCopilot:
		if m.opts.Copilot == nil {
			return domain.ErrAgentUnavailable
		}
		if err := validateWorkdir(workingDir); err != nil {
			return err
		}
		m.opts.Copilot.MarkStarted(workingDir)
	default:
		return domain.ErrAgentUnavailable
	}
	if m.opts.OnWorkdir != nil {
		m.opts.OnWorkdir(kind, workingDir)
	}
	return nil
}

func (m *multiServerManager) Stop(kind domain.AgentKind) {
	switch kind {
	case domain.AgentOpenCode:
		if m.opts.OpenCode != nil {
			m.opts.OpenCode.Stop()
		}
	case domain.AgentClaude:
		if m.opts.Claude != nil {
			m.opts.Claude.MarkStopped()
		}
	case domain.AgentKiro:
		if m.opts.Kiro != nil {
			m.opts.Kiro.MarkStopped()
		}
	case domain.AgentCopilot:
		if m.opts.Copilot != nil {
			m.opts.Copilot.MarkStopped()
		}
	}
}

func (m *multiServerManager) StopAll() {
	m.Stop(domain.AgentOpenCode)
	m.Stop(domain.AgentClaude)
	m.Stop(domain.AgentKiro)
	m.Stop(domain.AgentCopilot)
}

func (m *multiServerManager) StartedSubprocess(kind domain.AgentKind) bool {
	switch kind {
	case domain.AgentOpenCode:
		return m.opts.OpenCode != nil && m.opts.OpenCode.StartedSubprocess()
	case domain.AgentClaude:
		return m.opts.Claude != nil && m.opts.Claude.Started()
	case domain.AgentKiro:
		return m.opts.Kiro != nil && m.opts.Kiro.Started()
	case domain.AgentCopilot:
		return m.opts.Copilot != nil && m.opts.Copilot.Started()
	default:
		return false
	}
}

// OwnsSubprocess reports whether the manager started the running
// subprocess itself. The stdio agents always own what they spawn (there
// is no adoption concept), so Started is the right proxy.
func (m *multiServerManager) OwnsSubprocess(kind domain.AgentKind) bool {
	return m.StartedSubprocess(kind)
}

func (m *multiServerManager) WorkingDir(kind domain.AgentKind) string {
	switch kind {
	case domain.AgentOpenCode:
		if m.opts.OpenCode != nil {
			return m.opts.OpenCode.WorkingDir()
		}
	case domain.AgentClaude:
		if m.opts.Claude != nil {
			return m.opts.Claude.WorkingDir()
		}
	case domain.AgentKiro:
		if m.opts.Kiro != nil {
			return m.opts.Kiro.WorkingDir()
		}
	case domain.AgentCopilot:
		if m.opts.Copilot != nil {
			return m.opts.Copilot.WorkingDir()
		}
	}
	return ""
}

// validateWorkdir mirrors the opencode manager's guard so a stdio agent
// is never marked started against an empty or nonexistent directory.
func validateWorkdir(workingDir string) error {
	if workingDir == "" {
		return fmt.Errorf("working directory must not be empty")
	}
	if info, err := os.Stat(workingDir); err != nil {
		return fmt.Errorf("stat working directory %q: %w", workingDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", workingDir)
	}
	return nil
}

// Compile-time guard: multiServerManager must satisfy the domain port.
var _ domain.AgentServerManager = (*multiServerManager)(nil)
