package state

// Stack membership, in the store. A stack's identity is a `stacks` row;
// its membership and order are features.stack_id / stack_pos, so there is
// exactly one place either can be read from.
//
// Every write that changes membership runs in one transaction and
// renumbers the whole stack, because positions are contiguous and a gap
// would make "the card below me" ambiguous — which is the one question
// the base resolver asks of this data.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// ErrStackRepoMismatch reports an attempt to put a card into a stack
// belonging to a different repository. A branch cannot fork from a branch
// in another checkout, so this is refused rather than resolved.
var ErrStackRepoMismatch = errors.New("card and stack belong to different repositories")

// ErrStackNotStackable reports a card whose kind has no branch to chain:
// a research card (which runs in a detached scratch tree) or a goal
// (whose own cards already share one goal branch).
var ErrStackNotStackable = errors.New("card kind has no branch to stack")

// CreateStack records a new stack. The caller derives the id, normally
// from the bottom card's slug via domain.NewStackID.
func (s *Store) CreateStack(ctx context.Context, st *domain.Stack, at time.Time) error {
	if err := st.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO stacks (id, name, repo, created_at) VALUES (?,?,?,?)`,
		string(st.ID), st.Name, st.Repo, at.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("creating stack %s: %w", st.ID, err)
	}
	return nil
}

// GetStack reads one stack record. Returns ErrNotFound when there is none.
func (s *Store) GetStack(ctx context.Context, id domain.StackID) (domain.Stack, error) {
	var st domain.Stack
	var sid string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, repo FROM stacks WHERE id = ?`, string(id)).
		Scan(&sid, &st.Name, &st.Repo)
	if errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("stack %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return st, err
	}
	st.ID = domain.StackID(sid)
	return st, nil
}

// ListStacks returns every stack, newest last.
func (s *Store) ListStacks(ctx context.Context) ([]domain.Stack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, repo FROM stacks ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Stack
	for rows.Next() {
		var st domain.Stack
		var sid string
		if err := rows.Scan(&sid, &st.Name, &st.Repo); err != nil {
			return nil, err
		}
		st.ID = domain.StackID(sid)
		out = append(out, st)
	}
	return out, rows.Err()
}

// RenameStack changes the name the board shows. The id never changes —
// it is referenced by every member row.
func (s *Store) RenameStack(ctx context.Context, id domain.StackID, name string) error {
	st := domain.Stack{ID: id, Name: name}
	if err := st.Validate(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE stacks SET name = ? WHERE id = ?`, name, string(id))
	if err != nil {
		return fmt.Errorf("renaming stack %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("stack %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteStack removes a stack record. It refuses while cards still point
// at it: an orphaned stack_id would leave a card resolving its base
// against a stack that no longer exists.
func (s *Store) DeleteStack(ctx context.Context, id domain.StackID) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM features WHERE stack_id = ?`, string(id)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("stack %s still has %d card(s)", id, n)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM stacks WHERE id = ?`, string(id))
	return err
}

// ListStackCards returns the stack's members in order, bottom first.
func (s *Store) ListStackCards(ctx context.Context, id domain.StackID) ([]domain.Feature, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE stack_id = ? ORDER BY stack_pos, num`, string(id))
	if err != nil {
		return nil, err
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

// AddToStack appends card to the top of a stack, or inserts it at pos
// when pos is within the stack (everything from pos up shifts one place).
// A pos of -1 means "on top".
//
// It refuses a card that belongs to another stack, a repo that does not
// match the stack's, and a kind with no branch to chain. It does NOT
// refuse a card that is already running: joining a stack changes where a
// branch forks from, and the restack that follows is exactly the work the
// tick does for every other base change.
func (s *Store) AddToStack(ctx context.Context, id domain.StackID, card domain.FeatureID, pos int) error {
	return s.inStackTx(ctx, id, func(tx *sql.Tx, st domain.Stack, members []domain.Feature) error {
		f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureCols+` FROM features WHERE id = ?`, string(card)))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("card %s: %w", card, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if f.StackID != "" && f.StackID != id {
			return fmt.Errorf("adding %s to %s: it already belongs to stack %s", card, id, f.StackID)
		}
		if k := f.Kind; k == domain.KindResearch || k == domain.KindGoal {
			return fmt.Errorf("adding %s to %s: %w", card, id, ErrStackNotStackable)
		}
		if f.Repo != st.Repo {
			return fmt.Errorf("adding %s (repo %q) to %s (repo %q): %w",
				card, f.Repo, id, st.Repo, ErrStackRepoMismatch)
		}
		// Rebuild the order with card at the requested place, then write
		// contiguous positions over the result.
		var order []domain.FeatureID
		for _, m := range members {
			if m.ID == card {
				continue // a move within the stack, not a second copy
			}
			order = append(order, m.ID)
		}
		if pos < 0 || pos > len(order) {
			pos = len(order)
		}
		order = append(order, "")
		copy(order[pos+1:], order[pos:])
		order[pos] = card
		return renumber(ctx, tx, id, order)
	})
}

// RemoveFromStack takes card out of its stack and closes the gap, so the
// cards above it move down a place and their bases change with them.
func (s *Store) RemoveFromStack(ctx context.Context, card domain.FeatureID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureCols+` FROM features WHERE id = ?`, string(card)))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("card %s: %w", card, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if f.StackID == "" {
		return nil // not stacked: nothing to do
	}
	stackID := f.StackID
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET stack_id = '', stack_pos = 0 WHERE id = ?`, string(card)); err != nil {
		return fmt.Errorf("removing %s from %s: %w", card, stackID, err)
	}
	members, err := stackMembersTx(ctx, tx, stackID)
	if err != nil {
		return err
	}
	order := make([]domain.FeatureID, 0, len(members))
	for _, m := range members {
		order = append(order, m.ID)
	}
	if err := renumber(ctx, tx, stackID, order); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveInStack moves card to an absolute position in its own stack,
// clamped to the ends. Everything between shifts to make room.
func (s *Store) MoveInStack(ctx context.Context, card domain.FeatureID, pos int) error {
	f, err := s.GetFeature(ctx, card)
	if err != nil {
		return err
	}
	if f.StackID == "" {
		return fmt.Errorf("%s is not in a stack", card)
	}
	return s.AddToStack(ctx, f.StackID, card, pos)
}

// inStackTx runs fn inside a transaction with the stack record and its
// current members loaded, then commits.
func (s *Store) inStackTx(ctx context.Context, id domain.StackID, fn func(*sql.Tx, domain.Stack, []domain.Feature) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	var st domain.Stack
	var sid string
	err = tx.QueryRowContext(ctx, `SELECT id, name, repo FROM stacks WHERE id = ?`, string(id)).
		Scan(&sid, &st.Name, &st.Repo)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("stack %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return err
	}
	st.ID = domain.StackID(sid)
	members, err := stackMembersTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := fn(tx, st, members); err != nil {
		return err
	}
	return tx.Commit()
}

// stackMembersTx reads the stack's members in order inside a transaction.
func stackMembersTx(ctx context.Context, tx *sql.Tx, id domain.StackID) ([]domain.Feature, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE stack_id = ? ORDER BY stack_pos, num`, string(id))
	if err != nil {
		return nil, err
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

// renumber writes contiguous positions over order, which is the whole
// stack from bottom to top. Positions are rewritten unconditionally
// rather than diffed: the stack is short, and one UPDATE per member is
// cheaper to reason about than working out which ones moved.
func renumber(ctx context.Context, tx *sql.Tx, id domain.StackID, order []domain.FeatureID) error {
	for i, cardID := range order {
		if _, err := tx.ExecContext(ctx,
			`UPDATE features SET stack_id = ?, stack_pos = ? WHERE id = ?`,
			string(id), i, string(cardID)); err != nil {
			return fmt.Errorf("renumbering %s at %d: %w", cardID, i, err)
		}
	}
	return nil
}

// SetBase records the branch a card forks from.
func (s *Store) SetBase(ctx context.Context, card domain.FeatureID, base string) error {
	if err := domain.ValidateBaseBranch(base); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE features SET base = ? WHERE id = ?`, base, string(card))
	if err != nil {
		return fmt.Errorf("setting the base of %s: %w", card, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("card %s: %w", card, ErrNotFound)
	}
	return nil
}
