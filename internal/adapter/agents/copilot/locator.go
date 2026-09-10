package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what Copilot Chat session is the user
// driving inside VS Code right now" for the /continuar handler.
//
// VS Code's github.copilot-chat extension persists every chat to
//
//	<vscode-user-dir>/globalStorage/github.copilot-chat/
//
// The directory layout and on-disk format vary between Copilot Chat
// releases, so the locator treats the directory as an opaque bag of
// JSON files: it lists them, sorts by mtime, and pulls the sessionId
// / title / workspace path from the JSON contents using a defensive
// field-name map. The most recently modified file is the live
// session; older files are ignored even if they parse cleanly.
//
// The locator never starts the LSP. It only reads the disk, so it
// stays cheap and works even when VS Code is the only thing running
// (Agentower's own Copilot LSP subprocess may be offline).
type SessionLocator struct {
	mu      sync.RWMutex
	rootDir string
	now     func() time.Time
}

// SessionLocatorOptions tunes the locator. StateDir overrides the
// default VS Code globalStorage path (used by tests and by users who
// have a custom VS Code install).
type SessionLocatorOptions struct {
	StateDir string
	Now      func() time.Time
}

// NewSessionLocator builds the locator. When StateDir is empty the
// locator derives the default VS Code Copilot Chat path from the
// user's home directory and the host OS. An explicit StateDir takes
// precedence and is also how tests inject a temp dir.
func NewSessionLocator(opts SessionLocatorOptions) (*SessionLocator, error) {
	root := strings.TrimSpace(opts.StateDir)
	if root == "" {
		derived, err := defaultVSCodeCopilotDir()
		if err != nil {
			return nil, err
		}
		root = derived
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The directory may not exist yet (VS Code never opened,
			// or the extension was disabled). We still build the
			// locator so /continuar can render a clear "no
			// sessions" answer instead of crashing on startup.
			root = ""
		} else {
			return nil, fmt.Errorf("stat copilot state dir: %w", err)
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionLocator{rootDir: root, now: now}, nil
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentCopilot }

// Locate scans the VS Code globalStorage dir for the freshest JSON
// file and returns it as an ActiveSession. Returns ErrNoActiveSession
// when the directory is missing, empty, or the most recent file is
// older than the staleness threshold enforced by the caller.
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActiveSession{}, err
	}
	l.mu.RLock()
	root := l.rootDir
	l.mu.RUnlock()
	if root == "" {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.ActiveSession{}, domain.ErrNoActiveSession
		}
		return domain.ActiveSession{}, fmt.Errorf("read copilot state dir: %w", err)
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(root, name), modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	freshest := candidates[0]
	parsed, perr := parseCopilotSessionFile(freshest.path)
	if perr != nil {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	touchedAt := freshest.modTime
	if !parsed.touchedAt.IsZero() {
		touchedAt = parsed.touchedAt
	}
	return domain.ActiveSession{
		Kind:      domain.AgentCopilot,
		SessionID: parsed.sessionID,
		Project:   filepathBase(parsed.workspace),
		Directory: parsed.workspace,
		Title:     parsed.title,
		Preview:   parsed.preview,
		TouchedAt: touchedAt.UTC(),
		Source:    "fs",
	}, nil
}

// SetStateDir lets the composition root (or a Settings handler) swap
// the watched directory at runtime when the user moves their VS Code
// install.
func (l *SessionLocator) SetStateDir(root string) {
	l.mu.Lock()
	l.rootDir = strings.TrimSpace(root)
	l.mu.Unlock()
}

// defaultVSCodeCopilotDir returns the best-guess path to the
// github.copilot-chat globalStorage directory for the current OS.
// Resolution is best-effort: when the directory does not exist the
// caller treats it as "Copilot is not installed" rather than as an
// error.
func defaultVSCodeCopilotDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	var root string
	switch runtime.GOOS {
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "github.copilot-chat")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		root = filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat")
	default:
		// Linux and other unix-likes: XDG_CONFIG_HOME defaults to
		// ~/.config when unset.
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		root = filepath.Join(xdg, "Code", "User", "globalStorage", "github.copilot-chat")
	}
	return root, nil
}

type parsedCopilotSession struct {
	sessionID string
	workspace string
	title     string
	preview   string
	touchedAt time.Time
}

// parseCopilotSessionFile reads a single chat session file and
// returns the best-effort projection. The Copilot Chat JSON format
// has shifted between releases; the parser is defensive about field
// names and silently returns what it can find. The file is read in
// full — the per-file size is small (tens of KB) so streaming adds
// no value here.
func parseCopilotSessionFile(path string) (parsedCopilotSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return parsedCopilotSession{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4*1024*1024))
	if err != nil {
		return parsedCopilotSession{}, err
	}
	var doc map[string]any
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		// Not all files are JSON (e.g. a transient lock file).
		// Surface a sentinel so the locator can move on.
		return parsedCopilotSession{}, jerr
	}
	out := parsedCopilotSession{
		sessionID: firstString(doc, "sessionId", "id", "conversationId", "session_id"),
		workspace: firstString(doc, "workspacePath", "cwd", "workingDirectory", "workspaceFolder", "workspace"),
		title:     firstString(doc, "title", "name", "summary", "customTitle"),
	}
	if out.sessionID == "" {
		base := filepath.Base(path)
		out.sessionID = strings.TrimSuffix(base, ".json")
	}
	if ts := firstString(doc, "lastMessageDate", "updatedAt", "updated", "lastModified", "modified"); ts != "" {
		if t, err := parseFlexibleTime(ts); err == nil {
			out.touchedAt = t
		}
	}
	if out.title == "" {
		out.title = out.sessionID
	}
	out.preview = firstString(doc, "lastResponse", "preview", "lastMessage")
	return out, nil
}

// firstString returns the first non-empty string found at any of
// the supplied keys. Defensive against the various field names the
// Copilot Chat extension has used across releases.
func firstString(doc map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := doc[k]
		if !ok {
			continue
		}
		switch s := v.(type) {
		case string:
			if strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		case float64:
			// Some files serialise timestamps as numbers; treat
			// them as a last-resort string so the preview keeps
			// useful context.
			return strings.TrimSpace(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%v", s), "0"), "."))
		}
	}
	return ""
}

// parseFlexibleTime accepts the common time encodings the Copilot
// Chat extension has used. We only need an approximate wall clock so
// the time can be wrong by a few minutes without breaking
// /continuar; the caller filters out results older than
// AGENTOWER_STALE_AFTER.
func parseFlexibleTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	// Some files store millis since the epoch as a JSON number.
	if ms, err := parseMillis(s); err == nil {
		return time.UnixMilli(ms), nil
	}
	return time.Time{}, errors.New("unrecognised time format")
}

func parseMillis(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// filepathBase is a tiny local copy of filepath.Base to keep the
// import surface narrow.
func filepathBase(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
