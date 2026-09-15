package copilot

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// readCopilotMessages returns the user/assistant messages for a Copilot
// session by reading the VS Code session-store.db `turns` table. The
// store is opened read-only so reading history never mutates VS Code's
// state. Returns ErrNoActiveSession when the session has no turns.
func readCopilotMessages(root, sessionID string) ([]domain.Message, error) {
	dbPath := filepath.Join(root, "session-store.db")
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}
	dsn := "file:" + dbPath + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT turn_index, user_message, assistant_response FROM turns WHERE session_id = ? ORDER BY turn_index`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		var idx int
		var user, assistant sql.NullString
		if err := rows.Scan(&idx, &user, &assistant); err != nil {
			return nil, err
		}
		if user.Valid && user.String != "" {
			out = append(out, domain.Message{
				Info:  domain.MessageInfo{SessionID: sessionID, Role: "user"},
				Parts: []domain.MessagePart{{Type: "text", Text: user.String}},
			})
		}
		if assistant.Valid && assistant.String != "" {
			out = append(out, domain.Message{
				Info:  domain.MessageInfo{SessionID: sessionID, Role: "assistant"},
				Parts: []domain.MessagePart{{Type: "text", Text: assistant.String}},
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, domain.ErrNoActiveSession
	}
	return out, nil
}
