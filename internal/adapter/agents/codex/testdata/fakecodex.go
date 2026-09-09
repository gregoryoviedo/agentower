// fakecodex simulates the OpenAI Codex CLI for adapter tests. It reads
// the user prompt from stdin (the JSON envelope the real codex CLI
// accepts in non-interactive mode) and emits newline-delimited JSON
// events to stdout.
//
// The shape is a best-effort mirror of what codex emits today; if the
// real protocol drifts the test fixtures will catch it before the
// adapter does.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	prompt := readPrompt()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)
	_ = enc.Encode(map[string]any{
		"type":    "session",
		"subtype": "init",
		"cwd":     os.Getenv("PWD"),
	})
	_ = enc.Encode(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
	})
	_ = enc.Encode(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "fakecodex reply to: " + prompt}},
		},
	})
	_ = enc.Encode(map[string]any{
		"type":        "turn.completed",
		"subtype":     "success",
		"is_error":    false,
		"duration_ms": 1,
	})
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func readPrompt() string {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event["type"] != "user" {
			continue
		}
		msg, _ := event["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			part, _ := c.(map[string]any)
			if part["type"] == "text" {
				if text, ok := part["text"].(string); ok {
					return text
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex: read stdin:", err)
	}
	return ""
}
