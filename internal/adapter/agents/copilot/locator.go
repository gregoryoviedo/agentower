package copilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Copilot Chat session is the user
// driving right now" for the /resume handler.
//
// VS Code's github.copilot-chat extension stores chat sessions in
// a SQLite database called `session-store.db` under the extension's
// globalStorage dir. Older releases used one JSON file per
// session; the locator falls back to that format when the SQLite
// store is missing or empty so the bot keeps working across VS
// Code upgrades.
//
// The locator reads the `sessions` table (or scans the JSON files
// for older versions), enriches each row with the most recent
// assistant text from the `turns` table, and returns the freshest
// session as ActiveSession. Tied timestamps fall back to row
// order.
type SessionLocator struct {
	mu   sync.RWMutex
	root string
	now  func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the
// VS Code globalStorage path (used by tests and by users with a
// custom install). When empty the locator derives the default from
// the current OS.
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. It does not touch the disk
// until the first call to Locate, so the bot stays bootable on
// machines without VS Code.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	root := strings.TrimSpace(opts.StateDir)
	if root == "" {
		derived, err := defaultVSCodeCopilotDir()
		if err != nil {
			return nil, err
		}
		root = derived
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{root: root, now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentCopilot }

// Locate returns the most recent Copilot Chat session. Returns
// ErrNoActiveSession when the directory is missing, the SQLite
// store has no rows, and the legacy JSON layout has no files.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	l.mu.RLock()
	root := l.root
	l.mu.RUnlock()
	if root == "" {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.ActiveSession{}, domain.ErrNoActiveSession
		}
		return domain.ActiveSession{}, fmt.Errorf("stat copilot state dir: %w", err)
	}
	// Try the modern SQLite store first. The on-disk schema has
	// shifted at least twice in the last year; the locator tolerates
	// each variant and falls back to the JSON layout when SQLite is
	// not present (older VS Code installs).
	if sessions, err := readCopilotSQLite(root); err == nil && len(sessions) > 0 {
		return pickFreshest(sessions), nil
	} else if err != nil && !errors.Is(err, domain.ErrNoActiveSession) {
		// A real error (not just "empty"); surface it so the
		// caller logs and moves on.
		return domain.ActiveSession{}, err
	}
	sessions, err := readCopilotLegacyJSON(root)
	if err != nil {
		return domain.ActiveSession{}, err
	}
	if len(sessions) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	return pickFreshest(sessions), nil
}

// SetStateDir swaps the watched directory. Used by Settings or the
// macOS wrapper when the user moves their VS Code install at
// runtime.
func (l *SessionLocator) SetStateDir(root string) {
	l.mu.Lock()
	l.root = strings.TrimSpace(root)
	l.mu.Unlock()
}

// defaultVSCodeCopilotDir returns the github.copilot-chat
// globalStorage path for the host OS.
func defaultVSCodeCopilotDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	var root string
	switch runtime.GOOS {
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "github.copilot-chat")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		root = filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat")
	default:
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		root = filepath.Join(xdg, "Code", "User", "globalStorage", "github.copilot-chat")
	}
	return root, nil
}

// pickFreshest sorts the supplied sessions by TouchedAt desc and
// returns the first. The sort is stable so ties preserve the
// read order of the source (SQLite or filesystem).
func pickFreshest(in []parsedSession) domain.ActiveSession {
	sort.SliceStable(in, func(i, j int) bool {
		return in[i].TouchedAt.After(in[j].TouchedAt)
	})
	freshest := in[0]
	return domain.ActiveSession{
		Kind:      domain.AgentCopilot,
		SessionID: freshest.SessionID,
		Project:   filepath.Base(freshest.Workspace),
		Directory: freshest.Workspace,
		Title:     freshest.Title,
		Preview:   freshest.Preview,
		TouchedAt: freshest.TouchedAt,
		Source:    freshest.Source,
	}
}

// parsedSession is the in-memory projection the SQLite reader and
// the legacy JSON reader both fill in. The locator's "freshest"
// sort operates on this shared shape.
type parsedSession struct {
	SessionID string
	Title     string
	Workspace string
	Preview   string
	TouchedAt time.Time
	Source    string
}

// readCopilotSQLite opens session-store.db in read-only mode and
// joins `sessions` with the most recent `turns` row to enrich
// each entry with a one-line preview. Tolerant of missing tables
// (older schemas) and missing columns (newer fields).
func readCopilotSQLite(root string) ([]parsedSession, error) {
	dbPath := filepath.Join(root, "session-store.db")
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, fmt.Errorf("stat copilot session store: %w", err)
	}
	// mode=ro keeps the lookup side-effect free. We do not pass
	// immutable=1 because the extension writes through WAL and
	// an immutable reader would see an empty snapshot. The
	// session-store.db-wal file lives next to the main db on
	// every observed VS Code install.
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open copilot session store: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping copilot session store: %w", err)
	}
	// Verify the schema before issuing the real query. Older
	// builds may have a different table name or column set;
	// returning ErrNoActiveSession from here lets the legacy
	// reader take over instead of failing on a missing column.
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'`).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, fmt.Errorf("inspect copilot schema: %w", err)
	}
	// The `turns` table is the source of previews. If it is
	// missing (older schema) we still list sessions but skip
	// the subquery, leaving Preview empty.
	hasTurns := true
	var turnTable string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='turns'`).Scan(&turnTable); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			hasTurns = false
		} else {
			return nil, fmt.Errorf("inspect copilot turns table: %w", err)
		}
	}
	query := `SELECT s.id, s.cwd, s.summary, s.created_at, s.updated_at FROM sessions s ORDER BY s.updated_at DESC`
	if hasTurns {
		query = `
			SELECT s.id, s.cwd, s.summary, s.created_at, s.updated_at,
			       (SELECT assistant_response FROM turns t
			         WHERE t.session_id = s.id AND t.assistant_response IS NOT NULL
			         ORDER BY t.timestamp DESC, t.turn_index DESC LIMIT 1) AS preview
			FROM sessions s
			ORDER BY s.updated_at DESC
		`
	}
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query copilot sessions: %w", err)
	}
	defer rows.Close()
	out := make([]parsedSession, 0, 8)
	for rows.Next() {
		var (
			id, cwd, summary, createdAt, updatedAt string
			preview                                sql.NullString
		)
		if err := rows.Scan(&id, &cwd, &summary, &createdAt, &updatedAt, &preview); err != nil {
			return nil, fmt.Errorf("scan copilot session: %w", err)
		}
		touchedAt, _ := parseCopilotTimestamp(firstNonEmpty(updatedAt, createdAt))
		out = append(out, parsedSession{
			SessionID: id,
			Title:     firstNonEmpty(summary, "Copilot session "+id[:min(8, len(id))]),
			Workspace: cwd,
			Preview:   truncatePreview(preview.String, 240),
			TouchedAt: touchedAt,
			Source:    "sqlite",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate copilot sessions: %w", err)
	}
	if len(out) == 0 {
		return nil, domain.ErrNoActiveSession
	}
	return out, nil
}

// readCopilotLegacyJSON lists the per-session JSON files in the
// extension's globalStorage dir. Each file has a `sessionId`,
// `workspacePath`, `title`, and a `lastMessageDate` field; the
// locator takes the freshest file. This path covers older VS Code
// installs that predate the SQLite store.
func readCopilotLegacyJSON(root string) ([]parsedSession, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, fmt.Errorf("read copilot state dir: %w", err)
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(root, entry.Name()), modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return nil, domain.ErrNoActiveSession
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	out := make([]parsedSession, 0, len(candidates))
	for _, c := range candidates {
		parsed, err := parseLegacyJSONSession(c.path)
		if err != nil {
			continue
		}
		parsed.Source = "fs"
		// mtime is a more reliable signal than the file's
		// own lastMessageDate for "the most recent chat", so
		// prefer it when newer.
		if c.modTime.After(parsed.TouchedAt) {
			parsed.TouchedAt = c.modTime
		}
		out = append(out, parsed)
	}
	if len(out) == 0 {
		return nil, domain.ErrNoActiveSession
	}
	return out, nil
}

// parseLegacyJSONSession reads one chat session file and returns
// the best-effort projection. The Copilot Chat JSON format has
// shifted between releases; the parser is defensive about field
// names and silently returns what it can find.
func parseLegacyJSONSession(path string) (parsedSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return parsedSession{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4*1024*1024))
	if err != nil {
		return parsedSession{}, err
	}
	var doc map[string]any
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return parsedSession{}, jerr
	}
	out := parsedSession{
		SessionID: firstString(doc, "sessionId", "id", "conversationId", "session_id"),
		Workspace: firstString(doc, "workspacePath", "cwd", "workingDirectory", "workspaceFolder", "workspace"),
		Title:     firstString(doc, "title", "name", "summary", "customTitle"),
		Preview:   truncatePreview(firstString(doc, "lastResponse", "preview", "lastMessage"), 240),
	}
	if out.SessionID == "" {
		base := filepath.Base(path)
		out.SessionID = strings.TrimSuffix(base, ".json")
	}
	if ts := firstString(doc, "lastMessageDate", "updatedAt", "updated", "lastModified", "modified"); ts != "" {
		if t, err := parseCopilotTimestamp(ts); err == nil {
			out.TouchedAt = t
		}
	}
	if out.Title == "" {
		out.Title = out.SessionID
	}
	return out, nil
}

// firstNonEmpty returns the first non-empty string from the
// arguments. Used by the SQLite reader to choose between
// updated_at and created_at when the schema is partial.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// firstString returns the first non-empty string found at any of
// the supplied JSON keys. Mirrors the helper from the original
// locator implementation; kept here to keep the package's import
// surface narrow.
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

// parseCopilotTimestamp accepts the common time encodings the
// Copilot Chat extension (and its SQLite store) have used. The
// fallback path covers millis-since-epoch stored as a JSON
// number.
func parseCopilotTimestamp(s string) (time.Time, error) {
	if s == "" {
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
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	return time.Time{}, errors.New("unrecognised time format")
}

// truncatePreview keeps the preview line within Telegram's
// reasonable reply size. The Copilot Chat responses can be
// kilobytes long; we only need the opening sentence for the
// /resume card.
func truncatePreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// min is a tiny polyfill so this file builds under the module's
// go 1.22 toolchain without depending on the builtin (which is
// fine, but keeps the file self-contained).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
