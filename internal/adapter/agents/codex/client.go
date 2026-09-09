package codex

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Codex AgentAdapter. It talks to the Codex CLI
// subprocess(es) through the manager that owns the lifecycle.
type Adapter struct {
	manager *Manager
}

// NewAdapter wires the adapter to a Manager.
func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentCodex }

func (a *Adapter) DisplayName() string { return "Codex" }

// Health signals whether the manager considers Codex running. Like
// the Claude adapter there is no HTTP health endpoint to probe.
func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("codex manager not running")
}

// ListProjects is a no-op because Codex does not expose a server-side
// project list. /projects is satisfied by the workspace browser.
func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

// ListSessions returns the live Codex sessions owned by the manager.
// Codex itself does not expose a session index via JSON-RPC today so
// the adapter returns only the in-memory map; on-disk session
// reconstruction can be added once Codex's CLI exposes a directory.
func (a *Adapter) ListSessions(_ context.Context) ([]domain.Session, error) {
	// Without a documented Codex session directory, return an empty
	// list. The /sessions Telegram button still works because the
	// "Nueva sesión" branch goes through CreateSession.
	return nil, nil
}

// CreateSession allocates a fresh Codex session id.
func (a *Adapter) CreateSession(_ context.Context, _ string) (domain.Session, error) {
	id := a.manager.NewSessionID()
	return domain.Session{ID: id, ProjectID: "", Title: "Codex " + id[:8]}, nil
}

// SendPrompt delegates to the manager.
func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("codex manager not running")
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// Revert returns ErrAgentCapabilitiesLimited because Codex does not
// expose a /undo endpoint today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Codex CLI does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus falls back to `git diff --name-status` against the
// manager's working directory.
func (a *Adapter) FileStatus(ctx context.Context, _ string) ([]domain.FileChange, error) {
	workdir := a.manager.WorkingDir()
	if workdir == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "diff", "--name-status")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	changes := []domain.FileChange{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		status := fields[0]
		path := fields[1]
		changes = append(changes, domain.FileChange{Path: path, Status: gitStatusToLabel(status)})
	}
	return changes, nil
}

// ListMessages is a no-op for now because Codex does not expose a
// session-history format we can parse. The Telegram UI hides /messages
// for Codex-driven chats until a future CLI version ships one.
func (a *Adapter) ListMessages(_ context.Context, _ string) ([]domain.Message, error) {
	return nil, nil
}

// gitStatusToLabel maps git --name-status codes to the labels the
// Telegram UI already understands.
func gitStatusToLabel(code string) string {
	switch code {
	case "M":
		return "modified"
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R", "C":
		return "renamed"
	default:
		return code
	}
}
