package usecase

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionWatcher polls the active session in the background and
// records a completion snapshot once it goes idle. After the
// multi-agent migration the watcher dispatches by AgentKind:
//   - opencode, Claude Code, Codex and Kiro expose ListMessages
//     over their transport, so the watcher polls the message log
//     and applies the same "stable for idleThreshold" heuristic.
//   - GitHub Copilot speaks LSP and never streams a message log;
//     the watcher skips it. The handler publishes the completion
//     snapshot for Copilot at the moment SendPrompt returns.
//
// The watcher holds at most one (chat, kind, session) triple at a
// time. The handler re-arms it via Watch on every prompt so the
// counters reset when the user moves to a different session.
type SessionWatcher struct {
	registry  domain.AgentRegistry
	log       domain.SessionEventLog
	state     domain.StateRepository
	publisher domain.CompletionPublisher
	logger    *slog.Logger

	activeInterval time.Duration
	idleInterval   time.Duration
	idleAfter      time.Duration
	idleThreshold  time.Duration
	previewSize    int
	now            func() time.Time

	mu          sync.Mutex
	chatID      int64
	kind        domain.AgentKind
	sessionID   string
	lastCount   int
	lastStable  time.Time
	lastTouched time.Time
	recorded    bool
}

// SessionWatcherOptions configures a SessionWatcher.
type SessionWatcherOptions struct {
	PollInterval  time.Duration
	IdleInterval  time.Duration
	IdleAfter     time.Duration
	IdleThreshold time.Duration
	PreviewSize   int
	Clock         func() time.Time
	Logger        *slog.Logger
}

// NewSessionWatcher builds a SessionWatcher with sensible defaults.
// The registry is the multi-agent facade the watcher uses to
// resolve the right adapter per kind; the same registry is shared
// with the handler so a kind that the registry has not been told
// about is automatically skipped.
func NewSessionWatcher(registry domain.AgentRegistry, log domain.SessionEventLog, state domain.StateRepository, publisher domain.CompletionPublisher, opts SessionWatcherOptions) *SessionWatcher {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.IdleInterval <= 0 {
		opts.IdleInterval = 15 * time.Second
	}
	if opts.IdleAfter <= 0 {
		opts.IdleAfter = 60 * time.Second
	}
	if opts.IdleThreshold <= 0 {
		opts.IdleThreshold = 30 * time.Second
	}
	if opts.PreviewSize <= 0 {
		opts.PreviewSize = 240
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &SessionWatcher{
		registry:       registry,
		log:            log,
		state:          state,
		publisher:      publisher,
		logger:         opts.Logger,
		activeInterval: opts.PollInterval,
		idleInterval:   opts.IdleInterval,
		idleAfter:      opts.IdleAfter,
		idleThreshold:  opts.IdleThreshold,
		previewSize:    opts.PreviewSize,
		now:            opts.Clock,
	}
}

// Watch switches the watcher to follow a specific session on a
// specific agent. Passing an empty session id clears the current
// observation. The kind controls which adapter the tick loop
// dispatches to; non-streaming agents (today: Copilot) cause the
// tick loop to no-op and let the handler drive the completion
// flow.
func (w *SessionWatcher) Watch(chatID int64, kind domain.AgentKind, sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.chatID = chatID
	w.kind = kind
	w.sessionID = sessionID
	w.lastCount = 0
	w.lastStable = time.Time{}
	w.lastTouched = w.now()
	w.recorded = false
}

// Current returns the session id and agent kind currently being
// observed, if any.
func (w *SessionWatcher) Current() (chatID int64, kind domain.AgentKind, sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.chatID, w.kind, w.sessionID
}

// Run drives the polling loop until ctx is cancelled. The interval
// is adaptive: the active interval is used right after any change
// is observed; once the session has been stable for longer than
// idleAfter, the watcher switches to the (longer) idle interval
// until the next change.
func (w *SessionWatcher) Run(ctx context.Context) {
	timer := time.NewTimer(w.activeInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := w.tick(ctx); err != nil {
				w.logger.Debug("session watcher tick failed", "err", err)
			}
			timer.Reset(w.nextInterval())
		}
	}
}

// nextInterval picks the polling interval to use after the most
// recent tick. When the session has been visibly stable for longer
// than idleAfter, the watcher backs off to idleInterval to keep
// load on the upstream agent low while no work is happening.
func (w *SessionWatcher) nextInterval() time.Duration {
	w.mu.Lock()
	stableSince := w.lastStable
	w.mu.Unlock()
	if stableSince.IsZero() {
		return w.activeInterval
	}
	if w.now().Sub(stableSince) >= w.idleAfter {
		return w.idleInterval
	}
	return w.activeInterval
}

func (w *SessionWatcher) tick(ctx context.Context) error {
	w.mu.Lock()
	chatID, kind, sessionID := w.chatID, w.kind, w.sessionID
	lastCount := w.lastCount
	recorded := w.recorded
	w.mu.Unlock()
	if sessionID == "" || chatID == 0 {
		return nil
	}
	if kind == "" {
		kind = domain.AgentOpenCode
	}
	if !agentStreams(kind) {
		// Non-streaming agents: the handler publishes the
		// completion snapshot when SendPrompt returns, so the
		// watcher has nothing to do for this kind.
		return nil
	}
	adapter, err := w.registry.Get(kind)
	if err != nil {
		w.logger.Debug("watcher: agent not available", "kind", kind, "err", err)
		return nil
	}
	messages, err := adapter.ListMessages(ctx, sessionID)
	if err != nil {
		// Transport errors are treated as transient; the next
		// tick will retry. The completedSession log keeps the
		// last successful snapshot.
		return nil
	}
	count := len(messages)
	w.mu.Lock()
	if count != lastCount {
		w.lastCount = count
		w.lastStable = w.now()
		w.lastTouched = w.now()
		w.recorded = false
		recorded = false
	}
	stableSince := w.lastStable
	w.mu.Unlock()

	if stableSince.IsZero() {
		return nil
	}
	if recorded {
		return nil
	}
	if w.now().Sub(stableSince) < w.idleThreshold {
		return nil
	}
	// Refuse to call a session "complete" if the latest assistant
	// message only carries non-text parts (e.g. only step-start),
	// which usually means the model is still streaming and more
	// text is on the way.
	if !lastMessageIsFinal(messages) {
		return nil
	}
	if err := w.persistCompletion(ctx, chatID, kind, sessionID, messages, adapter); err != nil {
		return err
	}
	w.mu.Lock()
	w.recorded = true
	w.mu.Unlock()
	return nil
}

// lastMessageIsFinal reports whether the most recent message is an
// assistant message that already carries at least one text part.
// We use this as a proxy for "the model finished writing":
// streaming answers emit a step-start part first and only add the
// text part when the answer is fully buffered server-side.
func lastMessageIsFinal(messages []domain.Message) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if last.Info.Role != "assistant" {
		return false
	}
	for _, p := range last.Parts {
		if p.Type == "text" && p.Text != "" {
			return true
		}
	}
	return false
}

// agentStreams reports whether the watcher can poll message history
// for the given agent. Today only Copilot (LSP) is excluded; every
// other supported agent exposes a log-like history the watcher can
// tail. New agents default to "streams" so a future addition that
// exposes messages is picked up automatically.
func agentStreams(kind domain.AgentKind) bool {
	switch kind {
	case domain.AgentCopilot:
		return false
	}
	return true
}

func (w *SessionWatcher) persistCompletion(ctx context.Context, chatID int64, kind domain.AgentKind, sessionID string, messages []domain.Message, adapter domain.AgentAdapter) error {
	snapshot := domain.CompletedSession{
		ChatID:      chatID,
		SessionID:   sessionID,
		AgentKind:   kind,
		Preview:     previewFromMessages(messages, w.previewSize),
		CompletedAt: w.now().UTC(),
	}
	if sessions, err := adapter.ListSessions(ctx); err == nil {
		for _, s := range sessions {
			if s.ID == sessionID {
				snapshot.Title = s.Title
				snapshot.ProjectID = s.ProjectID
				snapshot.Directory = s.Directory
				snapshot.ProjectName = projectNameFromPath(s.Directory)
				break
			}
		}
	}
	if err := w.log.SaveCompletedSession(ctx, snapshot); err != nil {
		return err
	}
	if w.publisher != nil {
		w.publisher.PublishCompletion(snapshot)
	}
	w.logger.Info("session watcher recorded completion",
		"chat_id", chatID, "session_id", sessionID, "agent", kind, "messages", len(messages))
	return nil
}

// previewFromMessages walks the messages from newest to oldest,
// joins the text parts of the last assistant message, and truncates
// the result to max bytes. Callers should only invoke this once
// lastMessageIsFinal has accepted the message, so an empty preview
// implies no assistant text.
func previewFromMessages(messages []domain.Message, max int) string {
	if len(messages) == 0 || max <= 0 {
		return ""
	}
	var text strings.Builder
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role != "assistant" {
			continue
		}
		for _, p := range messages[i].Parts {
			if p.Type != "text" {
				continue
			}
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString(p.Text)
		}
		break
	}
	out := strings.TrimSpace(text.String())
	if max > 0 && len(out) > max {
		out = out[:max] + "…"
	}
	return out
}

func projectNameFromPath(path string) string {
	path = strings.TrimRight(path, "/")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// SessionEvent is the payload the publisher sends when the watcher
// records a fresh completion. The HTTP control surface turns it
// into a JSON snapshot for the macOS wrapper to consume. Currently
// unused but kept here as the canonical shape for future
// cross-process notifications.
type SessionEvent struct {
	ChatID     int64
	SessionID  string
	Project    string
	Preview    string
	OccurredAt time.Time
}
