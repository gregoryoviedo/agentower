package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the Codex AgentAdapter. It drives the Codex CLI through
// the Manager and reads the rollout JSONL history for listing messages
// and detecting completions.
type Adapter struct {
	manager  *Manager
	stateDir string
}

// NewAdapter wires the adapter to a Manager.
func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

// SetStateDir overrides the Codex state root used for on-disk history
// (defaults to ~/.codex, honouring CODEX_SESSIONS_DIR).
func (a *Adapter) SetStateDir(dir string) { a.stateDir = dir }

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentCodex }

func (a *Adapter) DisplayName() string { return "Codex" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("codex manager not running")
}

func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

func (a *Adapter) ListSessions(_ context.Context) ([]domain.Session, error) {
	dir := a.sessionsDir()
	if dir == "" {
		return nil, nil
	}
	files, err := collectRollouts(dir)
	if err != nil {
		return nil, nil
	}
	workdir := a.manager.WorkingDir()
	out := []domain.Session{}
	for _, f := range files {
		meta := readRolloutMeta(f)
		if meta.ID == "" {
			continue
		}
		// Prefer sessions that ran in the manager's working directory;
		// if none match, fall back to every known session so the user
		// still sees something useful.
		if workdir != "" && meta.Cwd != "" && meta.Cwd != workdir {
			continue
		}
		out = append(out, domain.Session{
			ID:        a.manager.PublicSessionID(meta.ID),
			Title:     "Codex " + truncateID(meta.ID),
			Directory: meta.Cwd,
		})
	}
	if len(out) == 0 {
		for _, f := range files {
			meta := readRolloutMeta(f)
			if meta.ID == "" {
				continue
			}
			out = append(out, domain.Session{
				ID:        a.manager.PublicSessionID(meta.ID),
				Title:     "Codex " + truncateID(meta.ID),
				Directory: meta.Cwd,
			})
		}
	}
	return out, nil
}

func (a *Adapter) CreateSession(_ context.Context, _ string) (domain.Session, error) {
	id := a.manager.NewSessionID()
	return domain.Session{ID: id, Title: "Codex " + truncateID(id)}, nil
}

func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	if !a.manager.Started() {
		return "", errors.New("codex manager not running")
	}
	return a.manager.SendPrompt(ctx, sessionID, text)
}

// Revert is not supported by Codex exec mode today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Codex does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus falls back to a git diff against the working directory.
func (a *Adapter) FileStatus(ctx context.Context, _ string) ([]domain.FileChange, error) {
	return gitFileStatus(ctx, a.manager.WorkingDir())
}

func (a *Adapter) ListMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	threadID := sessionID
	dir := a.sessionsDir()
	if dir == "" {
		return nil, nil
	}
	// The bot stores synthetic ids; resolve to the real thread id.
	if isSynthetic(sessionID) {
		a.manager.mu.Lock()
		if real, ok := a.manager.threads[sessionID]; ok {
			threadID = real
		}
		a.manager.mu.Unlock()
	}
	file := findRolloutFile(dir, threadID)
	if file == "" {
		return nil, nil
	}
	return parseRolloutMessages(file, sessionID), nil
}

// sessionsDir returns the directory holding the date-partitioned
// rollout files. CODEX_SESSIONS_DIR wins; otherwise ~/.codex/sessions;
// an explicit SetStateDir points at the state root (not the sessions
// subdir) so callers can pass the value from config directly.
func (a *Adapter) sessionsDir() string {
	if env := strings.TrimSpace(os.Getenv("CODEX_SESSIONS_DIR")); env != "" {
		return env
	}
	root := a.stateDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".codex")
	}
	if filepath.Base(root) == "sessions" {
		return root
	}
	return filepath.Join(root, "sessions")
}

// rolloutMeta is the session_meta payload of a rollout file.
type rolloutMeta struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

// collectRollouts walks the date-partitioned tree and returns every
// rollout-*.jsonl file, newest first by mtime.
func collectRollouts(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		ai, aerr := os.Stat(files[i])
		aj, jerr := os.Stat(files[j])
		if aerr != nil || jerr != nil {
			return false
		}
		return ai.ModTime().After(aj.ModTime())
	})
	return files, nil
}

// readRolloutMeta reads the first session_meta line of a rollout file.
func readRolloutMeta(path string) rolloutMeta {
	file, err := os.Open(path)
	if err != nil {
		return rolloutMeta{}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var env struct {
			Type    string      `json:"type"`
			Payload rolloutMeta `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type == "session_meta" {
			return env.Payload
		}
	}
	return rolloutMeta{}
}

// findRolloutFile returns the rollout matching threadID, or "".
func findRolloutFile(root, threadID string) string {
	if threadID == "" {
		return ""
	}
	files, err := collectRollouts(root)
	if err != nil {
		return ""
	}
	for _, f := range files {
		meta := readRolloutMeta(f)
		if meta.ID == threadID {
			return f
		}
		// Some builds name the file with the id even when session_meta
		// is missing; fall back to the filename.
		if strings.Contains(filepath.Base(f), threadID) {
			return f
		}
	}
	return ""
}

// parseRolloutMessages projects the response_item records into the
// domain.Message shape the watcher and /sessions consume. sessionID is
// the id the bot persisted (synthetic or real) so message ids stay
// stable for the caller.
func parseRolloutMessages(path, sessionID string) []domain.Message {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	out := []domain.Message{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type != "response_item" || env.Payload.Type != "message" {
			continue
		}
		role := env.Payload.Role
		if role == "" {
			role = "assistant"
		}
		parts := []domain.MessagePart{}
		for _, c := range env.Payload.Content {
			if c.Text == "" {
				continue
			}
			// Codex tags text parts as input_text/output_text; the
			// watcher only cares that it is text.
			if c.Type == "input_text" || c.Type == "output_text" || c.Type == "text" {
				parts = append(parts, domain.MessagePart{Type: "text", Text: c.Text})
			}
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, domain.Message{
			Info:  domain.MessageInfo{ID: sessionID + ":" + role, SessionID: sessionID, Role: role},
			Parts: parts,
		})
	}
	return out
}

// gitFileStatus shells out to git for the changed-files view shared by
// the git-backed adapters.
func gitFileStatus(ctx context.Context, workdir string) ([]domain.FileChange, error) {
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

func truncateID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
