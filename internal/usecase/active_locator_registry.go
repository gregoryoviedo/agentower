package usecase

import (
	"sort"
	"sync"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// activeLocatorRegistry is the in-process ActiveLocatorRegistry
// implementation. The composition root builds it once at startup by
// adding one SessionLocator per detected agent; the /continuar
// handler iterates over it without ever needing to know how many
// locators are wired or in what order.
//
// The registry is concurrency-safe: the composition root only calls
// Add during boot, but the handler reads from many goroutines
// (the fan-out timeout goroutine, the per-locate context, etc).
type activeLocatorRegistry struct {
	mu     sync.RWMutex
	byKind map[domain.AgentKind]domain.SessionLocator
	order  []domain.AgentKind
}

// NewActiveLocatorRegistry builds an empty registry.
func NewActiveLocatorRegistry() *activeLocatorRegistry {
	return &activeLocatorRegistry{
		byKind: map[domain.AgentKind]domain.SessionLocator{},
	}
}

// Add registers a SessionLocator for one agent. Re-registering the
// same kind replaces the previous locator; this is what lets the
// detector swap a "not detected" locator for a real one after
// re-running the scan.
//
// Add is safe to call after the bot is running; the handler does
// not care about the registry mutating underneath it because each
// /continuar invocation takes a fresh snapshot of the locator list.
func (r *activeLocatorRegistry) Add(locator domain.SessionLocator) {
	if locator == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := locator.Kind()
	if _, exists := r.byKind[kind]; !exists {
		r.order = append(r.order, kind)
		sort.SliceStable(r.order, func(i, j int) bool {
			return r.order[i] < r.order[j]
		})
	}
	r.byKind[kind] = locator
}

// Locators returns the registered locators in a deterministic order
// (sorted by AgentKind). The slice is a copy so callers can iterate
// without holding the registry lock.
func (r *activeLocatorRegistry) Locators() []domain.SessionLocator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.SessionLocator, 0, len(r.order))
	for _, kind := range r.order {
		if loc, ok := r.byKind[kind]; ok {
			out = append(out, loc)
		}
	}
	return out
}

// LocatorFor returns the locator registered for a single kind. The
// boolean mirrors the "exists" semantics of Go maps.
func (r *activeLocatorRegistry) LocatorFor(kind domain.AgentKind) (domain.SessionLocator, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	loc, ok := r.byKind[kind]
	return loc, ok
}

// Compile-time guard: activeLocatorRegistry must satisfy the
// domain port.
var _ domain.ActiveLocatorRegistry = (*activeLocatorRegistry)(nil)
