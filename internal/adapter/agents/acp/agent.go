package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// AgentOptions configures an ACP agent.
type AgentOptions struct {
	// Bin is the executable that speaks ACP (e.g. kiro-cli, copilot).
	Bin  string
	Args []string
	// Framing selects the wire framing (Copilot: ContentLength; Kiro:
	// Newline).
	Framing Framing
	// Stderr receives the agent's stderr; nil discards it.
	Stderr io.Writer
	// TrustAll auto-approves session/request_permission requests. When
	// false every permission request is cancelled (denied).
	TrustAll bool
}

// Agent is a high-level ACP client for one agent subprocess. It owns
// the process lifecycle and exposes the session operations AgenTower
// needs to drive Copilot and Kiro from Telegram.
type Agent struct {
	opts    AgentOptions
	cli     *Client
	started bool
	caps    *Capabilities

	mu      sync.Mutex
	text    map[string]*strings.Builder
	loading map[string]bool
}

// Capabilities is the initialize result surface AgenTower inspects.
type Capabilities struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			// Resume is non-nil when the agent advertises the
			// session/resume capability (restore without replaying).
			Resume *struct{} `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

// SessionInfo is one entry returned by session/list.
type SessionInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	Summary string `json:"summary"`
}

// NewAgent builds an agent without starting it.
func NewAgent(opts AgentOptions) *Agent {
	return &Agent{
		opts:    opts,
		text:    map[string]*strings.Builder{},
		loading: map[string]bool{},
	}
}

// Start spawns the subprocess and completes the ACP handshake.
func (a *Agent) Start(ctx context.Context) error {
	if a.started {
		return nil
	}
	cli := NewClient(ClientOptions{
		Bin:     a.opts.Bin,
		Args:    a.opts.Args,
		Framing: a.opts.Framing,
		Stderr:  a.opts.Stderr,
	})
	// Install the dispatcher before the read loop starts so no
	// notification can be missed. Tokens/requests are handled inline by
	// the read loop, which keeps notification processing ordered with
	// respect to request responses (needed by session/load replay).
	cli.SetNotifyHandler(func(msg *message) {
		if len(msg.ID) > 0 && string(msg.ID) != "null" && msg.Method != "" {
			a.handleRequest(msg)
			return
		}
		a.onNotify(msg)
	})
	if err := cli.Start(); err != nil {
		return err
	}
	a.cli = cli
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"writeTextFile": true, "readTextFile": true},
			"terminal": false,
			"session":  map[string]any{},
		},
		"clientInfo": map[string]any{"name": "Agentower", "version": "0.1"},
	}
	var caps Capabilities
	if err := cli.Request(ctx, "initialize", initParams, &caps); err != nil {
		cli.Close()
		return fmt.Errorf("acp initialize: %w", err)
	}
	a.caps = &caps
	a.started = true
	return nil
}

// Close terminates the subprocess.
func (a *Agent) Close() {
	if a.cli != nil {
		a.cli.Close()
	}
	a.started = false
}

// Running reports whether the subprocess is started.
func (a *Agent) Running() bool { return a.started }

// Capabilities returns the negotiated capabilities.
func (a *Agent) Capabilities() *Capabilities { return a.caps }

// NewSession starts a fresh session in cwd and returns its id.
func (a *Agent) NewSession(ctx context.Context, cwd string) (string, error) {
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := a.cli.Request(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &resp); err != nil {
		return "", err
	}
	if resp.SessionID == "" {
		return "", errors.New("acp: session/new returned no session id")
	}
	return resp.SessionID, nil
}

// ResumeSession reactivates an existing session (the "continue from
// Telegram" path). ACP split this into two methods:
//
//   - session/resume (capability agentCapabilities.sessionCapabilities.resume):
//     restores the session context without replaying history.
//   - session/load (capability agentCapabilities.loadSession): restores
//     the context and replays the full conversation as session/update
//     notifications before responding.
//
// Agent versions differ in which one they implement (Kiro CLI 2.x only
// answers session/load), so we prefer the advertised method and fall
// back to the other only on JSON-RPC "method not found" (-32601).
func (a *Agent) ResumeSession(ctx context.Context, sessionID, cwd string) error {
	params := map[string]any{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []any{},
	}
	methods := []string{"session/load", "session/resume"}
	if a.caps != nil && a.caps.AgentCapabilities.SessionCapabilities.Resume != nil {
		methods = []string{"session/resume", "session/load"}
	}
	// session/load replays the conversation as notifications. Drop them
	// while it runs: Prompt resets the per-session buffer anyway, and
	// skipping keeps the replay out of memory.
	a.mu.Lock()
	a.loading[sessionID] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.loading, sessionID)
		a.mu.Unlock()
	}()

	var firstErr error
	for _, method := range methods {
		err := a.cli.Request(ctx, method, params, nil)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !methodNotFound(err) {
			return err
		}
	}
	return firstErr
}

// methodNotFound reports whether err is the JSON-RPC -32601 error, used
// to fall back across ACP method-name revisions.
func methodNotFound(err error) bool {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		return rpc.Code == -32601
	}
	return false
}

// Prompt sends a user message to the session and returns the agent's
// full text reply (aggregated from session/update notifications).
func (a *Agent) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	a.resetText(sessionID)
	params := map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": text}},
	}
	var resp struct {
		StopReason string `json:"stopReason"`
	}
	if err := a.cli.Request(ctx, "session/prompt", params, &resp); err != nil {
		return a.takeText(sessionID), err
	}
	// The turn's notifications are queued immediately before the
	// response; give the dispatcher a moment to flush them before
	// reading the accumulated text.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if t := strings.TrimSpace(a.snapshotText(sessionID)); t != "" {
			a.takeText(sessionID)
			return t, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return strings.TrimSpace(a.takeText(sessionID)), nil
}

// ListSessions lists the sessions known for a working directory.
func (a *Agent) ListSessions(ctx context.Context, cwd string) ([]SessionInfo, error) {
	var resp struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := a.cli.Request(ctx, "session/list", map[string]any{"cwd": cwd}, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

// CloseSession ends a session on the agent side.
func (a *Agent) CloseSession(ctx context.Context, sessionID string) error {
	return a.cli.Request(ctx, "session/close", map[string]any{"sessionId": sessionID}, nil)
}

// handleRequest answers agent requests. The default policy:
//   - session/request_permission is approved when TrustAll is set,
//     cancelled otherwise;
//   - _kiro/auth/getAccessToken is answered with null so Kiro falls
//     back to its CLI credential store;
//   - fs/terminal/elicitation and protocol-level requests are declined
//     (the agent runs locally with its own tools).
func (a *Agent) handleRequest(msg *message) {
	method := msg.Method
	switch {
	case method == "session/request_permission":
		a.respondPermission(msg)
	case method == "_kiro/auth/getAccessToken":
		_ = a.cli.Respond(msg.ID, nil, nil)
	case strings.HasPrefix(method, "fs/"),
		strings.HasPrefix(method, "terminal/"),
		strings.HasPrefix(method, "elicitation/"),
		strings.HasPrefix(method, "$/"):
		_ = a.cli.Respond(msg.ID, nil, errors.New("not supported by Agentower"))
	default:
		_ = a.cli.Respond(msg.ID, nil, fmt.Errorf("unknown agent request %s", method))
	}
}

func (a *Agent) respondPermission(msg *message) {
	if !a.opts.TrustAll {
		_ = a.cli.Respond(msg.ID, map[string]any{"outcome": "cancelled"}, nil)
		return
	}
	var params struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(msg.Params, &params)
	optionID := ""
	for _, o := range params.Options {
		if o.Kind == "allow_always" {
			optionID = o.OptionID
			break
		}
	}
	if optionID == "" && len(params.Options) > 0 {
		optionID = params.Options[0].OptionID
	}
	_ = a.cli.Respond(msg.ID, map[string]any{"outcome": "selected", "optionId": optionID}, nil)
}

// onNotify aggregates assistant text from session/update notifications
// into the per-session buffer used by Prompt.
func (a *Agent) onNotify(msg *message) {
	if msg.Method != "session/update" {
		return
	}
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return
	}
	if params.SessionID == "" || len(params.Update.Content) == 0 {
		return
	}
	if params.Update.SessionUpdate != "" && params.Update.SessionUpdate != "agent_message_chunk" {
		return
	}
	if a.isLoading(params.SessionID) {
		// session/load history replay; Prompt resets the buffer before
		// the real turn, so there is nothing to aggregate.
		return
	}
	for _, t := range collectText(params.Update.Content) {
		a.appendText(params.SessionID, t)
	}
}

func (a *Agent) isLoading(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loading[sessionID]
}

func (a *Agent) resetText(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.text[sessionID] = &strings.Builder{}
}

func (a *Agent) appendText(sessionID, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.text[sessionID]
	if !ok {
		b = &strings.Builder{}
		a.text[sessionID] = b
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(text)
}

func (a *Agent) takeText(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.text[sessionID]
	if !ok {
		return ""
	}
	out := b.String()
	delete(a.text, sessionID)
	return out
}

// snapshotText returns the accumulated text without clearing it.
func (a *Agent) snapshotText(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.text[sessionID]; ok {
		return b.String()
	}
	return ""
}

// collectText walks a JSON value and returns every "text" string found
// on an object whose "type" is "text". It is used to pull assistant
// chunks out of the nested ContentChunk/ContentBlock structure without
// depending on a specific schema path.
func collectText(raw json.RawMessage) []string {
	var out []string
	walkText(raw, &out)
	return out
}

func walkText(raw json.RawMessage, out *[]string) {
	if len(raw) == 0 {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		var typ string
		_ = json.Unmarshal(obj["type"], &typ)
		var text string
		if typ == "text" && json.Unmarshal(obj["text"], &text) == nil && text != "" {
			*out = append(*out, text)
			return
		}
		for _, v := range obj {
			walkText(v, out)
		}
		return
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, v := range arr {
			walkText(v, out)
		}
	}
}
