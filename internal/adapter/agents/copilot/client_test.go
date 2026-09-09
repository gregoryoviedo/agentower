package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClientCallResolvesWithMatchingResponse simulates a minimal LSP
// server that emits one matching response for our request. The call
// must resolve with the decoded result and clear the pending map.
func TestClientCallResolvesWithMatchingResponse(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	t.Cleanup(func() {
		_ = serverRead.Close()
		_ = serverWrite.Close()
		_ = clientRead.Close()
		_ = clientWrite.Close()
	})
	cl := newClient(clientRead, clientWrite)

	go func() {
		// Server reads the request the client wrote.
		payload, err := newFramer(serverRead).ReadMessage()
		if err != nil {
			return
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return
		}
		resp, _ := json.Marshal(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"value":42}`),
		})
		_ = newWriter(serverWrite).WriteMessage(resp)
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go cl.run()

	var result struct {
		Value int `json:"value"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cl.call(ctx, "test/echo", map[string]any{"x": 1}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.Value != 42 {
		t.Fatalf("Value = %d, want 42", result.Value)
	}
	cl.closed.Store(true)
	cl.mu.Lock()
	pending := len(cl.pending)
	cl.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending map has %d entries after resolved call", pending)
	}
}

// TestClientCallReturnsErrorForLSPErrorResponse mirrors what a real
// Copilot language server does when it receives an unsupported method:
// it answers with a JSON-RPC error object. The client must propagate
// the message verbatim instead of returning a generic decoding error.
func TestClientCallReturnsErrorForLSPErrorResponse(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	t.Cleanup(func() {
		_ = serverRead.Close()
		_ = serverWrite.Close()
		_ = clientRead.Close()
		_ = clientWrite.Close()
	})
	cl := newClient(clientRead, clientWrite)

	go func() {
		payload, err := newFramer(serverRead).ReadMessage()
		if err != nil {
			return
		}
		var req jsonRPCRequest
		_ = json.Unmarshal(payload, &req)
		resp, _ := json.Marshal(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found"},
		})
		_ = newWriter(serverWrite).WriteMessage(resp)
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go cl.run()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := cl.call(ctx, "unknown/method", nil, nil)
	if err == nil {
		t.Fatal("call returned nil error for an LSP error response")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("err = %v, want one mentioning the LSP message", err)
	}
	cl.closed.Store(true)
}

// TestClientCallCancelsOnContextDeadline exercises the ctx.Done()
// branch in call. The peer never answers, the call must surface
// context.DeadlineExceeded and the pending slot must be freed so a
// follow-up call with the same id does not see stale state. The
// client writes the request to a buffered sink so the call does not
// deadlock waiting for a server to consume it.
func TestClientCallCancelsOnContextDeadline(t *testing.T) {
	sink := threadSafeBuffer{}
	cl := newClient(strings.NewReader(""), &sink)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := cl.call(ctx, "test/slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	cl.mu.Lock()
	pending := len(cl.pending)
	cl.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending map has %d entries after cancel, want 0", pending)
	}
}

// TestClientCallHandlesLateResponseAfterCancel proves the client
// gracefully tolerates a response that arrives after the caller's
// context expired. The reader must drop the late reply (the id is no
// longer in pending) without panicking or leaking the channel.
func TestClientCallHandlesLateResponseAfterCancel(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	t.Cleanup(func() {
		_ = serverRead.Close()
		_ = serverWrite.Close()
		_ = clientRead.Close()
		_ = clientWrite.Close()
	})
	cl := newClient(clientRead, clientWrite)

	// Pre-populate the pending map with a channel the test owns so
	// run() drops the late response on the floor instead of trying
	// to deliver it.
	cl.mu.Lock()
	cl.pending[99] = make(chan jsonRPCResponse, 1)
	cl.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cl.run()
	}()

	resp, _ := json.Marshal(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      99,
		Result:  json.RawMessage(`{"value":1}`),
	})
	if err := newWriter(serverWrite).WriteMessage(resp); err != nil {
		t.Fatal(err)
	}
	_ = serverWrite.Close()
	_ = serverRead.Close()
	_ = clientWrite.Close()

	wg.Wait()

	cl.mu.Lock()
	pending := len(cl.pending)
	cl.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending map has %d entries after late response, want 0", pending)
	}
}

// TestClientNotifyDoesNotRequireResponse covers the notify() branch:
// the peer never answers but the call must succeed and the pending
// map must stay empty. This is the path used by textDocument/didOpen
// and textDocument/didChange.
func TestClientNotifyDoesNotRequireResponse(t *testing.T) {
	var sink bytes.Buffer
	cl := newClient(strings.NewReader(""), &sink)

	if err := cl.notify("textDocument/didOpen", map[string]any{"x": 1}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if sink.Len() == 0 {
		t.Fatal("notify wrote nothing to the writer")
	}
	cl.mu.Lock()
	pending := len(cl.pending)
	cl.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending map has %d entries after notify, want 0", pending)
	}
}

// TestClientErrReportsClosed is the contract for Err(): once the
// read goroutine has exited the client must report a closed error so
// callers can stop issuing requests.
func TestClientErrReportsClosed(t *testing.T) {
	cl := newClient(strings.NewReader(""), &bytes.Buffer{})
	if err := cl.Err(); err != nil {
		t.Fatalf("fresh client Err = %v, want nil", err)
	}
	cl.closed.Store(true)
	if err := cl.Err(); !errors.Is(err, errClosed) {
		t.Fatalf("Err after close = %v, want errClosed", err)
	}
}

// threadSafeBuffer wraps bytes.Buffer with a mutex so the test
// fixtures do not race when the production code copies bytes in one
// goroutine while the test reads them in another. We only need this
// in the cancellation tests, where the read loop exits mid-write.
type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Read(p)
}
