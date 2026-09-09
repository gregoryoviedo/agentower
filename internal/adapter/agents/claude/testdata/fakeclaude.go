// fakeclaude simulates the Claude Code CLI for adapter tests. It
// reads the user prompt from stdin (Claude Code's documented wire
// format) and emits newline-delimited JSON events to stdout. The
// shape mirrors what Claude Code emits today; if the real protocol
// drifts the test fixtures will catch it before the adapter does.
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
		"type":    "system",
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
			"content": []map[string]any{{"type": "text", "text": "fakeclaude reply to: " + prompt}},
		},
	})
	_ = enc.Encode(map[string]any{
		"type":        "result",
		"subtype":     "success",
		"is_error":    false,
		"duration_ms": 1,
	})
	// Drain stdin so a peer that hasn't closed the pipe yet does not
	// keep us hanging. Claude Code itself does the same.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// readPrompt walks the newline-delimited JSON stream on stdin until
// it finds the first "user" event and returns its text. Matches the
// real Claude Code CLI behaviour where stdin is the channel for
// follow-up prompts.
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
		fmt.Fprintln(os.Stderr, "fakeclaude: read stdin:", err)
	}
	return ""
}
