package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// LaunchMode tells the manager how to spawn the language server.
// "cli" means the binary speaks LSP directly (the modern `copilot`
// CLI). "bundle" means we found a VS Code extension bundle and must
// run it through `node`.
type LaunchMode int

const (
	LaunchCLI LaunchMode = iota
	LaunchBundle
)

// LaunchConfig holds everything the manager needs to bring the
// language server up.
type LaunchConfig struct {
	Mode   LaunchMode
	Bin    string // for LaunchCLI: the copilot binary. for LaunchBundle: the node binary.
	Bundle string // for LaunchBundle: the path to dist/extension.js.
}

// Manager owns the GitHub Copilot LSP subprocess. It spawns the
// language server on demand and reuses the connection across prompts
// so the chat history is preserved for as long as the user keeps
// driving the same chat.
type Manager struct {
	cfg LaunchConfig

	workdirMu sync.RWMutex
	workdir   string

	mu      sync.Mutex
	running bool
	conn    *conn
}

type conn struct {
	cmd *exec.Cmd
	cl  *client
}

func NewManager(cfg LaunchConfig) *Manager {
	return &Manager{cfg: cfg}
}

// MarkStarted flags the manager as owning a Copilot subprocess.
func (m *Manager) MarkStarted(workdir string) {
	m.workdirMu.Lock()
	m.workdir = workdir
	m.workdirMu.Unlock()
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
}

// MarkStopped clears the running flag and tears down the LSP
// subprocess.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	conn := m.conn
	m.conn = nil
	m.running = false
	m.mu.Unlock()
	if conn != nil {
		_ = conn.cmd.Process.Kill()
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
	m.workdirMu.RLock()
	defer m.workdirMu.RUnlock()
	return m.workdir
}

// Client returns the LSP client. Lazy-starts the subprocess on first
// call; subsequent calls reuse the connection.
func (m *Manager) Client(ctx context.Context) (*client, error) {
	m.mu.Lock()
	if m.conn != nil {
		c := m.conn
		m.mu.Unlock()
		return c.cl, nil
	}
	m.mu.Unlock()

	c, err := m.dial(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.conn = c
	m.running = true
	m.mu.Unlock()
	return c.cl, nil
}

// dial spawns the LSP subprocess and runs the initialize handshake.
func (m *Manager) dial(ctx context.Context) (*conn, error) {
	workdir := m.WorkingDir()
	if workdir == "" {
		return nil, errors.New("copilot manager has no working directory")
	}
	var argv []string
	switch m.cfg.Mode {
	case LaunchCLI:
		argv = []string{m.cfg.Bin, "--stdio"}
	case LaunchBundle:
		argv = []string{m.cfg.Bin, m.cfg.Bundle, "--stdio"}
	default:
		return nil, errors.New("copilot manager: unknown launch mode")
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
		return nil, fmt.Errorf("spawn copilot: %w", err)
	}
	cl := newClient(stdout, stdin)
	conn := &conn{cmd: cmd, cl: cl}
	go cl.run()
	if err := cl.initialize(ctx); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("lsp initialize: %w", err)
	}
	return conn, nil
}
