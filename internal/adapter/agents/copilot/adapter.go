package copilot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the GitHub Copilot AgentAdapter. It drives the Copilot
// CLI over ACP and reads the VS Code session store for message listing
// and completion detection.
type Adapter struct {
	manager  *Manager
	stateDir string
}

func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

// SetStateDir overrides the VS Code globalStorage root used for
// on-disk history (defaults to github.copilot-chat globalStorage).
func (a *Adapter) SetStateDir(dir string) { a.stateDir = dir }

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentCopilot }

func (a *Adapter) DisplayName() string { return "GitHub Copilot" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("copilot manager not running")
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
		return domain.Session{}, errors.New("copilot manager has no working directory")
	}
	id, err := a.manager.CreateSession(ctx, workdir)
	if err != nil {
		return domain.Session{}, err
	}
	return domain.Session{ID: id, ProjectID: "", Title: "Copilot " + truncateID(id)}, nil
}

func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("copilot manager not running")
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// Revert returns ErrAgentCapabilitiesLimited because Copilot does not
// expose a per-prompt revert endpoint today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Copilot does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
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

// ListMessages reads the on-disk VS Code Copilot history for the
// session.
func (a *Adapter) ListMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	root := a.stateDir
	if root == "" {
		derived, err := defaultVSCodeCopilotDir()
		if err != nil {
			return nil, err
		}
		root = derived
	}
	return readCopilotMessages(root, sessionID)
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
