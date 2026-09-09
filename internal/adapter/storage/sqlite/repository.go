package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

type Repository struct {
	db *sql.DB
}

func Open(path string) (*Repository, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS runtime_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			workspace_root TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT '',
			relative_path TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			agent_kind TEXT NOT NULL DEFAULT 'opencode',
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create runtime_state: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS directory_navigation (
			id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			current_relative_path TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create directory_navigation: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS completed_session (
			chat_id INTEGER PRIMARY KEY,
			session_id TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT '',
			project_name TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			preview TEXT NOT NULL DEFAULT '',
			completed_at TEXT NOT NULL,
			notified_at TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create completed_session: %w", err)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) LoadRuntimeState(ctx context.Context) (domain.RuntimeState, error) {
	var state domain.RuntimeState
	var updated, agentKind string
	err := r.db.QueryRowContext(ctx, `
		SELECT workspace_root, project_id, relative_path, session_id, agent_kind, updated_at
		FROM runtime_state WHERE id = 1
	`).Scan(&state.WorkspaceRoot, &state.ProjectID, &state.RelativePath, &state.SessionID, &agentKind, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeState{}, nil
	}
	if err != nil {
		return domain.RuntimeState{}, fmt.Errorf("load runtime state: %w", err)
	}
	state.AgentKind = domain.AgentKind(agentKind)
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return domain.RuntimeState{}, fmt.Errorf("parse runtime state timestamp: %w", err)
	}
	return state, nil
}

func (r *Repository) SaveRuntimeState(ctx context.Context, state domain.RuntimeState) error {
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	agentKind := string(state.AgentKind)
	if agentKind == "" {
		agentKind = string(domain.AgentOpenCode)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO runtime_state (id, workspace_root, project_id, relative_path, session_id, agent_kind, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			workspace_root = excluded.workspace_root,
			project_id = excluded.project_id,
			relative_path = excluded.relative_path,
			session_id = excluded.session_id,
			agent_kind = excluded.agent_kind,
			updated_at = excluded.updated_at
	`, state.WorkspaceRoot, state.ProjectID, state.RelativePath, state.SessionID, agentKind, state.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save runtime state: %w", err)
	}
	return nil
}

// SaveAgentState records the per-chat enabled/disabled set and the
// active agent pick. Full implementation lands in commit 6; this stub
// keeps the domain refactor green while persistence is being migrated.
func (r *Repository) SaveAgentState(_ context.Context, _ int64, _ domain.AgentKind, _ bool) error {
	return nil
}

// LoadAgentState returns the per-chat agent state. The stub returns
// an empty AgentState (no rows) so callers fall back to the default
// (opencode) until the table is created in commit 6.
func (r *Repository) LoadAgentState(_ context.Context, chatID int64) (domain.AgentState, error) {
	return domain.AgentState{
		ChatID:  chatID,
		Enabled: map[domain.AgentKind]bool{},
		Active:  "",
	}, nil
}

// ListEnabledAgents returns the union of enabled agents across every
// chat. The stub returns an empty map so /agents has nothing to
// render until commit 6 wires the table up.
func (r *Repository) ListEnabledAgents(_ context.Context) (map[domain.AgentKind]bool, error) {
	return map[domain.AgentKind]bool{}, nil
}

// CountLegacySessions returns the number of rows in runtime_state
// whose agent_kind is the empty string. With the DEFAULT 'opencode'
// in the schema this should be zero on fresh installs, but historic
// databases upgraded via the PR-1 migration can have empty values
// until /agents migrate runs once.
func (r *Repository) CountLegacySessions(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_state WHERE agent_kind = ''`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count legacy sessions: %w", err)
	}
	return n, nil
}

// MarkLegacySessionsAsOpenCode rewrites every empty agent_kind row to
// 'opencode'. Returns the affected count.
func (r *Repository) MarkLegacySessionsAsOpenCode(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE runtime_state SET agent_kind = 'opencode' WHERE agent_kind = ''`)
	if err != nil {
		return 0, fmt.Errorf("mark legacy sessions as opencode: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}

func (r *Repository) SaveNavigation(ctx context.Context, state domain.NavigationState) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO directory_navigation (id, chat_id, current_relative_path, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			chat_id = excluded.chat_id,
			current_relative_path = excluded.current_relative_path,
			expires_at = excluded.expires_at,
			created_at = excluded.created_at
	`, state.ID, state.ChatID, state.CurrentRelativePath, state.ExpiresAt.UTC().Format(time.RFC3339Nano), state.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save navigation state: %w", err)
	}
	return nil
}

func (r *Repository) GetNavigation(ctx context.Context, id string) (domain.NavigationState, error) {
	var state domain.NavigationState
	var expiresAt, createdAt string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, chat_id, current_relative_path, expires_at, created_at
		FROM directory_navigation WHERE id = ?
	`, id).Scan(&state.ID, &state.ChatID, &state.CurrentRelativePath, &expiresAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NavigationState{}, domain.ErrNavigationNotFound
	}
	if err != nil {
		return domain.NavigationState{}, fmt.Errorf("load navigation state: %w", err)
	}
	state.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.NavigationState{}, fmt.Errorf("parse navigation expiry: %w", err)
	}
	state.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.NavigationState{}, fmt.Errorf("parse navigation creation time: %w", err)
	}
	if !time.Now().UTC().Before(state.ExpiresAt) {
		_ = r.DeleteNavigation(ctx, id)
		return domain.NavigationState{}, domain.ErrNavigationNotFound
	}
	return state, nil
}

func (r *Repository) DeleteNavigation(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM directory_navigation WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete navigation state: %w", err)
	}
	return nil
}

func (r *Repository) SaveCompletedSession(ctx context.Context, snapshot domain.CompletedSession) error {
	completedAt := snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	var notifiedAt string
	if !snapshot.NotifiedAt.IsZero() {
		notifiedAt = snapshot.NotifiedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO completed_session (chat_id, session_id, project_id, project_name, directory, title, preview, completed_at, notified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			session_id = excluded.session_id,
			project_id = excluded.project_id,
			project_name = excluded.project_name,
			directory = excluded.directory,
			title = excluded.title,
			preview = excluded.preview,
			completed_at = excluded.completed_at,
			notified_at = CASE WHEN excluded.notified_at = '' THEN completed_session.notified_at ELSE excluded.notified_at END
	`,
		snapshot.ChatID, snapshot.SessionID, snapshot.ProjectID, snapshot.ProjectName,
		snapshot.Directory, snapshot.Title, snapshot.Preview, completedAt, notifiedAt,
	)
	if err != nil {
		return fmt.Errorf("save completed session: %w", err)
	}
	return nil
}

func (r *Repository) LoadCompletedSession(ctx context.Context, chatID int64) (domain.CompletedSession, bool, error) {
	var snapshot domain.CompletedSession
	var completedAt, notifiedAt string
	err := r.db.QueryRowContext(ctx, `
		SELECT chat_id, session_id, project_id, project_name, directory, title, preview, completed_at, notified_at
		FROM completed_session WHERE chat_id = ?
	`, chatID).Scan(&snapshot.ChatID, &snapshot.SessionID, &snapshot.ProjectID, &snapshot.ProjectName,
		&snapshot.Directory, &snapshot.Title, &snapshot.Preview, &completedAt, &notifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CompletedSession{}, false, nil
	}
	if err != nil {
		return domain.CompletedSession{}, false, fmt.Errorf("load completed session: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, completedAt); err == nil {
		snapshot.CompletedAt = t
	}
	if notifiedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, notifiedAt); err == nil {
			snapshot.NotifiedAt = t
		}
	}
	return snapshot, true, nil
}

func (r *Repository) MarkNotified(ctx context.Context, chatID int64, when time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE completed_session SET notified_at = ? WHERE chat_id = ?
	`, when.UTC().Format(time.RFC3339Nano), chatID)
	if err != nil {
		return fmt.Errorf("mark completed session notified: %w", err)
	}
	return nil
}
