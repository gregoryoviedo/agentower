package domain

import "time"

type Project struct {
	ID            string
	DisplayName   string
	RelativePath  string
	AbsolutePath  string
	WorkspaceRoot string
	LastSeenAt    time.Time
}

type Session struct {
	ID        string
	ProjectID string
	Title     string
	Directory string
}

type RuntimeState struct {
	WorkspaceRoot string
	ProjectID     string
	RelativePath  string
	SessionID     string
	AgentKind     AgentKind
	UpdatedAt     time.Time
}

// AgentState records the per-chat agent pick and whether each known
// agent is enabled for that chat. A row with enabled=false hides the
// agent from /agents and from the project picker.
type AgentState struct {
	ChatID    int64
	Enabled   map[AgentKind]bool
	Active    AgentKind
	UpdatedAt time.Time
}

type DirectoryEntry struct {
	Name         string
	RelativePath string
}

type NavigationState struct {
	ID                  string
	ChatID              int64
	CurrentRelativePath string
	ExpiresAt           time.Time
	CreatedAt           time.Time
}

type HealthStatus struct {
	Healthy bool
	Version string
}

type FileChange struct {
	Path   string
	Status string
}

// Message is the lightweight projection of an OpenCode session message
// used by the session watcher. The wire format only exposes a stable id,
// the role and the session it belongs to, which is enough for activity
// tracking. Parts carry the typed segments the model emits; the watcher
// inspects them to distinguish a fully-formed assistant answer from a
// stream that is still in progress (e.g. only step-start parts so far).
type Message struct {
	Info  MessageInfo
	Parts []MessagePart
}

type MessageInfo struct {
	ID        string
	SessionID string
	Role      string
}

type MessagePart struct {
	Type string
	Text string
}

// CompletedSession is the snapshot of an OpenCode session right after the
// watcher decided it went idle. The bot stores one per chat and uses it to
// drive the "task done" notification + the /continue resume flow.
type CompletedSession struct {
	ChatID      int64
	SessionID   string
	ProjectID   string
	ProjectName string
	Directory   string
	Title       string
	Preview     string
	AgentKind   AgentKind
	CompletedAt time.Time
	NotifiedAt  time.Time
}

// Snapshot is the projected state the bot exposes over its local HTTP
// control socket so the macOS wrapper can decide when to send the
// "task done" notification.
type Snapshot struct {
	ChatID           int64
	ActiveProject    string
	ActiveSession    string
	ActiveAgent      AgentKind
	LastCompleted    *CompletedSession
	PendingNotifChat int64
}

type BotButton struct {
	Text string
	Data string
}

type BotResponse struct {
	Text    string
	Buttons [][]BotButton
	Edit    bool
}
