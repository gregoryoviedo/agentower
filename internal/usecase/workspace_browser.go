package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// WorkspaceBrowser exposes only directories below the configured workspace.
// All paths accepted from Telegram are relative to the workspace root.
type WorkspaceBrowser struct {
	fs   domain.WorkspaceFS
	root string
}

func NewWorkspaceBrowser(fs domain.WorkspaceFS, workspaceRoot string) (*WorkspaceBrowser, error) {
	root, err := fs.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := fs.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, domain.ErrNotDirectory
	}
	return &WorkspaceBrowser{fs: fs, root: filepath.Clean(root)}, nil
}

func (b *WorkspaceBrowser) Root() string { return b.root }

func (b *WorkspaceBrowser) List(ctx context.Context, relativePath string) ([]domain.DirectoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, _, err := b.resolve(relativePath)
	if err != nil {
		return nil, err
	}
	entries, err := b.fs.ReadDir(absolute)
	if err != nil {
		return nil, fmt.Errorf("read workspace directory: %w", err)
	}

	result := make([]domain.DirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() {
			continue
		}
		childAbsolute := filepath.Join(absolute, entry.Name())
		resolved, err := b.fs.EvalSymlinks(childAbsolute)
		if err != nil || !b.inside(resolved) {
			continue
		}
		childRelative, err := filepath.Rel(b.root, resolved)
		if err != nil {
			continue
		}
		result = append(result, domain.DirectoryEntry{
			Name:         entry.Name(),
			RelativePath: cleanRelative(childRelative),
		})
	}
	return result, nil
}

func (b *WorkspaceBrowser) Select(ctx context.Context, relativePath string) (domain.Project, error) {
	if err := ctx.Err(); err != nil {
		return domain.Project{}, err
	}
	absolute, relative, err := b.resolve(relativePath)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.Project{
		ID:            relative,
		DisplayName:   filepath.Base(absolute),
		RelativePath:  relative,
		AbsolutePath:  absolute,
		WorkspaceRoot: b.root,
	}, nil
}

// Resolve validates a path against the workspace root and returns both the
// absolute (symlink-resolved) and the cleaned relative representations.
// Absolute paths and anything escaping the workspace are rejected; this is
// the single place the "must stay inside the root" invariant is enforced.
func (b *WorkspaceBrowser) Resolve(relativePath string) (absolute string, relative string, err error) {
	return b.resolve(relativePath)
}

func (b *WorkspaceBrowser) resolve(relativePath string) (string, string, error) {
	if filepath.IsAbs(relativePath) {
		return "", "", domain.ErrOutsideWorkspace
	}
	relativePath = filepath.Clean(relativePath)
	if relativePath == "." {
		relativePath = ""
	}
	candidate := filepath.Join(b.root, relativePath)
	resolved, err := b.fs.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	if !b.inside(resolved) {
		return "", "", domain.ErrOutsideWorkspace
	}
	info, err := b.fs.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", "", domain.ErrNotDirectory
	}
	relative, err := filepath.Rel(b.root, resolved)
	if err != nil {
		return "", "", err
	}
	return resolved, cleanRelative(relative), nil
}

func (b *WorkspaceBrowser) inside(path string) bool {
	relative, err := filepath.Rel(b.root, filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func cleanRelative(path string) string {
	if path == "." {
		return ""
	}
	return filepath.ToSlash(path)
}
