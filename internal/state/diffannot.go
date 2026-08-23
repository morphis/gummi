package state

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// AddDiffAnnotation stores a line comment on a feature's diff and returns
// its row id. CreatedAt is set from `at` (injectable for tests).
//
// When a.SourceRef is non-empty, this is an idempotent introducer: a
// second call with the same (Feature, SourceRef) inserts nothing and
// returns the existing row's id, leaving that row's other fields (and
// created_at) untouched. It reconciles nothing — content differences on
// a conflicting call are silently ignored. a.SourceRef == "" (the
// reviewer/TUI sentinel) always inserts a new row, as before.
func (s *Store) AddDiffAnnotation(ctx context.Context, a domain.DiffAnnotation, at time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO diff_annotations (feature_id, file, anchor, excerpt, comment, resolved, created_at, source_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (feature_id, source_ref) WHERE source_ref != '' DO NOTHING
		RETURNING id`,
		string(a.Feature), a.File, a.Anchor, a.Excerpt, a.Comment, boolToInt(a.Resolved),
		at.UTC().Format(time.RFC3339Nano), a.SourceRef).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `
			SELECT id FROM diff_annotations WHERE feature_id=? AND source_ref=?`,
			string(a.Feature), a.SourceRef).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ListDiffAnnotations returns a feature's diff annotations, oldest first.
func (s *Store) ListDiffAnnotations(ctx context.Context, id domain.FeatureID) ([]domain.DiffAnnotation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feature_id, file, anchor, excerpt, comment, resolved, created_at, source_ref
		FROM diff_annotations WHERE feature_id=? ORDER BY id`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DiffAnnotation
	for rows.Next() {
		var a domain.DiffAnnotation
		var fid, created string
		var resolved int
		if err := rows.Scan(&a.ID, &fid, &a.File, &a.Anchor, &a.Excerpt, &a.Comment, &resolved, &created, &a.SourceRef); err != nil {
			return nil, err
		}
		a.Feature = domain.FeatureID(fid)
		a.Resolved = resolved != 0
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetDiffAnnotationResolved flips an annotation's resolved flag.
func (s *Store) SetDiffAnnotationResolved(ctx context.Context, annID int64, resolved bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE diff_annotations SET resolved=? WHERE id=?`, boolToInt(resolved), annID)
	return err
}

// DeleteDiffAnnotation removes an annotation.
func (s *Store) DeleteDiffAnnotation(ctx context.Context, annID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM diff_annotations WHERE id=?`, annID)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
