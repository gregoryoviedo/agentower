package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestRepositoryExpiresNavigationState(t *testing.T) {
	repository, err := sqlite.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	now := time.Now().UTC()
	state := domain.NavigationState{ID: "nav-1", ChatID: 42, ExpiresAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Minute)}
	if err := repository.SaveNavigation(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetNavigation(context.Background(), state.ID); !errors.Is(err, domain.ErrNavigationNotFound) {
		t.Fatalf("GetNavigation error = %v, want ErrNavigationNotFound", err)
	}
}
