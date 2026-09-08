package domain

import (
	"context"
	"os"
	"time"
)

type WorkspaceFS interface {
	ReadDir(name string) ([]os.DirEntry, error)
	EvalSymlinks(path string) (string, error)
	Stat(name string) (os.FileInfo, error)
}

type StateRepository interface {
	LoadRuntimeState(ctx context.Context) (RuntimeState, error)
	SaveRuntimeState(ctx context.Context, state RuntimeState) error
}

type NavigationRepository interface {
	SaveNavigation(ctx context.Context, state NavigationState) error
	GetNavigation(ctx context.Context, id string) (NavigationState, error)
	DeleteNavigation(ctx context.Context, id string) error
}

type OpenCodeClient interface {
	Health(ctx context.Context) (HealthStatus, error)
	ListProjects(ctx context.Context) ([]Project, error)
	ListSessions(ctx context.Context) ([]Session, error)
	CreateSession(ctx context.Context, parentID string) (Session, error)
	SendPrompt(ctx context.Context, sessionID, text string) (string, error)
	Revert(ctx context.Context, sessionID string) error
	FileStatus(ctx context.Context, sessionID string) ([]FileChange, error)
	ListMessages(ctx context.Context, sessionID string) ([]Message, error)
}

// SessionEventLog persists completion snapshots the bot discovers through
// polling. It is keyed by chat id so the macOS wrapper can ask "what was
// the last completed session for this Telegram chat".
type SessionEventLog interface {
	SaveCompletedSession(ctx context.Context, snapshot CompletedSession) error
	LoadCompletedSession(ctx context.Context, chatID int64) (CompletedSession, bool, error)
	MarkNotified(ctx context.Context, chatID int64, when time.Time) error
}

// SnapshotPublisher exposes the current snapshot of the bot to in-process
// consumers (HTTP handlers, tests). The macOS wrapper talks to the bot via
// the local HTTP server, which delegates to this interface.
type SnapshotPublisher interface {
	Snapshot() Snapshot
	SetActive(chatID int64, project, session string)
	RequestNotification(chatID int64)
	CancelPending(chatID int64)
}

// CompletionPublisher receives new completion snapshots from the
// SessionWatcher so the local HTTP control surface can serve them to the
// macOS wrapper.
type CompletionPublisher interface {
	PublishCompletion(snapshot CompletedSession)
}

// IdleMonitor reports how long the local user has been idle. The macOS
// wrapper implements this against CGEventSource; tests can substitute a
// fake clock.
type IdleMonitor interface {
	LastInputAt(ctx context.Context) (time.Time, error)
}

type BotHandler interface {
	HandleCommand(ctx context.Context, chatID int64, command string, args []string) (BotResponse, error)
	HandleText(ctx context.Context, chatID int64, text string) (BotResponse, error)
	HandleCallback(ctx context.Context, chatID int64, data string) (BotResponse, error)
}

// ChatNotifier lets the bot push out-of-band signals to a Telegram chat
// (e.g. the "typing…" indicator) without going through a regular response.
type ChatNotifier interface {
	NotifyTyping(ctx context.Context, chatID int64) error
	// SendMessage posts a free-form text message (Markdown is rendered to
	// Telegram HTML by the adapter) and is used for asynchronous
	// notifications that originate outside the user-driven request flow.
	SendMessage(ctx context.Context, chatID int64, text string) error
	// SendResponse is the same as SendMessage but allows attaching inline
	// keyboard buttons. It is used for asynchronous notifications that
	// benefit from quick-tap actions (e.g. "Continue session").
	SendResponse(ctx context.Context, chatID int64, response BotResponse) error
}

// OpenCodeServerManager owns the lifecycle of the local OpenCode serve
// subprocess. Implementations are expected to swap the running subprocess
// atomically when Start is called again with a different working dir.
type OpenCodeServerManager interface {
	Start(ctx context.Context, workingDir string) error
	Stop()
	StartedSubprocess() bool
	WorkingDir() string
}
