// fakecodex simulates the OpenAI Codex CLI in exec mode for adapter
// tests. It emits the `codex exec --json` JSONL event stream and reads
// the prompt from stdin, mirroring the real CLI ("-" reads stdin).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	prompt := readPrompt()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)

	threadID := os.Getenv("FAKECODEX_THREAD_ID")
	if threadID == "" {
		threadID = "0199a213-81c0-7800-8aa1-bbab2a035a53"
	}
	_ = enc.Encode(map[string]any{"type": "thread.started", "thread_id": threadID})
	_ = enc.Encode(map[string]any{"type": "turn.started"})
	_ = enc.Encode(map[string]any{
		"type": "item.completed",
		"item": map[string]any{"id": "item_0", "type": "reasoning", "text": "thinking"},
	})
	_ = enc.Encode(map[string]any{
		"type": "item.completed",
		"item": map[string]any{"id": "item_1", "type": "agent_message", "text": "fakecodex reply to: " + prompt},
	})
	_ = enc.Encode(map[string]any{
		"type":  "turn.completed",
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func readPrompt() string {
	data, _ := io.ReadAll(os.Stdin)
	return strings.TrimSpace(string(data))
}

var _ = fmt.Sprint
