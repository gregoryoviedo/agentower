package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Claude Code session is the user
// driving right now" for the /continuar handler. Claude Code persists
// every prompt and reply to
// ~/.claude/projects/<sanitized-cwd>/<session-id>.jsonl; the file
// with the most recent mtime inside the manager's working directory
// is the one the user is most likely to be working on.
//
// The locator is purely on-disk: it does not require a running
// claude subprocess and stays usable when the manager has not been
// started (e.g. when the user only opens the bot to peek at what is
// going on in the IDE). The /continuar handler fans out to every
// registered locator and the user picks the one they want.
type SessionLocator struct {
	mu       sync.RWMutex
	workdir  string
	stateDir string // override for AGENTOWER_CLAUDE_STATE_DIR (testing)
	now      func() time.Time
}

// SessionLocatorOptions tunes the locator. Workdir is required; the
// other fields are best-effort overrides used by tests.
type SessionLocatorOptions struct {
	Workdir  string
	StateDir string // base dir that contains "projects/". defaults to ~/.claude
	Now      func() time.Time
}

// NewSessionLocator builds the locator. Workdir must be the absolute
// path of the project the user is currently working in; it is the
// same value the manager is started with.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	if opts.Workdir == "" {
		return nil, errors.New("claude locator: Workdir is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{
		workdir:  opts.Workdir,
		stateDir: opts.StateDir,
		now:      now,
	}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentClaude }

// SetWorkdir updates the workdir at runtime so the same locator can
// track the manager's current project. Safe for concurrent use.
func (l *SessionLocator) SetWorkdir(workdir string) {
	l.mu.Lock()
	l.workdir = workdir
	l.mu.Unlock()
}

// Locate scans the on-disk JSONL history for the configured workdir
// and returns the session with the most recent mtime. Returns
// ErrNoActiveSession when no JSONL exists or the newest file is
// older than one hour (configurable via TouchedAt thresholds in the
// caller).
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	l.mu.RLock()
	workdir := l.workdir
	stateDir := l.stateDir
	l.mu.RUnlock()
	if workdir == "" {
		return domain.ActiveSession{}, errors.New("claude locator: no workdir configured")
	}
	root, err := l.sessionRoot(stateDir, workdir)
	if err != nil {
		return domain.ActiveSession{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ActiveSession{}, domain.ErrNoActiveSession
		}
		return domain.ActiveSession{}, fmt.Errorf("read claude history dir: %w", err)
	}
	var freshest os.DirEntry
	var freshestInfo os.FileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if freshestInfo == nil || info.ModTime().After(freshestInfo.ModTime()) {
			freshest = entry
			freshestInfo = info
		}
	}
	if freshest == nil {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	sessionID := strings.TrimSuffix(freshest.Name(), ".jsonl")
	preview, touchedAt, _ := l.peekJSONL(filepath.Join(root, freshest.Name()))
	return domain.ActiveSession{
		Kind:      domain.AgentClaude,
		SessionID: sessionID,
		Project:   filepath.Base(workdir),
		Directory: workdir,
		Title:     sessionID[:min(8, len(sessionID))],
		Preview:   preview,
		TouchedAt: touchedAt,
		Source:    "jsonl",
	}, nil
}

// sessionRoot returns the directory Claude Code writes per-cwd
// JSONL files to. The cwd is sanitized by replacing every path
// separator with "-" and prepending "-" so "/Users/me/proj" becomes
// "-Users-me-proj", matching the convention in manager.go.
func (l *SessionLocator) sessionRoot(stateDir, workdir string) (string, error) {
	base := stateDir
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home: %w", err)
		}
		base = filepath.Join(home, ".claude")
	}
	sanitized := strings.ReplaceAll(workdir, string(os.PathSeparator), "-")
	if !strings.HasPrefix(sanitized, "-") {
		sanitized = "-" + sanitized
	}
	return filepath.Join(base, "projects", sanitized), nil
}

// peekJSONL reads the tail of the file to extract the most recent
// assistant text and the most recent event timestamp. The Claude
// JSONL stream is one event per line; the last "assistant" event
// before a "result" is what the user last saw. TouchedAt falls back
// to the file's mtime when no timestamped event is found.
func (l *SessionLocator) peekJSONL(path string) (preview string, touchedAt time.Time, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return "", time.Time{}, statErr
	}
	touchedAt = info.ModTime()
	file, err := os.Open(path)
	if err != nil {
		return "", touchedAt, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lastAssistant string
	for scanner.Scan() {
		var event map[string]any
		if jerr := json.Unmarshal(scanner.Bytes(), &event); jerr != nil {
			continue
		}
		if ts, ok := event["timestamp"].(string); ok {
			if t, perr := time.Parse(time.RFC3339Nano, ts); perr == nil {
				if t.After(touchedAt) {
					touchedAt = t
				}
			}
		}
		role, _ := event["type"].(string)
		if role != "assistant" {
			continue
		}
		msg, _ := event["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		var b strings.Builder
		for _, c := range content {
			part, _ := c.(map[string]any)
			if part["type"] != "text" {
				continue
			}
			text, _ := part["text"].(string)
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(text)
		}
		out := strings.TrimSpace(b.String())
		if out != "" {
			lastAssistant = out
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return lastAssistant, touchedAt, err
	}
	if len(lastAssistant) > 240 {
		lastAssistant = lastAssistant[:240] + "…"
	}
	return lastAssistant, touchedAt, nil
}

// min avoids pulling in the builtin (Go 1.21+) so this file compiles
// under the module's go 1.22 toolchain. Trivial inline polyfill.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
