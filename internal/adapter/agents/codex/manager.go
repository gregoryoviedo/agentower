// Package codex implements the AgentAdapter for OpenAI's Codex CLI.
// The transport is the headless exec mode with newline-delimited JSON
// output: `codex exec --json -` for a new thread and
// `codex exec resume --json <id> -` to continue one. The prompt is
// written to stdin so it never has to be shell-escaped.
//
// Session history is read from the rollout JSONL files Codex writes
// under ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl so the watcher and
// /sessions keep working without a live subprocess.
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
)

// DefaultExecArgs is the flag set Agentower passes to `codex exec`.
// --json enables the JSONL event stream, --skip-git-repo-check lets the
// agent run outside a git repo (the workspace root is not always one),
// and --dangerously-bypass-approvals-and-sandbox auto-approves tool use
// because there is no TTY to answer the prompts. Users can override the
// whole set via AGENT_CODEX_ARGS.
var DefaultExecArgs = []string{
	"--json",
	"--skip-git-repo-check",
	"--dangerously-bypass-approvals-and-sandbox",
}

// Manager owns the Codex subprocess lifecycle. One process is spawned
// per prompt (Codex has no long-lived stdio server in exec mode), so
// the manager only tracks the synthetic-session -> thread-id mapping
// and serializes concurrent prompts to the same session.
type Manager struct {
	bin      string
	port     int
	execArgs []string

	mu        sync.Mutex
	workdir   string
	running   bool
	threads   map[string]string // synthetic session id -> real thread id
	aliases   map[string]string // real thread id -> synthetic session id
	sessionMu map[string]*sync.Mutex
}

// NewManager builds the manager. bin is the `codex` binary the detector
// found; execArgs overrides DefaultExecArgs when non-empty.
func NewManager(bin string, port int, execArgs ...string) *Manager {
	args := DefaultExecArgs
	if len(execArgs) > 0 {
		args = execArgs
	}
	return &Manager{
		bin:       bin,
		port:      port,
		execArgs:  append([]string(nil), args...),
		threads:   map[string]string{},
		aliases:   map[string]string{},
		sessionMu: map[string]*sync.Mutex{},
	}
}

// MarkStarted flags the manager as owning a Codex session in workdir.
func (m *Manager) MarkStarted(workdir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workdir = workdir
	m.running = true
}

// MarkStopped clears the running flag. In-flight subprocesses finish on
// their own; there is no persistent process to tear down.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
}

// Started reports whether the manager considers Codex running.
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

// HasSession reports whether the id was created or resumed here.
func (m *Manager) HasSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.threads[id]; ok {
		return true
	}
	_, ok := m.aliases[id]
	return ok
}

// NewSessionID allocates a synthetic client-side session id. Codex
// assigns the real thread id on the first prompt; the manager keeps the
// synthetic id as the stable handle the bot persists.
func (m *Manager) NewSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "codex-" + hex.EncodeToString(buf)
}

// PublicSessionID maps a real Codex thread id back to the synthetic id
// the bot stored, when known. Returns the argument unchanged otherwise.
func (m *Manager) PublicSessionID(threadID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if alias, ok := m.aliases[threadID]; ok {
		return alias
	}
	return threadID
}

// SendPrompt runs one Codex turn for the session and returns the
// assistant's final message.
func (m *Manager) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	lock := m.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	workdir := m.WorkingDir()
	if workdir == "" {
		return "", errors.New("codex manager has no working directory")
	}

	m.mu.Lock()
	threadID := m.threads[sessionID]
	resume := threadID
	if resume == "" && sessionID != "" && !isSynthetic(sessionID) {
		// A real thread id handed over by /resume: continue it.
		resume = sessionID
	}
	m.mu.Unlock()

	reply, newThreadID, err := m.run(ctx, workdir, resume, text)
	if err != nil {
		return "", err
	}
	if newThreadID != "" && threadID == "" {
		m.mu.Lock()
		m.threads[sessionID] = newThreadID
		m.aliases[newThreadID] = sessionID
		m.mu.Unlock()
	}
	return reply, nil
}

// run spawns `codex exec` (or `codex exec resume`) and parses the JSONL
// event stream, returning the assistant text and the thread id.
func (m *Manager) run(ctx context.Context, workdir, resume, prompt string) (string, string, error) {
	argv := buildArgv(m.bin, m.execArgs, resume)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", "", fmt.Errorf("codex: open stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("codex: open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("codex: spawn %s: %w", m.bin, err)
	}
	go func() {
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
	}()

	var (
		reply   strings.Builder
		thread  string
		turnErr error
	)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				thread = ev.ThreadID
			}
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				if reply.Len() > 0 {
					reply.WriteString("\n\n")
				}
				reply.WriteString(ev.Item.Text)
			}
		case "turn.failed":
			turnErr = errors.New(firstNonEmpty(ev.Error.Message, "codex turn failed"))
		}
	}
	waitErr := cmd.Wait()
	if turnErr != nil {
		return "", "", turnErr
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("codex: read stdout: %w", err)
	}
	if waitErr != nil {
		return "", "", fmt.Errorf("codex exec: %w", waitErr)
	}
	return strings.TrimSpace(reply.String()), thread, nil
}

// codexEvent is the projection of one `codex exec --json` line.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// buildArgv assembles the argv for a new (resume=="") or resumed run.
// Options go after the subcommand so both the exec and resume flag sets
// are honoured; "-" makes Codex read the prompt from stdin.
func buildArgv(bin string, execArgs []string, resume string) []string {
	argv := []string{bin, "exec"}
	if resume != "" {
		argv = append(argv, "resume")
	}
	argv = append(argv, execArgs...)
	if resume != "" {
		argv = append(argv, resume)
	}
	argv = append(argv, "-")
	return argv
}

func (m *Manager) sessionLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.sessionMu[id]
	if !ok {
		lock = &sync.Mutex{}
		m.sessionMu[id] = lock
	}
	return lock
}

// isSynthetic reports whether id was minted by NewSessionID rather than
// by Codex. Synthetic ids carry a stable prefix so a resumed real id can
// be told apart after a restart.
func isSynthetic(id string) bool {
	return strings.HasPrefix(id, "codex-")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Port is exposed for parity with the other managers (Codex has no HTTP
// server; the value is only used by the detector descriptor).
func (m *Manager) Port() int { return m.port }
