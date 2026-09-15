// Package copilot implements the AgentAdapter for GitHub Copilot over
// the Agent Client Protocol (ACP). The Copilot CLI exposes an ACP
// server via `copilot --acp`; Agentower drives it to create, resume and
// continue Copilot sessions from Telegram, and reads the VS Code
// session store (~/Library/Application Support/Code/...) to detect
// sessions the user started in the editor.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents/acp"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// LaunchMode keeps the old LSP-era launch modes for compatibility.
type LaunchMode int

const (
	// LaunchCLI spawns the Copilot CLI binary directly (ACP).
	LaunchCLI LaunchMode = iota
	// LaunchBundle is kept for compatibility but ACP mode always uses
	// the CLI; bundle-based launches are no longer supported.
	LaunchBundle
)

// LaunchConfig holds the launch configuration for the Copilot CLI.
type LaunchConfig struct {
	Mode   LaunchMode
	Bin    string
	Bundle string
}

// Manager owns the Copilot ACP subprocess lifecycle. Sessions are lazy:
// the subprocess spawns on first use and stays alive across prompts.
type Manager struct {
	bin string

	mu      sync.Mutex
	workdir string
	running bool
	agent   *acp.Agent
	known   map[string]bool
}

// NewManager builds the manager. cfg.Bin is the copilot CLI binary; the
// ACP mode ignores LaunchBundle.
func NewManager(cfg LaunchConfig) *Manager {
	bin := cfg.Bin
	if bin == "" {
		bin = "copilot"
	}
	return &Manager{bin: bin, known: map[string]bool{}}
}

// MarkStarted flags the manager as owning a Copilot subprocess.
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

// Started reports whether the manager considers Copilot running.
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

// ensureAgent lazily starts the Copilot ACP subprocess.
func (m *Manager) ensureAgent(ctx context.Context) (*acp.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agent != nil && m.agent.Running() {
		return m.agent, nil
	}
	workdir := m.workdir
	if workdir == "" {
		return nil, errors.New("copilot manager has no working directory")
	}
	agent := acp.NewAgent(acp.AgentOptions{
		Bin:      m.bin,
		Args:     []string{"--acp", "--allow-all"},
		Framing:  acp.FramingNewline,
		TrustAll: true,
	})
	if err := agent.Start(ctx); err != nil {
		return nil, fmt.Errorf("start copilot acp: %w", err)
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
			return "", fmt.Errorf("resume copilot session: %w", err)
		}
		m.mu.Lock()
		m.known[sessionID] = true
		m.mu.Unlock()
	}
	return agent.Prompt(ctx, sessionID, text)
}

// CreateSession starts a fresh Copilot session via ACP.
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

// listSessions returns the Copilot sessions for the working directory
// via ACP session/list.
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
		out = append(out, domain.Session{
			ID:        info.ID,
			Directory: info.Cwd,
			Title:     firstNonEmpty(info.Summary, info.Title, "Copilot "+truncateID(info.ID)),
		})
	}
	return out, nil
}

func truncateID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
