package antigravity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Antigravity AgentAdapter. It drives the `agy` CLI over
// headless stream-json and reads the brain transcript for history.
type Adapter struct {
	manager  *Manager
	stateDir string
}

// NewAdapter wires the adapter to a Manager.
func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

// SetStateDir overrides the shared ~/.gemini root. The adapter derives
// the CLI (antigravity-cli) and IDE (antigravity) subtrees from it.
func (a *Adapter) SetStateDir(dir string) { a.stateDir = dir }

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentAntigravity }

func (a *Adapter) DisplayName() string { return "Antigravity" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("antigravity manager not running")
}

func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

func (a *Adapter) ListSessions(_ context.Context) ([]domain.Session, error) {
	entries := a.history()
	out := []domain.Session{}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ConversationID == "" || seen[e.ConversationID] {
			continue
		}
		seen[e.ConversationID] = true
		out = append(out, domain.Session{
			ID:        a.manager.PublicSessionID(e.ConversationID),
			Title:     firstNonEmpty(e.Title, "Antigravity "+truncate(e.ConversationID, 8)),
			Directory: e.Workspace,
		})
	}
	return out, nil
}

func (a *Adapter) CreateSession(_ context.Context, _ string) (domain.Session, error) {
	id := a.manager.NewSessionID()
	return domain.Session{ID: id, Title: "Antigravity " + truncate(id, 8)}, nil
}

func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("antigravity manager not running")
	}
	// `agy --conversation` only reads the CLI root; a conversation the
	// user ran in the Antigravity IDE lives under a different subtree and
	// cannot be resumed from the bot. Fail fast with a clear sentinel.
	if sessionID != "" && !isSynthetic(sessionID) && !a.conversationInCLI(sessionID) {
		return "", fmt.Errorf("%w: antigravity conversation %s", domain.ErrSessionNotResumable, sessionID)
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// cliRoots returns the state roots the `agy` CLI can read, excluding the
// IDE subtree.
func (a *Adapter) cliRoots() []string {
	base := strings.TrimSpace(a.stateDir)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		return []string{filepath.Join(home, ".gemini", "antigravity-cli")}
	}
	switch filepath.Base(base) {
	case "antigravity-cli":
		return []string{base}
	case "antigravity":
		// Pointed at the IDE subtree: the CLI has nothing to resume here.
		return nil
	default:
		return []string{filepath.Join(base, "antigravity-cli")}
	}
}

// conversationInCLI reports whether the conversation exists under a CLI
// root, which is the only place a backend resume can find it.
func (a *Adapter) conversationInCLI(conversationID string) bool {
	for _, root := range a.cliRoots() {
		if info, err := os.Stat(filepath.Join(root, "brain", conversationID)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// Revert is not supported by Antigravity headless mode today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Antigravity does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
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

// ListMessages reads the brain transcript for the session.
func (a *Adapter) ListMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	conversationID := sessionID
	if isSynthetic(sessionID) {
		a.manager.mu.Lock()
		if real, ok := a.manager.convos[sessionID]; ok {
			conversationID = real
		}
		a.manager.mu.Unlock()
	}
	path := a.findTranscript(conversationID)
	if path == "" {
		return nil, nil
	}
	return parseTranscript(path, sessionID), nil
}

// stateRoots returns the CLI and IDE state roots in priority order.
func (a *Adapter) stateRoots() []string {
	if root := strings.TrimSpace(a.stateDir); root != "" {
		// A caller-supplied root may be either the shared ~/.gemini or
		// one product subtree; probe the product subdirs relative to it
		// and the value itself.
		return productRoots(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return productRoots(filepath.Join(home, ".gemini"))
}

// productRoots expands a base dir into the CLI and IDE product roots.
func productRoots(base string) []string {
	out := []string{}
	if filepath.Base(base) == "antigravity-cli" || filepath.Base(base) == "antigravity" {
		return []string{base}
	}
	out = append(out,
		filepath.Join(base, "antigravity-cli"),
		filepath.Join(base, "antigravity"),
	)
	return out
}

// findTranscript returns the transcript_full.jsonl for a conversation.
func (a *Adapter) findTranscript(conversationID string) string {
	if conversationID == "" {
		return ""
	}
	for _, root := range a.stateRoots() {
		p := transcriptPath(root, conversationID)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// history reads the prompt recall log (history.jsonl) from every product
// root, merging CLI and IDE entries newest-first.
func (a *Adapter) history() []historyEntry {
	out := []historyEntry{}
	for _, root := range a.stateRoots() {
		out = append(out, readHistory(root)...)
	}
	sortHistory(out)
	return out
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
