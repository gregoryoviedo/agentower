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
	if err := migrateAgentState(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateCompletedSessionAgent(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Repository{db: db}, nil
}

// migrateAgentState installs the per-chat agent_state table and applies
// the runtime_state.agent_kind column for databases created before the
// multi-agent migration. Both checks are no-ops on already-up-to-date
// schemas so callers can invoke Open on every boot without paying for a
// migration every time.
func migrateAgentState(db *sql.DB) error {
	if err := ensureColumn(db, "runtime_state", "agent_kind", "opencode"); err != nil {
		return err
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_state (
			chat_id INTEGER NOT NULL,
			agent_kind TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			active INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (chat_id, agent_kind)
		)
	`)
	if err != nil {
		return fmt.Errorf("create agent_state: %w", err)
	}
	return nil
}

// migrateCompletedSessionAgent adds the agent_kind column to the
// completed_session table so /continue and /continuar can route the
// snapshot back to the right adapter. Idempotent: existing rows
// keep the default "opencode" so the historical UX is unchanged.
func migrateCompletedSessionAgent(db *sql.DB) error {
	return ensureColumn(db, "completed_session", "agent_kind", "opencode")
}

// ensureColumn applies a non-destructive ALTER TABLE that adds the
// named column with the given default. It is shared by the
// multi-agent migrations so both runtime_state.agent_kind and
// completed_session.agent_kind can be added without duplicating
// the table_info inspection.
func ensureColumn(db *sql.DB, table, column, def string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	hasColumn := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan %s column: %w", table, err)
		}
		if name == column {
			hasColumn = true
			break
		}
	}
	if hasColumn {
		return nil
	}
	stmt := `ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT '` + def + `'`
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
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

// SaveAgentState toggles the enabled flag for one (chatID, kind) pair
// and flips the active marker if this is the first time the user
// picks this kind for the chat. The function is idempotent and
// upserts on conflict.
func (r *Repository) SaveAgentState(ctx context.Context, chatID int64, kind domain.AgentKind, enabled bool) error {
	enabledFlag := 0
	if enabled {
		enabledFlag = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_state (chat_id, agent_kind, enabled, active, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, agent_kind) DO UPDATE SET
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, chatID, string(kind), enabledFlag, 0, now)
	if err != nil {
		return fmt.Errorf("save agent state: %w", err)
	}
	if enabled {
		// When an agent is enabled it becomes the active one for that
		// chat; when it is disabled we leave the active marker alone
		// so the picker remembers the previous pick.
		if _, err := r.db.ExecContext(ctx, `UPDATE agent_state SET active = 0 WHERE chat_id = ?`, chatID); err != nil {
			return fmt.Errorf("clear active marker: %w", err)
		}
		_, err = r.db.ExecContext(ctx, `
			UPDATE agent_state SET active = 1, updated_at = ?
			WHERE chat_id = ? AND agent_kind = ?
		`, now, chatID, string(kind))
		if err != nil {
			return fmt.Errorf("set active marker: %w", err)
		}
	}
	return nil
}

// LoadAgentState returns the per-chat enabled flags plus the active
// pick. The Enabled map always contains a row for every kind the
// caller may iterate over, even when the table is empty (defaults
// to enabled for opencode).
func (r *Repository) LoadAgentState(ctx context.Context, chatID int64) (domain.AgentState, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT agent_kind, enabled, active
		FROM agent_state
		WHERE chat_id = ?
	`, chatID)
	if err != nil {
		return domain.AgentState{}, fmt.Errorf("load agent state: %w", err)
	}
	defer rows.Close()
	state := domain.AgentState{
		ChatID:  chatID,
		Enabled: map[domain.AgentKind]bool{},
	}
	for rows.Next() {
		var kind string
		var enabled, active int
		if err := rows.Scan(&kind, &enabled, &active); err != nil {
			return domain.AgentState{}, fmt.Errorf("scan agent state: %w", err)
		}
		state.Enabled[domain.AgentKind(kind)] = enabled != 0
		if active != 0 {
			state.Active = domain.AgentKind(kind)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.AgentState{}, err
	}
	if len(state.Enabled) == 0 {
		state.Enabled[domain.AgentOpenCode] = true
		state.Active = domain.AgentOpenCode
	}
	return state, nil
}

// ListEnabledAgents returns the union of enabled agents across every
// chat. Useful for /agents when the user wants a global view of
// which agents have ever been turned on.
func (r *Repository) ListEnabledAgents(ctx context.Context) (map[domain.AgentKind]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT agent_kind FROM agent_state WHERE enabled = 1
	`)
	if err != nil {
		return map[domain.AgentKind]bool{}, fmt.Errorf("list enabled agents: %w", err)
	}
	defer rows.Close()
	out := map[domain.AgentKind]bool{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return out, err
		}
		out[domain.AgentKind(kind)] = true
	}
	return out, rows.Err()
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
	agentKind := string(snapshot.AgentKind)
	if agentKind == "" {
		agentKind = string(domain.AgentOpenCode)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO completed_session (chat_id, session_id, project_id, project_name, directory, title, preview, completed_at, notified_at, agent_kind)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			session_id = excluded.session_id,
			project_id = excluded.project_id,
			project_name = excluded.project_name,
			directory = excluded.directory,
			title = excluded.title,
			preview = excluded.preview,
			completed_at = excluded.completed_at,
			notified_at = CASE WHEN excluded.notified_at = '' THEN completed_session.notified_at ELSE excluded.notified_at END,
			agent_kind = excluded.agent_kind
	`,
		snapshot.ChatID, snapshot.SessionID, snapshot.ProjectID, snapshot.ProjectName,
		snapshot.Directory, snapshot.Title, snapshot.Preview, completedAt, notifiedAt, agentKind,
	)
	if err != nil {
		return fmt.Errorf("save completed session: %w", err)
	}
	return nil
}

func (r *Repository) LoadCompletedSession(ctx context.Context, chatID int64) (domain.CompletedSession, bool, error) {
	var snapshot domain.CompletedSession
	var completedAt, notifiedAt, agentKind string
	err := r.db.QueryRowContext(ctx, `
		SELECT chat_id, session_id, project_id, project_name, directory, title, preview, completed_at, notified_at, agent_kind
		FROM completed_session WHERE chat_id = ?
	`, chatID).Scan(&snapshot.ChatID, &snapshot.SessionID, &snapshot.ProjectID, &snapshot.ProjectName,
		&snapshot.Directory, &snapshot.Title, &snapshot.Preview, &completedAt, &notifiedAt, &agentKind)
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
	snapshot.AgentKind = domain.AgentKind(agentKind)
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
