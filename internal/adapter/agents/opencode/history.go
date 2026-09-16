package agents_opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// DefaultDBPath returns the on-disk SQLite store opencode keeps its
// sessions in. Both the interactive TUI and `opencode serve` persist to
// the same file, so reading it lets the bot follow a locally-launched
// `opencode` without a running HTTP server.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db"), nil
}

// History is a read-only view over opencode's SQLite store. It is safe
// for concurrent use; the underlying *sql.DB owns the connection pool.
type History struct {
	db *sql.DB
}

// OpenHistory opens the database read-only. The caller owns the handle
// and must Close it on shutdown.
func OpenHistory(path string) (*History, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("opencode history: empty db path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("stat opencode db %q: %w", path, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open opencode db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open opencode db read-only: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 2000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set opencode db busy timeout: %w", err)
	}
	return &History{db: db}, nil
}

// Close releases the database handle. Safe on a nil receiver.
func (h *History) Close() error {
	if h == nil || h.db == nil {
		return nil
	}
	return h.db.Close()
}

// Locate returns the session with the newest time_updated across every
// project, the same "freshest wins" heuristic the HTTP locator used.
// Returns ErrNoActiveSession when the store has no sessions yet.
func (h *History) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if h == nil || h.db == nil {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	var (
		id        string
		directory string
		title     string
		updated   int64
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(directory, ''), COALESCE(title, ''), time_updated
		FROM session
		ORDER BY time_updated DESC
		LIMIT 1`).Scan(&id, &directory, &title, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ActiveSession{}, domain.ErrNoActiveSession
		}
		return domain.ActiveSession{}, fmt.Errorf("locate opencode session: %w", err)
	}
	touched := time.Time{}
	if updated > 0 {
		touched = time.UnixMilli(updated).UTC()
	}
	return domain.ActiveSession{
		Kind:      domain.AgentOpenCode,
		SessionID: id,
		Project:   filepathBaseName(directory),
		Directory: directory,
		Title:     title,
		Preview:   h.latestAssistantPreview(ctx, id),
		TouchedAt: touched,
		Source:    "sqlite",
	}, nil
}

// ListMessages reconstructs the session's message list from the
// message/part tables, oldest first, mirroring the shape the HTTP
// adapter returns. Non-text parts keep their type with an empty Text so
// the watcher can still see them.
func (h *History) ListMessages(ctx context.Context, sessionID string) ([]domain.Message, error) {
	if h == nil || h.db == nil {
		return nil, errors.New("opencode history: not available")
	}
	if sessionID == "" {
		return nil, errors.New("session id must not be empty")
	}
	rows, err := h.db.QueryContext(ctx, `
		SELECT m.id, m.data, p.data
		FROM message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created ASC, m.id ASC, p.time_created ASC, p.id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list opencode messages: %w", err)
	}
	defer rows.Close()

	var (
		out   []domain.Message
		index = map[string]int{}
	)
	for rows.Next() {
		var (
			messageID string
			messageJS string
			partJS    sql.NullString
		)
		if err := rows.Scan(&messageID, &messageJS, &partJS); err != nil {
			return nil, fmt.Errorf("scan opencode message: %w", err)
		}
		idx, ok := index[messageID]
		if !ok {
			out = append(out, domain.Message{
				Info: domain.MessageInfo{
					ID:        messageID,
					SessionID: sessionID,
					Role:      messageRole(messageJS),
				},
			})
			idx = len(out) - 1
			index[messageID] = idx
		}
		if partJS.Valid {
			out[idx].Parts = append(out[idx].Parts, messagePart(partJS.String))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode messages: %w", err)
	}
	return out, nil
}

// latestAssistantPreview returns a trimmed snippet of the most recent
// assistant text, matching the preview the HTTP locator surfaces for
// /resume. Best-effort: any failure yields an empty string.
func (h *History) latestAssistantPreview(ctx context.Context, sessionID string) string {
	rows, err := h.db.QueryContext(ctx, `
		SELECT m.id, m.data, p.data
		FROM message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created DESC, p.time_created DESC
		LIMIT 200`, sessionID)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var (
		lastID   string
		lastText strings.Builder
	)
	for rows.Next() {
		var (
			messageID string
			messageJS string
			partJS    sql.NullString
		)
		if err := rows.Scan(&messageID, &messageJS, &partJS); err != nil {
			return ""
		}
		if lastID == "" {
			if messageRole(messageJS) != "assistant" {
				continue
			}
			lastID = messageID
		}
		if messageID != lastID || !partJS.Valid {
			continue
		}
		part := messagePart(partJS.String)
		if part.Type != "text" || part.Text == "" {
			continue
		}
		if lastText.Len() > 0 {
			lastText.WriteString("\n\n")
		}
		lastText.WriteString(part.Text)
	}
	out := strings.TrimSpace(lastText.String())
	if len(out) > 240 {
		out = out[:240] + "…"
	}
	return out
}

// messageRole extracts the role from a message row's JSON payload.
func messageRole(data string) string {
	var meta struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(data), &meta); err != nil {
		return ""
	}
	return meta.Role
}

// messagePart extracts the type/text from a part row's JSON payload.
func messagePart(data string) domain.MessagePart {
	var part struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(data), &part); err != nil {
		return domain.MessagePart{}
	}
	return domain.MessagePart{Type: part.Type, Text: part.Text}
}

// HistoryLocator adapts History to the domain.SessionLocator port so
// the watcher can auto-follow a locally-launched `opencode`.
type HistoryLocator struct {
	history *History
}

// NewHistoryLocator builds the locator around an open store.
func NewHistoryLocator(history *History) *HistoryLocator {
	return &HistoryLocator{history: history}
}

// Kind reports the agent this locator serves.
func (l *HistoryLocator) Kind() domain.AgentKind { return domain.AgentOpenCode }

// Locate delegates to the store.
func (l *HistoryLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if l == nil || l.history == nil {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	return l.history.Locate(ctx)
}

// Compile-time guard: HistoryLocator must satisfy the domain port.
var _ domain.SessionLocator = (*HistoryLocator)(nil)
