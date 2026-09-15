package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Codex session is the user driving right
// now" for the /resume handler.
//
// Codex stores every session as a rollout JSONL file under
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl (override with
// CODEX_SESSIONS_DIR). The first line is a session_meta record carrying
// the real thread id and the cwd; the locator picks the newest file by
// mtime and peeks the tail for the last assistant text.
type SessionLocator struct {
	root string
	now  func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the
// Codex state root (defaults to ~/.codex); CODEX_SESSIONS_DIR still
// wins when set, mirroring the adapter.
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. It does not touch the disk
// until the first call to Locate.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{root: strings.TrimSpace(opts.StateDir), now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentCodex }

// SetStateDir swaps the watched root at runtime.
func (l *SessionLocator) SetStateDir(root string) { l.root = strings.TrimSpace(root) }

// Locate returns the most recently touched Codex session.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	dir := l.sessionsDir()
	if dir == "" {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	files, err := collectRollouts(dir)
	if err != nil || len(files) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	path := files[0]
	meta := readRolloutMeta(path)
	if meta.ID == "" {
		base := filepath.Base(path)
		meta.ID = strings.TrimSuffix(strings.TrimPrefix(base, "rollout-"), ".jsonl")
	}
	touched := l.now()
	if info, err := os.Stat(path); err == nil {
		touched = info.ModTime().UTC()
	}
	return domain.ActiveSession{
		Kind:      domain.AgentCodex,
		SessionID: meta.ID,
		Project:   filepath.Base(meta.Cwd),
		Directory: meta.Cwd,
		Title:     "Codex " + truncateID(meta.ID),
		Preview:   lastAssistantPreview(path, 240),
		TouchedAt: touched,
		Source:    "jsonl",
	}, nil
}

func (l *SessionLocator) sessionsDir() string {
	if env := strings.TrimSpace(os.Getenv("CODEX_SESSIONS_DIR")); env != "" {
		return env
	}
	root := l.root
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

// lastAssistantPreview walks the rollout from the bottom and returns the
// text of the most recent assistant message, truncated.
func lastAssistantPreview(path string, max int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	for i := len(lines) - 1; i >= 0; i-- {
		text := assistantTextFromLine(lines[i])
		if text == "" {
			continue
		}
		if max > 0 && len(text) > max {
			return text[:max] + "…"
		}
		return text
	}
	return ""
}

// assistantTextFromLine extracts the assistant text from one rollout
// line, accepting the response_item/message shape.
func assistantTextFromLine(raw string) string {
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
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return ""
	}
	if env.Type != "response_item" || env.Payload.Type != "message" || env.Payload.Role != "assistant" {
		return ""
	}
	var b strings.Builder
	for _, c := range env.Payload.Content {
		if c.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(c.Text)
	}
	return strings.TrimSpace(b.String())
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
