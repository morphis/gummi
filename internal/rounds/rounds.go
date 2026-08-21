// Package rounds is the single seam through which every automatic round
// counter — plan-critique, review→fix, and (via the shared review kind)
// the research slice's review loop — reads and writes its persistence.
// The in-memory field the driver (and the TUI loop) keeps as its fast
// path is meaningless across a fresh process unless it is seeded from,
// and every mutation mirrored to, the store. This package owns that
// store contract once, keyed by round kind, so its consumers cannot
// drift apart and silently re-grant a full round budget on every
// resume. It replaces the former internal/planround and
// internal/reviewround packages, which owned the same shape twice.
package rounds

import (
	"context"

	"github.com/morphis/gummi/internal/domain"
)

// Store is the persistence surface the counter round-trips through,
// keyed by (id, round_kind). *state.Store satisfies it directly.
type Store interface {
	Rounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) (int, error)
	IncrementRounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) error
	ClearRounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) error
}

// Load seeds the in-memory round count from the store on loop entry, so
// a resume reads the rounds already burned instead of a fresh zero. It
// returns the persisted value for the caller to seed its fast-path field.
func Load(ctx context.Context, s Store, id domain.FeatureID, kind domain.RoundKind) (int, error) {
	return s.Rounds(ctx, id, kind)
}

// Bump persists one under-cap round — the atomic increment that makes a
// burned round outlive the process. It runs at each changes verdict that
// bounces back inside the cap.
func Bump(ctx context.Context, s Store, id domain.FeatureID, kind domain.RoundKind) error {
	return s.IncrementRounds(ctx, id, kind)
}

// Reset clears the persisted count on a pass that crosses the gate and on
// an escalation/cap reset, so the next loop starts a fresh budget.
func Reset(ctx context.Context, s Store, id domain.FeatureID, kind domain.RoundKind) error {
	return s.ClearRounds(ctx, id, kind)
}
