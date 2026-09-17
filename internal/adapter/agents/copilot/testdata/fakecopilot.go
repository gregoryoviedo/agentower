// fakecopilot simulates the Copilot CLI's ACP server (copilot --acp)
// for adapter tests. It speaks newline-delimited JSON-RPC 2.0:
// initialize, session/new, session/load (replaying one notification) and
// session/prompt, streaming one agent_message_chunk before answering.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func main() {
	out := bufio.NewWriter(os.Stdout)
	send := func(m map[string]any) {
		b, _ := json.Marshal(m)
		fmt.Fprintln(out, string(b))
		_ = out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		id := m.ID
		switch m.Method {
		case "initialize":
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion":   1,
					"agentCapabilities": map[string]any{"loadSession": true},
					"agentInfo":         map[string]any{"name": "fakecopilot", "version": "1.0"},
				},
			})
		case "session/new":
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"sessionId": "sess_" + randHex()},
			})
		case "session/load":
			replyLoad(m.Params, send)
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/prompt":
			replyPrompt(m.Params, send)
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
		default:
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32601, "message": "method not found: " + m.Method},
			})
		}
	}
}

func replyPrompt(params json.RawMessage, send func(map[string]any)) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(params, &p)
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": p.SessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"content": map[string]any{
						"content": map[string]any{"type": "text", "text": "fakecopilot reply"},
					},
				},
			},
		},
	})
}

// replyLoad replays one history notification, mirroring session/load.
func replyLoad(params json.RawMessage, send func(map[string]any)) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": p.SessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"content": map[string]any{
						"content": map[string]any{"type": "text", "text": "replayed history"},
					},
				},
			},
		},
	})
}

func randHex() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = "0123456789abcdef"[os.Getpid()%16]
	}
	return string(b)
}
