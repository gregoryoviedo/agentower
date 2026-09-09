package agents_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestRegistryReturnsUnavailableForUnregisteredKind(t *testing.T) {
	r := agents.NewRegistry(agents.RegistryOptions{})
	if _, err := r.Get(domain.AgentClaude); !errors.Is(err, domain.ErrAgentUnavailable) {
		t.Fatalf("Get(AgentClaude) = %v, want ErrAgentUnavailable", err)
	}
}

func TestRegistryBuildsAdapterLazily(t *testing.T) {
	var calls atomic.Int32
	r := agents.NewRegistry(agents.RegistryOptions{})
	r.Register(domain.AgentOpenCode, func() (domain.AgentAdapter, error) {
		calls.Add(1)
		return stubAdapter{kind: domain.AgentOpenCode}, nil
	})
	a, err := r.Get(domain.AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind() != domain.AgentOpenCode {
		t.Fatalf("kind=%s", a.Kind())
	}
	b, _ := r.Get(domain.AgentOpenCode)
	if b != a {
		t.Fatal("expected same adapter instance on second Get")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory called %d times, want 1", got)
	}
}

func TestRegistryActiveFallsBackToFirstAvailable(t *testing.T) {
	r := agents.NewRegistry(agents.RegistryOptions{
		Descriptors: []domain.AgentDescriptor{
			{Kind: domain.AgentOpenCode, Available: true},
		},
	})
	kind, err := r.Active(42)
	if err != nil {
		t.Fatal(err)
	}
	if kind != domain.AgentOpenCode {
		t.Fatalf("Active = %s, want opencode", kind)
	}
}

func TestRegistrySetActiveIsIdempotent(t *testing.T) {
	r := agents.NewRegistry(agents.RegistryOptions{})
	if err := r.SetActive(7, domain.AgentOpenCode); err != nil {
		t.Fatal(err)
	}
	kind, _ := r.Active(7)
	if kind != domain.AgentOpenCode {
		t.Fatalf("Active after SetActive = %s", kind)
	}
	if err := r.SetActive(7, domain.AgentOpenCode); err != nil {
		t.Fatal(err)
	}
}

// stubAdapter is the minimal AgentAdapter for tests; it returns no data
// for any call but proves the registry dispatches and caches correctly.
type stubAdapter struct{ kind domain.AgentKind }

func (s stubAdapter) Kind() domain.AgentKind { return s.kind }
func (s stubAdapter) DisplayName() string    { return string(s.kind) }
func (s stubAdapter) Health(context.Context) (domain.HealthStatus, error) {
	return domain.HealthStatus{Healthy: true}, nil
}
func (s stubAdapter) ListProjects(context.Context) ([]domain.Project, error) {
	return nil, nil
}
func (s stubAdapter) ListSessions(context.Context) ([]domain.Session, error) {
	return nil, nil
}
func (s stubAdapter) CreateSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (s stubAdapter) SendPrompt(context.Context, string, string) (string, error) {
	return "", nil
}
func (s stubAdapter) Revert(context.Context, string) error { return nil }
func (s stubAdapter) FileStatus(context.Context, string) ([]domain.FileChange, error) {
	return nil, nil
}
func (s stubAdapter) ListMessages(context.Context, string) ([]domain.Message, error) {
	return nil, nil
}
