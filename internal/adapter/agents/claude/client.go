// Package claude implements the AgentAdapter for Anthropic's Claude
// Code CLI. The transport is newline-delimited JSON over stdio
// (claude --print --output-format stream-json --session-id <uuid>),
// which lets Agentower drive long-lived Claude sessions without going
// through any HTTP daemon.
//
// The adapter is best-effort: the Claude Code CLI evolves quickly and
// the documented flags today may shift tomorrow. The testdata
// fakeclaude binary simulates the protocol so the adapter can be
// exercised in CI without depending on a real claude install.
package claude

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Claude Code AgentAdapter. It talks to the Claude CLI
// subprocess(es) through the manager that owns the lifecycle.
type Adapter struct {
	manager *Manager
}

// NewAdapter wires the adapter to a Manager. The manager must already
// have its binary/port configuration set; this constructor is a
// pure wiring step.
func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

// Compile-time assertion that the adapter satisfies the AgentAdapter
// port.
var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentClaude }

func (a *Adapter) DisplayName() string { return "Claude" }

// Health signals whether the manager currently considers the agent
// running. We do not probe the subprocess directly because Claude
// Code does not expose an HTTP health endpoint; "running" means the
// manager reports a live subprocess (either owned or adopted).
func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("claude manager not running")
}

// ListProjects is a no-op for now. Claude Code does not expose a
// server-side project list comparable to opencode's /project, so we
// surface an empty slice and let the bot fall back to the workspace
// browser.
func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

// ListSessions returns the sessions known to the adapter. For Claude
// Code we rely on the on-disk JSONL history under
// ~/.claude/projects/<cwd>/<id>.jsonl. The implementation lives in
// sessions.go and is best-effort: malformed files are skipped.
func (a *Adapter) ListSessions(ctx context.Context) ([]domain.Session, error) {
	return a.manager.listSessions(ctx)
}

// CreateSession allocates a fresh Claude session id (UUID). The id
// becomes the SessionID the adapter uses for subsequent SendPrompt
// calls; the manager spawns the subprocess lazily on first prompt.
func (a *Adapter) CreateSession(_ context.Context, _ string) (domain.Session, error) {
	id := a.manager.NewSessionID()
	return domain.Session{ID: id, ProjectID: "", Title: "Claude " + id[:8]}, nil
}

// SendPrompt writes the prompt to the subprocess's stdin and reads
// the assistant reply off stdout. The reply is the concatenation of
// every assistant text part emitted before the final result event.
func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("claude manager not running")
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// Revert is not supported by Claude Code today. The adapter returns
// ErrAgentCapabilitiesLimited so the handler can hide the /undo
// button for chats driven by Claude.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Claude Code does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus falls back to a git diff against the session's working
// directory. Claude Code does not expose file diffs natively so the
// adapter shells out to git to give users the same UX as opencode.
func (a *Adapter) FileStatus(ctx context.Context, _ string) ([]domain.FileChange, error) {
	workdir := a.manager.WorkingDir()
	if workdir == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "diff", "--name-status")
	out, err := cmd.Output()
	if err != nil {
		// No git or no changes — treat as empty.
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

// ListMessages returns the message history for a session by parsing
// the on-disk JSONL file Claude Code writes under
// ~/.claude/projects/<cwd>/<id>.jsonl.
func (a *Adapter) ListMessages(ctx context.Context, sessionID string) ([]domain.Message, error) {
	return a.manager.readSessionMessages(ctx, sessionID)
}

// gitStatusToLabel converts git --name-status codes into the labels
// the Telegram UI already understands.
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
