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
	SaveAgentState(ctx context.Context, chatID int64, kind AgentKind, enabled bool) error
	LoadAgentState(ctx context.Context, chatID int64) (AgentState, error)
	ListEnabledAgents(ctx context.Context) (map[AgentKind]bool, error)
}

type NavigationRepository interface {
	SaveNavigation(ctx context.Context, state NavigationState) error
	GetNavigation(ctx context.Context, id string) (NavigationState, error)
	DeleteNavigation(ctx context.Context, id string) error
}

// AgentKind identifies which AI agent the bot is talking to for a given
// chat/project. Every concrete adapter (opencode, claude, kiro,
// copilot) declares its kind and the registry uses it to dispatch.
type AgentKind string

const (
	AgentOpenCode AgentKind = "opencode"
	AgentClaude   AgentKind = "claude"
	AgentKiro     AgentKind = "kiro"
	AgentCopilot  AgentKind = "copilot"
)

// AllAgentKinds returns the supported agent kinds in the order they
// appear in the Telegram picker. Stable order keeps the picker layout
// deterministic between renders.
func AllAgentKinds() []AgentKind {
	return []AgentKind{AgentOpenCode, AgentClaude, AgentKiro, AgentCopilot}
}

// AgentCapabilities describes which features an adapter currently
// exposes. The detector fills this in at boot from the binary's
// --help output or a handshake, and the Telegram UI hides commands
// the active agent does not support.
type AgentCapabilities struct {
	Health        bool
	ListProjects  bool
	ListSessions  bool
	CreateSession bool
	SendPrompt    bool
	Revert        bool
	FileStatus    bool
	ListMessages  bool
}

// AgentDescriptor is the boot-time fingerprint of a single agent: where
// the binary lives, whether it is already running, and what it can do.
type AgentDescriptor struct {
	Kind         AgentKind
	DisplayName  string
	Bin          string
	Port         int
	Detected     bool // the binary is reachable in PATH or the configured bundle path
	Running      bool // the agent's local server (if any) is already up
	Available    bool // the adapter is implemented and the binary handshake succeeded
	Capabilities AgentCapabilities
	Reason       string // human-readable explanation when Available=false
}

// AgentAdapter is the per-agent transport (HTTP, stdio JSON-RPC, LSP,
// etc.). Every concrete adapter — opencode/claude/kiro/copilot
// — implements this surface and the registry uses Kind() to route
// commands to the right instance.
type AgentAdapter interface {
	Kind() AgentKind
	DisplayName() string
	Health(ctx context.Context) (HealthStatus, error)
	ListProjects(ctx context.Context) ([]Project, error)
	ListSessions(ctx context.Context) ([]Session, error)
	CreateSession(ctx context.Context, parentID string) (Session, error)
	SendPrompt(ctx context.Context, sessionID, text string) (string, error)
	Revert(ctx context.Context, sessionID string) error
	FileStatus(ctx context.Context, sessionID string) ([]FileChange, error)
	ListMessages(ctx context.Context, sessionID string) ([]Message, error)
}

// AgentRegistry is the read/write facade over the set of adapters the
// bot knows about. It hides concrete adapter construction (lazy,
// per-chat) and tracks which agent each chat is currently driving.
type AgentRegistry interface {
	// Descriptors returns the boot-time fingerprints for every known
	// agent. The Telegram picker iterates over this list.
	Descriptors() []AgentDescriptor
	// Available returns only the descriptors whose adapter is fully
	// implemented and the binary is detected.
	Available() []AgentDescriptor
	// DescriptorFor returns the descriptor for a single kind so the
	// caller can render a specific card or reason about its state.
	DescriptorFor(kind AgentKind) (AgentDescriptor, bool)
	// Get returns the adapter for the given kind. ErrAgentUnavailable
	// if the kind is not Available; ErrAgentCapabilitiesLimited if the
	// caller invoked an unsupported method on a partial adapter.
	Get(kind AgentKind) (AgentAdapter, error)
	// Active returns the agent the chat is currently driving. The empty
	// AgentKind is returned when nothing has been picked yet.
	Active(chatID int64) (AgentKind, error)
	// SetActive persists the chat's agent pick. Idempotent.
	SetActive(chatID int64, kind AgentKind) error
}

// AgentServerManager owns the subprocess lifecycle of every agent
// simultaneously. Each kind has its own slot, so two agents can be
// running at the same time and the bot can switch the "active" one in
// Telegram without tearing the others down.
type AgentServerManager interface {
	Start(ctx context.Context, kind AgentKind, workingDir string) error
	Stop(kind AgentKind)
	StopAll()
	StartedSubprocess(kind AgentKind) bool
	OwnsSubprocess(kind AgentKind) bool
	WorkingDir(kind AgentKind) string
}

// OpenCodeClient is the legacy single-agent port. It is kept as a
// thin alias of AgentAdapter so existing usecase code keeps compiling
// during the migration. New code should depend on AgentAdapter (or
// AgentRegistry) instead.
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

// SessionLocator is the per-agent side of "what session is currently
// running on the machine". Each adapter knows where its active session
// metadata lives (opencode: HTTP API; claude code: ~/.claude JSONL;
// copilot: VS Code globalStorage). The /continuar handler asks every
// registered locator, filters out stale and missing results, and shows
// the freshest one.
type SessionLocator interface {
	Kind() AgentKind
	// Locate returns the most recently active session for this agent.
	// Implementations MUST return ErrNoActiveSession when the user is
	// not driving this agent right now; any other error is treated as
	// a transient failure and the locator is skipped.
	Locate(ctx context.Context) (ActiveSession, error)
}

// ActiveLocatorRegistry is the facade over the set of SessionLocators
// the bot has wired. The composition root calls Add once per agent
// during boot; the /continuar handler iterates over Locators() in
// parallel. Tests can call Add themselves to wire fakes.
type ActiveLocatorRegistry interface {
	Add(locator SessionLocator)
	Locators() []SessionLocator
	LocatorFor(kind AgentKind) (SessionLocator, bool)
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

// OpenCodeServerManager is the legacy single-agent subprocess port. It
// mirrors the methods of AgentServerManager for the opencode slot only
// and is kept so existing usecase code keeps compiling during the
// migration. New code should depend on AgentServerManager.
type OpenCodeServerManager interface {
	Start(ctx context.Context, workingDir string) error
	Stop()
	StartedSubprocess() bool
	WorkingDir() string
}
