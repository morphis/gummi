package pr

import (
	"context"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// IngestResult tallies one Ingest pass. Written and Orphaned overlap by
// design: a thread whose hunk can't be located onto worktreeLines is still
// written (with an empty anchor, degrading to a file-level row at render
// time) and counted as orphaned — the same convention `gummi pr comments
// --ingest` has always printed, kept here rather than replaced now that the
// counting moved.
type IngestResult struct {
	Written         int
	AlreadyExisting int
	Orphaned        int
}

// Ingest anchors each of threads onto worktreeLines and writes one
// DiffAnnotation per thread into store, for feature f. A thread whose id
// already carries a stored annotation (store's (Feature, SourceRef)
// uniqueness) is re-written idempotently and counted as AlreadyExisting
// rather than Written — the same snapshot-then-write shape
// runPRCommentsIngest used before this was extracted from it. Callers own
// producing worktreeLines (a diff of the feature's current worktree); Ingest
// has no reason to know how that diff was taken, only how to anchor threads
// against it.
func Ingest(ctx context.Context, store *state.Store, f domain.FeatureID, worktreeLines []string, threads []ReviewThread) (IngestResult, error) {
	existing, err := store.ListDiffAnnotations(ctx, f)
	if err != nil {
		return IngestResult{}, err
	}
	seen := map[string]bool{}
	for _, a := range existing {
		if a.SourceRef != "" {
			seen[a.SourceRef] = true
		}
	}

	var res IngestResult
	now := time.Now()
	for _, t := range threads {
		if seen[t.Id] {
			res.AlreadyExisting++
		} else {
			res.Written++
			seen[t.Id] = true
		}
		ann := AnnotationFor(f, t, worktreeLines)
		if ann.Anchor == "" {
			res.Orphaned++
		}
		if _, err := store.AddDiffAnnotation(ctx, ann, now); err != nil {
			return res, err
		}
	}
	return res, nil
}
