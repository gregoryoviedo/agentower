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

// findSessionFile looks for <sessionID>.jsonl under every project
// directory in <base>/projects. Session ids are globally unique, so the
// first match wins. This lets history readers resolve a session that
// lives outside the manager's current workdir.
func findSessionFile(base, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	projects := filepath.Join(base, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return "", false
	}
	target := sessionID + ".jsonl"
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projects, entry.Name(), target)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}
