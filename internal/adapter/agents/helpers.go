package agents

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func itoa(i int) string { return strconv.Itoa(i) }

// userHomeDir is overridable in tests; defaults to os.UserHomeDir.
var userHomeDir = os.UserHomeDir

// firstDirWithPrefix scans parent for directories whose name starts with
// prefix and returns the lexicographically last match (VS Code extension
// folders include the version in their name, so this picks the newest
// installed version).
func firstDirWithPrefix(parent string) (string, bool) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", false
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), filepath.Base(prefix(parent))) {
			matches = append(matches, filepath.Join(parent, e.Name()))
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	sort.Strings(matches)
	return matches[len(matches)-1], true
}

// prefix returns the parent directory plus everything before the final
// path separator. Used by firstDirWithPrefix to separate the "directory
// to scan" from the "name prefix to match".
func prefix(path string) string {
	dir, file := filepath.Split(path)
	if dir == "" {
		return file
	}
	return strings.TrimRight(dir, string(filepath.Separator)) + "/" + file
}

// probeHTTP returns true if the URL responds with 2xx within 1 second.
// Errors (refused, timeout, non-2xx) all map to false; the caller uses
// this only as a "is something listening?" hint.
func probeHTTP(parent context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(parent, 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
