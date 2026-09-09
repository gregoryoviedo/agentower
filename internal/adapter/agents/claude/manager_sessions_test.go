package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeHome redirects os.UserHomeDir to a temp dir so the session
// history root resolves inside the test sandbox instead of the
// developer's real ~/.claude folder. The override is reverted on
// test cleanup.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// On macOS the UserHomeDir falls back to a few well-known
	// locations before honouring $HOME. Override HOME via t.Setenv
	// for the test process and make sure USERPROFILE is also
	// pointed at the temp dir in case the runtime prefers it.
	t.Setenv("USERPROFILE", home)
	return home
}

// writeSessionJSONL creates ~/.claude/projects/<sanitized cwd>/<id>.jsonl
// in the temp home and writes the given lines into it. Used by the
// listSessions / readSessionMessages tests. The sanitization mirrors
// the production code in manager.sessionHistoryRoot: slashes become
// dashes and the result always starts with a leading dash.
func writeSessionJSONL(t *testing.T, home, id string, lines []string) {
	t.Helper()
	workdir := "/Users/test/dev/proj"
	sanitized := strings.ReplaceAll(workdir, "/", "-")
	if !strings.HasPrefix(sanitized, "-") {
		sanitized = "-" + sanitized
	}
	dir := filepath.Join(home, ".claude", "projects", sanitized)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// projectDirFor returns the same sanitized path the manager uses so
// the fixtures land exactly where listSessions / readSessionMessages
// expect them.
func projectDirFor(home, workdir string) string {
	sanitized := strings.ReplaceAll(workdir, "/", "-")
	if !strings.HasPrefix(sanitized, "-") {
		sanitized = "-" + sanitized
	}
	return filepath.Join(home, ".claude", "projects", sanitized)
}

// TestListSessionsReadsJSONLFiles proves the manager surfaces every
// .jsonl file under the project root as a session, while ignoring
// sub-directories and non-jsonl files (the latter simulate the
// .lock and .tmp artefacts Claude Code writes during a turn).
func TestListSessionsReadsJSONLFiles(t *testing.T) {
	home := withFakeHome(t)
	workdir := "/Users/test/dev/proj"
	writeSessionJSONL(t, home, "abc123def456", []string{`{"type":"user"}`})
	writeSessionJSONL(t, home, "xyz789ghi012", []string{`{"type":"assistant"}`})
	if err := os.WriteFile(filepath.Join(projectDirFor(home, workdir), "session.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDirFor(home, workdir), "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	m := NewManager("claude", 4097)
	m.MarkStarted(workdir)

	sessions, err := m.listSessions(context.Background())
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2; sessions=%#v", len(sessions), sessions)
	}
	gotIDs := map[string]bool{}
	for _, s := range sessions {
		gotIDs[s.ID] = true
		if s.Directory != workdir {
			t.Errorf("session %s Directory = %q, want %q", s.ID, s.Directory, workdir)
		}
		if s.Title == "" {
			t.Errorf("session %s has empty Title", s.ID)
		}
	}
	for _, id := range []string{"abc123def456", "xyz789ghi012"} {
		if !gotIDs[id] {
			t.Errorf("session %s missing from result", id)
		}
	}
}

// TestListSessionsReturnsEmptyWhenProjectDirMissing makes sure the
// manager swallows ENOENT (the project folder only exists after the
// first Claude turn) and returns an empty slice so the bot handler
// can fall back to "no sessions, use /sessions new" cleanly.
func TestListSessionsReturnsEmptyWhenProjectDirMissing(t *testing.T) {
	withFakeHome(t)
	m := NewManager("claude", 4097)
	m.MarkStarted("/Users/test/dev/never-touched")
	sessions, err := m.listSessions(context.Background())
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}

// TestListSessionsWithoutWorkingDirIsSafe proves the manager never
// panics when Started() is true but WorkingDir() has been cleared
// (e.g. after MarkStopped).
func TestListSessionsWithoutWorkingDirIsSafe(t *testing.T) {
	withFakeHome(t)
	m := NewManager("claude", 4097)
	m.MarkStarted("")
	sessions, err := m.listSessions(context.Background())
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}

// TestReadSessionMessagesParsesUserAndAssistantTurns covers the
// happy path: a JSONL file with one user turn and one assistant turn
// yields two Messages in the right order with the right parts.
func TestReadSessionMessagesParsesUserAndAssistantTurns(t *testing.T) {
	home := withFakeHome(t)
	workdir := "/Users/test/dev/proj"
	writeSessionJSONL(t, home, "s1", []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hola"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hola humano"}]}}`,
		`{"type":"result","subtype":"success"}`,
	})

	m := NewManager("claude", 4097)
	m.MarkStarted(workdir)
	msgs, err := m.readSessionMessages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("readSessionMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (system and result filtered)", len(msgs))
	}
	if msgs[0].Info.Role != "user" {
		t.Errorf("msgs[0].Role = %q, want user", msgs[0].Info.Role)
	}
	if msgs[0].Parts[0].Text != "hola" {
		t.Errorf("msgs[0].Parts[0].Text = %q, want hola", msgs[0].Parts[0].Text)
	}
	if msgs[1].Info.Role != "assistant" {
		t.Errorf("msgs[1].Role = %q, want assistant", msgs[1].Info.Role)
	}
	if msgs[1].Parts[0].Text != "hola humano" {
		t.Errorf("msgs[1].Parts[0].Text = %q, want hola humano", msgs[1].Parts[0].Text)
	}
}

// TestReadSessionMessagesSkipsMalformedLines covers resilience: a
// half-written JSONL file must not abort the whole read. The parser
// drops the bad line and continues with the rest.
func TestReadSessionMessagesSkipsMalformedLines(t *testing.T) {
	home := withFakeHome(t)
	workdir := "/Users/test/dev/proj"
	writeSessionJSONL(t, home, "s2", []string{
		`{not-json`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`also-not-json`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"world"}]}}`,
	})

	m := NewManager("claude", 4097)
	m.MarkStarted(workdir)
	msgs, err := m.readSessionMessages(context.Background(), "s2")
	if err != nil {
		t.Fatalf("readSessionMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (malformed lines dropped)", len(msgs))
	}
	if msgs[0].Parts[0].Text != "hello" || msgs[1].Parts[0].Text != "world" {
		t.Fatalf("messages decoded out of order: %#v", msgs)
	}
}

// TestReadSessionMessagesMissingFileIsSafe covers the no-history
// case (a brand-new session the manager has not seen yet). The
// function must return an empty slice without panicking.
func TestReadSessionMessagesMissingFileIsSafe(t *testing.T) {
	withFakeHome(t)
	m := NewManager("claude", 4097)
	m.MarkStarted("/Users/test/dev/proj")
	msgs, err := m.readSessionMessages(context.Background(), "never-existed")
	if err != nil {
		t.Fatalf("readSessionMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0", len(msgs))
	}
}

// TestSessionHistoryRootSanitizesWorkdir makes sure the path layout
// matches Claude Code's convention: slashes become dashes and the
// result always starts with a dash. This is what `claude --resume`
// expects so the manager must produce identical paths.
func TestSessionHistoryRootSanitizesWorkdir(t *testing.T) {
	home := withFakeHome(t)
	m := NewManager("claude", 4097)
	m.MarkStarted("/Users/test/dev/proj")
	got, err := m.sessionHistoryRoot()
	if err != nil {
		t.Fatalf("sessionHistoryRoot: %v", err)
	}
	want := filepath.Join(home, ".claude", "projects", "-Users-test-dev-proj")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestNewSessionIDIsHexShape verifies the UUID-like id format the
// manager hands back. It must be hex-only and 32 chars long so it
// round-trips through the wire format without escaping.
func TestNewSessionIDIsHexShape(t *testing.T) {
	m := NewManager("claude", 4097)
	id := m.NewSessionID()
	if len(id) != 32 {
		t.Fatalf("len(id) = %d, want 32", len(id))
	}
	for _, r := range id {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'a' && r <= 'f' {
			continue
		}
		t.Fatalf("id %q contains non-hex character %q", id, r)
	}
	// Two consecutive calls must yield distinct ids; collision
	// would corrupt the session key.
	if m.NewSessionID() == id {
		t.Fatal("two NewSessionID calls returned the same value")
	}
}

// TestMarkStartedAndStoppedReflectsState exercises the lifecycle
// flags the adapter reads (Started, WorkingDir) so the bot can
// decide whether to spawn a subprocess on the next prompt.
func TestMarkStartedAndStoppedReflectsState(t *testing.T) {
	m := NewManager("claude", 4097)
	if m.Started() {
		t.Fatal("fresh manager reports Started = true")
	}
	m.MarkStarted("/Users/test/dev/proj")
	if !m.Started() {
		t.Fatal("MarkStarted did not flip Started to true")
	}
	if m.WorkingDir() != "/Users/test/dev/proj" {
		t.Fatalf("WorkingDir = %q, want /Users/test/dev/proj", m.WorkingDir())
	}
	m.MarkStopped()
	if m.Started() {
		t.Fatal("MarkStopped did not flip Started to false")
	}
}

// TestAdapterFileStatusFallsBackToGitDiff covers the git diff
// fallback the adapter uses when Claude Code does not expose a
// native file diff endpoint. We seed a git repo with a tracked file,
// commit it, then modify the tracked file and assert the labels
// match git's --name-status codes.
func TestAdapterFileStatusFallsBackToGitDiff(t *testing.T) {
	requireGit(t)
	workdir := initGitRepo(t)
	tracked := filepath.Join(workdir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "untracked.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, workdir, "tracked.txt")
	gitCommit(t, workdir, "initial")

	// Modify the tracked file so the next diff has a real change.
	if err := os.WriteFile(tracked, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager("claude", 4097)
	m.MarkStarted(workdir)
	adapter := NewAdapter(m)
	changes, err := adapter.FileStatus(context.Background(), "x")
	if err != nil {
		t.Fatalf("FileStatus: %v", err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.Path] = c.Status
	}
	if got["tracked.txt"] != "modified" {
		t.Errorf("tracked.txt status = %q, want modified (full changes: %#v)", got["tracked.txt"], changes)
	}
	if _, ok := got["untracked.txt"]; ok {
		t.Errorf("untracked.txt should not appear in --name-status output (it is untracked)")
	}
}

// TestAdapterFileStatusWithoutWorkdirIsSafe covers the edge case
// where the manager has not been told the project folder yet. The
// adapter returns an empty slice rather than panicking.
func TestAdapterFileStatusWithoutWorkdirIsSafe(t *testing.T) {
	m := NewManager("claude", 4097)
	adapter := NewAdapter(m)
	changes, err := adapter.FileStatus(context.Background(), "x")
	if err != nil {
		t.Fatalf("FileStatus: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("got %d changes, want 0", len(changes))
	}
}

// TestAdapterSendPromptRequiresStartedManager covers the
// defensive check in the adapter: SendPrompt before MarkStarted
// must return an error rather than silently hanging on a missing
// subprocess.
func TestAdapterSendPromptRequiresStartedManager(t *testing.T) {
	m := NewManager("claude", 4097)
	adapter := NewAdapter(m)
	_, err := adapter.SendPrompt(context.Background(), "x", "hola")
	if err == nil {
		t.Fatal("SendPrompt on an unstarted manager returned nil")
	}
}

// (no extra fixtures needed — the parser tests above exercise every
// code path on the session-history helper.)
