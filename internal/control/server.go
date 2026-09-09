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
		ChatID:        p.chatID,
		ActiveProject: p.activeProj,
		ActiveSession: p.activeSess,
		LastCompleted: cloneCompleted(p.last),
	}
	if len(p.pendingChats) > 0 {
		for chatID := range p.pendingChats {
			snap.PendingNotifChat = chatID
			break
		}
	}
	return snap
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
	writeJSON(w, http.StatusOK, snap)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"chat_id":      body.ChatID,
		"session_id":   snap.LastCompleted.SessionID,
		"project_name": snap.LastCompleted.ProjectName,
		"preview":      snap.LastCompleted.Preview,
		"completed_at": snap.LastCompleted.CompletedAt,
	})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func cloneCompleted(src *domain.CompletedSession) *domain.CompletedSession {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}
