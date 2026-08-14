// Package planround is the single seam through which the plan-critique
// round counter reads and writes its persistence. The in-memory field the
// driver (and, later, the TUI loop) keeps as its fast path is meaningless
// across a fresh process unless it is seeded from, and every mutation
// mirrored to, the store. This package owns that store contract once so
// the two consumers cannot drift apart and silently re-grant a full round
// budget on every resume.
package planround

import (
	"context"

	"github.com/morphis/gummi/internal/domain"
)

// Store is the persistence surface the counter round-trips through.
// *state.Store satisfies it directly.
type Store interface {
	PlanRounds(ctx context.Context, id domain.FeatureID) (int, error)
	IncrementPlanRounds(ctx context.Context, id domain.FeatureID) error
	ClearPlanRounds(ctx context.Context, id domain.FeatureID) error
}

// Load seeds the in-memory round count from the store on plan-cycle entry,
// so a resume reads the rounds already burned instead of a fresh zero. It
// returns the persisted value for the caller to seed its fast-path field.
func Load(ctx context.Context, s Store, id domain.FeatureID) (int, error) {
	return s.PlanRounds(ctx, id)
}

// Bump persists one replan-under-cap round — the atomic increment that
// makes a burned round outlive the process. It runs at each changes
// verdict that replans inside the cap.
func Bump(ctx context.Context, s Store, id domain.FeatureID) error {
	return s.IncrementPlanRounds(ctx, id)
}

// Reset clears the persisted count on a pass that crosses the gate and on
// an escalation/cap reset, so the next plan cycle starts a fresh budget.
func Reset(ctx context.Context, s Store, id domain.FeatureID) error {
	return s.ClearPlanRounds(ctx, id)
}
