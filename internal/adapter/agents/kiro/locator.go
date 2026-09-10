package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Kiro session is the user driving
// right now" for the /resume handler.
//
// The Kiro IDE and the Kiro CLI both persist chat sessions under
// ~/.kiro/:
//
//	~/.kiro/session-index/<workspace-hash>.jsonl   # newline-delimited ops
//	~/.kiro/sessions/<workspace-hash>/<id>/session.json
//	~/.kiro/sessions/<workspace-hash>/<id>/messages.jsonl
//
// Each index line carries an operation (`add`), the session path
// (relative to ~/.kiro/sessions/) and a millisecond epoch. The
// freshest line is the most recently touched session. The
// locator reads session.json for the metadata, then peeks the
// tail of messages.jsonl for the last assistant text.
//
// The locator does not depend on the Kiro IDE being open: the CLI
// writes the same layout, so a CLI-driven session and an
// IDE-driven one are reported identically.
type SessionLocator struct {
	mu      sync.RWMutex
	rootDir string
	now     func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the
// Kiro state root (used by tests and by users who have a custom
// install). When empty the locator defaults to ~/.kiro.
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. It does not touch the
// disk until the first call to Locate, so the bot stays bootable
// on machines without Kiro.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	root := strings.TrimSpace(opts.StateDir)
	if root == "" {
		root = defaultKiroStateDir()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{rootDir: root, now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentKiro }

// Locate returns the most recent Kiro chat session across every
// workspace. Returns ErrNoActiveSession when ~/.kiro/ is missing
// or the session index is empty. The locator is best-effort:
// malformed index lines or unreadable session files are dropped
// silently so a single corrupt workspace does not blank the
// whole /resume card.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	l.mu.RLock()
	root := l.rootDir
	l.mu.RUnlock()
	if root == "" {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	entries, err := readKiroIndex(root)
	if err != nil {
		return domain.ActiveSession{}, err
	}
	if len(entries) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	// Sort by the index timestamp; fall back to the session.json
	// mtime when the index line is missing one.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].TouchedAt.After(entries[j].TouchedAt)
	})
	freshest := entries[0]
	return domain.ActiveSession{
		Kind:      domain.AgentKiro,
		SessionID: freshest.SessionID,
		Project:   filepath.Base(freshest.Workspace),
		Directory: freshest.Workspace,
		Title:     freshest.Title,
		Preview:   freshest.Preview,
		TouchedAt: freshest.TouchedAt,
		Source:    "jsonl",
	}, nil
}

// SetStateDir swaps the watched root at runtime. Used by Settings
// or the macOS wrapper when the user changes the Kiro install.
func (l *SessionLocator) SetStateDir(root string) {
	l.mu.Lock()
	l.rootDir = strings.TrimSpace(root)
	l.mu.Unlock()
}

// defaultKiroStateDir returns ~/.kiro. Kiro keeps its runtime
// state under a single hidden directory in the user's home; this
// is the same for every supported OS.
func defaultKiroStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".kiro")
	}
	return filepath.Join(home, ".kiro")
}

// indexEntry is the in-memory projection of one line of
// ~/.kiro/session-index/<hash>.jsonl enriched with the metadata
// read from session.json. The locator sorts these by TouchedAt
// desc and returns the freshest.
type indexEntry struct {
	SessionID string
	Title     string
	Workspace string
	Preview   string
	TouchedAt time.Time
	Path      string // absolute path to the session.json
}

// readKiroIndex lists every JSONL file under
// ~/.kiro/session-index/, parses each line as a `{op, sessionPath,
// at}` record, follows the sessionPath into the per-session
// directory, reads session.json for the canonical metadata, and
// peeks messages.jsonl for the most recent assistant text.
//
// Lines whose `op` is not "add" (the file is a small journal: we
// only care about creates) are ignored. The function never fails
// the whole call because of one bad workspace: a missing or
// malformed entry is dropped and the rest are returned.
func readKiroIndex(root string) ([]indexEntry, error) {
	indexDir := filepath.Join(root, "session-index")
	entries, err := os.ReadDir(indexDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, fmt.Errorf("read kiro session index: %w", err)
	}
	out := make([]indexEntry, 0, 8)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(indexDir, entry.Name())
		lines, err := readKiroIndexFile(path)
		if err != nil {
			continue
		}
		out = append(out, lines...)
	}
	if len(out) == 0 {
		return nil, domain.ErrNoActiveSession
	}
	return out, nil
}

// readKiroIndexFile parses one JSONL file under session-index. It
// keeps only the latest `add` op per `sessionPath` (the file is a
// small journal, so duplicates can accumulate when the IDE
// re-registers a session).
func readKiroIndexFile(path string) ([]indexEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	latest := map[string]indexEntry{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var rec struct {
			Op          string `json:"op"`
			SessionPath string `json:"sessionPath"`
			At          int64  `json:"at"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Op != "add" || rec.SessionPath == "" {
			continue
		}
		entry := latest[rec.SessionPath]
		entry.Path = rec.SessionPath
		if rec.At > 0 {
			entry.TouchedAt = time.UnixMilli(rec.At).UTC()
		}
		latest[rec.SessionPath] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	out := make([]indexEntry, 0, len(latest))
	for relPath, entry := range latest {
		// `entry.Path` is relative to ~/.kiro/sessions/.
		// Resolve to the on-disk location and load session.json
		// for the canonical metadata.
		absSessionDir := filepath.Join(filepath.Dir(path), "..", "sessions", relPath)
		// filepath.Clean collapses the "../" hop.
		absSessionDir = filepath.Clean(absSessionDir)
		enrichFromSessionJSON(&entry, absSessionDir)
		// Drop entries whose session.json we could not read AND
		// the index has no useful timestamp. The user would
		// otherwise see ghost entries that we cannot title.
		if entry.Title == "" && entry.Workspace == "" && entry.TouchedAt.IsZero() {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// enrichFromSessionJSON reads session.json for the canonical
// metadata and messages.jsonl for the last assistant text. The
// on-disk format is documented in
// ~/.kiro/sessions/<hash>/<id>/session.json (schemaVersion
// 1.0.0, see kiro-cli). Missing files leave the entry unchanged.
func enrichFromSessionJSON(entry *indexEntry, sessionDir string) {
	metaPath := filepath.Join(sessionDir, "session.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return
	}
	var doc struct {
		ID             string   `json:"id"`
		Title          string   `json:"title"`
		WorkspacePaths []string `json:"workspacePaths"`
		RootPaths      []string `json:"rootPaths"`
		CreatedAt      string   `json:"createdAt"`
		LastModifiedAt string   `json:"lastModifiedAt"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	if doc.ID != "" {
		entry.SessionID = doc.ID
	}
	if doc.Title != "" {
		entry.Title = doc.Title
	}
	if len(doc.WorkspacePaths) > 0 && doc.WorkspacePaths[0] != "" {
		entry.Workspace = doc.WorkspacePaths[0]
	} else if len(doc.RootPaths) > 0 && doc.RootPaths[0] != "" {
		entry.Workspace = doc.RootPaths[0]
	}
	if t, err := parseKiroTimestamp(firstNonEmpty(doc.LastModifiedAt, doc.CreatedAt)); err == nil && !t.IsZero() {
		entry.TouchedAt = t
	}
	// Fall back to the file's mtime when neither timestamp is
	// present (older Kiro builds that did not stamp the JSON).
	if entry.TouchedAt.IsZero() {
		if info, err := os.Stat(metaPath); err == nil {
			entry.TouchedAt = info.ModTime().UTC()
		}
	}
	entry.Preview = peekAssistantText(filepath.Join(sessionDir, "messages.jsonl"))
}

// peekAssistantText returns the most recent assistant message
// in messages.jsonl. The file is a chronological log; we read it
// fully (sessions are bounded to a few MB even for long runs) and
// walk the lines from the bottom. The first non-empty assistant
// text wins, so the function is O(n) in the file size and matches
// the user's "what did the model just say" mental model.
func peekAssistantText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	for i := len(lines) - 1; i >= 0; i-- {
		text := extractAssistantText(lines[i])
		if text != "" {
			if len(text) > 240 {
				return text[:240] + "…"
			}
			return text
		}
	}
	return ""
}

// extractAssistantText pulls the assistant text out of one
// messages.jsonl line. The Kiro wire format (per
// ~/.kiro/sessions/<id>/messages.jsonl) keeps the response as a
// single `content` string at the top level of the payload,
// alongside operational fields (operationType, executionId,
// _meta). Older revisions used an array of typed parts; both
// shapes are accepted so the locator keeps working across Kiro
// releases.
func extractAssistantText(line string) string {
	var env struct {
		Payload struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return ""
	}
	if env.Payload.Type != "assistant" {
		return ""
	}
	if len(env.Payload.Content) == 0 {
		return ""
	}
	// Path 1: content is a string. The common case in current
	// Kiro releases.
	var asString string
	if err := json.Unmarshal(env.Payload.Content, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	// Path 2: content is an array of typed parts. Older releases.
	var asParts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(env.Payload.Content, &asParts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range asParts {
		if part.Type != "text" || part.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part.Text)
	}
	return strings.TrimSpace(b.String())
}

// firstNonEmpty returns the first non-empty argument. Used by
// enrichFromSessionJSON to prefer lastModifiedAt over createdAt
// when both are present.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseKiroTimestamp accepts the ISO 8601 strings the Kiro CLI
// stamps into session.json. Anything we cannot parse leaves the
// entry's TouchedAt zero so the caller falls back to mtime.
func parseKiroTimestamp(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unrecognised time format")
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
