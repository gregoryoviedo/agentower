package agents

import (
	"context"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// opencodeServerManager adapts the existing single-agent opencode.Manager
// to the multi-agent AgentServerManager surface for the opencode slot.
// Stdio-based agents (Claude, Kiro, Copilot) will use a more
// generic subprocess.Manager when their adapters land in later PRs.
type opencodeServerManager struct {
	mgr *agents_opencode.Manager
}

// NewOpenCodeServerManager builds the per-kind manager for opencode.
func NewOpenCodeServerManager(mgr *agents_opencode.Manager) domain.AgentServerManager {
	return &opencodeServerManager{mgr: mgr}
}

func (o *opencodeServerManager) Start(ctx context.Context, kind domain.AgentKind, workingDir string) error {
	if kind != domain.AgentOpenCode {
		return domain.ErrAgentUnavailable
	}
	return o.mgr.Start(ctx, workingDir)
}

func (o *opencodeServerManager) Stop(kind domain.AgentKind) {
	if kind != domain.AgentOpenCode {
		return
	}
	o.mgr.Stop()
}

func (o *opencodeServerManager) StopAll() { o.mgr.Stop() }

func (o *opencodeServerManager) StartedSubprocess(kind domain.AgentKind) bool {
	if kind != domain.AgentOpenCode {
		return false
	}
	return o.mgr.StartedSubprocess()
}

func (o *opencodeServerManager) OwnsSubprocess(kind domain.AgentKind) bool {
	if kind != domain.AgentOpenCode {
		return false
	}
	return o.mgr.OwnsSubprocess()
}

func (o *opencodeServerManager) WorkingDir(kind domain.AgentKind) string {
	if kind != domain.AgentOpenCode {
		return ""
	}
	return o.mgr.WorkingDir()
}
