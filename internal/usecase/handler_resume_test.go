package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// fakeLocator implements domain.SessionLocator for handler tests.
// Each test sets the result it wants the locator to return.
type fakeLocator struct {
	kind     domain.AgentKind
	result   domain.ActiveSession
	err      error
	called   int
	delay    time.Duration
	validate func(domain.ActiveSession) error
}

func (f *fakeLocator) Kind() domain.AgentKind { return f.kind }

func (f *fakeLocator) Locate(ctx context.Context) (domain.ActiveSession, error) {
	f.called++
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return domain.ActiveSession{}, ctx.Err()
		}
	}
	if f.err != nil {
		return domain.ActiveSession{}, f.err
	}
	if f.validate != nil {
		if err := f.validate(f.result); err != nil {
			return domain.ActiveSession{}, err
		}
	}
	return f.result, nil
}

func TestActiveLocatorRegistryAddAndLookup(t *testing.T) {
	reg := usecase.NewActiveLocatorRegistry()
	opencode := &fakeLocator{kind: domain.AgentOpenCode, result: domain.ActiveSession{Kind: domain.AgentOpenCode, SessionID: "oc1"}}
	claude := &fakeLocator{kind: domain.AgentClaude, result: domain.ActiveSession{Kind: domain.AgentClaude, SessionID: "cl1"}}
	reg.Add(opencode)
	reg.Add(claude)

	if got := reg.Locators(); len(got) != 2 {
		t.Fatalf("Locators len = %d, want 2", len(got))
	}
	if got, ok := reg.LocatorFor(domain.AgentClaude); !ok || got != claude {
		t.Fatalf("LocatorFor(claude) = %v, %v", got, ok)
	}
	if _, ok := reg.LocatorFor(domain.AgentKiro); ok {
		t.Fatal("LocatorFor(kiro) = ok, want false")
	}
}

func TestActiveLocatorRegistryReplaceByKind(t *testing.T) {
	reg := usecase.NewActiveLocatorRegistry()
	first := &fakeLocator{kind: domain.AgentOpenCode, result: domain.ActiveSession{SessionID: "v1"}}
	second := &fakeLocator{kind: domain.AgentOpenCode, result: domain.ActiveSession{SessionID: "v2"}}
	reg.Add(first)
	reg.Add(second)
	got, ok := reg.LocatorFor(domain.AgentOpenCode)
	if !ok {
		t.Fatal("missing locator")
	}
	if got != second {
		t.Fatalf("expected second locator, got %p", got)
	}
	if len(reg.Locators()) != 1 {
		t.Fatalf("Locators len = %d, want 1", len(reg.Locators()))
	}
}

func TestActiveLocatorRegistryIgnoresNil(t *testing.T) {
	reg := usecase.NewActiveLocatorRegistry()
	reg.Add(nil)
	if got := reg.Locators(); len(got) != 0 {
		t.Fatalf("Locators len = %d, want 0", len(got))
	}
}

func TestHandlerResumeShowsFreshest(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	workspace := t.TempDir()
	handler, store, reg := newResumeFixture(t, workspace, now)
	opencodeLoc := &fakeLocator{
		kind: domain.AgentOpenCode,
		result: domain.ActiveSession{
			Kind:      domain.AgentOpenCode,
			SessionID: "ses_oc",
			Project:   "agentower",
			Directory: workspace,
			Title:     "opencode chat",
			Preview:   "oc preview",
			TouchedAt: now.Add(-90 * time.Second),
			Source:    "http",
		},
	}
	claudeLoc := &fakeLocator{
		kind: domain.AgentClaude,
		result: domain.ActiveSession{
			Kind:      domain.AgentClaude,
			SessionID: "ses_cl",
			Project:   "demo",
			Directory: workspace,
			Title:     "claude chat",
			Preview:   "cl preview",
			TouchedAt: now.Add(-15 * time.Second),
			Source:    "jsonl",
		},
	}
	reg.Add(opencodeLoc)
	reg.Add(claudeLoc)
	_ = store

	resp, err := handler.HandleCommand(context.Background(), 42, "/resume", nil)
	if err != nil {
		t.Fatalf("/continuar: %v", err)
	}
	if !contains(resp.Text, "claude") {
		t.Fatalf("expected freshest session in response, got: %q", resp.Text)
	}
	if !contains(resp.Text, "demo") {
		t.Fatalf("expected project name in response, got: %q", resp.Text)
	}
	if !contains(resp.Text, "15s") {
		t.Fatalf("expected 'hace 15s' label, got: %q", resp.Text)
	}
	// The alternative row should mention opencode.
	foundAlt := false
	for _, row := range resp.Buttons {
		for _, b := range row {
			if contains(b.Text, "opencode") {
				foundAlt = true
			}
		}
	}
	if !foundAlt {
		t.Fatalf("expected alternative button for opencode, got buttons: %#v", resp.Buttons)
	}
	// And the primary confirm button should target the claude session.
	confirm := primaryConfirmButton(resp)
	if confirm == "" {
		t.Fatal("expected confirm button")
	}
	if !contains(confirm, "ses_cl") {
		t.Fatalf("confirm data = %q, want ses_cl", confirm)
	}
}

func TestHandlerResumeFiltersStale(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	handler, _, reg := newResumeFixture(t, t.TempDir(), now)
	// Set a 5-minute staleness window.
	handler.SetStaleAfter(5 * time.Minute)
	reg.Add(&fakeLocator{
		kind: domain.AgentOpenCode,
		result: domain.ActiveSession{
			Kind:      domain.AgentOpenCode,
			SessionID: "old",
			Project:   "stale-project",
			TouchedAt: now.Add(-30 * time.Minute),
		},
	})
	resp, err := handler.HandleCommand(context.Background(), 42, "/resume", nil)
	if err != nil {
		t.Fatalf("/continuar: %v", err)
	}
	if contains(resp.Text, "stale-project") {
		t.Fatalf("expected stale session to be filtered, got: %q", resp.Text)
	}
	if !contains(resp.Text, "No detecté") {
		t.Fatalf("expected empty-state message, got: %q", resp.Text)
	}
}

func TestHandlerResumeIgnoresLocatorErrors(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	handler, _, reg := newResumeFixture(t, t.TempDir(), now)
	reg.Add(&fakeLocator{kind: domain.AgentOpenCode, err: errors.New("boom")})
	reg.Add(&fakeLocator{
		kind: domain.AgentClaude,
		result: domain.ActiveSession{
			Kind:      domain.AgentClaude,
			SessionID: "alive",
			Project:   "live",
			TouchedAt: now.Add(-10 * time.Second),
		},
	})
	resp, err := handler.HandleCommand(context.Background(), 42, "/resume", nil)
	if err != nil {
		t.Fatalf("/continuar: %v", err)
	}
	if !contains(resp.Text, "claude") {
		t.Fatalf("expected claude session in response, got: %q", resp.Text)
	}
}

func TestHandlerResumeNarrowByKind(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	handler, _, reg := newResumeFixture(t, t.TempDir(), now)
	ocLoc := &fakeLocator{
		kind: domain.AgentOpenCode,
		result: domain.ActiveSession{
			Kind:      domain.AgentOpenCode,
			SessionID: "ses_oc",
			Project:   "oc-proj",
			TouchedAt: now.Add(-3 * time.Minute),
		},
	}
	clLoc := &fakeLocator{
		kind: domain.AgentClaude,
		result: domain.ActiveSession{
			Kind:      domain.AgentClaude,
			SessionID: "ses_cl",
			Project:   "cl-proj",
			TouchedAt: now.Add(-10 * time.Second),
		},
	}
	reg.Add(ocLoc)
	reg.Add(clLoc)

	resp, err := handler.HandleCommand(context.Background(), 42, "/resume", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Text, "claude") {
		t.Fatalf("expected freshest claude session, got: %q", resp.Text)
	}
	// Narrowing via callback re-runs with one locator.
	resp, err = handler.HandleCallback(context.Background(), 42, "rs|42|opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Text, "opencode") {
		t.Fatalf("narrowed callback should mention opencode, got: %q", resp.Text)
	}
	if contains(resp.Text, "claude") {
		t.Fatalf("narrowed callback should not mention claude, got: %q", resp.Text)
	}
}

func TestHandlerResumeCallbackConfirm(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	workspace := t.TempDir()
	handler, store, reg := newResumeFixture(t, workspace, now)
	reg.Add(&fakeLocator{
		kind: domain.AgentClaude,
		result: domain.ActiveSession{
			Kind:      domain.AgentClaude,
			SessionID: "ses_to_confirm",
			Project:   "demo",
			Directory: workspace,
			TouchedAt: now.Add(-5 * time.Second),
		},
	})
	resp, err := handler.HandleCallback(context.Background(), 42, "rsc|42|claude|ses_to_confirm")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Text, "Listo") {
		t.Fatalf("expected confirmation message, got: %q", resp.Text)
	}
	state, err := store.LoadRuntimeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "ses_to_confirm" {
		t.Fatalf("SessionID = %q, want ses_to_confirm", state.SessionID)
	}
	if state.AgentKind != domain.AgentClaude {
		t.Fatalf("AgentKind = %q, want claude", state.AgentKind)
	}
}

func TestHandlerResumeCallbackRejectsForeignChat(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	handler, _, reg := newResumeFixture(t, t.TempDir(), now)
	reg.Add(&fakeLocator{kind: domain.AgentClaude, result: domain.ActiveSession{
		Kind: domain.AgentClaude, SessionID: "x", TouchedAt: now,
	}})
	resp, err := handler.HandleCallback(context.Background(), 99, "rsc|42|claude|x")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Text, "expir") {
		t.Fatalf("expected expiry message, got: %q", resp.Text)
	}
}
