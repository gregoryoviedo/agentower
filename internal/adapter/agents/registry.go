package agents

import (
	"context"
	"fmt"
	"sync"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Registry is the in-process AgentRegistry implementation. It maps
// AgentKind to an adapter, keeps the boot-time descriptors, and
// remembers which agent each Telegram chat is currently driving.
//
// Adapters are constructed lazily on first Get() so an unused agent
// never opens a socket.
type Registry struct {
	descriptors []domain.AgentDescriptor
	byKind      map[domain.AgentKind]domain.AgentDescriptor
	factories   map[domain.AgentKind]func() (domain.AgentAdapter, error)

	state domain.StateRepository

	mu      sync.Mutex
	clients map[domain.AgentKind]domain.AgentAdapter
	active  map[int64]domain.AgentKind
}

// RegistryOptions wires the registry up.
type RegistryOptions struct {
	Descriptors []domain.AgentDescriptor
	Factories   map[domain.AgentKind]func() (domain.AgentAdapter, error)
	State       domain.StateRepository
}

// NewRegistry builds an empty registry. Add opencode via Register; the
// other kinds can be added when their adapters ship.
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.Factories == nil {
		opts.Factories = map[domain.AgentKind]func() (domain.AgentAdapter, error){}
	}
	byKind := make(map[domain.AgentKind]domain.AgentDescriptor, len(opts.Descriptors))
	for _, d := range opts.Descriptors {
		byKind[d.Kind] = d
	}
	return &Registry{
		descriptors: opts.Descriptors,
		byKind:      byKind,
		factories:   opts.Factories,
		state:       opts.State,
		clients:     map[domain.AgentKind]domain.AgentAdapter{},
		active:      map[int64]domain.AgentKind{},
	}
}

// Register adds a factory for a single kind. Call this from main.go
// after wiring each adapter's constructor.
func (r *Registry) Register(kind domain.AgentKind, factory func() (domain.AgentAdapter, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[kind] = factory
	if _, ok := r.byKind[kind]; !ok {
		r.byKind[kind] = domain.AgentDescriptor{
			Kind:        kind,
			DisplayName: string(kind),
			Available:   true,
			Detected:    true,
		}
	}
}

// Descriptors returns every known agent's boot-time fingerprint. The
// Telegram picker iterates over this list (regardless of Available).
func (r *Registry) Descriptors() []domain.AgentDescriptor {
	out := make([]domain.AgentDescriptor, 0, len(r.descriptors))
	out = append(out, r.descriptors...)
	return out
}

// Available returns the subset of descriptors with Available=true.
// These are the only kinds the user can pick from Telegram right now.
func (r *Registry) Available() []domain.AgentDescriptor {
	out := make([]domain.AgentDescriptor, 0, len(r.descriptors))
	for _, d := range r.descriptors {
		if d.Available {
			out = append(out, d)
		}
	}
	return out
}

// Get returns the adapter for the given kind. ErrAgentUnavailable when
// no factory is registered (this is the "Próximamente" path during
// PR-1). The adapter is constructed lazily on first call.
func (r *Registry) Get(kind domain.AgentKind) (domain.AgentAdapter, error) {
	r.mu.Lock()
	if existing, ok := r.clients[kind]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	factory, ok := r.factories[kind]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrAgentUnavailable, kind)
	}
	adapter, err := factory()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.clients[kind] = adapter
	r.mu.Unlock()
	return adapter, nil
}

// Active returns the agent currently driving the chat. Falls back to
// opencode (the historical default) and then to the first Available
// descriptor so callers never see an empty result unless every
// adapter is unavailable.
func (r *Registry) Active(chatID int64) (domain.AgentKind, error) {
	r.mu.Lock()
	if k, ok := r.active[chatID]; ok && k != "" {
		r.mu.Unlock()
		return k, nil
	}
	r.mu.Unlock()
	if r.state != nil {
		state, err := r.state.LoadAgentState(context.Background(), chatID)
		if err == nil && state.Active != "" {
			r.mu.Lock()
			r.active[chatID] = state.Active
			r.mu.Unlock()
			return state.Active, nil
		}
	}
	if k := firstAvailableKind(r.descriptors); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("%w", domain.ErrNoActiveAgent)
}

// SetActive persists the per-chat pick. Idempotent. Updates the
// in-memory cache and writes through to the state repository so the
// next process restart picks up the same agent.
func (r *Registry) SetActive(chatID int64, kind domain.AgentKind) error {
	r.mu.Lock()
	r.active[chatID] = kind
	r.mu.Unlock()
	if r.state != nil {
		if err := r.state.SaveAgentState(context.Background(), chatID, kind, true); err != nil {
			return err
		}
	}
	return nil
}

// AvailableByKind returns the descriptor for one kind, plus an "ok"
// flag. Useful for the Telegram picker when it needs to render a
// specific button's tooltip.
func (r *Registry) DescriptorFor(kind domain.AgentKind) (domain.AgentDescriptor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.byKind[kind]
	return d, ok
}

func firstAvailableKind(descriptors []domain.AgentDescriptor) domain.AgentKind {
	for _, d := range descriptors {
		if d.Available {
			return d.Kind
		}
	}
	return ""
}
