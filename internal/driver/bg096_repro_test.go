package driver

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// TestBG096CreatedEventDescribesTheKindItCreated: the created event told
// a caller two things about a research card that were not true of it —
// a branch name, and the quick route.
//
// The branch is the one that could be acted on: every research stage is
// worktree-less, so the name announced was one nothing would ever
// create. The route came from the caller's own --full flag, while Create
// overrides the flags for research a dozen lines earlier, so it reported
// "quick" for a kind whose own doc comment says it has no quick one-pass
// route.
//
// Driven through Create so the assertion is on the line a caller really
// reads, and checked against workflow's own answer per kind rather than
// against the one kind the drive saw.
func TestBG096CreatedEventDescribesTheKindItCreated(t *testing.T) {
	cases := []struct {
		kind domain.Kind
		full bool
		want string // expected route
	}{
		{domain.KindFeature, false, "quick"},
		{domain.KindFeature, true, "full"},
		{domain.KindBug, false, "quick"},
		{domain.KindResearch, false, "full"},
		{domain.KindResearch, true, "full"},
	}
	for _, c := range cases {
		h := newHarness(t, false, nil)
		h.fake.Caps.ReadOnlyEnforce = true
		d := h.driver(Options{Full: c.full})
		f, err := d.Create(context.Background(), c.kind, "some piece of work")
		if err != nil {
			t.Fatalf("%s full=%v: Create: %v", c.kind, c.full, err)
		}
		var created map[string]any
		for _, e := range h.events() {
			if e["event"] == "created" {
				created = e
			}
		}
		if created == nil {
			t.Fatalf("%s full=%v: no created event", c.kind, c.full)
		}
		if got := created["route"]; got != c.want {
			t.Errorf("%s full=%v: route = %v, want %q", c.kind, c.full, got, c.want)
		}
		// the branch is announced exactly where one will exist
		branchy := workflow.NeedsWorktree(c.kind, workflow.WorkStage(c.kind))
		_, got := created["branch"]
		if got != branchy {
			t.Errorf("%s full=%v: created event carries a branch = %v, want %v (%v)",
				c.kind, c.full, got, branchy, created)
		}
		if branchy && created["branch"] != f.BranchName() {
			t.Errorf("%s: branch = %v, want %q", c.kind, created["branch"], f.BranchName())
		}
	}
}
