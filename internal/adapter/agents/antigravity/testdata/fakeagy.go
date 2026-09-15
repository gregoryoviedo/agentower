// fakeagy simulates the Antigravity CLI (`agy`) in headless print mode
// for adapter tests. It emits the `--output-format stream-json` events
// and reads nothing from stdin (the prompt arrives via -p).
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

func main() {
	prompt := promptFromArgs(os.Args)
	convo := os.Getenv("FAKEAGY_CONVERSATION_ID")
	if convo == "" {
		convo = "c3b66b04-872b-4fbe-a3a4-058a026ef20a"
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)

	_ = enc.Encode(map[string]any{
		"event":           "init",
		"conversation_id": convo,
		"init":            map[string]any{"cwd": os.Getenv("PWD")},
	})
	_ = enc.Encode(map[string]any{
		"event":           "step_update",
		"conversation_id": convo,
		"step_update":     map[string]any{"step_index": 0, "state": "DONE", "step_type": "user_input"},
	})
	_ = enc.Encode(map[string]any{
		"event":           "step_update",
		"conversation_id": convo,
		"step_update": map[string]any{
			"step_index": 1,
			"state":      "DONE",
			"step_type":  "agent_response",
			"text_delta": "fakeagy reply to: " + prompt,
		},
	})
	_ = enc.Encode(map[string]any{
		"event":           "result",
		"conversation_id": convo,
		"result": map[string]any{
			"status":   "SUCCESS",
			"response": "fakeagy reply to: " + prompt,
		},
	})
}

// promptFromArgs pulls the value passed to -p/--print/--prompt.
func promptFromArgs(args []string) string {
	for i, a := range args {
		if a == "-p" || a == "--print" || a == "--prompt" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return strings.TrimSpace(strings.Join(args, " "))
}
