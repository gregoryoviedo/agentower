package kiro

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// readKiroMessages returns the user/assistant messages for a Kiro
// session by parsing ~/.kiro/sessions/<workspace>/<id>/messages.jsonl.
// It returns an empty (non-error) slice when the session has no
// history on disk yet, and ErrNoSessionHistory when the session dir
// cannot be found.
func readKiroMessages(root, sessionID string) ([]domain.Message, error) {
	if root == "" {
		root = defaultKiroStateDir()
	}
	path := findKiroSessionMessages(root, sessionID)
	if path == "" {
		return nil, domain.ErrNoActiveSession
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}
	return parseKiroMessages(data)
}

// findKiroSessionMessages locates messages.jsonl for a session id
// under ~/.kiro/sessions/<workspace>/<id>/.
func findKiroSessionMessages(root, sessionID string) string {
	sessionsDir := filepath.Join(root, "sessions")
	workspaces, err := os.ReadDir(sessionsDir)
	if err != nil {
		return ""
	}
	for _, ws := range workspaces {
		if !ws.IsDir() {
			continue
		}
		msgPath := filepath.Join(sessionsDir, ws.Name(), sessionID, "messages.jsonl")
		if _, err := os.Stat(msgPath); err == nil {
			return msgPath
		}
	}
	return ""
}

// parseKiroMessages converts a messages.jsonl blob into domain
// messages. Each line carries a `payload` with a `type` (user or
// assistant) and a `content` string (current releases) or an array of
// typed parts (older releases).
func parseKiroMessages(data []byte) ([]domain.Message, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []domain.Message
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var env struct {
			Payload struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		role := env.Payload.Type
		if role != "user" && role != "assistant" {
			continue
		}
		text := kiroContentText(env.Payload.Content)
		if text == "" {
			continue
		}
		out = append(out, domain.Message{
			Info:  domain.MessageInfo{SessionID: "", Role: role},
			Parts: []domain.MessagePart{{Type: "text", Text: text}},
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// kiroContentText extracts the plain text from a Kiro message content
// field (string or array of typed parts).
func kiroContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asParts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &asParts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range asParts {
		if part.Type != "text" || part.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part.Text)
	}
	return strings.TrimSpace(b.String())
}
