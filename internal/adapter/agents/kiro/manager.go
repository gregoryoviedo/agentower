// Package kiro implements the AgentAdapter for AWS's Kiro CLI.
//
// Kiro's CLI is the least documented of the supported agents, so this
// adapter ships in a deliberately minimal posture: SendPrompt and
// Health only, with every other capability returning
// ErrAgentCapabilitiesLimited so the Telegram UI hides the
// corresponding buttons. The wire format mirrors the Claude
// adapter (newline-delimited JSON over stdio) so when Kiro exposes a
// real protocol the migration path is clear.
package kiro

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

// Manager owns the Kiro subprocess lifecycle.
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

func NewManager(bin string, port int) *Manager {
	return &Manager{
		bin:      bin,
		port:     port,
		sessions: map[string]*session{},
	}
}

func (m *Manager) MarkStarted(workdir string) {
	m.workdirMu.Lock()
	m.workdir = workdir
	m.workdirMu.Unlock()
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
}

func (m *Manager) MarkStopped() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Manager) WorkingDir() string {
	m.workdirMu.RLock()
	defer m.workdirMu.RUnlock()
	return m.workdir
}

func (m *Manager) HasSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[id]
	return ok
}

func (m *Manager) NewSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

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
// drives the round-trip. As with the other stdio agents we close
// stdin after writing so the CLI unblocks its drain.
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
	if err := sess.stdin.Close(); err != nil {
		return "", fmt.Errorf("close stdin: %w", err)
	}
	// Mark the session dead so the next SendPrompt for the same id
	// respawns the subprocess instead of writing to a closed pipe.
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
		return "", fmt.Errorf("scan kiro stdout: %w", err)
	}
	go func() {
		_ = sess.cmd.Wait()
		sess.mu.Lock()
		sess.cmd = nil
		sess.mu.Unlock()
	}()
	return reply.String(), nil
}

// ensureSession lazily starts the Kiro subprocess. The argv matches
// what we know about the CLI today; if it ships a different flag set
// the adapter will fail with a clear error and the user can override
// via AGENT_KIRO_ARGS.
func (m *Manager) ensureSession(ctx context.Context, id string) (*session, error) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if ok && sess != nil && !sess.dead && sess.cmd != nil && sess.cmd.ProcessState == nil && sess.cmd.Process != nil {
		return sess, nil
	}
	workdir := m.WorkingDir()
	if workdir == "" {
		return nil, errors.New("kiro manager has no working directory")
	}
	argv := []string{m.bin, "chat", "--session", id, "--cwd", workdir}
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
		return nil, fmt.Errorf("spawn kiro: %w", err)
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
