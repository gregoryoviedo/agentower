package usecase_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

func TestNavigationServiceValidatesChatAndNavigates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work", "work1"), 0o700); err != nil {
		t.Fatal(err)
	}
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := usecase.NewNavigationService(browser, store)

	state, entries, err := service.Start(context.Background(), 10)
	if err != nil || len(entries) != 1 || entries[0].Name != "work" {
		t.Fatalf("Start = %#v, %#v, %v", state, entries, err)
	}
	if _, _, err := service.Enter(context.Background(), state.ID, 99, "work"); err == nil {
		t.Fatal("Enter accepted a different chat")
	}
	state, entries, err = service.Enter(context.Background(), state.ID, 10, "work")
	if err != nil || state.CurrentRelativePath != "work" || len(entries) != 1 {
		t.Fatalf("Enter = %#v, %#v, %v", state, entries, err)
	}
	project, err := service.Select(context.Background(), state.ID, 10, "work/work1")
	if err != nil || project.RelativePath != "work/work1" {
		t.Fatalf("Select = %#v, %v", project, err)
	}
	if _, err := store.GetNavigation(context.Background(), state.ID); !errors.Is(err, domain.ErrNavigationNotFound) {
		t.Fatalf("navigation state still exists: %v", err)
	}
}
