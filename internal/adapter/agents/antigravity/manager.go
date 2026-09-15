// Package antigravity implements the AgentAdapter for Google's
// Antigravity CLI (`agy`). The transport is the headless print mode:
// `agy -p <prompt> --output-format stream-json [--conversation <id>]`,
// which streams NDJSON `init`/`step_update`/`result` events to stdout.
//
// Readable conversation history lives under
// ~/.gemini/antigravity-cli/brain/<conversation_id>/.system_generated/logs/transcript_full.jsonl
// (the CLI root) and ~/.gemini/antigravity/brain/... (the IDE root);
// the adapter and locator read both so a task finished in either surface
// can be detected.
package antigravity

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

// DefaultExecArgs is the flag set Agentower passes to `agy`. Print mode
// with the streaming JSON output, auto-approval of tool permissions
// (there is no TTY to answer prompts) and a generous per-run timeout.
var DefaultExecArgs = []string{
	"--output-format", "stream-json",
	"--dangerously-skip-permissions",
	"--print-timeout", "30m",
}

// Manager owns the Antigravity subprocess lifecycle. Each prompt spawns
// a short-lived `agy -p` process; the manager keeps the synthetic-id ->
// conversation-id mapping and serializes concurrent prompts.
type Manager struct {
	bin      string
	port     int
	execArgs []string

	mu        sync.Mutex
	workdir   string
	running   bool
	convos    map[string]string // synthetic session id -> conversation id
	aliases   map[string]string // conversation id -> synthetic session id
	sessionMu map[string]*sync.Mutex
}

// NewManager builds the manager. bin is the `agy` binary the detector
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
		convos:    map[string]string{},
		aliases:   map[string]string{},
		sessionMu: map[string]*sync.Mutex{},
	}
}

// MarkStarted flags the manager as owning an Antigravity session.
func (m *Manager) MarkStarted(workdir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workdir = workdir
	m.running = true
}

// MarkStopped clears the running flag.
func (m *Manager) MarkStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
}

// Started reports whether the manager considers Antigravity running.
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
	if _, ok := m.convos[id]; ok {
		return true
	}
	_, ok := m.aliases[id]
	return ok
}

// NewSessionID allocates a synthetic client-side session id. The real
// conversation id is learned from the first run's `init` event and kept
// under this handle so the bot's persisted id stays stable.
func (m *Manager) NewSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "antigravity-" + hex.EncodeToString(buf)
}

// PublicSessionID maps a real conversation id back to the synthetic id
// the bot stored, when known.
func (m *Manager) PublicSessionID(conversationID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if alias, ok := m.aliases[conversationID]; ok {
		return alias
	}
	return conversationID
}

// SendPrompt runs one Antigravity turn and returns the assistant text.
func (m *Manager) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	lock := m.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	workdir := m.WorkingDir()
	if workdir == "" {
		return "", errors.New("antigravity manager has no working directory")
	}

	m.mu.Lock()
	conversationID := m.convos[sessionID]
	resume := conversationID
	if resume == "" && sessionID != "" && !isSynthetic(sessionID) {
		resume = sessionID
	}
	m.mu.Unlock()

	reply, newConvo, err := m.run(ctx, workdir, resume, text)
	if err != nil {
		return "", err
	}
	if newConvo != "" && conversationID == "" {
		m.mu.Lock()
		m.convos[sessionID] = newConvo
		m.aliases[newConvo] = sessionID
		m.mu.Unlock()
	}
	return reply, nil
}

// run spawns `agy -p` and parses the stream-json events.
func (m *Manager) run(ctx context.Context, workdir, resume, prompt string) (string, string, error) {
	argv := buildArgv(m.bin, m.execArgs, resume, prompt)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Stderr = io.Discard

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("antigravity: open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("antigravity: spawn %s: %w", m.bin, err)
	}

	var (
		text  strings.Builder
		final string
		convo string
	)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev agyEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Older `agy` builds may emit plain text on stdout; keep it
			// as assistant text instead of dropping it.
			text.WriteString(scanner.Text())
			continue
		}
		switch ev.Event {
		case "init":
			if ev.Init.ConversationID != "" {
				convo = ev.Init.ConversationID
			}
			if ev.ConversationID != "" {
				convo = ev.ConversationID
			}
		case "step_update":
			if ev.StepUpdate.ConversationID != "" {
				convo = ev.StepUpdate.ConversationID
			}
			if ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.TextDelta != "" {
				text.WriteString(ev.StepUpdate.TextDelta)
			}
		case "result":
			if ev.Result.ConversationID != "" {
				convo = ev.Result.ConversationID
			}
			if ev.Result.Status != "" && !strings.EqualFold(ev.Result.Status, "SUCCESS") {
				if ev.Result.Error != "" {
					return "", "", fmt.Errorf("antigravity: %s", ev.Result.Error)
				}
				return "", "", fmt.Errorf("antigravity: run finished with status %s", ev.Result.Status)
			}
			if ev.Result.Response != "" {
				final = ev.Result.Response
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		if final == "" && strings.TrimSpace(text.String()) == "" {
			return "", "", fmt.Errorf("agy: %w", err)
		}
	}
	if strings.TrimSpace(final) != "" {
		return strings.TrimSpace(final), convo, nil
	}
	return strings.TrimSpace(text.String()), convo, nil
}

// agyEvent is the projection of one stream-json line. The envelope keys
// the payload by its own event name, and also repeats conversation_id at
// the top level, so both are read.
type agyEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Init           struct {
		ConversationID string `json:"conversation_id"`
	} `json:"init"`
	StepUpdate struct {
		ConversationID string `json:"conversation_id"`
		StepType       string `json:"step_type"`
		State          string `json:"state"`
		TextDelta      string `json:"text_delta"`
	} `json:"step_update"`
	Result struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
	} `json:"result"`
}

// buildArgv assembles the argv for a new or resumed run. The prompt is
// passed with -p (print mode); --conversation continues an existing
// thread.
func buildArgv(bin string, execArgs []string, resume, prompt string) []string {
	argv := []string{bin, "-p", prompt}
	if resume != "" {
		argv = append(argv, "--conversation", resume)
	}
	argv = append(argv, execArgs...)
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

func isSynthetic(id string) bool {
	return strings.HasPrefix(id, "antigravity-")
}

// Port is exposed for parity with the other managers.
func (m *Manager) Port() int { return m.port }
