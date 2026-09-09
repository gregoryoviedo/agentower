package usecase_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

func TestWorkspaceBrowserNavigatesRecursively(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"lear", "work/work1", "work/work2", "personal"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := browser.List(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].RelativePath != "work/work1" || entries[1].RelativePath != "work/work2" {
		t.Fatalf("unexpected entries: %#v", entries)
	}

	project, err := browser.Select(context.Background(), "work/work1")
	if err != nil {
		t.Fatal(err)
	}
	if project.RelativePath != "work/work1" {
		t.Fatalf("unexpected project: %#v", project)
	}
}

func TestWorkspaceBrowserRejectsOutsidePathsAndEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"../", filepath.Join(outside, "file"), "escape"} {
		_, err := browser.Select(context.Background(), path)
		if !errors.Is(err, domain.ErrOutsideWorkspace) {
			t.Errorf("Select(%q) error = %v, want ErrOutsideWorkspace", path, err)
		}
	}
}

func TestWorkspaceBrowserSkipsHiddenAndEscapingDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := browser.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}
