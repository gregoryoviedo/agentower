// fakekiro simulates the Kiro CLI for adapter tests. Kiro is the most
// under-documented of the supported agents, so the fixture keeps the
// protocol minimal: one session.init, one user echo, one assistant
// reply, one turn.completed. If real Kiro ships a richer format the
// adapter can grow without breaking the test contract.
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
			"content": []map[string]any{{"type": "text", "text": "fakekiro reply: " + prompt}},
		},
	})
	_ = enc.Encode(map[string]any{
		"type":        "turn.completed",
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
		fmt.Fprintln(os.Stderr, "fakekiro: read stdin:", err)
	}
	return ""
}
