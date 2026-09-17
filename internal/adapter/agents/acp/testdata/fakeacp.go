// fakeacp simulates a minimal ACP agent for the acp package tests: it
// speaks newline-delimited JSON-RPC and answers initialize,
// session/new, session/load, session/resume, session/prompt (streaming
// one agent_message_chunk) and session/request_permission.
//
// Env knobs used by the tests:
//
//	FAKEACP_ADVERTISE_RESUME=1  advertise sessionCapabilities.resume
//	FAKEACP_NO_RESUME=1         answer session/resume with -32601
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
			agentCaps := map[string]any{"loadSession": true}
			if os.Getenv("FAKEACP_ADVERTISE_RESUME") == "1" {
				agentCaps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			}
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion":   1,
					"agentCapabilities": agentCaps,
					"agentInfo":         map[string]any{"name": "fakeacp", "version": "1.0"},
				},
			})
		case "session/new":
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"sessionId": "acp-session-1"},
			})
		case "session/load":
			replayHistory(m.Params, send)
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/resume":
			if os.Getenv("FAKEACP_NO_RESUME") == "1" {
				send(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32601, "message": "Method not found: session/resume"},
				})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session/request_permission":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": "selected", "optionId": "allow"}})
		case "session/prompt":
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionIDOf(m.Params),
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content": map[string]any{
							"content": map[string]any{
								"content": map[string]any{"type": "text", "text": "fakeacp reply"},
							},
						},
					},
				},
			})
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
		default:
			send(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32601, "message": "method not found: " + m.Method},
			})
		}
	}
}

// replayHistory simulates session/load replaying the conversation as
// agent_message_chunk notifications before it responds.
func replayHistory(params json.RawMessage, send func(map[string]any)) {
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionIDOf(params),
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

func sessionIDOf(params json.RawMessage) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.SessionID
}
