// Package acp implements a minimal Agent Client Protocol (ACP) client
// over stdio. ACP is the JSON-RPC 2.0 protocol Zed standardised for
// driving coding agents; both the GitHub Copilot CLI (--acp) and the
// Kiro CLI (kiro-cli acp) expose an ACP server.
//
// The package owns the subprocess lifecycle and the wire framing:
// Copilot uses LSP-style Content-Length framing, Kiro uses
// newline-delimited JSON. Both are supported.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Framing selects the wire framing used by the agent's ACP server.
type Framing int

const (
	// FramingNewline is one JSON-RPC message per line (Kiro CLI).
	FramingNewline Framing = iota
	// FramingContentLength is LSP-style "Content-Length: N\r\n\r\n"
	// framing (Copilot CLI).
	FramingContentLength
)

// rpcError mirrors the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("jsonrpc error %d", e.Code)
	}
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// message is the JSON-RPC 2.0 envelope used on the wire.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Client is a JSON-RPC 2.0 client over a framed stdio transport. It
// runs a background reader that routes responses to pending requests,
// dispatches agent requests to RequestHandler, and forwards
// notifications to NotifyHandler.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	r       *bufio.Reader
	framing Framing
	stderr  io.Writer

	mu       sync.Mutex
	nextID   int64
	pending  map[string]chan *message
	closed   bool
	readErr  error
	notify   chan *message
	notifyFn func(*message)
	done     chan struct{}
}

// ClientOptions configures a Client.
type ClientOptions struct {
	Bin     string
	Args    []string
	Framing Framing
	Stderr  io.Writer
	// Notify is called for every notification the agent sends. It must
	// be safe for concurrent use.
	Notify func(method string, params json.RawMessage)
}

// NewClient builds a client without starting it. Call Start to spawn
// the subprocess.
func NewClient(opts ClientOptions) *Client {
	c := &Client{
		framing: opts.Framing,
		stderr:  opts.Stderr,
		pending: map[string]chan *message{},
		notify:  make(chan *message, 256),
		done:    make(chan struct{}),
	}
	if c.stderr == nil {
		c.stderr = io.Discard
	}
	c.cmd = exec.Command(opts.Bin, opts.Args...)
	c.cmd.Stderr = c.stderr
	return c
}

// Start spawns the subprocess and begins the read loop.
func (c *Client) Start() error {
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp: open stdin: %w", err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp: open stdout: %w", err)
	}
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("acp: spawn %s: %w", c.cmd.Path, err)
	}
	c.stdin = stdin
	c.r = bufio.NewReader(stdout)
	go c.readLoop()
	return nil
}

// Close terminates the subprocess.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.notify)
	c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}

// readLoop parses framed messages and dispatches them. It exits when
// the stream closes or the process dies.
func (c *Client) readLoop() {
	defer close(c.done)
	for {
		msg, err := c.readMessage()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
		if len(msg.ID) > 0 && string(msg.ID) != "null" {
			if msg.Method != "" {
				// Agent request (e.g. session/request_permission).
				c.dispatch(msg)
				continue
			}
			// Response to one of our requests.
			key := string(msg.ID)
			c.mu.Lock()
			ch, ok := c.pending[key]
			if ok {
				delete(c.pending, key)
			}
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}
		// Notification (no id).
		c.dispatch(msg)
	}
}

// dispatch delivers a notification or agent request. When a synchronous
// handler is installed with SetNotifyHandler it runs inline, before the
// read loop parses the next message. That guarantees the handler has
// fully processed every notification the agent emitted before a request
// response — essential for session/load, whose history replay arrives as
// notifications immediately before the response. Otherwise the message
// goes to the NotifyHandler channel for asynchronous consumption.
func (c *Client) dispatch(msg *message) {
	c.mu.Lock()
	fn := c.notifyFn
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	if fn != nil {
		fn(msg)
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.notify <- msg
	}
	c.mu.Unlock()
}

// Request sends a request and waits for the matching response,
// decoding it into resultOut (which may be nil).
func (c *Client) Request(ctx context.Context, method string, params any, resultOut any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("acp: encode %s params: %w", method, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("acp: client closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[strconv.FormatInt(id, 10)] = ch
	c.mu.Unlock()

	msg := message{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: body}
	if err := c.writeMessage(&msg); err != nil {
		c.mu.Lock()
		delete(c.pending, strconv.FormatInt(id, 10))
		c.mu.Unlock()
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if resultOut != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, resultOut); err != nil {
				return fmt.Errorf("acp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, strconv.FormatInt(id, 10))
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Respond answers an agent request with a result. Pass an error to
// reply with a JSON-RPC error instead.
func (c *Client) Respond(id json.RawMessage, result any, err error) error {
	msg := message{JSONRPC: "2.0", ID: id}
	if err != nil {
		msg.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		b, merr := json.Marshal(result)
		if merr != nil {
			return fmt.Errorf("acp: encode response: %w", merr)
		}
		msg.Result = b
	}
	return c.writeMessage(&msg)
}

// NotifyHandler returns the channel the read loop pushes notifications
// and agent requests to. It is closed when the client closes.
func (c *Client) NotifyHandler() <-chan *message { return c.notify }

// SetNotifyHandler installs a synchronous handler for notifications and
// agent requests. When set, the read loop invokes it inline instead of
// pushing to the NotifyHandler channel, so every notification is fully
// processed before the next message (including request responses) is
// handled. The handler MUST NOT call Request: that would deadlock the
// read loop waiting for a response it cannot read.
func (c *Client) SetNotifyHandler(fn func(*message)) {
	c.mu.Lock()
	c.notifyFn = fn
	c.mu.Unlock()
}

// Done is closed when the read loop exits.
func (c *Client) Done() <-chan struct{} { return c.done }

// RequestHandler is the signature for handling agent requests. It is
// used by Agent to answer session/request_permission, auth, fs and
// terminal requests.
func (c *Client) RequestHandler(handler func(msg *message)) {
	go func() {
		for msg := range c.notify {
			if len(msg.ID) > 0 && string(msg.ID) != "null" && msg.Method != "" {
				handler(msg)
			}
		}
	}()
}

// readMessage reads one JSON-RPC message using the configured framing.
func (c *Client) readMessage() (*message, error) {
	switch c.framing {
	case FramingContentLength:
		return c.readContentLength()
	default:
		return c.readNewline()
	}
}

func (c *Client) readNewline() (*message, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return c.readNewline()
	}
	var msg message
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, fmt.Errorf("acp: parse json line: %w", err)
	}
	return &msg, nil
}

func (c *Client) readContentLength() (*message, error) {
	var length int
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			raw := strings.TrimSpace(line[len("content-length:"):])
			n, perr := strconv.Atoi(raw)
			if perr != nil {
				return nil, fmt.Errorf("acp: bad content-length %q: %w", raw, perr)
			}
			length = n
		}
	}
	if length <= 0 {
		return nil, errors.New("acp: missing content-length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, err
	}
	// Some servers emit a trailing newline after the payload; consume it.
	if b, err := c.r.Peek(1); err == nil && (b[0] == '\n' || b[0] == '\r') {
		_, _ = c.r.ReadByte()
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("acp: parse json body: %w", err)
	}
	return &msg, nil
}

// writeMessage frames and writes a message.
func (c *Client) writeMessage(msg *message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	var out []byte
	switch c.framing {
	case FramingContentLength:
		out = append(out, []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))...)
	default:
		out = append(out, body...)
		out = append(out, '\n')
	}
	if _, err := c.stdin.Write(out); err != nil {
		return fmt.Errorf("acp: write: %w", err)
	}
	return nil
}
