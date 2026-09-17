package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

// notResumableAdapter returns ErrSessionNotResumable from SendPrompt.
type notResumableAdapter struct{ domain.AgentAdapter }

func (notResumableAdapter) Kind() domain.AgentKind { return domain.AgentCopilot }
func (notResumableAdapter) DisplayName() string    { return "GitHub Copilot" }
func (notResumableAdapter) SendPrompt(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("%w: copilot session x", domain.ErrSessionNotResumable)
}

// resumableRegistry serves one adapter for one kind.
type resumableRegistry struct {
	domain.AgentRegistry
	adapter domain.AgentAdapter
	kind    domain.AgentKind
}

func (r *resumableRegistry) Get(k domain.AgentKind) (domain.AgentAdapter, error) {
	if k != r.kind {
		return nil, domain.ErrAgentUnavailable
	}
	return r.adapter, nil
}

func (r *resumableRegistry) Active(int64) (domain.AgentKind, error) { return r.kind, nil }

func TestHandleTextMapsSessionNotResumable(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	registry := &resumableRegistry{adapter: notResumableAdapter{}, kind: domain.AgentCopilot}
	handler := NewHandler(store, registry, &localFakeServer{started: true}, root)
	ctx := context.Background()
	if err := store.SaveRuntimeState(ctx, domain.RuntimeState{WorkspaceRoot: root, SessionID: "ses_1", AgentKind: domain.AgentCopilot}); err != nil {
		t.Fatal(err)
	}

	resp, err := handler.HandleText(ctx, 42, "hola")
	if err != nil {
		t.Fatalf("HandleText err=%v", err)
	}
	if !strings.Contains(resp.Text, "GitHub Copilot") || !strings.Contains(resp.Text, "Copilot CLI") {
		t.Fatalf("resp = %q, want the per-agent resumability message", resp.Text)
	}
}

func TestSessionNotResumableMessagePerAgent(t *testing.T) {
	cases := map[domain.AgentKind]string{
		domain.AgentCopilot:     "Copilot CLI",
		domain.AgentAntigravity: "Antigravity CLI",
		domain.AgentCodex:       "codex",
	}
	for kind, want := range cases {
		msg := sessionNotResumableMessage(kind)
		if kind == domain.AgentCodex {
			if !strings.Contains(strings.ToLower(msg), want) {
				t.Fatalf("message for %s = %q, want it to mention %q", kind, msg, want)
			}
			continue
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("message for %s = %q, want it to mention %q", kind, msg, want)
		}
	}
}
