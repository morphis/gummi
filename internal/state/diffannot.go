package state

import (
	"context"
	"time"

	"github.com/morphia/gummi/internal/domain"
)

// AddDiffAnnotation stores a line comment on a feature's diff and returns
// its new ID. CreatedAt is set from `at` (injectable for tests).
func (s *Store) AddDiffAnnotation(ctx context.Context, a domain.DiffAnnotation, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO diff_annotations (feature_id, file, anchor, excerpt, comment, resolved, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(a.Feature), a.File, a.Anchor, a.Excerpt, a.Comment, boolToInt(a.Resolved),
		at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListDiffAnnotations returns a feature's diff annotations, oldest first.
func (s *Store) ListDiffAnnotations(ctx context.Context, id domain.FeatureID) ([]domain.DiffAnnotation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feature_id, file, anchor, excerpt, comment, resolved, created_at
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
		if err := rows.Scan(&a.ID, &fid, &a.File, &a.Anchor, &a.Excerpt, &a.Comment, &resolved, &created); err != nil {
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
