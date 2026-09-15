package claude

import (
	"os"
	"path/filepath"
	"strings"
)

// projectDirCandidates lists the sanitized "<cwd>" directory names
// Claude Code may use under ~/.claude/projects for workdir, in
// preference order. Claude Code's normalization has shifted across
// releases and differs per OS (notably the Windows drive colon), so
// readers probe each candidate and pick the one that exists; the first
// candidate is the canonical name used when creating a new directory.
func projectDirCandidates(workdir string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		if raw == "" {
			return
		}
		variants := []string{raw}
		if !strings.HasPrefix(raw, "-") {
			variants = append(variants, "-"+raw)
		}
		for _, s := range variants {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	// Replaces both separators (covers Windows backslash and POSIX slash).
	add(strings.NewReplacer("/", "-", "\\", "-").Replace(workdir))
	// Also folds the Windows drive colon.
	add(strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(workdir))
	// Historical POSIX-only scheme.
	add(strings.ReplaceAll(workdir, string(os.PathSeparator), "-"))
	if len(out) == 0 {
		return []string{"-"}
	}
	return out
}

// resolveProjectDir returns the project directory for workdir, preferring
// an existing candidate under <base>/projects and falling back to the
// canonical first candidate so callers can create it when missing.
func resolveProjectDir(base, workdir string) string {
	candidates := projectDirCandidates(workdir)
	for _, name := range candidates {
		dir := filepath.Join(base, "projects", name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return filepath.Join(base, "projects", candidates[0])
}
