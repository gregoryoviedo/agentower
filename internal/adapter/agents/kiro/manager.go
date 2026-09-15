// Package kiro implements the AgentAdapter for AWS's Kiro CLI over
// the Agent Client Protocol (ACP). Kiro exposes an ACP server via
// `kiro-cli acp --agent-engine v3 --auth-method cli`, which lets
// Agentower create, resume and drive Kiro sessions from Telegram —
// including chats the user started in the Kiro IDE (session/resume).
package kiro

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/acp"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Manager owns the Kiro ACP subprocess lifecycle. Sessions are lazy:
// the subprocess spawns on the first prompt or session creation and
// stays alive across prompts so history is preserved.
type Manager struct {
	bin  string
	port int

	mu      sync.Mutex
	workdir string
	running bool
	agent   *acp.Agent
	known   map[string]bool
}

// NewManager builds the manager. bin is the kiro-cli binary (usually
// the path the detector found inside Kiro CLI.app).
func NewManager(bin string, port int) *Manager {
	return &Manager{bin: bin, port: port, known: map[string]bool{}}
}

// MarkStarted flags the manager as owning a Kiro subprocess.
func (m *Manager) MarkStarted(workdir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workdir = workdir
	m.running = true
}

// MarkStopped clears the running flag and tears down the subprocess.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	if m.agent != nil {
		m.agent.Close()
		m.agent = nil
	}
}

// Started reports whether the manager considers Kiro running.
func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// WorkingDir returns the cwd the manager was last marked with.
func (m *Manager) WorkingDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workdir
}

// HasSession reports whether the given session id was created or
// resumed through this manager.
func (m *Manager) HasSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.known[id]
}

// ensureAgent lazily starts the ACP subprocess for the configured
// working directory.
func (m *Manager) ensureAgent(ctx context.Context) (*acp.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agent != nil && m.agent.Running() {
		return m.agent, nil
	}
	workdir := m.workdir
	if workdir == "" {
		return nil, errors.New("kiro manager has no working directory")
	}
	agent := acp.NewAgent(acp.AgentOptions{
		Bin:      m.bin,
		Args:     []string{"acp", "--agent-engine", "v3", "--auth-method", "cli"},
		Framing:  acp.FramingNewline,
		TrustAll: true,
	})
	if err := agent.Start(ctx); err != nil {
		return nil, fmt.Errorf("start kiro acp: %w", err)
	}
	m.agent = agent
	return agent, nil
}

// SendPrompt resumes (when needed) the session and sends a message,
// returning the assistant's text reply.
func (m *Manager) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	agent, err := m.ensureAgent(ctx)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	known := m.known[sessionID]
	m.mu.Unlock()
	if !known {
		if err := agent.ResumeSession(ctx, sessionID, m.WorkingDir()); err != nil {
			return "", fmt.Errorf("resume kiro session: %w", err)
		}
		m.mu.Lock()
		m.known[sessionID] = true
		m.mu.Unlock()
	}
	return agent.Prompt(ctx, sessionID, text)
}

// CreateSession starts a fresh Kiro session via ACP and returns its
// real session id.
func (m *Manager) CreateSession(ctx context.Context, cwd string) (string, error) {
	agent, err := m.ensureAgent(ctx)
	if err != nil {
		return "", err
	}
	id, err := agent.NewSession(ctx, cwd)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.known[id] = true
	m.mu.Unlock()
	return id, nil
}

// listSessions returns the sessions Kiro knows for the working
// directory via ACP session/list.
func (m *Manager) listSessions(ctx context.Context) ([]domain.Session, error) {
	agent, err := m.ensureAgent(ctx)
	if err != nil {
		return nil, err
	}
	infos, err := agent.ListSessions(ctx, m.WorkingDir())
	if err != nil {
		return nil, err
	}
	out := make([]domain.Session, 0, len(infos))
	for _, info := range infos {
		out = append(out, domain.Session{ID: info.ID, Title: firstNonEmpty(info.Summary, info.Title, "Kiro "+truncate(info.ID, 8)), Directory: info.Cwd})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
