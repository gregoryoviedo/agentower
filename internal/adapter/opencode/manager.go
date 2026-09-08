package opencode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	probeInitialWait  = 100 * time.Millisecond
	probeMaxWait      = 500 * time.Millisecond
	probeTimeout      = 10 * time.Second
	adoptProbeTimeout = 500 * time.Millisecond
	startupGrace      = 250 * time.Millisecond
	shutdownGrace     = 5 * time.Second
)

type Manager struct {
	bin      string
	port     int
	probeURL string
	logger   *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	cwd      string
	external bool
}

type ManagerOptions struct {
	Bin    string
	Port   int
	Logger *slog.Logger
}

func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		bin:      opts.Bin,
		port:     opts.Port,
		probeURL: fmt.Sprintf("http://127.0.0.1:%d/global/health", opts.Port),
		logger:   opts.Logger,
	}
}

func (m *Manager) Port() int { return m.port }

func (m *Manager) Binary() string { return m.bin }

// StartedSubprocess reports whether the manager considers a server to be
// running on the configured port — either because it owns the subprocess or
// because it adopted an already-running one started outside the bot.
func (m *Manager) StartedSubprocess() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil || m.external
}

// OwnsSubprocess reports whether the manager started the running server
// itself. False when the manager merely adopted an externally-started one.
func (m *Manager) OwnsSubprocess() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// WorkingDir reports the working directory the manager was started against.
// For adopted servers this is a hint, not necessarily the server's real cwd.
func (m *Manager) WorkingDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cwd
}

// Start prefers an already-running `opencode serve` on the configured port
// over spawning a new one. If /global/health responds 200, the manager
// adopts the external server (it does not own the subprocess) and the
// caller can use it as if it had been started here. Otherwise Start kills
// any subprocess it owns and spawns a fresh one bound to workingDir. It
// blocks until the server is healthy or an error is returned.
func (m *Manager) Start(ctx context.Context, workingDir string) error {
	if workingDir == "" {
		return fmt.Errorf("working directory must not be empty")
	}
	if info, err := os.Stat(workingDir); err != nil {
		return fmt.Errorf("stat working directory %q: %w", workingDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", workingDir)
	}

	m.mu.Lock()
	// If we already adopted an external server, leave it alone and just
	// refresh the working-dir hint so subsequent WorkingDir() calls reflect
	// what the caller asked for.
	if m.external {
		m.cwd = workingDir
		m.mu.Unlock()
		return nil
	}
	m.terminateLocked()
	m.cmd = nil
	m.cwd = workingDir
	m.mu.Unlock()

	// Probe for an external server before spawning to avoid binding the port
	// twice when the user already has `opencode serve` running on the laptop.
	adoptCtx, adoptCancel := context.WithTimeout(ctx, adoptProbeTimeout)
	if err := m.probe(adoptCtx, adoptProbeTimeout); err == nil {
		adoptCancel()
		m.mu.Lock()
		m.external = true
		m.cwd = workingDir
		m.mu.Unlock()
		m.logger.Info("opencode server already running; adopting it", "port", m.port, "cwd", workingDir)
		return nil
	}
	adoptCancel()

	bin, err := exec.LookPath(m.bin)
	if err != nil {
		return fmt.Errorf("locate %q in PATH: %w", m.bin, err)
	}

	cmd := exec.CommandContext(ctx, bin, "serve", "--port", fmt.Sprint(m.port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = workingDir
	cmd.Stdin = nil
	cmd.Stdout = &subprocessLogWriter{logger: m.logger, source: "opencode-serve:stdout"}
	cmd.Stderr = &subprocessLogWriter{logger: m.logger, source: "opencode-serve:stderr"}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", bin, err)
	}
	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()
	m.logger.Info("opencode server spawned", "pid", cmd.Process.Pid, "port", m.port, "cwd", workingDir)

	select {
	case <-time.After(startupGrace):
	case <-ctx.Done():
		m.terminate(cmd)
		m.mu.Lock()
		m.cmd = nil
		m.mu.Unlock()
		return ctx.Err()
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := m.waitHealthy(probeCtx); err != nil {
		m.terminate(cmd)
		m.mu.Lock()
		m.cmd = nil
		m.mu.Unlock()
		return fmt.Errorf("opencode server did not become healthy within %s: %w", probeTimeout, err)
	}
	return nil
}

// Stop terminates the subprocess the manager owns. Adopted external servers
// are never killed — the user explicitly started them and may still be using
// them outside of this bot.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil {
		m.terminateLocked()
	}
	m.cmd = nil
	m.external = false
	m.cwd = ""
}

func (m *Manager) terminateLocked() {
	if m.cmd == nil {
		return
	}
	m.terminate(m.cmd)
}

func (m *Manager) terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		_ = cmd.Process.Kill()
		<-done
	}
}

func (m *Manager) waitHealthy(ctx context.Context) error {
	wait := probeInitialWait
	for {
		if err := m.probe(ctx, wait); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if wait < probeMaxWait {
			wait *= 2
		}
	}
}

func (m *Manager) probe(parent context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.probeURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, response.Body); response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health: %s", response.Status)
	}
	return nil
}

type subprocessLogWriter struct {
	logger *slog.Logger
	source string
	buf    bytes.Buffer
}

func (w *subprocessLogWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			w.flush()
		} else {
			w.buf.WriteByte(b)
		}
	}
	return len(p), nil
}

func (w *subprocessLogWriter) flush() {
	if w.buf.Len() == 0 {
		return
	}
	w.logger.Info(w.buf.String(), "source", w.source)
	w.buf.Reset()
}
