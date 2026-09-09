package sqlite_test

import (
	"context"
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
