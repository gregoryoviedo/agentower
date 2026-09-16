package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// typingRefreshInterval is how often we re-post the "typing…" chat action
// while a long-running prompt is in flight. Telegram's chat action expires
// on the client after roughly five seconds, so anything safely under that
// keeps the indicator alive for the whole duration.
const typingRefreshInterval = 4 * time.Second

type Handler struct {
	state         domain.StateRepository
	registry      domain.AgentRegistry
	manager       domain.AgentServerManager
	notifier      domain.ChatNotifier
	watcher       SessionController
	events        domain.SessionEventLog
	snapshot      domain.SnapshotPublisher
	completions   domain.CompletionPublisher
	locators      domain.ActiveLocatorRegistry
	questions     domain.QuestionBroker
	workspaceRoot string
	staleAfter    time.Duration
	now           func() time.Time
	startMu       sync.Mutex
}

// SessionController is the surface Handler needs from the SessionWatcher.
// It is its own type so tests can supply a fake without dragging in the
// full polling loop.
type SessionController interface {
	Watch(chatID int64, kind domain.AgentKind, sessionID string)
}

// SetNotifier attaches a ChatNotifier (typically the Telegram adapter) that
// the handler can use to push "typing…" indicators while long-running
// operations are in flight. Passing nil disables the indicator.
func (h *Handler) SetNotifier(n domain.ChatNotifier) { h.notifier = n }

// SetSessionController wires the SessionWatcher so the handler can keep it
// pointed at the session the user is currently driving.
func (h *Handler) SetSessionController(s SessionController) { h.watcher = s }

// NewHandler wires the companion bot. The registry hides the per-kind
// transport (HTTP, stdio JSON-RPC, LSP); the manager owns the per-kind
// subprocess lifecycle; the state repository persists the session the
// user is following. workspaceRoot bounds the directories the bot will
// accept when it derives a project path from a session.
func NewHandler(state domain.StateRepository, registry domain.AgentRegistry, manager domain.AgentServerManager, workspaceRoot string) *Handler {
	return &Handler{
		state:         state,
		registry:      registry,
		manager:       manager,
		workspaceRoot: workspaceRoot,
		staleAfter:    defaultStaleAfter,
		now:           time.Now,
	}
}

// SetSessionEventLog wires the completed-session repository so /continue can
// reactivate the last task the watcher recorded.
func (h *Handler) SetSessionEventLog(log domain.SessionEventLog) { h.events = log }

// SetSnapshotPublisher wires the in-process snapshot store so the handler
// can mark notifications as pending whenever a prompt finishes.
func (h *Handler) SetSnapshotPublisher(s domain.SnapshotPublisher) { h.snapshot = s }

// SetCompletionPublisher wires the publisher the handler uses to
// record completions for non-streaming agents (today: Copilot) at
// the moment SendPrompt returns. The same concrete publisher is
// shared with the SessionWatcher so the macOS wrapper sees one
// unified completion stream regardless of which agent produced it.
func (h *Handler) SetCompletionPublisher(p domain.CompletionPublisher) { h.completions = p }

// SetActiveLocators wires the registry of SessionLocators the /continuar
// command fans out to. Passing nil disables the command (it returns the
// "no locator available" message).
func (h *Handler) SetActiveLocators(r domain.ActiveLocatorRegistry) { h.locators = r }

// SetQuestionBroker wires the store of pending agent questions so the
// handler can answer them from Telegram, either through inline buttons
// or a free-form text reply.
func (h *Handler) SetQuestionBroker(q domain.QuestionBroker) { h.questions = q }

// SetStaleAfter overrides the staleness threshold used by /continuar.
// Sessions whose TouchedAt is older than the threshold are filtered out
// so the user is not offered a chat that finished hours ago.
func (h *Handler) SetStaleAfter(d time.Duration) {
	if d > 0 {
		h.staleAfter = d
	}
}

// SetClock replaces the function the handler uses to read the
// current time. Production code keeps the default (time.Now);
// tests inject a fixed clock so the "hace Xs" text and the stale
// filter behave deterministically.
func (h *Handler) SetClock(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// defaultStaleAfter is the freshness window /continuar uses when the
// composition root does not override it. Generous enough to cover a
// lunch break without including abandoned chats from the previous day.
const defaultStaleAfter = 30 * time.Minute

// StateForTest exposes the underlying StateRepository to tests in the same
// package. Production code must depend on the Handler port instead.
func (h *Handler) StateForTest() domain.StateRepository { return h.state }

// activeAdapter resolves the agent currently driving the chat. Falls
// back to the first available agent if the chat has never picked one
// so handlers can keep working right after the multi-agent migration.
func (h *Handler) activeAdapter(chatID int64) (domain.AgentAdapter, domain.AgentKind, error) {
	kind, err := h.registry.Active(chatID)
	if err != nil {
		return nil, "", err
	}
	adapter, err := h.registry.Get(kind)
	if err != nil {
		return nil, "", err
	}
	return adapter, kind, nil
}

func (h *Handler) HandleCommand(ctx context.Context, chatID int64, command string, args []string) (domain.BotResponse, error) {
	switch command {
	case "/start", "/help":
		return helpResponse(), nil
	case "/status":
		return h.status(ctx, chatID)
	case "/continue":
		return h.continueLast(ctx, chatID)
	case "/resume":
		return h.continuar(ctx, chatID, "")
	default:
		return domain.BotResponse{Text: "Comando no reconocido. Usa /help."}, nil
	}
}

func helpResponse() domain.BotResponse {
	return domain.BotResponse{Text: strings.Join([]string{
		"Agentower listo.",
		"",
		"Te aviso por acá cuando el agente termine una tarea o necesite que respondas una pregunta.",
		"",
		"• /status — qué agente y sesión estoy siguiendo.",
		"• /continue — retoma la última tarea completada.",
		"• /resume — detecta la sesión que corre en tu Mac y te ofrece seguirla.",
		"• texto libre — responde a la sesión activa.",
	}, "\n")}
}

const telegramMaxMessageLen = 4096

func (h *Handler) HandleText(ctx context.Context, chatID int64, text string) (domain.BotResponse, error) {
	if resp, handled, err := h.answerPendingQuestionFromText(ctx, chatID, text); handled {
		return resp, err
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if state.SessionID == "" {
		return domain.BotResponse{Text: "Todavía no hay una sesión activa. Toca «▶️ Continuar sesión» en una notificación o usa /continue."}, nil
	}
	adapter, kind, err := h.adapterForSession(chatID, state.AgentKind)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	stopTyping := h.startTypingIndicator(chatID)
	if err := h.ensureAgentStarted(ctx, kind, state); err != nil {
		stopTyping()
		return domain.BotResponse{Text: startAgentError(kind, err)}, nil
	}
	reply, err := adapter.SendPrompt(ctx, state.SessionID, text)
	stopTyping()
	if h.watcher != nil {
		h.watcher.Watch(chatID, kind, state.SessionID)
	}
	publishNonStreamCompletion(h, ctx, kind, chatID, state.SessionID, reply)
	if h.snapshot != nil {
		h.snapshot.SetActive(chatID, state.RelativePath, state.SessionID)
	}
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("%s no pudo responder: %s", kind, err.Error())}, nil
	}
	if reply == "" {
		return domain.BotResponse{Text: "Agentower terminó la respuesta sin texto."}, nil
	}
	return domain.BotResponse{Text: truncateForTelegram(reply, kind)}, nil
}

// adapterForSession resolves the adapter that owns the followed
// session. It prefers the kind persisted with the session over the
// chat's default agent so a continuation from, say, a Kiro completion
// is not misrouted to opencode.
func (h *Handler) adapterForSession(chatID int64, kind domain.AgentKind) (domain.AgentAdapter, domain.AgentKind, error) {
	if kind != "" {
		adapter, err := h.registry.Get(kind)
		if err != nil {
			return nil, kind, err
		}
		return adapter, kind, nil
	}
	return h.activeAdapter(chatID)
}

// ensureAgentStarted makes sure the agent's subprocess (HTTP server for
// opencode, stdio CLI for the rest) is running before the handler tries
// to send a prompt. The managers are lazy: nothing starts them at boot,
// so the first message a user sends after reactivating a session is what
// brings the agent up in the project directory persisted with the
// session. opencode adopts an already-running `opencode serve` when one
// exists instead of spawning a second server.
func (h *Handler) ensureAgentStarted(ctx context.Context, kind domain.AgentKind, state domain.RuntimeState) error {
	if h.manager == nil || kind == "" {
		return nil
	}
	// Serialize starts so two near-simultaneous messages do not race to
	// spawn (or restart) the same agent.
	h.startMu.Lock()
	defer h.startMu.Unlock()
	if h.manager.StartedSubprocess(kind) {
		return nil
	}
	workdir := h.resolveWorkingDir(state)
	if workdir == "" {
		return domain.ErrAgentUnavailable
	}
	return h.manager.Start(ctx, kind, workdir)
}

// resolveWorkingDir picks the directory the agent should run in. It
// prefers the session's project (workspace root + relative path) and
// falls back to the workspace root when the path is empty or no longer
// exists.
func (h *Handler) resolveWorkingDir(state domain.RuntimeState) string {
	root := state.WorkspaceRoot
	if root == "" {
		root = h.workspaceRoot
	}
	if root == "" {
		return ""
	}
	if state.RelativePath != "" {
		candidate := filepath.Join(root, state.RelativePath)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return root
}

// directoryForSession asks the locator registered for the kind for the
// directory of a specific session. It is best-effort: a missing locator,
// a transient error or a different freshest session all yield "".
func (h *Handler) directoryForSession(ctx context.Context, kind domain.AgentKind, sessionID string) string {
	if h.locators == nil || kind == "" || sessionID == "" {
		return ""
	}
	loc, ok := h.locators.LocatorFor(kind)
	if !ok {
		return ""
	}
	active, err := loc.Locate(ctx)
	if err != nil || active.SessionID != sessionID {
		return ""
	}
	return active.Directory
}

func startAgentError(kind domain.AgentKind, err error) string {
	if errors.Is(err, domain.ErrAgentUnavailable) {
		return fmt.Sprintf("No pude iniciar %s: no encontré un directorio de proyecto válido para esa sesión.", kind)
	}
	return fmt.Sprintf("No pude iniciar %s: %s", kind, err)
}

// startTypingIndicator fires a "typing…" chat action now and keeps
// refreshing it until the returned cancel function is called. If no notifier
// is configured it is a no-op that returns a no-op cancel.
func (h *Handler) startTypingIndicator(chatID int64) (cancel func()) {
	if h.notifier == nil || chatID == 0 {
		return func() {}
	}
	ctx, cancelCtx := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Fire one immediately so the user sees the indicator without delay.
		_ = h.notifier.NotifyTyping(ctx, chatID)
		ticker := time.NewTicker(typingRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = h.notifier.NotifyTyping(ctx, chatID)
			}
		}
	}()
	return func() {
		cancelCtx()
		wg.Wait()
	}
}

func truncateForTelegram(text string, kind domain.AgentKind) string {
	if len(text) <= telegramMaxMessageLen {
		return text
	}
	return text[:telegramMaxMessageLen] + "\n\n... (truncado, sigue en " + string(kind) + ")"
}

func (h *Handler) HandleCallback(ctx context.Context, chatID int64, data string) (domain.BotResponse, error) {
	parts := strings.Split(data, "|")
	if len(parts) < 2 {
		return expiredNavigation(), nil
	}
	// "co" and "cd" are quick-tap callbacks from the asynchronous
	// completion notification. They carry the chat id as the second
	// segment so the handler can confirm the tap originated from the
	// whitelisted chat.
	if parts[0] == "co" || parts[0] == "cd" {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		if parts[0] == "co" {
			return h.continueLast(ctx, chatID)
		}
		return h.diffForCompleted(ctx, chatID)
	}
	// "rs" is the /resume callback family: rs|<chatID> confirms
	// the freshest active session, rs|<chatID>|<kind> narrows the
	// picker to a different agent's session before confirming.
	if parts[0] == "rs" {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		kind := ""
		if len(parts) == 3 {
			kind = parts[2]
		}
		return h.continuar(ctx, chatID, kind)
	}
	// "rsc" is the /resume confirm action. The kind and
	// sessionID are carried in the callback so the handler does
	// not need a per-chat cache to act on a tap.
	if parts[0] == "rsc" {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		if len(parts) != 4 {
			return expiredNavigation(), nil
		}
		return h.confirmActiveSession(ctx, chatID, parts[2], parts[3])
	}
	// "q" and "qd" are the pending-question callbacks. "q" carries the
	// question and option indices; "qd" finalizes a multi-select prompt.
	if parts[0] == "q" || parts[0] == "qd" {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		if parts[0] == "q" {
			if len(parts) != 4 {
				return expiredNavigation(), nil
			}
			qIdx, errQ := strconv.Atoi(parts[2])
			optIdx, errO := strconv.Atoi(parts[3])
			if errQ != nil || errO != nil {
				return expiredNavigation(), nil
			}
			return h.answerQuestionOption(ctx, chatID, qIdx, optIdx)
		}
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		qIdx, err := strconv.Atoi(parts[2])
		if err != nil {
			return expiredNavigation(), nil
		}
		return h.settleQuestion(ctx, chatID, qIdx)
	}
	switch parts[0] {
	default:
		return expiredNavigation(), nil
	}
}

// answerQuestionOption handles a tap on one of a question's option
// buttons. Single-select prompts are answered and finalized in one tap;
// multi-select prompts toggle the option until "Listo" is tapped.
func (h *Handler) answerQuestionOption(ctx context.Context, chatID int64, qIdx, optIdx int) (domain.BotResponse, error) {
	if h.questions == nil {
		return expiredNavigation(), nil
	}
	pending, ok := h.questions.PendingQuestion(chatID)
	if !ok || qIdx < 0 || qIdx >= len(pending.Questions) {
		return domain.BotResponse{Text: "Esa pregunta ya no está disponible.", Edit: true}, nil
	}
	prompt := pending.Questions[qIdx]
	if optIdx < 0 || optIdx >= len(prompt.Options) {
		return expiredNavigation(), nil
	}
	label := prompt.Options[optIdx].Label
	if prompt.Multiple {
		updated, ready := h.questions.Answer(chatID, pending.RequestID, qIdx, toggleAnswer(pending.Answers[qIdx], label), false)
		if ready {
			return h.submitAnswers(ctx, chatID, updated)
		}
		resp := questionRender(chatID, updated, qIdx)
		resp.Edit = true
		return resp, nil
	}
	updated, ready := h.questions.Answer(chatID, pending.RequestID, qIdx, []string{label}, true)
	if ready {
		return h.submitAnswers(ctx, chatID, updated)
	}
	resp := questionAnsweredResponse(updated, qIdx, label)
	resp.Edit = true
	return resp, nil
}

// settleQuestion finalizes a multi-select prompt whose options were
// already toggled.
func (h *Handler) settleQuestion(ctx context.Context, chatID int64, qIdx int) (domain.BotResponse, error) {
	if h.questions == nil {
		return expiredNavigation(), nil
	}
	pending, ok := h.questions.PendingQuestion(chatID)
	if !ok || qIdx < 0 || qIdx >= len(pending.Questions) {
		return domain.BotResponse{Text: "Esa pregunta ya no está disponible.", Edit: true}, nil
	}
	values := pending.Answers[qIdx]
	if len(values) == 0 && !pending.Questions[qIdx].Custom {
		resp := questionRender(chatID, pending, qIdx)
		resp.Edit = true
		return resp, nil
	}
	updated, ready := h.questions.Answer(chatID, pending.RequestID, qIdx, values, true)
	if ready {
		return h.submitAnswers(ctx, chatID, updated)
	}
	resp := questionAnsweredResponse(updated, qIdx, strings.Join(values, ", "))
	resp.Edit = true
	return resp, nil
}

// answerPendingQuestionFromText routes a free-form Telegram message to a
// pending question instead of the model. This is how the user types a
// custom answer. Returns handled=true when there was a question to answer.
func (h *Handler) answerPendingQuestionFromText(ctx context.Context, chatID int64, text string) (domain.BotResponse, bool, error) {
	if h.questions == nil {
		return domain.BotResponse{}, false, nil
	}
	pending, ok := h.questions.PendingQuestion(chatID)
	if !ok || len(pending.Questions) == 0 {
		return domain.BotResponse{}, false, nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return domain.BotResponse{Text: "Estoy esperando tu respuesta a la pregunta del agente."}, true, nil
	}
	idx := firstUnsettled(pending.Settled)
	if idx < 0 {
		idx = 0
	}
	values := []string{trimmed}
	if pending.Questions[idx].Multiple {
		values = splitAnswers(trimmed)
	}
	updated, ready := h.questions.Answer(chatID, pending.RequestID, idx, values, true)
	if ready {
		resp, err := h.submitAnswers(ctx, chatID, updated)
		return resp, true, err
	}
	return questionRender(chatID, updated, idx), true, nil
}

// submitAnswers sends the collected answers back to the agent and clears
// the pending question so normal completion tracking resumes.
func (h *Handler) submitAnswers(ctx context.Context, chatID int64, pending domain.PendingQuestion) (domain.BotResponse, error) {
	kind := pending.AgentKind
	if kind == "" {
		kind, _ = h.registry.Active(chatID)
	}
	adapter, err := h.registry.Get(kind)
	if err != nil {
		return domain.BotResponse{Text: "No pude resolver el agente que preguntó: " + err.Error(), Edit: true}, nil
	}
	qa, ok := adapter.(domain.QuestionAdapter)
	if !ok {
		return domain.BotResponse{Text: "Ese agente no admite respuestas a preguntas.", Edit: true}, nil
	}
	if state, err := h.state.LoadRuntimeState(ctx); err == nil {
		if startErr := h.ensureAgentStarted(ctx, kind, state); startErr != nil {
			return domain.BotResponse{Text: startAgentError(kind, startErr), Edit: true}, nil
		}
	}
	if err := qa.ReplyQuestion(ctx, pending.SessionID, pending.RequestID, pending.Answers); err != nil {
		return domain.BotResponse{Text: "No pude enviar tus respuestas al agente: " + err.Error(), Edit: true}, nil
	}
	h.questions.ClearPendingQuestion(chatID)
	return domain.BotResponse{Text: "✅ Listo, envié tus respuestas al agente. Te aviso cuando termine.", Edit: true}, nil
}

// questionRender re-renders a question with its current selections so
// multi-select prompts can show check marks while the user picks.
func questionRender(chatID int64, pending domain.PendingQuestion, index int) domain.BotResponse {
	if index < 0 || index >= len(pending.Questions) {
		return domain.BotResponse{Text: "Pregunta no disponible."}
	}
	prompt := pending.Questions[index]
	selected := map[string]bool{}
	if index < len(pending.Answers) {
		for _, v := range pending.Answers[index] {
			selected[v] = true
		}
	}
	var b strings.Builder
	b.WriteString("⏸️ El agente " + string(pending.AgentKind) + " necesita tu respuesta\n\n")
	if len(pending.Questions) > 1 {
		fmt.Fprintf(&b, "Pregunta %d/%d\n", index+1, len(pending.Questions))
	}
	if prompt.Header != "" {
		b.WriteString("**" + prompt.Header + "**\n")
	}
	b.WriteString(prompt.Question)
	for i, opt := range prompt.Options {
		marker := ""
		if selected[opt.Label] {
			marker = " ✅"
		}
		fmt.Fprintf(&b, "\n\n%d. %s%s", i+1, opt.Label, marker)
		if opt.Description != "" {
			b.WriteString(" — " + opt.Description)
		}
	}
	if prompt.Custom {
		b.WriteString("\n\n✍️ También puedes responder escribiendo el texto.")
	}
	resp := domain.BotResponse{Text: b.String()}
	for i, opt := range prompt.Options {
		label := opt.Label
		if selected[opt.Label] {
			label = "✅ " + label
		}
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: label,
			Data: fmt.Sprintf("q|%d|%d|%d", chatID, index, i),
		}})
	}
	if prompt.Multiple {
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: "✅ Listo",
			Data: fmt.Sprintf("qd|%d|%d", chatID, index),
		}})
	}
	return resp
}

// questionAnsweredResponse renders the confirmation shown after a
// single-select prompt has been answered, mentioning any questions still
// waiting.
func questionAnsweredResponse(pending domain.PendingQuestion, index int, answer string) domain.BotResponse {
	header := pending.Questions[index].Header
	if header == "" {
		header = fmt.Sprintf("Pregunta %d/%d", index+1, len(pending.Questions))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✅ %s: %s\n", header, answer)
	if remaining := unsettledCount(pending.Settled); remaining > 1 {
		fmt.Fprintf(&b, "\nQuedan %d preguntas por responder.", remaining-1)
	}
	return domain.BotResponse{Text: b.String()}
}

func toggleAnswer(current []string, value string) []string {
	out := make([]string, 0, len(current)+1)
	found := false
	for _, v := range current {
		if v == value {
			found = true
			continue
		}
		out = append(out, v)
	}
	if !found {
		out = append(out, value)
	}
	return out
}

func splitAnswers(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return out
}

func firstUnsettled(settled []bool) int {
	for i, ok := range settled {
		if !ok {
			return i
		}
	}
	return -1
}

func unsettledCount(settled []bool) int {
	n := 0
	for _, ok := range settled {
		if !ok {
			n++
		}
	}
	return n
}

func relativeUnderWorkspace(workspaceRoot, absolutePath string) string {
	rel, err := filepath.Rel(workspaceRoot, filepath.Clean(absolutePath))
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return ""
	}
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (h *Handler) status(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	agent := state.AgentKind
	if agent == "" {
		agent, _ = h.registry.Active(chatID)
	}
	if agent == "" {
		agent = domain.AgentOpenCode
	}
	project := orDefault(state.RelativePath, "ninguno")
	if state.SessionID == "" {
		return domain.BotResponse{Text: fmt.Sprintf("Agente: %s\nProyecto: %s\nSesión: ninguna\n\nToca «▶️ Continuar sesión» en una notificación o usa /continue.", agent, project)}, nil
	}
	session := truncateID(state.SessionID)
	adapter, err := h.registry.Get(agent)
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("Agente: %s\nProyecto: %s\nSesión: %s", agent, project, session)}, nil
	}
	health, err := adapter.Health(ctx)
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("Agente: %s\nProyecto: %s\nSesión: %s\n\nEl agente no responde ahora mismo.", agent, project, session)}, nil
	}
	return domain.BotResponse{Text: fmt.Sprintf("Agente: %s\nSalud: %t (%s)\nCwd: %s\nProyecto: %s\nSesión: %s",
		agent, health.Healthy, health.Version, h.manager.WorkingDir(agent), project, session)}, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// diffForCompleted is the quick-tap variant invoked from the asynchronous
// "task done" notification. It uses the session id from the latest
// completed snapshot so users can inspect changes even before tapping
// "Continuar sesión".
func (h *Handler) diffForCompleted(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	if h.events == nil {
		return domain.BotResponse{Text: "No hay historial de sesiones completadas todavía."}, nil
	}
	snapshot, ok, err := h.events.LoadCompletedSession(ctx, chatID)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if !ok || snapshot.SessionID == "" {
		return domain.BotResponse{Text: "Aún no registramos ninguna tarea completada."}, nil
	}
	adapter, err := h.adapterForCompleted(chatID, snapshot.AgentKind)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	return h.diffSession(ctx, adapter, snapshot.SessionID)
}

// adapterForCompleted resolves the adapter that produced a completed
// session, falling back to the chat's active agent when the snapshot
// does not carry a kind.
func (h *Handler) adapterForCompleted(chatID int64, kind domain.AgentKind) (domain.AgentAdapter, error) {
	if kind != "" {
		if adapter, err := h.registry.Get(kind); err == nil {
			return adapter, nil
		}
	}
	adapter, _, err := h.activeAdapter(chatID)
	return adapter, err
}

// diffSession fetches the file status of a specific session and renders
// it. Backs the "Ver cambios" button on the completion notification.
// notification.
func (h *Handler) diffSession(ctx context.Context, adapter domain.AgentAdapter, sessionID string) (domain.BotResponse, error) {
	changes, err := adapter.FileStatus(ctx, sessionID)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if len(changes) == 0 {
		return domain.BotResponse{Text: "No hay cambios."}, nil
	}
	var text strings.Builder
	for _, change := range changes {
		text.WriteString(change.Status + " `" + change.Path + "`\n")
	}
	return domain.BotResponse{Text: text.String()}, nil
}

func (h *Handler) continueLast(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	if h.events == nil {
		return domain.BotResponse{Text: "No hay historial de sesiones completadas todavía."}, nil
	}
	snapshot, ok, err := h.events.LoadCompletedSession(ctx, chatID)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if !ok || snapshot.SessionID == "" {
		return domain.BotResponse{Text: "Aún no registramos ninguna tarea completada. Espera a que el agente termine la próxima."}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	state.SessionID = snapshot.SessionID
	if snapshot.AgentKind != "" {
		// Route the continuation to the agent that produced the
		// completed session (e.g. Copilot or Kiro, not just the
		// currently-active opencode).
		state.AgentKind = snapshot.AgentKind
	}
	if snapshot.ProjectID != "" {
		state.ProjectID = snapshot.ProjectID
	}
	directory := snapshot.Directory
	if directory == "" {
		// The opencode/Kiro adapters cannot always list sessions while
		// their manager is stopped (opencode needs the HTTP server, Kiro
		// needs the ACP subprocess), so the stored snapshot may lack the
		// directory. Ask the locator — which reads on-disk state — so the
		// agent is restarted in the right project.
		directory = h.directoryForSession(ctx, state.AgentKind, snapshot.SessionID)
	}
	if directory != "" {
		state.WorkspaceRoot = h.workspaceRoot
		state.RelativePath = relativeUnderWorkspace(h.workspaceRoot, directory)
	}
	if err := h.state.SaveRuntimeState(ctx, state); err != nil {
		return domain.BotResponse{}, err
	}
	preview := snapshot.Preview
	if preview == "" {
		preview = "Sesión sin previsualización."
	}
	return domain.BotResponse{
		Text: fmt.Sprintf("Sesión `%s` reactivada.\nProyecto: `%s`\n%s",
			snapshot.SessionID, orDefault(snapshot.ProjectName, snapshot.Directory), preview),
	}, nil
}

// continuar is the /continuar handler. It asks every registered
// SessionLocator for the active session on the machine, filters out
// stale and missing results, and renders either a single suggestion
// or a small picker so the user can choose which session to bring
// into Telegram.
//
// When called via the rs|<chatID> or rs|<chatID>|<kind> callback the
// same flow runs, optionally narrowed to a single kind so the user
// can drill into a specific agent's session without retyping the
// command.
//
// The confirm action lives in its own callback
// (rsc|<chatID>|<kind>|<sessionID>) so the user can review the card
// for a few seconds before tapping without the locator being
// re-queried in the background.
func (h *Handler) continuar(ctx context.Context, chatID int64, narrow string) (domain.BotResponse, error) {
	if h.locators == nil {
		return domain.BotResponse{Text: "No hay locators configurados. Reconstruye el bot con al menos un agente detectado."}, nil
	}
	locators := h.locators.Locators()
	if len(locators) == 0 {
		return domain.BotResponse{Text: "No hay agentes con locator activo. Instalá opencode, Claude Code, GitHub Copilot, Codex o Antigravity."}, nil
	}
	if narrow != "" {
		filtered := locators[:0]
		for _, l := range locators {
			if string(l.Kind()) == narrow {
				filtered = append(filtered, l)
			}
		}
		locators = filtered
		if len(locators) == 0 {
			return domain.BotResponse{Text: fmt.Sprintf("No hay locator para `%s`.", narrow)}, nil
		}
	}
	active, err := h.locateAll(ctx, locators)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if len(active) == 0 {
		return domain.BotResponse{Text: "No detecté sesiones activas en tu Mac. Asegurate de tener opencode, Claude Code, Kiro, GitHub Copilot, Codex o Antigravity ejecutándose."}, nil
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].TouchedAt.After(active[j].TouchedAt)
	})
	primary := active[0]
	alternatives := active[1:]

	preview := primary.Preview
	if preview == "" {
		preview = "Sin previsualización."
	}
	var b strings.Builder
	b.WriteString("Sesión activa en tu Mac:\n\n")
	fmt.Fprintf(&b, "• Agente: `%s`\n", primary.Kind)
	fmt.Fprintf(&b, "• Proyecto: `%s`\n", orDefault(primary.Project, primary.Directory))
	fmt.Fprintf(&b, "• Sesión: `%s`\n", truncateID(primary.SessionID))
	if !primary.TouchedAt.IsZero() {
		fmt.Fprintf(&b, "• Última actividad: hace %s\n\n", humanizeSince(primary.TouchedAt, h.handlerNow()))
	} else {
		b.WriteString("\n")
	}
	b.WriteString(preview)
	b.WriteString("\n\n¿Querés continuar desde acá?")
	resp := domain.BotResponse{Text: b.String()}
	resp.Buttons = append(resp.Buttons, []domain.BotButton{
		{Text: "✅ Sí, continuar", Data: h.confirmCallback(chatID, primary)},
	})
	if preview != "Sin previsualización." && preview != "" {
		resp.Buttons = append(resp.Buttons, []domain.BotButton{
			{Text: "📝 Ver preview", Data: h.previewCallback(chatID, primary)},
		})
	}
	if len(alternatives) > 0 {
		var row []domain.BotButton
		for _, alt := range alternatives {
			label := fmt.Sprintf("🔁 %s · %s", alt.Kind, orDefault(alt.Project, alt.Directory))
			row = append(row, domain.BotButton{Text: label, Data: fmt.Sprintf("rs|%d|%s", chatID, alt.Kind)})
		}
		resp.Buttons = append(resp.Buttons, row)
	}
	resp.Buttons = append(resp.Buttons, []domain.BotButton{{Text: "❌ Cancelar", Data: "noop"}})
	return resp, nil
}

// locateAll fans out to every locator in parallel with a 3-second
// deadline. Locators that return ErrNoActiveSession or stale entries
// are silently filtered; the caller gets the surviving set sorted
// by freshness (the caller is responsible for the final sort so it
// can interleave additional logic).
func (h *Handler) locateAll(ctx context.Context, locators []domain.SessionLocator) ([]domain.ActiveSession, error) {
	type result struct {
		session domain.ActiveSession
		err     error
	}
	ch := make(chan result, len(locators))
	locCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, loc := range locators {
		loc := loc
		go func() {
			sess, err := loc.Locate(locCtx)
			ch <- result{session: sess, err: err}
		}()
	}
	now := h.handlerNow()
	out := make([]domain.ActiveSession, 0, len(locators))
	for range locators {
		r := <-ch
		if r.err != nil {
			// ErrNoActiveSession and any other error mean the
			// locator is not contributing this round; /continuar
			// never fails because one agent is unreachable.
			continue
		}
		if !r.session.TouchedAt.IsZero() && now.Sub(r.session.TouchedAt) > h.staleAfter {
			continue
		}
		out = append(out, r.session)
	}
	return out, nil
}

// confirmCallback encodes the "yes, switch to this session" button
// for a single active session. The session id is included so the
// callback stays valid even if the locator later returns a
// different "freshest" answer.
func (h *Handler) confirmCallback(chatID int64, sess domain.ActiveSession) string {
	return fmt.Sprintf("rsc|%d|%s|%s", chatID, sess.Kind, sess.SessionID)
}

// previewCallback encodes the "show me the full preview" button.
// We reuse the rs| family because the preview is just another
// /resume render with the chosen kind focused.
func (h *Handler) previewCallback(chatID int64, sess domain.ActiveSession) string {
	return fmt.Sprintf("rs|%d|%s", chatID, sess.Kind)
}

// now is the time source /continuar uses for staleness and the
// "hace Xs" rendering. Defaults to time.Now; SetClock swaps it in
// tests.
func (h *Handler) handlerNow() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// confirmActiveSession is the rsc| callback handler. It receives the
// kind+sessionID encoded in the button data, re-validates the
// session via the matching locator (best-effort; a failure leaves
// the state change standing because the user has already tapped),
// and switches the chat's runtime state to that session.
func (h *Handler) confirmActiveSession(ctx context.Context, chatID int64, kindRaw, sessionID string) (domain.BotResponse, error) {
	agentKind := domain.AgentKind(kindRaw)
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	state.AgentKind = agentKind
	state.SessionID = sessionID
	state.ProjectID = ""
	state.WorkspaceRoot = h.workspaceRoot
	// Best-effort: ask the locator for the directory so the
	// state can remember which project the session is bound to.
	// If the locator is gone, the session still works; only the
	// project label is left blank.
	if h.locators != nil {
		if loc, ok := h.locators.LocatorFor(agentKind); ok {
			if active, locErr := loc.Locate(ctx); locErr == nil {
				if active.SessionID == sessionID {
					state.RelativePath = relativeUnderWorkspace(h.workspaceRoot, active.Directory)
				}
			}
		}
	}
	if err := h.state.SaveRuntimeState(ctx, state); err != nil {
		return domain.BotResponse{}, err
	}
	if h.registry != nil {
		_ = h.registry.SetActive(chatID, agentKind)
	}
	label := string(agentKind)
	if label == "" {
		label = "el agente"
	}
	return domain.BotResponse{
		Text: fmt.Sprintf("Listo: cambiaste a la sesión `%s` en `%s`. Enviame el próximo prompt y lo mando a esa sesión.", truncateID(sessionID), label),
	}, nil
}

func truncateID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}

func humanizeSince(t, now time.Time) string {
	if t.IsZero() {
		return "desconocido"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// publishNonStreamCompletion records a CompletedSession for agents
// that do not stream message history (today: GitHub Copilot via
// LSP). For every other agent the SessionWatcher handles the
// completion asynchronously so this is a no-op; we still call it
// unconditionally to keep the handler uniform.
func publishNonStreamCompletion(h *Handler, ctx context.Context, kind domain.AgentKind, chatID int64, sessionID, reply string) {
	if h.completions == nil {
		return
	}
	if !isInlineCompletionAgent(kind) {
		return
	}
	if reply == "" {
		// An empty reply means the LSP did not produce a
		// completion (e.g. Copilot had no suggestion). We do not
		// publish so the user is not paged for nothing.
		return
	}
	preview := reply
	if len(preview) > 240 {
		preview = preview[:240] + "…"
	}
	projectName := ""
	directory := ""
	if h.state != nil {
		if state, err := h.state.LoadRuntimeState(ctx); err == nil {
			directory = state.WorkspaceRoot
			projectName = filepath.Base(state.WorkspaceRoot)
		}
	}
	snapshot := domain.CompletedSession{
		ChatID:      chatID,
		SessionID:   sessionID,
		AgentKind:   kind,
		Directory:   directory,
		ProjectName: projectName,
		Preview:     preview,
		CompletedAt: h.handlerNow().UTC(),
	}
	if h.events != nil {
		// Best-effort: persist so /continue and /continuar
		// can rehydrate even if the macOS wrapper missed the
		// notification.
		_ = h.events.SaveCompletedSession(ctx, snapshot)
	}
	h.completions.PublishCompletion(snapshot)
}

// isInlineCompletionAgent reports whether the agent's transport
// delivers the response in a single inline reply (no streaming
// message log). Today only GitHub Copilot fits: its LSP returns
// `insertText` on the inlineCompletion call, so there is no
// message history to tail. The check is kept as a switch so future
// agents slot in without touching the call sites.
func isInlineCompletionAgent(kind domain.AgentKind) bool {
	switch kind {
	case domain.AgentCopilot:
		return true
	}
	return false
}

func agentUnavailableResponse(err error) domain.BotResponse {
	if errors.Is(err, domain.ErrNoActiveAgent) {
		return domain.BotResponse{Text: "Todavía no hay un agente activo. Esperá a la próxima notificación o usa /resume."}
	}
	if errors.Is(err, domain.ErrAgentUnavailable) {
		return domain.BotResponse{Text: "El agente de esta sesión no está disponible ahora mismo."}
	}
	return domain.BotResponse{Text: "No se pudo resolver el agente activo: " + err.Error()}
}

func expiredNavigation() domain.BotResponse {
	return domain.BotResponse{Text: "Ese botón ya expiró. Usa /help para ver las opciones.", Edit: true}
}
