package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// SetObjective writes a freeform card's objective (domain.Objective),
// replacing any it had; nil clears it. Only a freeform card takes one: a
// workflow card already loops, and its stages' critiques are its auditor.
func (s *Store) SetObjective(ctx context.Context, id domain.FeatureID, o *domain.Objective) error {
	if o == nil {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM objectives WHERE feature_id = ?`, string(id)); err != nil {
			return fmt.Errorf("clearing %s's objective: %w", id, err)
		}
		return nil
	}
	if err := o.Validate(); err != nil {
		return fmt.Errorf("setting %s's objective: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO objectives (feature_id, text, check_cmd, state, turns, stuck_streak, note, base_rev, set_at, set_by)
		SELECT id, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM features WHERE id = ? AND kind = ?
		ON CONFLICT(feature_id) DO UPDATE SET
			text = excluded.text, check_cmd = excluded.check_cmd, state = excluded.state,
			turns = excluded.turns, stuck_streak = excluded.stuck_streak, note = excluded.note,
			base_rev = excluded.base_rev, set_at = excluded.set_at, set_by = excluded.set_by`,
		o.Text, o.Check, string(o.State), o.Turns, o.StuckStreak, o.Note, o.BaseRev,
		o.SetAt.UTC().Format(timeFmt), o.SetBy, string(id), string(domain.KindFreeform))
	if err != nil {
		return fmt.Errorf("setting %s's objective: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("setting %s's objective: only a freeform card takes one", id)
	}
	return nil
}

// Objective reads a card's objective, nil when it has none.
func (s *Store) Objective(ctx context.Context, id domain.FeatureID) (*domain.Objective, error) {
	var o domain.Objective
	var state, setAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT text, check_cmd, state, turns, stuck_streak, note, base_rev, set_at, set_by
		FROM objectives WHERE feature_id = ?`, string(id)).
		Scan(&o.Text, &o.Check, &state, &o.Turns, &o.StuckStreak, &o.Note, &o.BaseRev, &setAt, &o.SetBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's objective: %w", id, err)
	}
	o.State = domain.ObjectiveState(state)
	o.SetAt, _ = time.Parse(timeFmt, setAt)
	return &o, nil
}
