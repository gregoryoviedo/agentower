package usecase

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	navigation    *NavigationService
	state         domain.StateRepository
	registry      domain.AgentRegistry
	manager       domain.AgentServerManager
	browser       *WorkspaceBrowser
	notifier      domain.ChatNotifier
	watcher       SessionController
	events        domain.SessionEventLog
	snapshot      domain.SnapshotPublisher
	workspaceRoot string
}

// SessionController is the surface Handler needs from the SessionWatcher.
// It is its own type so tests can supply a fake without dragging in the
// full polling loop.
type SessionController interface {
	Watch(chatID int64, sessionID string)
}

// SetNotifier attaches a ChatNotifier (typically the Telegram adapter) that
// the handler can use to push "typing…" indicators while long-running
// operations are in flight. Passing nil disables the indicator.
func (h *Handler) SetNotifier(n domain.ChatNotifier) { h.notifier = n }

// SetSessionController wires the SessionWatcher so the handler can keep it
// pointed at the session the user is currently driving.
func (h *Handler) SetSessionController(s SessionController) { h.watcher = s }

// NewHandler wires the multi-agent bot. The registry hides the per-kind
// transport (HTTP, stdio JSON-RPC, LSP); the manager owns the per-kind
// subprocess lifecycle; the state repository persists the active agent
// pick.
func NewHandler(navigation *NavigationService, state domain.StateRepository, registry domain.AgentRegistry, manager domain.AgentServerManager, browser *WorkspaceBrowser) *Handler {
	return &Handler{
		navigation:    navigation,
		state:         state,
		registry:      registry,
		manager:       manager,
		browser:       browser,
		workspaceRoot: browser.Root(),
	}
}

// SetSessionEventLog wires the completed-session repository so /continue can
// reactivate the last task the watcher recorded.
func (h *Handler) SetSessionEventLog(log domain.SessionEventLog) { h.events = log }

// SetSnapshotPublisher wires the in-process snapshot store so the handler
// can mark notifications as pending whenever a prompt finishes.
func (h *Handler) SetSnapshotPublisher(s domain.SnapshotPublisher) { h.snapshot = s }

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
	case "/projects":
		state, entries, err := h.navigation.Start(ctx, chatID)
		if err != nil {
			return domain.BotResponse{}, err
		}
		return directoryResponse(state, entries), nil
	case "/agent":
		return h.agentCommand(ctx, chatID), nil
	case "/agents":
		return h.agentsCommand(ctx, chatID, args)
	case "/init":
		return h.init(ctx, args)
	case "/status":
		return h.status(ctx, chatID)
	case "/sessions":
		return h.sessions(ctx, chatID, args)
	case "/diff", "/changes":
		return h.diff(ctx, chatID)
	case "/undo":
		return h.undo(ctx, chatID)
	case "/continue":
		return h.continueLast(ctx, chatID)
	case "/watch":
		return h.watch(ctx, chatID, args)
	default:
		return domain.BotResponse{Text: "Comando no reconocido. Usa /help."}, nil
	}
}

func helpResponse() domain.BotResponse {
	return domain.BotResponse{Text: strings.Join([]string{
		"Agentower listo.",
		"",
		"• /projects — selecciona la carpeta del proyecto.",
		"• /agent — elige o cambia el agente de IA activo (opencode, Claude, Codex, Kiro, Copilot).",
		"• /agents — lista los agentes detectados y permite habilitarlos.",
		"• /agents migrate — marca sesiones heredadas como opencode (compatibilidad con versiones anteriores).",
		"• /init — rearranca el agente activo en la carpeta activa.",
		"• /sessions — lista o crea sesiones del proyecto y agente activos.",
		"• /status — salud del agente activo y proyecto/sesión activos.",
		"• /diff — archivos modificados por la sesión activa.",
		"• /undo — revierte el último cambio.",
		"• /watch [sesión] — vigila la sesión activa hasta que termine.",
		"• /continue — reactiva la última sesión completada.",
		"• texto libre — prompt directo a la sesión activa.",
	}, "\n")}
}

const telegramMaxMessageLen = 4096

func (h *Handler) HandleText(ctx context.Context, chatID int64, text string) (domain.BotResponse, error) {
	adapter, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init.", kind)}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if state.SessionID == "" {
		return domain.BotResponse{Text: "Selecciona primero una sesión con /sessions."}, nil
	}
	stopTyping := h.startTypingIndicator(chatID)
	reply, err := adapter.SendPrompt(ctx, state.SessionID, text)
	stopTyping()
	if h.watcher != nil {
		h.watcher.Watch(chatID, state.SessionID)
	}
	if h.snapshot != nil {
		h.snapshot.SetActive(chatID, state.RelativePath, state.SessionID)
	}
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("%s no pudo responder: %s", kind, err.Error())}, nil
	}
	if reply == "" {
		return domain.BotResponse{Text: "Agentower terminó la respuesta sin texto (revisa /diff por si hubo cambios silenciosos)."}, nil
	}
	return domain.BotResponse{Text: truncateForTelegram(reply, kind)}, nil
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
	parts := strings.SplitN(data, "|", 3)
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
	switch parts[0] {
	case "e":
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		state, entries, err := h.navigation.Enter(ctx, parts[1], chatID, parts[2])
		if err != nil {
			return navigationError(err), nil
		}
		return directoryResponse(state, entries), nil
	case "b":
		state, entries, err := h.navigation.Back(ctx, parts[1], chatID)
		if err != nil {
			return navigationError(err), nil
		}
		return directoryResponse(state, entries), nil
	case "h":
		state, entries, err := h.navigation.Home(ctx, parts[1], chatID)
		if err != nil {
			return navigationError(err), nil
		}
		return directoryResponse(state, entries), nil
	case "s":
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		project, err := h.navigation.Select(ctx, parts[1], chatID, parts[2])
		if err != nil {
			return navigationError(err), nil
		}
		state, err := h.state.LoadRuntimeState(ctx)
		if err != nil {
			return domain.BotResponse{}, err
		}
		state.ProjectID = project.ID
		state.RelativePath = project.RelativePath
		state.WorkspaceRoot = project.WorkspaceRoot
		state.SessionID = ""
		if err := h.state.SaveRuntimeState(ctx, state); err != nil {
			return domain.BotResponse{}, err
		}
		// Project is saved; instead of starting the agent immediately
		// we hand off to the agent picker so the user can choose
		// (or confirm) which agent drives this folder.
		return h.bindServerPicker(ctx, chatID, project.AbsolutePath), nil
	case "ag":
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		return h.pickAgent(ctx, chatID, domain.AgentKind(parts[2]))
	case "ag_unavailable":
		if len(parts) != 2 {
			return expiredNavigation(), nil
		}
		return domain.BotResponse{
			Text: fmt.Sprintf("El agente `%s` todavía no está disponible en esta versión. Próximamente.", parts[1]),
			Edit: true,
		}, nil
	case "ae":
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		return h.toggleAgent(ctx, chatID, domain.AgentKind(parts[2]))
	case "am_yes":
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		return h.confirmMigrate(ctx, chatID)
	case "sn":
		if len(parts) != 3 {
			return expiredNavigation(), nil
		}
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed != chatID {
			return expiredNavigation(), nil
		}
		adapter, kind, err := h.activeAdapter(chatID)
		if err != nil {
			return agentUnavailableResponse(err), nil
		}
		if !h.manager.StartedSubprocess(kind) {
			return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind), Edit: true}, nil
		}
		state, err := h.state.LoadRuntimeState(ctx)
		if err != nil {
			return domain.BotResponse{}, err
		}
		if state.ProjectID == "" {
			return domain.BotResponse{Text: "Selecciona primero un proyecto con /projects.", Edit: true}, nil
		}
		if parts[2] == "new" {
			session, err := adapter.CreateSession(ctx, "")
			if err != nil {
				return domain.BotResponse{}, err
			}
			state.SessionID = session.ID
			if err := h.state.SaveRuntimeState(ctx, state); err != nil {
				return domain.BotResponse{}, err
			}
			return domain.BotResponse{Text: "Nueva sesión activa: " + session.ID, Edit: true}, nil
		}
		state.SessionID = parts[2]
		if err := h.state.SaveRuntimeState(ctx, state); err != nil {
			return domain.BotResponse{}, err
		}
		return domain.BotResponse{Text: "Sesión activa: " + parts[2], Edit: true}, nil
	default:
		return expiredNavigation(), nil
	}
}

func directoryResponse(state domain.NavigationState, entries []domain.DirectoryEntry) domain.BotResponse {
	path := state.CurrentRelativePath
	if path == "" {
		path = "/"
	}
	response := domain.BotResponse{Text: fmt.Sprintf("Workspace: `%s`\n\nSelecciona una carpeta:", path)}
	for _, entry := range entries {
		response.Buttons = append(response.Buttons, []domain.BotButton{{Text: "📂 " + entry.Name, Data: "e|" + state.ID + "|" + entry.RelativePath}})
	}
	if state.CurrentRelativePath != "" {
		response.Buttons = append(response.Buttons, []domain.BotButton{{Text: "✅ Usar esta carpeta", Data: "s|" + state.ID + "|" + state.CurrentRelativePath}})
		response.Buttons = append(response.Buttons, []domain.BotButton{{Text: "⬅️ Atrás", Data: "b|" + state.ID}, {Text: "🏠 Inicio", Data: "h|" + state.ID}})
	}
	return response
}

// bindServerPicker shows the agent picker so the user can choose (or
// confirm) which agent drives the freshly selected folder. If the bot
// already had a running agent and the folder change didn't require a
// restart, the picker still appears because the user might want to
// switch agents at the same time.
func (h *Handler) bindServerPicker(_ context.Context, chatID int64, absolutePath string) domain.BotResponse {
	return agentPickerResponse(h.registry, chatID, fmt.Sprintf("Proyecto guardado: `%s`\n\nElige el agente con el que quieres trabajar en esta carpeta:", filepath.Base(absolutePath)), false)
}

func (h *Handler) init(ctx context.Context, args []string) (domain.BotResponse, error) {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	workingDir, err := h.resolveInitTarget(ctx, target)
	if err != nil {
		return domain.BotResponse{Text: err.Error()}, nil
	}
	kind, err := h.registry.Active(0)
	if err != nil || kind == "" {
		kind = domain.AgentOpenCode
	}
	if err := h.manager.Start(ctx, kind, workingDir); err != nil {
		return domain.BotResponse{Text: "No se pudo arrancar el agente: " + err.Error()}, nil
	}
	state, _ := h.state.LoadRuntimeState(ctx)
	state.SessionID = ""
	state.AgentKind = kind
	state.RelativePath = relativeUnderWorkspace(h.workspaceRoot, workingDir)
	if state.WorkspaceRoot == "" {
		state.WorkspaceRoot = h.workspaceRoot
	}
	if err := h.state.SaveRuntimeState(ctx, state); err != nil {
		return domain.BotResponse{}, err
	}
	return domain.BotResponse{Text: fmt.Sprintf("Agente %s arrancado en `%s`. Usa /sessions para abrir o crear una sesión.", kind, workingDir)}, nil
}

func (h *Handler) resolveInitTarget(ctx context.Context, override string) (string, error) {
	if override != "" {
		if filepath.IsAbs(override) {
			return "", domain.ErrOutsideWorkspace
		}
		absolute, _, err := h.browser.Resolve(override)
		if err != nil {
			return "", err
		}
		return absolute, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return "", err
	}
	if state.WorkspaceRoot != "" && state.RelativePath != "" {
		abs := filepath.Join(state.WorkspaceRoot, state.RelativePath)
		if rel, err := filepath.Rel(state.WorkspaceRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return abs, nil
		}
	}
	if state.ProjectID != "" && state.WorkspaceRoot != "" {
		return state.WorkspaceRoot, nil
	}
	if h.workspaceRoot != "" {
		return h.workspaceRoot, nil
	}
	return "", domain.ErrWorkspaceNotConfigured
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
	project := state.RelativePath
	if project == "" {
		project = "ninguno"
	}
	session := state.SessionID
	if session == "" {
		session = "ninguna"
	}
	kind, _ := h.registry.Active(chatID)
	if kind == "" {
		kind = domain.AgentOpenCode
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("Agente: %s (apagado)\nProyecto: %s\nSesión: %s\n\nArranca con /init.", kind, project, session)}, nil
	}
	adapter, err := h.registry.Get(kind)
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("Agente %s no disponible: %s", kind, err)}, nil
	}
	health, err := adapter.Health(ctx)
	if err != nil {
		return domain.BotResponse{Text: fmt.Sprintf("%s no responde.", kind)}, nil
	}
	return domain.BotResponse{Text: fmt.Sprintf("Agente: %s\nSalud: %t (%s)\nCwd: %s\nProyecto: %s\nSesión: %s",
		kind, health.Healthy, health.Version, h.manager.WorkingDir(kind), project, session)}, nil
}

func (h *Handler) sessions(ctx context.Context, chatID int64, args []string) (domain.BotResponse, error) {
	adapter, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind)}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if state.ProjectID == "" {
		return domain.BotResponse{Text: "Selecciona primero un proyecto con /projects."}, nil
	}
	for _, arg := range args {
		switch arg {
		case "new":
			session, err := adapter.CreateSession(ctx, "")
			if err != nil {
				return domain.BotResponse{}, err
			}
			state.SessionID = session.ID
			if err := h.state.SaveRuntimeState(ctx, state); err != nil {
				return domain.BotResponse{}, err
			}
			return domain.BotResponse{Text: "Nueva sesión activa: " + session.ID}, nil
		case "current":
			return domain.BotResponse{Text: "Sesión activa: " + orDefault(state.SessionID, "ninguna")}, nil
		}
	}
	if len(args) > 0 {
		sessionID := args[0]
		state.SessionID = sessionID
		if err := h.state.SaveRuntimeState(ctx, state); err != nil {
			return domain.BotResponse{}, err
		}
		return domain.BotResponse{Text: "Sesión activa: " + sessionID}, nil
	}
	all, err := adapter.ListSessions(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	activeDir := ""
	if state.WorkspaceRoot != "" {
		activeDir, _ = filepath.Abs(filepath.Join(state.WorkspaceRoot, state.RelativePath))
	}
	sessions := filterSessionsByDirectory(all, activeDir)
	if len(sessions) == 0 {
		return domain.BotResponse{Text: "No hay sesiones abiertas todavía. Usa /sessions new para crear una."}, nil
	}
	response := domain.BotResponse{Text: "Sesiones disponibles:"}
	for _, session := range sessions {
		label := session.Title
		if label == "" {
			label = session.ID
		}
		response.Buttons = append(response.Buttons, []domain.BotButton{{Text: label, Data: fmt.Sprintf("sn|%d|%s", chatID, session.ID)}})
	}
	response.Buttons = append(response.Buttons, []domain.BotButton{{Text: "Nueva sesión", Data: fmt.Sprintf("sn|%d|new", chatID)}})
	return response, nil
}

func filterSessionsByDirectory(sessions []domain.Session, dir string) []domain.Session {
	if dir == "" {
		return sessions
	}
	out := make([]domain.Session, 0, len(sessions))
	for _, s := range sessions {
		if s.Directory == dir {
			out = append(out, s)
		}
	}
	return out
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (h *Handler) diff(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	adapter, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind)}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if state.SessionID == "" {
		return domain.BotResponse{Text: "No hay sesión activa."}, nil
	}
	return h.diffSession(ctx, adapter, state.SessionID)
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
	adapter, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind)}, nil
	}
	return h.diffSession(ctx, adapter, snapshot.SessionID)
}

// diffSession fetches the file status of a specific session and renders
// it. Shared by /diff and the "Ver cambios" button on the completion
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

func (h *Handler) undo(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	adapter, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind)}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if state.SessionID == "" {
		return domain.BotResponse{Text: "No hay sesión activa."}, nil
	}
	if err := adapter.Revert(ctx, state.SessionID); err != nil {
		return domain.BotResponse{}, err
	}
	return domain.BotResponse{Text: "Último cambio revertido."}, nil
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
	if snapshot.ProjectID != "" {
		state.ProjectID = snapshot.ProjectID
	}
	if snapshot.Directory != "" {
		state.WorkspaceRoot = h.workspaceRoot
		state.RelativePath = relativeUnderWorkspace(h.workspaceRoot, snapshot.Directory)
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

func (h *Handler) watch(ctx context.Context, chatID int64, args []string) (domain.BotResponse, error) {
	if h.watcher == nil {
		return domain.BotResponse{Text: "El observador de sesiones no está activo."}, nil
	}
	_, kind, err := h.activeAdapter(chatID)
	if err != nil {
		return agentUnavailableResponse(err), nil
	}
	if !h.manager.StartedSubprocess(kind) {
		return domain.BotResponse{Text: fmt.Sprintf("El agente %s está apagado. Ejecuta /init primero.", kind)}, nil
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	target := state.SessionID
	if len(args) > 0 {
		target = strings.TrimSpace(args[0])
	}
	if target == "" {
		return domain.BotResponse{Text: "No hay sesión activa que vigilar. Selecciona una con /sessions o pasa un id."}, nil
	}
	if chatID == 0 {
		return domain.BotResponse{Text: "Chat no resuelto para esta orden."}, nil
	}
	h.watcher.Watch(chatID, target)
	return domain.BotResponse{Text: fmt.Sprintf("Vigilando la sesión `%s`. Te aviso por Telegram cuando quede inactiva.", target)}, nil
}

// agentCommand shows the picker for the chat. Same UI as the post-project
// picker so the user only has to learn one flow.
func (h *Handler) agentCommand(_ context.Context, chatID int64) domain.BotResponse {
	return agentPickerResponse(h.registry, chatID, "Elige el agente con el que quieres trabajar:", true)
}

// agentsCommand renders the agents list and, when called with the
// "migrate" subcommand, asks the user to confirm a one-shot migration
// of legacy sessions to opencode.
func (h *Handler) agentsCommand(ctx context.Context, chatID int64, args []string) (domain.BotResponse, error) {
	if len(args) > 0 && args[0] == "migrate" {
		return h.askMigrate(ctx, chatID)
	}
	return h.listAgentsResponse(ctx, chatID), nil
}

func (h *Handler) listAgentsResponse(ctx context.Context, chatID int64) domain.BotResponse {
	descriptors := h.registry.Descriptors()
	state, err := h.state.LoadAgentState(ctx, chatID)
	enabled := state.Enabled
	if err != nil || len(enabled) == 0 {
		enabled = map[domain.AgentKind]bool{}
		for _, d := range descriptors {
			if d.Available {
				enabled[d.Kind] = true
			}
		}
	}
	active, _ := h.registry.Active(chatID)

	var text strings.Builder
	text.WriteString("Agentes detectados:\n")
	for _, d := range descriptors {
		emoji := emojiForKind(d.Kind)
		status := "próximamente"
		if d.Available {
			status = "disponible"
		} else if !d.Detected {
			status = "binario no encontrado"
		}
		marker := ""
		if d.Kind == active {
			marker = " ◀ activo"
		}
		flag := "✗"
		if enabled[d.Kind] {
			flag = "✓"
		}
		fmt.Fprintf(&text, "\n%s %s — %s [%s]%s\n", emoji, d.DisplayName, status, flag, marker)
		if d.Reason != "" && !d.Available {
			text.WriteString("   " + d.Reason + "\n")
		}
	}
	text.WriteString("\nToca un agente para habilitarlo o deshabilitarlo.")

	resp := domain.BotResponse{Text: text.String()}
	for _, d := range descriptors {
		if !d.Available {
			continue
		}
		flag := "✗ Deshabilitar"
		if enabled[d.Kind] {
			flag = "✓ Habilitado"
		}
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: fmt.Sprintf("%s %s — %s", toggleEmoji(enabled[d.Kind]), d.DisplayName, flag),
			Data: fmt.Sprintf("ae|%d|%s", chatID, d.Kind),
		}})
	}
	return resp
}

func (h *Handler) toggleAgent(ctx context.Context, chatID int64, kind domain.AgentKind) (domain.BotResponse, error) {
	descriptor, ok := h.registry.DescriptorFor(kind)
	if !ok || !descriptor.Available {
		return domain.BotResponse{Text: "Ese agente no está disponible.", Edit: true}, nil
	}
	state, err := h.state.LoadAgentState(ctx, chatID)
	if err != nil {
		state = domain.AgentState{ChatID: chatID, Enabled: map[domain.AgentKind]bool{}}
	}
	if state.Enabled == nil {
		state.Enabled = map[domain.AgentKind]bool{}
	}
	state.Enabled[kind] = !state.Enabled[kind]
	if err := h.state.SaveAgentState(ctx, chatID, kind, state.Enabled[kind]); err != nil {
		return domain.BotResponse{}, err
	}
	// Disabling the active agent is allowed: the next /status or
	// /agent will offer the next available one. No state change is
	// required here, but a comment keeps the intent visible.
	resp := h.listAgentsResponse(ctx, chatID)
	resp.Edit = true
	return resp, nil
}

// pickAgent reacts to the user's tap on an agent card. It starts the
// agent's subprocess in the current project folder and then renders
// the session menu.
func (h *Handler) pickAgent(ctx context.Context, chatID int64, kind domain.AgentKind) (domain.BotResponse, error) {
	descriptor, ok := h.registry.DescriptorFor(kind)
	if !ok {
		return domain.BotResponse{Text: "Agente desconocido.", Edit: true}, nil
	}
	if !descriptor.Available {
		return domain.BotResponse{Text: fmt.Sprintf("El agente `%s` todavía no está disponible en esta versión. Próximamente.", kind), Edit: true}, nil
	}
	if err := h.registry.SetActive(chatID, kind); err != nil {
		return domain.BotResponse{}, err
	}
	state, err := h.state.LoadRuntimeState(ctx)
	if err != nil {
		return domain.BotResponse{}, err
	}
	state.AgentKind = kind
	workingDir := ""
	if state.WorkspaceRoot != "" {
		workingDir = filepath.Join(state.WorkspaceRoot, state.RelativePath)
		if info, err := osStat(workingDir); err != nil || !info.IsDir() {
			workingDir = state.WorkspaceRoot
		}
	}
	if workingDir == "" {
		workingDir = h.workspaceRoot
	}
	if workingDir != "" {
		if err := h.manager.Start(ctx, kind, workingDir); err != nil {
			return domain.BotResponse{Text: fmt.Sprintf("Agente %s seleccionado, pero no se pudo arrancar: %s", kind, err), Edit: true}, nil
		}
	}
	if err := h.state.SaveRuntimeState(ctx, state); err != nil {
		return domain.BotResponse{}, err
	}
	return h.sessionsAfterAgent(ctx, chatID, kind)
}

func (h *Handler) sessionsAfterAgent(_ context.Context, chatID int64, kind domain.AgentKind) (domain.BotResponse, error) {
	return domain.BotResponse{
		Text: fmt.Sprintf("Agente activo: `%s`.\nUsa /sessions para abrir o crear una sesión, o envía un mensaje directo.", kind),
		Buttons: [][]domain.BotButton{
			{{Text: "💬 Abrir / crear sesión", Data: fmt.Sprintf("sn|%d|new", chatID)}},
		},
		Edit: true,
	}, nil
}

func (h *Handler) askMigrate(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	count, err := h.countLegacySessions(ctx, chatID)
	if err != nil {
		return domain.BotResponse{}, err
	}
	if count == 0 {
		return domain.BotResponse{Text: "No hay sesiones que migrar. La columna agent_kind ya está en opencode para todo tu historial."}, nil
	}
	return domain.BotResponse{
		Text: fmt.Sprintf("Detecté %d sesión/es sin agente asignado. Marcar como `opencode` (compatibilidad con versiones anteriores)?", count),
		Buttons: [][]domain.BotButton{
			{
				{Text: "Sí, migrar", Data: fmt.Sprintf("am_yes|%d", chatID)},
				{Text: "Cancelar", Data: "noop"},
			},
		},
	}, nil
}

func (h *Handler) confirmMigrate(ctx context.Context, chatID int64) (domain.BotResponse, error) {
	count, err := h.markLegacyAsOpenCode(ctx, chatID)
	if err != nil {
		return domain.BotResponse{Text: "No se pudo migrar: " + err.Error(), Edit: true}, nil
	}
	return domain.BotResponse{
		Text: fmt.Sprintf("Listo: %d sesión/es marcadas como opencode.", count),
		Edit: true,
	}, nil
}

// agentPickerResponse builds the Telegram picker for one chat. Every
// known agent is a button; available ones dispatch to ag|<chat>|<kind>,
// unavailable ones (Próximamente) dispatch to ag_unavailable|<kind>.
// The first row lets the user keep the current agent without picking.
func agentPickerResponse(reg domain.AgentRegistry, chatID int64, header string, showCancel bool) domain.BotResponse {
	descriptors := reg.Descriptors()
	active, _ := reg.Active(chatID)
	resp := domain.BotResponse{Text: header}
	if active != "" {
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: fmt.Sprintf("▶️ Continuar con %s", active),
			Data: fmt.Sprintf("ag|%d|%s", chatID, active),
		}})
	}
	for _, d := range descriptors {
		label := fmt.Sprintf("%s %s", emojiForKind(d.Kind), d.DisplayName)
		if !d.Available {
			label += " (próximamente)"
			resp.Buttons = append(resp.Buttons, []domain.BotButton{{
				Text: label, Data: fmt.Sprintf("ag_unavailable|%s", d.Kind),
			}})
			continue
		}
		if d.Kind == active {
			continue // already shown in the first row
		}
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{
			Text: label, Data: fmt.Sprintf("ag|%d|%s", chatID, d.Kind),
		}})
	}
	if showCancel {
		resp.Buttons = append(resp.Buttons, []domain.BotButton{{Text: "Cancelar", Data: "noop"}})
	}
	return resp
}

func agentUnavailableResponse(err error) domain.BotResponse {
	if errors.Is(err, domain.ErrNoActiveAgent) {
		return domain.BotResponse{Text: "Ningún agente activo todavía. Usa /agent o /projects."}
	}
	if errors.Is(err, domain.ErrAgentUnavailable) {
		return domain.BotResponse{Text: "El agente activo no está disponible. Usa /agent para cambiar."}
	}
	return domain.BotResponse{Text: "No se pudo resolver el agente activo: " + err.Error()}
}

func emojiForKind(kind domain.AgentKind) string {
	switch kind {
	case domain.AgentOpenCode:
		return "🟢"
	case domain.AgentClaude:
		return "🟣"
	case domain.AgentCodex:
		return "⚪"
	case domain.AgentKiro:
		return "🟠"
	case domain.AgentCopilot:
		return "🐙"
	default:
		return "•"
	}
}

func toggleEmoji(enabled bool) string {
	if enabled {
		return "✓"
	}
	return "✗"
}

func expiredNavigation() domain.BotResponse {
	return domain.BotResponse{Text: "El menú expiró. Usa /projects para abrir uno nuevo.", Edit: true}
}

func navigationError(err error) domain.BotResponse {
	if errors.Is(err, domain.ErrNavigationNotFound) || errors.Is(err, domain.ErrUnauthorizedNavigation) {
		return expiredNavigation()
	}
	return domain.BotResponse{Text: "No se pudo completar la navegación.", Edit: true}
}
