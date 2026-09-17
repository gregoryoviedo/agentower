package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Antigravity session is the user driving
// right now" for the /resume handler.
//
// Antigravity keeps its state under ~/.gemini: the CLI under
// `antigravity-cli/` and the IDE under `antigravity/`. Both write a
// prompt recall log (`history.jsonl`) and a readable transcript under
// `brain/<id>/.system_generated/logs/transcript_full.jsonl`. The locator
// merges both roots and returns the freshest conversation, so a task the
// user ran in the IDE is detected even when the CLI has never been used.
type SessionLocator struct {
	base string
	now  func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the shared
// ~/.gemini root (or points directly at one product subtree).
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. It does not touch the disk until
// the first call to Locate.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	base := strings.TrimSpace(opts.StateDir)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".gemini")
	}
	return &SessionLocator{base: base, now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentAntigravity }

// SetStateDir swaps the watched root at runtime.
func (l *SessionLocator) SetStateDir(root string) { l.base = strings.TrimSpace(root) }

// Locate returns the freshest Antigravity conversation across the CLI and
// IDE state roots.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	roots := productRoots(l.base)
	var (
		best     *historyEntry
		bestRoot string
	)
	for _, root := range roots {
		for _, e := range readHistory(root) {
			if e.ConversationID == "" {
				continue
			}
			candidate := e
			if best == nil || candidate.TimestampMS > best.TimestampMS {
				best = &candidate
				bestRoot = root
			}
		}
	}
	if best == nil {
		// No recall log entry; fall back to the newest transcript file on
		// disk so /resume still has something to offer.
		if e, root, ok := newestTranscript(roots); ok {
			best = &e
			bestRoot = root
		}
	}
	if best == nil {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	touched := l.now()
	if best.TimestampMS > 0 {
		touched = time.UnixMilli(best.TimestampMS).UTC()
	}
	preview := ""
	if path := findTranscriptIn(roots, best.ConversationID); path != "" {
		preview = lastAssistantPreview(path, 240)
	}
	return domain.ActiveSession{
		Kind:      domain.AgentAntigravity,
		SessionID: best.ConversationID,
		Project:   filepath.Base(best.Workspace),
		Directory: best.Workspace,
		Title:     firstNonEmpty(best.Title, "Antigravity "+truncate(best.ConversationID, 8)),
		Preview:   preview,
		TouchedAt: touched,
		Source:    sourceForRoot(bestRoot),
	}, nil
}

// sourceForRoot labels which product subtree a conversation came from,
// so callers can tell CLI (resumable by `agy`) from IDE (read-only for
// the bot).
func sourceForRoot(root string) string {
	switch filepath.Base(root) {
	case "antigravity-cli":
		return "cli"
	case "antigravity":
		return "ide"
	default:
		return "jsonl"
	}
}

// findTranscriptIn probes every root for the transcript of a
// conversation.
func findTranscriptIn(roots []string, conversationID string) string {
	if conversationID == "" {
		return ""
	}
	for _, root := range roots {
		p := transcriptPath(root, conversationID)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// newestTranscript scans brain/<id> dirs for the most recently modified
// transcript and returns a synthetic history entry for it, along with
// the root it was found under.
func newestTranscript(roots []string) (historyEntry, string, bool) {
	var (
		best     historyEntry
		bestRoot string
		bestMod  time.Time
		found    bool
	)
	for _, root := range roots {
		brainDir := filepath.Join(root, "brain")
		entries, err := os.ReadDir(brainDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			id := entry.Name()
			path := transcriptPath(root, id)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			mod := info.ModTime().UTC()
			if !found || mod.After(bestMod) {
				best = historyEntry{ConversationID: id, TimestampMS: mod.UnixMilli()}
				bestRoot = root
				bestMod = mod
				found = true
			}
		}
	}
	return best, bestRoot, found
}

// lastAssistantPreview returns the text of the last assistant turn in a
// transcript, truncated.
func lastAssistantPreview(path string, max int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	lines, err := readLines(file)
	if err != nil {
		return ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var line transcriptLine
		if err := unmarshalJSON(lines[i], &line); err != nil {
			continue
		}
		if roleFromSource(line.Source) != "assistant" {
			continue
		}
		text := strings.TrimSpace(line.Content)
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

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
