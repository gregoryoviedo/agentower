package kiro

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Kiro AgentAdapter. It drives the Kiro CLI over ACP
// and reads the on-disk session history (~/.kiro) for message listing
// and completion detection.
type Adapter struct {
	manager  *Manager
	stateDir string
}

func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

// SetStateDir overrides the Kiro state root used for on-disk history
// (defaults to ~/.kiro, matching the locator).
func (a *Adapter) SetStateDir(dir string) { a.stateDir = dir }

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentKiro }

func (a *Adapter) DisplayName() string { return "Kiro" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("kiro manager not running")
}

func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

func (a *Adapter) ListSessions(ctx context.Context) ([]domain.Session, error) {
	return a.manager.listSessions(ctx)
}

func (a *Adapter) CreateSession(ctx context.Context, _ string) (domain.Session, error) {
	workdir := a.manager.WorkingDir()
	if workdir == "" {
		return domain.Session{}, errors.New("kiro manager has no working directory")
	}
	id, err := a.manager.CreateSession(ctx, workdir)
	if err != nil {
		return domain.Session{}, err
	}
	return domain.Session{ID: id, ProjectID: "", Title: "Kiro " + truncate(id, 8)}, nil
}

func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("kiro manager not running")
	}
	reply, err := a.manager.SendPrompt(ctx, sessionID, text)
	return stripAssistantSentinels(reply), err
}

// Revert returns ErrAgentCapabilitiesLimited because Kiro does not
// expose a revert endpoint today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Kiro does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus falls back to a git diff against the working directory.
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
		changes = append(changes, domain.FileChange{Path: fields[1], Status: gitStatusToLabel(fields[0])})
	}
	return changes, nil
}

// ListMessages reads the on-disk Kiro history for the session.
func (a *Adapter) ListMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	root := a.stateDir
	if root == "" {
		root = defaultKiroStateDir()
	}
	return readKiroMessages(root, sessionID)
}

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
