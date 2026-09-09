// fakecopilot simulates a GitHub Copilot Language Server for adapter
// tests. It speaks JSON-RPC 2.0 over stdio with Content-Length framed
// headers and answers the subset of LSP that the adapter uses
// today: initialize, initialized, textDocument/didOpen,
// textDocument/didChange and textDocument/inlineCompletion.
//
// The fixture is intentionally minimal: the goal is to prove the
// adapter's framing, request routing and error handling, not to
// replicate Copilot's full chat surface.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	payload []byte
}

func main() {
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	var mu sync.Mutex
	writeMsg := func(resp response) error {
		mu.Lock()
		defer mu.Unlock()
		payload, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
		return err
	}
	for {
		payload, err := readMessage(in)
		if err != nil {
			if err == io.EOF {
				return
			}
			fmt.Fprintln(os.Stderr, "fakecopilot:", err)
			return
		}
		var req request
		if err := json.Unmarshal(payload, &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			caps := map[string]any{
				"capabilities": map[string]any{
					"inlineCompletionProvider": true,
				},
				"serverInfo": map[string]any{"name": "fakecopilot", "version": "0.0.0"},
			}
			result, _ := json.Marshal(caps)
			resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
			_ = writeMsg(resp)
		case "initialized":
			// notification; nothing to answer
		case "textDocument/didOpen":
			// notification
		case "textDocument/didChange":
			// notification
		case "textDocument/inlineCompletion":
			result := map[string]any{
				"items": []map[string]any{{
					"insertText": "fakecopilot reply",
				}},
			}
			payload, _ := json.Marshal(result)
			resp := response{JSONRPC: "2.0", ID: req.ID, Result: payload}
			_ = writeMsg(resp)
		default:
			if req.ID != 0 {
				resp := response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
				}
				_ = writeMsg(resp)
			}
		}
	}
}

// readMessage reads one LSP message from the reader.
func readMessage(rd *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for contentLength < 0 {
		line, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = line[:len(line)-1] // strip \n
		if line == "\r" || line == "" {
			continue
		}
		if len(line) > 16 && line[:16] == "Content-Length: " {
			var n int
			if _, err := fmt.Sscanf(line[16:], "%d", &n); err == nil {
				contentLength = n
			}
		}
	}
	// Consume the trailing blank line.
	if _, err := rd.ReadString('\n'); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(rd, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

var _ = envelope{}
