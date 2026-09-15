package antigravity

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// readLines drains a reader into a slice of raw lines.
func readLines(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := []string{}
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	return out, scanner.Err()
}

func unmarshalJSON(raw string, v any) error {
	return json.Unmarshal([]byte(raw), v)
}

// transcriptPath returns the readable JSONL transcript for a
// conversation. Antigravity writes it next to the brain artifacts:
// <root>/brain/<id>/.system_generated/logs/transcript_full.jsonl.
func transcriptPath(root, conversationID string) string {
	return filepath.Join(root, "brain", conversationID, ".system_generated", "logs", "transcript_full.jsonl")
}

// historyEntry is one prompt-recall line enriched with the conversation
// title, if one can be recovered from the brain artifacts.
type historyEntry struct {
	Display        string
	Workspace      string
	ConversationID string
	TimestampMS    int64
	Title          string
}

// readHistory parses <root>/history.jsonl. Lines carry `display`
// (prompt text), `timestamp` (Unix milliseconds), `workspace` and an
// optional `conversationId`.
func readHistory(root string) []historyEntry {
	path := filepath.Join(root, "history.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	out := []historyEntry{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var rec struct {
			Display        string `json:"display"`
			Workspace      string `json:"workspace"`
			ConversationID string `json:"conversationId"`
			Timestamp      int64  `json:"timestamp"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		out = append(out, historyEntry{
			Display:        rec.Display,
			Workspace:      rec.Workspace,
			ConversationID: rec.ConversationID,
			TimestampMS:    rec.Timestamp,
		})
	}
	return out
}

// sortHistory orders entries by timestamp desc (stable for ties).
func sortHistory(in []historyEntry) {
	sort.SliceStable(in, func(i, j int) bool {
		return in[i].TimestampMS > in[j].TimestampMS
	})
}

// transcriptLine is the projection of one transcript_full.jsonl line.
// Only lines carrying a string `content` are user/assistant turns; the
// rest (thinking, tool-only, history markers) are skipped.
type transcriptLine struct {
	StepIndex int    `json:"step_index"`
	Type      string `json:"type"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Content   string `json:"content"`
}

// parseTranscript projects the transcript into domain messages. The
// role is derived from `source` (user vs model/assistant).
func parseTranscript(path, sessionID string) []domain.Message {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	out := []domain.Message{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		text := strings.TrimSpace(line.Content)
		if text == "" {
			continue
		}
		role := roleFromSource(line.Source)
		if role == "" {
			continue
		}
		out = append(out, domain.Message{
			Info:  domain.MessageInfo{ID: sessionID + ":" + itoa(line.StepIndex), SessionID: sessionID, Role: role},
			Parts: []domain.MessagePart{{Type: "text", Text: text}},
		})
	}
	return out
}

// roleFromSource maps the transcript `source` field to the role the
// watcher understands. Unknown sources are dropped so tool noise does
// not look like an assistant answer.
func roleFromSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "user", "user_input":
		return "user"
	case "model", "assistant", "agent", "agent_response", "planner_response":
		return "assistant"
	default:
		return ""
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
