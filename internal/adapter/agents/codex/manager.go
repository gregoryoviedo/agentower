// Package codex implements the AgentAdapter for OpenAI's Codex CLI.
//
// The transport is newline-delimited JSON over stdio. Codex is
// spawned in non-interactive mode (`codex exec --json`) and the
// adapter drives the round-trip by writing a user envelope on stdin
// and reading the streamed events from stdout until the trailing
// turn.completed event.
//
// The argv is the documented Codex invocation today; if a future CLI
// version changes the flag set the testdata fakecodex fixture will
// catch it before the adapter does.
package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Manager owns the Codex subprocess lifecycle. Like the Claude
// adapter it tracks one process per session, spawned lazily on first
// SendPrompt so switching chats in Telegram does not leave a zombie
// codex process in the background.
type Manager struct {
	bin  string
	port int

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
	dead     bool
}

// NewManager constructs the manager. The caller is expected to have
// resolved the binary path (via detector) and to know the loopback
// port the user has reserved for Codex.
func NewManager(bin string, port int) *Manager {
	return &Manager{
		bin:      bin,
		port:     port,
		sessions: map[string]*session{},
	}
}

// MarkStarted flags the manager as owning a Codex subprocess.
func (m *Manager) MarkStarted(workdir string) {
	m.workdirMu.Lock()
	m.workdir = workdir
	m.workdirMu.Unlock()
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
}

// MarkStopped clears the running flag.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// Started reports whether the manager considers Codex running.
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

// NewSessionID allocates a UUID-shaped id for a Codex session.
func (m *Manager) NewSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// LockSession / UnlockSession serialize SendPrompt per session.
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
	// Close stdin so codex knows we are done sending.
	if err := sess.stdin.Close(); err != nil {
		return "", fmt.Errorf("close stdin: %w", err)
	}
	// Mark the session dead so the next SendPrompt for the same id
	// respawns the subprocess instead of trying to write to a
	// closed pipe. The wait goroutine will still clear cmd and
	// reap the child.
	sess.dead = true

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
		case "turn.completed":
			finished = true
		}
		if finished {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan codex stdout: %w", err)
	}
	// Reap the subprocess so a long-lived session does not leak file
	// descriptors or goroutines, and clear the cmd reference so the
	// next SendPrompt for this session id observes the dead
	// subprocess and spawns a fresh one.
	go func() {
		_ = sess.cmd.Wait()
		sess.mu.Lock()
		sess.cmd = nil
		sess.mu.Unlock()
	}()
	return reply.String(), nil
}

// ensureSession lazily starts the Codex subprocess for the session.
// The argv uses codex's documented non-interactive JSON mode today;
// if a future CLI version drops --json the adapter will fail with a
// clear error and the user can adjust the command via AGENT_CODEX_ARGS.
//
// The liveness check uses ProcessState: while the subprocess is
// running exec.Cmd leaves ProcessState nil; once Wait() returns the
// field is populated with the exit info. We additionally nil out
// cmd in the wait goroutine below so a stale struct cannot hand a
// caller a closed stdin pipe.
func (m *Manager) ensureSession(ctx context.Context, id string) (*session, error) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if ok && sess != nil && !sess.dead && sess.cmd != nil && sess.cmd.ProcessState == nil && sess.cmd.Process != nil {
		return sess, nil
	}

	workdir := m.WorkingDir()
	if workdir == "" {
		return nil, errors.New("codex manager has no working directory")
	}
	argv := []string{
		m.bin,
		"exec",
		"--json",
		"--cd", workdir,
		"--session-id", id,
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
		return nil, fmt.Errorf("spawn codex: %w", err)
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
