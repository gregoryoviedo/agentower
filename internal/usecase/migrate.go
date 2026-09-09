package usecase

import (
	"context"
	"os"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// osStat is a tiny indirection so the bot handler stays testable
// without dragging in os.Stat directly in every test fixture.
var osStat = func(path string) (os.FileInfo, error) { return os.Stat(path) }

// agentMigrationScanner lets the handler count and migrate legacy
// sessions without depending on the sqlite package directly. The
// default implementation uses the StateRepository contract; tests can
// substitute a fake.
type agentMigrationScanner interface {
	CountLegacySessions(ctx context.Context) (int, error)
	MarkLegacySessionsAsOpenCode(ctx context.Context) (int, error)
}

// countLegacySessions returns the number of rows in runtime_state
// whose agent_kind is empty (the historical default). Once the
// column has the DEFAULT 'opencode' migration applied at boot, this
// should normally be zero.
func (h *Handler) countLegacySessions(ctx context.Context, _ int64) (int, error) {
	scanner, ok := h.state.(agentMigrationScanner)
	if !ok {
		return 0, nil
	}
	return scanner.CountLegacySessions(ctx)
}

// markLegacyAsOpenCode rewrites every runtime_state row whose
// agent_kind is empty to 'opencode' and writes a matching agent_state
// entry so /agents lists opencode as enabled.
func (h *Handler) markLegacyAsOpenCode(ctx context.Context, chatID int64) (int, error) {
	scanner, ok := h.state.(agentMigrationScanner)
	if !ok {
		return 0, domain.ErrMigrationNotNeeded
	}
	count, err := scanner.MarkLegacySessionsAsOpenCode(ctx)
	if err != nil {
		return 0, err
	}
	if err := h.state.SaveAgentState(ctx, chatID, domain.AgentOpenCode, true); err != nil {
		return count, err
	}
	return count, nil
}
