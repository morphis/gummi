package pr

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// ingestStore opens a bare store for Ingest's own tests — no git worktree
// needed, mirroring internal/state's TestStoreSetPullRequestRoundtrip.
func ingestStore(t *testing.T) *state.Store {
	t.Helper()
	dbPath := t.TempDir() + "/state.db"
	s, err := state.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ingestFeature(t *testing.T, s *state.Store) domain.FeatureID {
	t.Helper()
	id, err := domain.NewFeatureID(1)
	if err != nil {
		t.Fatal(err)
	}
	slug, err := domain.Slugify("PR ingest fixture")
	if err != nil {
		t.Fatal(err)
	}
	f := &domain.Feature{ID: id, Num: 1, Title: "PR ingest fixture", Slug: slug, Stage: domain.StageTodo, Profile: "thrifty"}
	if err := s.CreateFeature(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestIngestCountsWrittenExistingOrphaned exercises the three-way count
// contract: a thread anchored onto worktreeLines is Written; re-ingesting
// the same thread id (the store's (Feature, SourceRef) uniqueness) counts it
// AlreadyExisting instead; and a thread whose hunk can't be located is
// Written *and* Orphaned — the overlapping convention `gummi pr comments
// --ingest` printed before this loop moved into pr.Ingest.
func TestIngestCountsWrittenExistingOrphaned(t *testing.T) {
	s := ingestStore(t)
	f := ingestFeature(t, s)

	worktreeLines := []string{
		"diff --git a/main.go b/main.go",
		"+++ b/main.go",
		"@@ -1,1 +1,1 @@",
		"+func main() {}",
	}
	hit := ReviewThread{
		Id: "thread-1", Path: "main.go",
		DiffHunk: "@@ -1,1 +1,1 @@\n+func main() {}",
		Comments: []ThreadComment{{Id: "c1", AuthorLogin: "reviewer", Body: "needs a doc comment"}},
	}
	miss := ReviewThread{
		Id: "thread-2", Path: "missing.go",
		DiffHunk: "@@ -1,1 +1,1 @@\n+no such line",
		Comments: []ThreadComment{{Id: "c2", AuthorLogin: "reviewer", Body: "this file isn't in the diff"}},
	}

	res, err := Ingest(context.Background(), s, f, worktreeLines, []ReviewThread{hit, miss})
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 2 || res.AlreadyExisting != 0 || res.Orphaned != 1 {
		t.Fatalf("first pass = %+v, want {Written:2 AlreadyExisting:0 Orphaned:1}", res)
	}

	// re-ingesting the same threads: both ids are already stored, so both
	// count as AlreadyExisting; the miss is still Orphaned every time it is
	// written, since orphaned is a property of the write, not a running tally.
	res, err = Ingest(context.Background(), s, f, worktreeLines, []ReviewThread{hit, miss})
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 0 || res.AlreadyExisting != 2 || res.Orphaned != 1 {
		t.Fatalf("second pass = %+v, want {Written:0 AlreadyExisting:2 Orphaned:1}", res)
	}

	anns, err := s.ListDiffAnnotations(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 2 {
		t.Fatalf("stored %d annotations, want 2 (one per thread id, idempotent)", len(anns))
	}
}
