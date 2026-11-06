package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// SetSessionWorkflows records a person's per-session switch for workflows
// and specs: nil follows the workspace setting, true or false overrides it.
// Like the publish allowance it is not part of UpdateFeature.
func (s *Store) SetSessionWorkflows(ctx context.Context, id domain.FeatureID, on *bool) error {
	v := ""
	if on != nil {
		v = "off"
		if *on {
			v = "on"
		}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE features SET session_workflows = ? WHERE id = ?`, v, string(id))
	if err != nil {
		return fmt.Errorf("setting workflows for %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("setting workflows for %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// SessionWorkflows is the session's own switch: nil when it follows the
// workspace setting (or the card is missing).
func (s *Store) SessionWorkflows(ctx context.Context, id domain.FeatureID) (*bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT session_workflows FROM features WHERE id = ?`, string(id)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading workflows for %s: %w", id, err)
	}
	return switchValue(v), nil
}

func switchValue(v string) *bool {
	switch v {
	case "on":
		t := true
		return &t
	case "off":
		f := false
		return &f
	}
	return nil
}

// SessionWorkflowOverrides are the cards with a switch of their own, read in
// one query so the board never reads the store once per row.
func (s *Store) SessionWorkflowOverrides(ctx context.Context) (map[domain.FeatureID]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_workflows FROM features WHERE session_workflows != ''`)
	if err != nil {
		return nil, fmt.Errorf("listing workflow switches: %w", err)
	}
	defer rows.Close()
	out := map[domain.FeatureID]bool{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		if p := switchValue(v); p != nil {
			out[domain.FeatureID(id)] = *p
		}
	}
	return out, rows.Err()
}
