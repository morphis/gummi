package state

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// SetDelegation writes a freeform card's delegation (domain.Delegation):
// the credits its session may hand to cards it creates, and whether the
// person has stopped being asked about each one. A zero budget withdraws
// the delegation. It is a side-channel write, like SetGateApproval: it
// neither moves the stage nor touches updated_at.
func (s *Store) SetDelegation(ctx context.Context, id domain.FeatureID, d domain.Delegation) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("setting delegation for %s: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE features SET delegate_budget = ?, delegate_all = ? WHERE id = ? AND kind = ?`,
		d.Budget, d.ConfirmAll, string(id), string(domain.KindFreeform))
	if err != nil {
		return fmt.Errorf("setting delegation for %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("setting delegation for %s: no such freeform card", id)
	}
	return nil
}

// DelegatedCards lists the cards a freeform card's session created, in
// mint order.
func (s *Store) DelegatedCards(ctx context.Context, parent domain.FeatureID) ([]domain.Feature, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE parent_id = ? ORDER BY num`, string(parent))
	if err != nil {
		return nil, fmt.Errorf("listing %s's cards: %w", parent, err)
	}
	defer rows.Close()
	var out []domain.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
