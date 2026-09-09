package kiro

import (
	"context"
	"errors"
	"fmt"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Kiro AgentAdapter. It is intentionally minimal:
// SendPrompt and Health only. Every other capability returns
// ErrAgentCapabilitiesLimited so the Telegram UI hides the
// corresponding buttons until Kiro exposes a richer protocol.
type Adapter struct {
	manager *Manager
}

func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentKiro }

func (a *Adapter) DisplayName() string { return "Kiro" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("kiro manager not running")
}

// ListProjects is intentionally a no-op because Kiro does not expose
// a server-side project list today.
func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

// ListSessions is intentionally a no-op because Kiro's CLI does not
// expose a session index today.
func (a *Adapter) ListSessions(_ context.Context) ([]domain.Session, error) {
	return nil, nil
}

// CreateSession allocates a fresh Kiro session id.
func (a *Adapter) CreateSession(_ context.Context, _ string) (domain.Session, error) {
	id := a.manager.NewSessionID()
	return domain.Session{ID: id, ProjectID: "", Title: "Kiro " + id[:8]}, nil
}

// SendPrompt delegates to the manager.
func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("kiro manager not running")
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// Revert returns ErrAgentCapabilitiesLimited because Kiro does not
// expose a revert endpoint today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Kiro CLI does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus returns ErrAgentCapabilitiesLimited because Kiro does not
// expose file diffs natively; the bot hides /diff for kiro-driven
// chats until the CLI ships one.
func (a *Adapter) FileStatus(_ context.Context, _ string) ([]domain.FileChange, error) {
	return nil, fmt.Errorf("%w: Kiro CLI does not expose a file diff", domain.ErrAgentCapabilitiesLimited)
}

// ListMessages returns ErrAgentCapabilitiesLimited because Kiro does
// not expose a session history format we can parse.
func (a *Adapter) ListMessages(_ context.Context, _ string) ([]domain.Message, error) {
	return nil, fmt.Errorf("%w: Kiro CLI does not expose a session history", domain.ErrAgentCapabilitiesLimited)
}
