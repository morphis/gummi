package state

// Dependency edges between cards, backed by the feature_deps join table.
// Forward edges (what a card depends on) live keyed by feature_id;
// reverse edges (who depends on a card) are the indexed lookup on
// depends_on_id. All guards live here as domain errors — a raw SQLite
// constraint failure never escapes.

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// ErrSelfLoop is returned when a card would be made to depend on itself.
var ErrSelfLoop = errors.New("self-dependency")

// ErrCycle is returned when an edge would close a dependency cycle.
var ErrCycle = errors.New("dependency cycle")

// ErrLateAttachment is returned when a card at or past its coding stage
// is asked to take on a new dependency.
var ErrLateAttachment = errors.New("late dependency attachment")

// listEdgeIDs returns the IDs on the other side of the feature_deps
// rows matching whereCol = id, ordered by that other side's ID. It is
// the shared forward/reverse read path: forward listing is
// listEdgeIDs(depends_on_id, feature_id, id) and reverse listing is
// listEdgeIDs(feature_id, depends_on_id, id).
func (s *Store) listEdgeIDs(ctx context.Context, idCol, whereCol string, id domain.FeatureID) ([]domain.FeatureID, error) {
	// The columns are drawn from a closed set of two; the query is chosen
	// here, never interpolated, so a caller cannot inject SQL.
	var q string
	switch {
	case idCol == "depends_on_id" && whereCol == "feature_id":
		q = `SELECT depends_on_id FROM feature_deps WHERE feature_id=? ORDER BY depends_on_id`
	case idCol == "feature_id" && whereCol == "depends_on_id":
		q = `SELECT feature_id FROM feature_deps WHERE depends_on_id=? ORDER BY feature_id`
	default:
		return nil, fmt.Errorf("listEdgeIDs: unsupported column pair (%s, %s)", idCol, whereCol)
	}
	rows, err := s.db.QueryContext(ctx, q, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeatureID
	for rows.Next() {
		var fid string
		if err := rows.Scan(&fid); err != nil {
			return nil, err
		}
		out = append(out, domain.FeatureID(fid))
	}
	return out, rows.Err()
}

// atOrPastCoding delegates to the single domain definition so the store
// and the TUI picker share one predicate.
func atOrPastCoding(st domain.Stage) bool { return domain.AtOrPastCoding(st) }

// hasPath reports whether to is reachable from from via forward edges.
func (s *Store) hasPath(ctx context.Context, from, to domain.FeatureID) (bool, error) {
	visited := map[domain.FeatureID]bool{}
	stack := []domain.FeatureID{from}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == to {
			return true, nil
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		next, err := s.listEdgeIDs(ctx, "depends_on_id", "feature_id", cur)
		if err != nil {
			return false, err
		}
		stack = append(stack, next...)
	}
	return false, nil
}

// WouldCycle reports whether adding the edge featureID→dependsOnID
// would close a dependency cycle — exactly the condition AddDependency
// rejects. It is a read-only pre-check so the TUI picker can reject a
// cycle inline without mutating the store.
func (s *Store) WouldCycle(ctx context.Context, featureID, dependsOnID domain.FeatureID) (bool, error) {
	return s.hasPath(ctx, dependsOnID, featureID)
}

// AddDependency records that featureID depends on dependsOnID. It
// rejects a self-loop, an edge that would close a cycle, an edge onto a
// card already at or past its coding stage, and any reference to an
// unknown card. Re-adding an existing edge is an idempotent no-op.
func (s *Store) AddDependency(ctx context.Context, featureID, dependsOnID domain.FeatureID) error {
	if _, err := s.GetFeature(ctx, featureID); err != nil {
		return fmt.Errorf("%s: %w", featureID, err)
	}
	if _, err := s.GetFeature(ctx, dependsOnID); err != nil {
		return fmt.Errorf("%s: %w", dependsOnID, err)
	}
	if featureID == dependsOnID {
		return fmt.Errorf("%s depends on itself: %w", featureID, ErrSelfLoop)
	}
	dep, err := s.GetFeature(ctx, featureID)
	if err != nil {
		return fmt.Errorf("%s: %w", featureID, err)
	}
	if atOrPastCoding(dep.Stage) {
		return fmt.Errorf("%s is already coding: %w", featureID, ErrLateAttachment)
	}
	reachable, err := s.hasPath(ctx, dependsOnID, featureID)
	if err != nil {
		return err
	}
	if reachable {
		return fmt.Errorf("%s→%s closes a cycle: %w", featureID, dependsOnID, ErrCycle)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO feature_deps (feature_id, depends_on_id) VALUES (?,?)`,
		string(featureID), string(dependsOnID)); err != nil {
		return fmt.Errorf("adding dependency %s→%s: %w", featureID, dependsOnID, err)
	}
	return nil
}

// ListDependencies returns the cards featureID depends on (forward edges).
func (s *Store) ListDependencies(ctx context.Context, featureID domain.FeatureID) ([]domain.FeatureID, error) {
	if _, err := s.GetFeature(ctx, featureID); err != nil {
		return nil, fmt.Errorf("%s: %w", featureID, err)
	}
	return s.listEdgeIDs(ctx, "depends_on_id", "feature_id", featureID)
}

// ListDependents returns the cards that depend on featureID (reverse edges).
func (s *Store) ListDependents(ctx context.Context, featureID domain.FeatureID) ([]domain.FeatureID, error) {
	if _, err := s.GetFeature(ctx, featureID); err != nil {
		return nil, fmt.Errorf("%s: %w", featureID, err)
	}
	return s.listEdgeIDs(ctx, "feature_id", "depends_on_id", featureID)
}

// RemoveDependency deletes the edge featureID→dependsOnID. Removing an
// edge that does not exist is an idempotent no-op; no FK guard applies
// because an edge, not a card, is being removed.
func (s *Store) RemoveDependency(ctx context.Context, featureID, dependsOnID domain.FeatureID) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM feature_deps WHERE feature_id=? AND depends_on_id=?`,
		string(featureID), string(dependsOnID)); err != nil {
		return fmt.Errorf("removing dependency %s→%s: %w", featureID, dependsOnID, err)
	}
	return nil
}
