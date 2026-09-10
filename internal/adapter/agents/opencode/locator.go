package agents_opencode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// SessionLocator answers "what opencode session is the user driving
// right now" for the /continuar handler. The opencode server exposes
// /session with each row's time.updated; the freshest row is the one
// most likely to be the live session.
//
// The locator is a thin wrapper around the existing Client. It
// deliberately reuses the configured HTTP transport so it respects the
// same timeouts the rest of the adapter honours.
type SessionLocator struct {
	client *Client
}

// NewSessionLocator wires the locator to an already-constructed
// Client. The caller (composition root) is expected to have built the
// client once and to share it across the registry and the locator.
func NewSessionLocator(client *Client) *SessionLocator {
	return &SessionLocator{client: client}
}

// Kind reports the agent this locator serves.
func (l *SessionLocator) Kind() domain.AgentKind { return domain.AgentOpenCode }

// Locate queries the opencode /session endpoint and returns the
// session with the most recent time.updated. Returns
// ErrNoActiveSession when the server has no sessions or when no
// session has been touched in the caller's lifetime (i.e. all rows
// are stale because the opencode server has not been used yet).
//
// The function uses a short context deadline so /continuar can fan
// out to multiple locators without one slow server blocking the
// rest. Errors from the HTTP client are surfaced as-is so the caller
// can log them; only the "empty list" case is treated as the
// sentinel "nothing to continue".
func (l *SessionLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	listCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sessions, err := l.client.ListSessions(listCtx)
	if err != nil {
		return domain.ActiveSession{}, fmt.Errorf("list opencode sessions: %w", err)
	}
	if len(sessions) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	// ListSessions already returns the summary; the most recent
	// session is the one with the highest time.updated. We re-query
	// the full session list with timestamps so we can pick by
	// recency, not just the API order.
	raw, err := l.fetchSessions(listCtx)
	if err != nil {
		return domain.ActiveSession{}, err
	}
	if len(raw) == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	freshest := raw[0]
	for _, s := range raw[1:] {
		if s.Time.Updated > freshest.Time.Updated {
			freshest = s
		}
	}
	if freshest.Time.Updated == 0 {
		return domain.ActiveSession{}, domain.ErrNoActiveSession
	}
	preview, _ := l.fetchPreview(listCtx, freshest.ID)
	return domain.ActiveSession{
		Kind:      domain.AgentOpenCode,
		SessionID: freshest.ID,
		Project:   filepathBaseName(freshest.Directory),
		Directory: freshest.Directory,
		Title:     freshest.Title,
		Preview:   preview,
		TouchedAt: time.UnixMilli(freshest.Time.Updated).UTC(),
		Source:    "http",
	}, nil
}

// fetchSessions re-issues the /session call so the locator can sort
// by time.updated; ListSessions collapses the DTO to domain.Session
// and drops the timestamp. We keep the raw DTO here so we don't have
// to widen the public client surface.
func (l *SessionLocator) fetchSessions(ctx context.Context) ([]sessionDTO, error) {
	var dto []sessionDTO
	if err := l.client.getJSON(ctx, "/session", &dto); err != nil {
		return nil, fmt.Errorf("list opencode sessions: %w", err)
	}
	return dto, nil
}

// fetchPreview pulls the latest assistant text from the session so
// /continuar can show a one-liner hint. Best-effort: a failure here
// only leaves the preview empty, not the whole Locate call.
func (l *SessionLocator) fetchPreview(ctx context.Context, sessionID string) (string, error) {
	messages, err := l.client.ListMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, p := range messages[i].Parts {
			if p.Type != "text" || p.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
		out := strings.TrimSpace(b.String())
		if len(out) > 240 {
			out = out[:240] + "…"
		}
		return out, nil
	}
	return "", nil
}

// Compile-time guard: SessionLocator must satisfy the domain port.
var _ domain.SessionLocator = (*SessionLocator)(nil)
