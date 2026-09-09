package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestRepositoryPersistsRuntimeState(t *testing.T) {
	repository, err := sqlite.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	want := domain.RuntimeState{
		WorkspaceRoot: "/Users/me/dev",
		ProjectID:     "work/work1",
		RelativePath:  "work/work1",
		SessionID:     "session-1",
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repository.SaveRuntimeState(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repository.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) || got.WorkspaceRoot != want.WorkspaceRoot || got.ProjectID != want.ProjectID || got.RelativePath != want.RelativePath || got.SessionID != want.SessionID {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRepositoryPersistsAgentKind(t *testing.T) {
	repository, err := sqlite.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	state := domain.RuntimeState{
		WorkspaceRoot: "/Users/me/dev",
		AgentKind:     domain.AgentClaude,
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repository.SaveRuntimeState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	got, err := repository.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentKind != domain.AgentClaude {
		t.Fatalf("AgentKind=%s, want claude", got.AgentKind)
	}
}

func TestRepositoryAgentStateRoundTrip(t *testing.T) {
	repository, err := sqlite.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	ctx := context.Background()
	if err := repository.SaveAgentState(ctx, 7, domain.AgentOpenCode, true); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveAgentState(ctx, 7, domain.AgentClaude, true); err != nil {
		t.Fatal(err)
	}
	got, err := repository.LoadAgentState(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled[domain.AgentOpenCode] || !got.Enabled[domain.AgentClaude] {
		t.Fatalf("enabled flags lost: %#v", got.Enabled)
	}
	if got.Active != domain.AgentClaude {
		t.Fatalf("active = %s, want claude (last saved)", got.Active)
	}
	if err := repository.SaveAgentState(ctx, 7, domain.AgentClaude, false); err != nil {
		t.Fatal(err)
	}
	got, _ = repository.LoadAgentState(ctx, 7)
	if got.Enabled[domain.AgentClaude] {
		t.Fatal("expected Claude disabled after second save")
	}
	if got.Active != domain.AgentClaude {
		t.Fatalf("active changed on disable: %s", got.Active)
	}
}

func TestRepositoryMigrationAddsAgentKindColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE runtime_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			workspace_root TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT '',
			relative_path TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO runtime_state (id, workspace_root, updated_at) VALUES (1, '/dev', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	got, err := repo.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentKind != domain.AgentOpenCode {
		t.Fatalf("AgentKind=%s, want opencode (default after migration)", got.AgentKind)
	}
	if got.WorkspaceRoot != "/dev" {
		t.Fatalf("WorkspaceRoot=%s, want /dev (row preserved)", got.WorkspaceRoot)
	}
}

func TestRepositoryMigrationIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	repo, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo.Close()
	// Re-opening must not error even though the schema already has
	// the agent_kind column and agent_state table.
	repo, err = sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer repo.Close()
}
