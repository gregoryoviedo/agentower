package copilot

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

func writeCLIStore(t *testing.T, root, sessionID string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "session-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, cwd TEXT, summary TEXT, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id,cwd,summary,created_at,updated_at) VALUES (?,?,?,?,?)`,
		sessionID, "/tmp/proj", "cli session", "2026-09-15T19:34:25.214Z", "2026-09-15T19:34:25.214Z"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionInCLIStore(t *testing.T) {
	root := t.TempDir()
	writeCLIStore(t, root, "cli-session-1")

	if !sessionInCLIStore(root, "cli-session-1") {
		t.Fatal("expected the CLI session to be found")
	}
	if sessionInCLIStore(root, "vscode-session") {
		t.Fatal("unknown session must not be reported as present")
	}
	if sessionInCLIStore(filepath.Join(root, "missing"), "cli-session-1") {
		t.Fatal("missing store must yield false")
	}
}

// TestSendPromptRejectsNonCLISession guards the fail-fast path: a
// session that is not in the Copilot CLI store must return the
// resumability sentinel before any ACP subprocess is spawned.
func TestSendPromptRejectsNonCLISession(t *testing.T) {
	m := NewManager(LaunchConfig{Bin: "copilot-does-not-exist"})
	m.SetCLIStoreDir(t.TempDir()) // empty store
	m.MarkStarted(t.TempDir())

	_, err := m.SendPrompt(context.Background(), "vscode-session", "hola")
	if !errors.Is(err, domain.ErrSessionNotResumable) {
		t.Fatalf("err = %v, want ErrSessionNotResumable", err)
	}
}
