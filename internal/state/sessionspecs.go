package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
)

// A session's specs (DESIGN §25.3): a list, one per piece of work, each a
// file in the workspace and a numbered run of revisions here. A spec's
// state is not stored: it comes from the runs on it (domain.SpecStateOf).

// AddSessionSpec adds a spec to session id, numbered after its last, with
// its file at domain.SessionSpecPath. It writes no file and no revision:
// the engine does both.
func (s *Store) AddSessionSpec(ctx context.Context, id domain.FeatureID, title string, at time.Time) (domain.SessionSpec, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SessionSpec{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(n) FROM session_specs WHERE feature_id=?`, string(id)).Scan(&last); err != nil {
		return domain.SessionSpec{}, err
	}
	sp := domain.SessionSpec{
		Feature: id, N: int(last.Int64) + 1, Title: title,
		State: domain.SpecDraft, CreatedAt: at.UTC(),
	}
	sp.Path = domain.SessionSpecPath(id, sp.N, title)
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_specs (feature_id, n, title, path, created_at) VALUES (?,?,?,?,?)`,
		string(id), sp.N, sp.Title, sp.Path, sp.CreatedAt.Format(timeFmt)); err != nil {
		return domain.SessionSpec{}, fmt.Errorf("adding a spec to %s: %w", id, err)
	}
	return sp, tx.Commit()
}

// SessionSpecs lists a session's specs, first to last, each with its
// state from the runs on it.
func (s *Store) SessionSpecs(ctx context.Context, id domain.FeatureID) ([]domain.SessionSpec, error) {
	out, err := s.sessionSpecRows(ctx, id)
	if err != nil || len(out) == 0 {
		return out, err
	}
	runs, err := s.SessionRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range out {
		var on []domain.Run
		for _, r := range runs {
			if r.Spec == out[i].N {
				on = append(on, r)
			}
		}
		out[i].State = domain.SpecStateOf(on)
	}
	return out, nil
}

// sessionSpecRows reads a session's spec rows, their state unset. The rows
// are closed before SessionSpecs reads the runs: the store has a single
// connection.
func (s *Store) sessionSpecRows(ctx context.Context, id domain.FeatureID) ([]domain.SessionSpec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n, title, path, created_at FROM session_specs WHERE feature_id=? ORDER BY n`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SessionSpec
	for rows.Next() {
		sp := domain.SessionSpec{Feature: id}
		var created string
		if err := rows.Scan(&sp.N, &sp.Title, &sp.Path, &created); err != nil {
			return nil, err
		}
		sp.CreatedAt, _ = time.Parse(timeFmt, created)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// SessionSpec reads one of a session's specs; ok is false when it has no
// spec numbered n.
func (s *Store) SessionSpec(ctx context.Context, id domain.FeatureID, n int) (domain.SessionSpec, bool, error) {
	specs, err := s.SessionSpecs(ctx, id)
	if err != nil {
		return domain.SessionSpec{}, false, err
	}
	for _, sp := range specs {
		if sp.N == n {
			return sp, true, nil
		}
	}
	return domain.SessionSpec{}, false, nil
}

// SessionSpecFile is spec n's file under the workspace root; ok is false
// when the session has no such spec.
func (s *Store) SessionSpecFile(ctx context.Context, root string, id domain.FeatureID, n int) (string, bool) {
	var rel string
	err := s.db.QueryRowContext(ctx, `SELECT path FROM session_specs WHERE feature_id=? AND n=?`, string(id), n).Scan(&rel)
	if err != nil {
		return "", false
	}
	return gummidir.FromRel(root, rel), true
}

// AddSpecRevision saves content as spec n's next revision and returns its
// number. Revisions count from 1 per spec.
func (s *Store) AddSpecRevision(ctx context.Context, id domain.FeatureID, n int, content, by string, at time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(rev) FROM spec_revisions WHERE feature_id=? AND spec_n=?`, string(id), n).Scan(&last); err != nil {
		return 0, err
	}
	rev := int(last.Int64) + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO spec_revisions (feature_id, spec_n, rev, content, by, at) VALUES (?,?,?,?,?,?)`,
		string(id), n, rev, content, by, at.UTC().Format(timeFmt)); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

// SpecRevisions lists spec n's revisions, newest first, without their
// content.
func (s *Store) SpecRevisions(ctx context.Context, id domain.FeatureID, n int) ([]domain.SpecRevision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rev, by, at FROM spec_revisions WHERE feature_id=? AND spec_n=? ORDER BY rev DESC`, string(id), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SpecRevision
	for rows.Next() {
		r := domain.SpecRevision{Spec: n}
		var at string
		if err := rows.Scan(&r.Rev, &r.By, &at); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(timeFmt, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SpecRevision reads one revision of spec n whole; ok is false when there
// is none.
func (s *Store) SpecRevision(ctx context.Context, id domain.FeatureID, n, rev int) (domain.SpecRevision, bool, error) {
	r := domain.SpecRevision{Spec: n, Rev: rev}
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT content, by, at FROM spec_revisions WHERE feature_id=? AND spec_n=? AND rev=?`,
		string(id), n, rev).Scan(&r.Content, &r.By, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.At, _ = time.Parse(timeFmt, at)
	return r, true, nil
}
