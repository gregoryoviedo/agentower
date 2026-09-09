// Package copilot — LSP client surface.
package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// jsonRPCRequest is the wire shape of an LSP request payload.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// client is a tiny JSON-RPC 2.0 client over stdio. It tracks pending
// requests by id and resolves them when their matching response
// arrives on the read goroutine.
type client struct {
	framer *framer
	writer *writer

	mu       sync.Mutex
	pending  map[int64]chan jsonRPCResponse
	nextID   int64
	closed   atomic.Bool
	readErr  chan error
	serverCaps map[string]any
}

func newClient(rd io.Reader, wr io.Writer) *client {
	return &client{
		framer:    newFramer(rd),
		writer:    newWriter(wr),
		pending:   map[int64]chan jsonRPCResponse{},
		nextID:    1,
		readErr:   make(chan error, 1),
		serverCaps: map[string]any{},
	}
}

// run drives the read loop until the stream closes. Pending requests
// receive an error and are removed.
func (c *client) run() {
	for {
		payload, err := c.framer.ReadMessage()
		if err != nil {
			c.readErr <- err
			return
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *client) notify(method string, params any) error {
	req := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode %s: %w", method, err)
	}
	return c.writer.WriteMessage(payload)
}

// call sends a JSON-RPC request and waits for the matching response.
// The context cancels the wait.
func (c *client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan jsonRPCResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("encode %s: %w", method, err)
	}
	if err := c.writer.WriteMessage(payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("write %s: %w", method, err)
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("lsp %s: %s", method, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// initialize runs the LSP initialize / initialized handshake.
func (c *client) initialize(ctx context.Context) error {
	var initResult struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	params := map[string]any{
		"processId": nil,
		"rootUri":   nil,
		"capabilities": map[string]any{
			"workspace":     map[string]any{"workspaceFolders": []any{}},
			"textDocument": map[string]any{"synchronization": map[string]any{"didSave": true}},
		},
		"initializationOptions": map[string]any{},
	}
	if err := c.call(ctx, "initialize", params, &initResult); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	c.serverCaps = initResult.Capabilities
	return c.notify("initialized", map[string]any{})
}

// errClosed is returned by call/notify after the read goroutine exits.
var errClosed = errors.New("lsp: client closed")

func (c *client) Err() error {
	if c.closed.Load() {
		return errClosed
	}
	return nil
}
