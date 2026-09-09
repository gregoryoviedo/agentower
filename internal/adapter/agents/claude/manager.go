package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Manager owns the Claude Code subprocess lifecycle. It tracks one
// spawned process per active session (lazy: a session does not start
// a process until its first SendPrompt) so the user can switch between
// chats without the previous Claude process lingering in the
// background.
type Manager struct {
	bin  string
	port int // Claude Code is stdio-only today; port is reserved for future HTTP modes.

	workdirMu sync.RWMutex
	workdir   string

	mu       sync.Mutex
	running  bool
	sessions map[string]*session
}

type session struct {
	id       string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	cwd      string
	mu       sync.Mutex
	lastUsed time.Time
}

// NewManager constructs the manager. The caller is expected to have
// resolved the binary path (via detector) and to know the loopback
// port the user has reserved for Claude.
func NewManager(bin string, port int) *Manager {
	return &Manager{
		bin:      bin,
		port:     port,
		sessions: map[string]*session{},
	}
}

// MarkStarted flags the manager as owning a Claude subprocess (or
// adopting one already running). The AgentServerManager adapter
// calls this when the bot starts or when /init succeeds.
func (m *Manager) MarkStarted(workdir string) {
	m.workdirMu.Lock()
	m.workdir = workdir
	m.workdirMu.Unlock()
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
}

// MarkStopped clears the running flag. Per-session subprocesses are
// kept alive across stop/start so /continue resumes without losing
// history.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// Started reports whether the manager considers Claude running.
func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// WorkingDir returns the cwd the manager was last marked with.
func (m *Manager) WorkingDir() string {
	m.workdirMu.RLock()
	defer m.workdirMu.RUnlock()
	return m.workdir
}

// HasSession reports whether the given session id is known.
func (m *Manager) HasSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[id]
	return ok
}

// NewSessionID allocates a UUID-shaped id for a Claude session. The
// id is purely client-side; Claude Code picks its own on-disk name.
func (m *Manager) NewSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// LockSession and UnlockSession serialize SendPrompt calls per session
// so two concurrent prompts to the same Claude session do not
// interleave on stdin/stdout.
func (m *Manager) LockSession(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if ok {
		s.mu.Lock()
	}
}

func (m *Manager) UnlockSession(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if ok {
		s.mu.Unlock()
	}
}

// SessionCmd exposes the *exec.Cmd of the session so the adapter can
// drive its pipes. Returns nil if the session has no live subprocess.
func (m *Manager) SessionCmd(id string) *exec.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil
	}
	return s.cmd
}

// SendPrompt spawns (or reuses) the subprocess for sessionID and
// drives the round-trip. The newline-delimited JSON parsing lives
// here so the adapter can stay a thin shell.
func (m *Manager) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	sess, err := m.ensureSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.lastUsed = time.Now()

	payload, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode prompt: %w", err)
	}
	if _, err := sess.stdin.Write(append(payload, '\n')); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	// Close stdin so the subprocess knows the user is done sending.
	// Without this Claude Code (and our fakeclaude test fixture)
	// keep blocking on their own stdin drain and never emit events.
	if err := sess.stdin.Close(); err != nil {
		return "", fmt.Errorf("close stdin: %w", err)
	}

	scanner := bufio.NewScanner(sess.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var reply strings.Builder
	var finished bool
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		switch event["type"] {
		case "assistant":
			msg, _ := event["message"].(map[string]any)
			content, _ := msg["content"].([]any)
			for _, c := range content {
				part, _ := c.(map[string]any)
				if part["type"] == "text" {
					if txt, ok := part["text"].(string); ok {
						if reply.Len() > 0 {
							reply.WriteString("\n\n")
						}
						reply.WriteString(txt)
					}
				}
			}
		case "result":
			finished = true
		}
		if finished {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan claude stdout: %w", err)
	}
	// The subprocess exits once it has flushed the result event;
	// reap it so the goroutine and any file descriptors are released.
	go func() { _ = sess.cmd.Wait() }()
	return reply.String(), nil
}

// ensureSession lazily starts the Claude subprocess for the session.
// The argv is the documented Claude Code invocation today; if a
// future CLI version drops --output-format stream-json the adapter
// will fail with a clear error and the user can adjust the command
// via the AGENT_CLAUDE_ARGS env var (planned, see PR-2 follow-ups).
func (m *Manager) ensureSession(ctx context.Context, id string) (*session, error) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if ok && sess != nil && sess.cmd != nil && sess.cmd.Process != nil {
		return sess, nil
	}

	workdir := m.WorkingDir()
	if workdir == "" {
		return nil, errors.New("claude manager has no working directory")
	}
	argv := []string{
		m.bin,
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--session-id", id,
		"--cwd", workdir,
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn claude: %w", err)
	}
	sess = &session{
		id:       id,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		cwd:      workdir,
		lastUsed: time.Now(),
	}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()
	return sess, nil
}

// listSessions returns the Claude sessions on disk that match the
// manager's working directory.
func (m *Manager) listSessions(_ context.Context) ([]domain.Session, error) {
	root, err := m.sessionHistoryRoot()
	if err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	out := []domain.Session{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		out = append(out, domain.Session{ID: id, Title: id[:min(8, len(id))], Directory: m.WorkingDir()})
	}
	return out, nil
}

// readSessionMessages parses the Claude JSONL file for the session and
// returns the messages in order. The Claude wire format is one JSON
// event per line; we collapse to one Message per role switch.
func (m *Manager) readSessionMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	root, err := m.sessionHistoryRoot()
	if err != nil {
		return nil, nil
	}
	file, err := os.Open(filepath.Join(root, sessionID+".jsonl"))
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	out := []domain.Message{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		role, _ := event["type"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		msg, _ := event["message"].(map[string]any)
		parts := []domain.MessagePart{}
		if content, ok := msg["content"].([]any); ok {
			for _, c := range content {
				part, _ := c.(map[string]any)
				text, _ := part["text"].(string)
				parts = append(parts, domain.MessagePart{Type: "text", Text: text})
			}
		}
		out = append(out, domain.Message{
			Info:  domain.MessageInfo{ID: sessionID + ":" + role, SessionID: sessionID, Role: role},
			Parts: parts,
		})
	}
	return out, nil
}

// sessionHistoryRoot returns ~/.claude/projects/<sanitized cwd>. The
// sanitization mirrors Claude Code's convention of replacing / with -.
func (m *Manager) sessionHistoryRoot() (string, error) {
	workdir := m.WorkingDir()
	if workdir == "" {
		return "", errors.New("claude manager has no working directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sanitized := strings.ReplaceAll(workdir, "/", "-")
	if sanitized != "" && !strings.HasPrefix(sanitized, "-") {
		sanitized = "-" + sanitized
	}
	return filepath.Join(home, ".claude", "projects", sanitized), nil
}
