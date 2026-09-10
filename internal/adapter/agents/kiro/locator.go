package kiro

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Kiro chat session is the user
// driving right now" for the /resume handler.
//
// Kiro is an Electron-based fork of Code OSS, so it inherits
// VS Code's storage layout: every extension keeps its data under
// <userData>/User/globalStorage/<publisher>.<extension>/. The
// kiro.kiroagent extension writes its chat sessions as blobs in a
// SQLite `state.vscdb` (VS Code's storage database) under
// <userData>/User/globalStorage/kiro.kiroagent/default/.
//
// The locator reads `chat.ChatSessionStore.index` (the session
// index) and, when present, the per-session blobs at
// `chat.ChatSessionStore.<id>`. The on-disk format has shifted
// between Kiro releases, so the parser is defensive: it tries a
// handful of field names, accepts either "id" or "sessionId" as the
// session key, and silently drops sessions it cannot decode. This
// keeps the locator working when Kiro renames an internal field.
type SessionLocator struct {
	mu     sync.RWMutex
	dbPath string
	now    func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the
// Kiro userData root (used by tests and by users with a custom
// install). When empty the locator derives the default from the
// current OS.
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. It does not open the SQLite
// file until the first call to Locate; the bot stays bootable on
// machines without Kiro.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	root := strings.TrimSpace(opts.StateDir)
	if root == "" {
		derived, err := defaultKiroStateDir()
		if err != nil {
			return nil, err
		}
		root = derived
	}
	dbPath := filepath.Join(root, "User", "globalStorage", "kiro.kiroagent", "default", "state.vscdb")
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{dbPath: dbPath, now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentKiro }

// Locate opens the Kiro SQLite store (read-only, no journal touch)
// and returns the most recently active chat session. The returned
// ActiveSession is the best-effort projection; fields the locator
// could not determine are left empty. Returns ErrNoActiveSession
// when the database is missing, the index is empty, or every
// session entry is malformed.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	l.mu.RLock()
	dbPath := l.dbPath
	l.mu.RUnlock()
	if dbPath == "" {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ActiveSession{}, domain.ErrNoActiveSession
		}
		return domain.ActiveSession{}, fmt.Errorf("stat kiro state db: %w", err)
	}
	entries, err := readKiroSessionIndex(dbPath)
	if err != nil {
		return domain.ActiveSession{}, err
	}
	if len(entries) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	// Pick the freshest by the parsed timestamp; fall back to
	// the iteration order when timestamps are missing.
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
		Source:    "sqlite",
	}, nil
}

// SetStateDir swaps the watched database path. Used by Settings or
// by the macOS wrapper when the user changes the Kiro install
// location at runtime.
func (l *SessionLocator) SetStateDir(root string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dbPath = filepath.Join(strings.TrimSpace(root), "User", "globalStorage", "kiro.kiroagent", "default", "state.vscdb")
}

// defaultKiroStateDir returns the Kiro userData root for the host
// OS. Kiro mirrors Code OSS conventions: ~/Library/Application
// Support/Kiro on macOS, %APPDATA%\Kiro on Windows, $XDG_CONFIG_HOME
// or ~/.config/Kiro on Linux.
func defaultKiroStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Kiro"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Kiro"), nil
	default:
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		return filepath.Join(xdg, "Kiro"), nil
	}
}

// kiroSession is the in-memory projection of one row from the
// chat.ChatSessionStore.index blob. The locator collects these,
// sorts by TouchedAt, and returns the freshest.
type kiroSession struct {
	SessionID string
	Title     string
	Workspace string
	Preview   string
	TouchedAt time.Time
}

// readKiroSessionIndex opens the SQLite db in read-only mode and
// pulls the chat.ChatSessionStore.index blob. It also opportunistically
// enriches each session with the per-session blob at
// chat.ChatSessionStore.<id> when one exists, so titles and previews
// survive Kiro's tendency to keep the index lean.
//
// The function is package-private to keep the SQLite dependency
// isolated from the rest of the project: only this file imports
// modernc.org/sqlite. Tests can swap the implementation by
// pointing StateDir at a temp dir.
func readKiroSessionIndex(dbPath string) ([]kiroSession, error) {
	// mode=ro + immutable=1 keep the lookup cheap and side-effect
	// free. modernc.org/sqlite uses sqlite3_open_v2 under the
	// hood; the "immutable" flag is a performance hint.
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open kiro state db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping kiro state db: %w", err)
	}
	rows, err := db.Query(`SELECT key, value FROM ItemTable WHERE key LIKE 'chat.%'`)
	if err != nil {
		return nil, fmt.Errorf("query kiro state db: %w", err)
	}
	defer rows.Close()

	// First pass: the index. Second pass enriches with per-session
	// blobs when present. We materialise into maps because the
	// per-session rows may arrive in any order.
	indexBlob := []byte(nil)
	perSession := map[string][]byte{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan kiro state db: %w", err)
		}
		switch {
		case key == "chat.ChatSessionStore.index":
			indexBlob = value
		case strings.HasPrefix(key, "chat.ChatSessionStore."):
			perSession[strings.TrimPrefix(key, "chat.ChatSessionStore.")] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kiro state db: %w", err)
	}
	if len(indexBlob) == 0 {
		return nil, nil
	}
	return parseKiroIndex(indexBlob, perSession)
}

// parseKiroIndex unpacks the chat.ChatSessionStore.index blob and
// returns the surviving sessions. The JSON shape is best-effort:
//   - older Kiro versions used {"version":1,"entries":{...}}
//   - newer releases may use {"version":2,"sessions":[{...}]}
//   - field names inside each entry drift (id / sessionId,
//     title / name, lastMessageDate / updatedAt)
//
// The parser tries every shape, then enriches each entry with the
// per-session blob when one exists in the map. Malformed entries
// are dropped silently; the user would otherwise see noisy errors
// every time Kiro changes an internal field.
func parseKiroIndex(blob []byte, perSession map[string][]byte) ([]kiroSession, error) {
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, fmt.Errorf("decode kiro index: %w", err)
	}
	out := make([]kiroSession, 0, 8)
	switch entries := doc["entries"].(type) {
	case map[string]any:
		// {entries: {<id>: {...}}}
		for id, raw := range entries {
			entry, _ := raw.(map[string]any)
			sess := entryToSession(id, entry)
			if sess.SessionID == "" {
				continue
			}
			enrichFromBlob(&sess, perSession[sess.SessionID])
			out = append(out, sess)
		}
	case []any:
		// {entries: [{...}]} (defensive; not observed)
		for _, raw := range entries {
			entry, _ := raw.(map[string]any)
			id := firstString(entry, "id", "sessionId", "session_id", "uuid")
			if id == "" {
				continue
			}
			sess := entryToSession(id, entry)
			enrichFromBlob(&sess, perSession[id])
			out = append(out, sess)
		}
	}
	if list, ok := doc["sessions"].([]any); ok {
		// {sessions: [{...}]}
		for _, raw := range list {
			entry, _ := raw.(map[string]any)
			id := firstString(entry, "id", "sessionId", "session_id", "uuid")
			if id == "" {
				continue
			}
			sess := entryToSession(id, entry)
			enrichFromBlob(&sess, perSession[id])
			out = append(out, sess)
		}
	}
	return out, nil
}

// entryToSession projects one parsed JSON object into kiroSession.
// All fields are best-effort: a missing field leaves the empty
// zero value, which the caller filters out.
func entryToSession(id string, entry map[string]any) kiroSession {
	return kiroSession{
		SessionID: id,
		Title:     firstString(entry, "title", "name", "summary", "customTitle"),
		Workspace: firstString(entry, "workspacePath", "cwd", "workingDirectory", "workspaceFolder", "workspace", "folder"),
		Preview:   firstString(entry, "preview", "lastResponse", "lastMessage", "snippet"),
		TouchedAt: parseKiroTimestamp(firstString(entry, "lastMessageDate", "updatedAt", "updated", "lastModified", "modified", "lastActiveAt")),
	}
}

// enrichFromBlob layers per-session metadata on top of an
// index-only projection. The per-session blob often carries a
// better title and a more accurate timestamp than the index, so
// the locator prefers it whenever present.
func enrichFromBlob(sess *kiroSession, blob []byte) {
	if len(blob) == 0 {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		return
	}
	if title := firstString(doc, "title", "name", "summary", "customTitle"); title != "" {
		sess.Title = title
	}
	if preview := firstString(doc, "preview", "lastResponse", "lastMessage", "snippet"); preview != "" {
		sess.Preview = preview
	}
	if touched := parseKiroTimestamp(firstString(doc, "lastMessageDate", "updatedAt", "updated", "lastModified", "modified", "lastActiveAt")); !touched.IsZero() {
		sess.TouchedAt = touched
	}
	if ws := firstString(doc, "workspacePath", "cwd", "workingDirectory", "workspaceFolder", "workspace", "folder"); ws != "" {
		sess.Workspace = ws
	}
}

// firstString returns the first non-empty string found at any of
// the supplied keys. Mirrors the same defensive helper the copilot
// locator uses; the implementations are kept separate to avoid
// pulling the copilot package into kiro's dependency graph.
func firstString(doc map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := doc[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// parseKiroTimestamp accepts the time encodings the Kiro extension
// has used across releases. Anything we cannot parse becomes the
// zero time so the sort treats the entry as "unknown" and the
// caller falls back to the iteration order.
func parseKiroTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
