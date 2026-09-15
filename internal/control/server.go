package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Publisher is the in-process store the HTTP control surface serves. The
// SessionWatcher writes to it, the handler reads from it, and the macOS
// wrapper queries /state via this server.
type Publisher struct {
	mu           sync.RWMutex
	chatID       int64
	activeProj   string
	activeSess   string
	last         *domain.CompletedSession
	question     *domain.PendingQuestion
	pendingChats map[int64]struct{}
}

// NewPublisher builds an empty publisher.
func NewPublisher() *Publisher {
	return &Publisher{pendingChats: map[int64]struct{}{}}
}

// SetActive updates the active project / session labels exposed in /state.
func (p *Publisher) SetActive(chatID int64, project, session string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chatID = chatID
	p.activeProj = project
	p.activeSess = session
}

// PublishCompletion is called by the SessionWatcher when it records a new
// completion snapshot.
func (p *Publisher) PublishCompletion(snap domain.CompletedSession) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil || snap.SessionID != p.last.SessionID || snap.CompletedAt.After(p.last.CompletedAt) {
		copy := snap
		p.last = &copy
	}
}

// Snapshot returns a copy of the publisher's current state.
func (p *Publisher) Snapshot() domain.Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := domain.Snapshot{
		ChatID:          p.chatID,
		ActiveProject:   p.activeProj,
		ActiveSession:   p.activeSess,
		LastCompleted:   cloneCompleted(p.last),
		PendingQuestion: clonePendingQuestion(p.question),
	}
	if len(p.pendingChats) > 0 {
		for chatID := range p.pendingChats {
			snap.PendingNotifChat = chatID
			break
		}
	}
	return snap
}

// SetPendingQuestion publishes the question an agent is blocked on for a
// chat. When the same request is already stored it preserves the user's
// partial answers and the notified flag so the watcher can call this on
// every tick without losing progress.
func (p *Publisher) SetPendingQuestion(q domain.PendingQuestion) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.question != nil && p.question.ChatID == q.ChatID && p.question.RequestID == q.RequestID {
		q.Answers = p.question.Answers
		q.Settled = p.question.Settled
		q.NotifiedAt = p.question.NotifiedAt
		if !p.question.AskedAt.IsZero() {
			q.AskedAt = p.question.AskedAt
		}
	}
	if q.AskedAt.IsZero() {
		q.AskedAt = time.Now().UTC()
	}
	q.Answers = ensureAnswers(q.Answers, len(q.Questions))
	q.Settled = ensureSettled(q.Settled, len(q.Questions))
	stored := clonePendingQuestion(&q)
	p.question = stored
}

// PendingQuestion returns the question currently blocking the chat.
func (p *Publisher) PendingQuestion(chatID int64) (domain.PendingQuestion, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.question == nil || p.question.ChatID != chatID {
		return domain.PendingQuestion{}, false
	}
	return *clonePendingQuestion(p.question), true
}

// Answer stores the selection for a single question. settle marks the
// prompt finalized; ready reports whether every prompt is settled and the
// request can be sent back to the agent.
func (p *Publisher) Answer(chatID int64, requestID string, index int, values []string, settle bool) (domain.PendingQuestion, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.question
	if q == nil || q.ChatID != chatID || (requestID != "" && q.RequestID != requestID) {
		return domain.PendingQuestion{}, false
	}
	if index < 0 || index >= len(q.Questions) {
		return *clonePendingQuestion(q), false
	}
	q.Answers = ensureAnswers(q.Answers, len(q.Questions))
	q.Settled = ensureSettled(q.Settled, len(q.Questions))
	q.Answers[index] = append([]string(nil), values...)
	q.Settled[index] = settle
	return *clonePendingQuestion(q), settle && allSettled(q.Settled)
}

// ClearPendingQuestion drops the pending question for the chat (the
// agent resolved it, usually because the user answered locally).
func (p *Publisher) ClearPendingQuestion(chatID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.question != nil && p.question.ChatID == chatID {
		p.question = nil
	}
}

// MarkQuestionNotified records that the Telegram question prompt was
// sent for the request so the idle loop does not repeat it.
func (p *Publisher) MarkQuestionNotified(chatID int64, requestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.question != nil && p.question.ChatID == chatID && (requestID == "" || p.question.RequestID == requestID) {
		p.question.NotifiedAt = time.Now().UTC()
	}
}

func ensureAnswers(answers [][]string, n int) [][]string {
	for len(answers) < n {
		answers = append(answers, nil)
	}
	if len(answers) > n {
		answers = answers[:n]
	}
	return answers
}

func ensureSettled(settled []bool, n int) []bool {
	for len(settled) < n {
		settled = append(settled, false)
	}
	if len(settled) > n {
		settled = settled[:n]
	}
	return settled
}

func allSettled(settled []bool) bool {
	for _, ok := range settled {
		if !ok {
			return false
		}
	}
	return len(settled) > 0
}

// RequestNotification records that the macOS wrapper detected an
// idle+completed condition and the bot should send a Telegram message.
func (p *Publisher) RequestNotification(chatID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pendingChats[chatID] = struct{}{}
}

// CancelPending clears any pending notification for the chat.
func (p *Publisher) CancelPending(chatID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pendingChats, chatID)
}

// ConsumePending returns whether a notification is pending for the chat and
// clears it so it doesn't fire twice.
func (p *Publisher) ConsumePending(chatID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.pendingChats[chatID]
	delete(p.pendingChats, chatID)
	return ok
}

// Server hosts the local HTTP control surface. The macOS wrapper talks to
// it over 127.0.0.1, so it's safe to bind without TLS.
type Server struct {
	publisher *Publisher
	log       domain.SessionEventLog
	snapshot  domain.SnapshotPublisher
	notifier  domain.ChatNotifier
	logger    *slog.Logger
	listener  net.Listener
	srv       *http.Server
}

// NewServer builds the control surface.
func NewServer(publisher *Publisher, log domain.SessionEventLog, snapshot domain.SnapshotPublisher, notifier domain.ChatNotifier, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		publisher: publisher,
		log:       log,
		snapshot:  snapshot,
		notifier:  notifier,
		logger:    logger,
	}
	return s
}

// Start binds the listener on 127.0.0.1 and serves until ctx is cancelled
// or the listener is closed. addr may be "host:port" or ":port".
func (s *Server) Start(ctx context.Context, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("control server listen %s: %w", addr, err)
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/state", s.handleState)
	mux.HandleFunc("/notify", s.handleNotify)
	mux.HandleFunc("/question-notify", s.handleQuestionNotify)
	mux.HandleFunc("/cancel", s.handleCancel)
	mux.HandleFunc("/health", s.handleHealth)
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := s.srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("control server stopped", "err", err)
		}
	}()
	return nil
}

// Addr returns the bound address (host:port). Useful for tests and for
// printing the chosen ephemeral port.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Close stops the server.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.snapshot.Snapshot()
	writeJSON(w, snap)
}

// notifyRequest is the body for POST /notify.
type notifyRequest struct {
	ChatID int64 `json:"chat_id"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ChatID == 0 {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	if !s.publisher.ConsumePending(body.ChatID) {
		http.Error(w, "no pending notification", http.StatusConflict)
		return
	}
	snap := s.publisher.Snapshot()
	if snap.LastCompleted == nil {
		http.Error(w, "no completed session recorded", http.StatusNotFound)
		return
	}
	// Mark notified now so a second macOS tick doesn't re-fire.
	if err := s.log.MarkNotified(r.Context(), body.ChatID, time.Now().UTC()); err != nil {
		s.logger.Warn("mark notified failed", "err", err, "chat_id", body.ChatID)
	}
	if s.notifier != nil {
		resp := buildCompletionResponse(body.ChatID, snap.LastCompleted)
		if err := s.notifier.SendResponse(r.Context(), body.ChatID, resp); err != nil {
			s.logger.Warn("telegram notification send failed", "err", err, "chat_id", body.ChatID)
		}
	}
	writeJSON(w, map[string]any{
		"chat_id":      body.ChatID,
		"session_id":   snap.LastCompleted.SessionID,
		"project_name": snap.LastCompleted.ProjectName,
		"preview":      snap.LastCompleted.Preview,
		"completed_at": snap.LastCompleted.CompletedAt,
	})
}

// questionNotifyRequest is the body for POST /question-notify.
type questionNotifyRequest struct {
	ChatID    int64  `json:"chat_id"`
	RequestID string `json:"request_id"`
}

// handleQuestionNotify is the idle-triggered path that pushes a pending
// agent question to Telegram with tappable options. It is fired by the
// macOS wrapper once the local user has been away long enough.
func (s *Server) handleQuestionNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body questionNotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ChatID == 0 {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	question, ok := s.publisher.PendingQuestion(body.ChatID)
	if !ok {
		http.Error(w, "no pending question", http.StatusConflict)
		return
	}
	if body.RequestID != "" && question.RequestID != body.RequestID {
		http.Error(w, "stale request", http.StatusConflict)
		return
	}
	if !question.NotifiedAt.IsZero() {
		http.Error(w, "already notified", http.StatusConflict)
		return
	}
	if s.notifier != nil {
		for i := range question.Questions {
			resp := buildQuestionResponse(body.ChatID, question, i)
			if err := s.notifier.SendResponse(r.Context(), body.ChatID, resp); err != nil {
				s.logger.Warn("telegram question send failed", "err", err, "chat_id", body.ChatID)
			}
		}
	}
	s.publisher.MarkQuestionNotified(body.ChatID, question.RequestID)
	writeJSON(w, map[string]any{
		"chat_id":    body.ChatID,
		"request_id": question.RequestID,
		"questions":  len(question.Questions),
	})
}

// buildQuestionResponse renders one question of a pending request as a
// Telegram message with one inline button per option.
func buildQuestionResponse(chatID int64, question domain.PendingQuestion, index int) domain.BotResponse {
	if index < 0 || index >= len(question.Questions) {
		return domain.BotResponse{Text: "Pregunta no disponible."}
	}
	prompt := question.Questions[index]
	var b strings.Builder
	b.WriteString("⏸️ El agente " + string(question.AgentKind) + " necesita tu respuesta\n\n")
	if len(question.Questions) > 1 {
		fmt.Fprintf(&b, "Pregunta %d/%d\n", index+1, len(question.Questions))
	}
	if prompt.Header != "" {
		b.WriteString("**" + prompt.Header + "**\n")
	}
	b.WriteString(prompt.Question)
	for i, opt := range prompt.Options {
		fmt.Fprintf(&b, "\n\n%d. %s", i+1, opt.Label)
		if opt.Description != "" {
			b.WriteString(" — " + opt.Description)
		}
	}
	if prompt.Custom {
		b.WriteString("\n\n✍️ También puedes responder escribiendo el texto.")
	}
	resp := domain.BotResponse{Text: b.String()}
	for i, opt := range prompt.Options {
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: opt.Label,
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

func buildCompletionResponse(chatID int64, snap *domain.CompletedSession) domain.BotResponse {
	if snap == nil {
		return domain.BotResponse{Text: "Tarea completada."}
	}
	project := snap.ProjectName
	if project == "" {
		project = snap.Directory
	}
	preview := snap.Preview
	if preview == "" {
		preview = "Agentower terminó sin previsualización."
	}
	agentLabel := "agente"
	if snap.AgentKind != "" {
		agentLabel = string(snap.AgentKind)
	}
	var b strings.Builder
	b.WriteString("✅ Tarea completada en " + agentLabel + "\n\n")
	if project != "" {
		b.WriteString("Proyecto: `" + project + "`\n")
	}
	b.WriteString("Sesión: `" + snap.SessionID + "`\n\n")
	b.WriteString(preview + "\n\n")
	b.WriteString("Vuelve a la laptop o sigue desde aquí con un tap:")
	return domain.BotResponse{
		Text: b.String(),
		Buttons: [][]domain.BotButton{
			{
				{Text: "▶️ Continuar sesión", Data: "co|" + strconv.FormatInt(chatID, 10)},
				{Text: "📝 Ver cambios", Data: "cd|" + strconv.FormatInt(chatID, 10)},
			},
		},
	}
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chatID := parseChatID(r)
	if chatID == 0 {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	s.snapshot.CancelPending(chatID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

func parseChatID(r *http.Request) int64 {
	raw := r.URL.Query().Get("chat_id")
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func cloneCompleted(src *domain.CompletedSession) *domain.CompletedSession {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}

func clonePendingQuestion(src *domain.PendingQuestion) *domain.PendingQuestion {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Questions = make([]domain.QuestionPrompt, len(src.Questions))
	for i, q := range src.Questions {
		options := make([]domain.QuestionOption, len(q.Options))
		copy(options, q.Options)
		q.Options = options
		cp.Questions[i] = q
	}
	cp.Answers = make([][]string, len(src.Answers))
	for i, a := range src.Answers {
		cp.Answers[i] = append([]string(nil), a...)
	}
	cp.Settled = append([]bool(nil), src.Settled...)
	return &cp
}
