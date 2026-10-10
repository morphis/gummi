package repoview

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

func TestRowsSortBranchesByWhoHoldsThem(t *testing.T) {
	branches := []worktree.BranchInfo{
		{Name: "wip/simon", AheadBase: 2},
		{Name: "spike/old"},
		{Name: "release", Upstream: "origin/release"},
		{Name: "out", Worktree: "/ws/elsewhere", AheadBase: 1},
		{Name: "pr/412", AheadBase: 3},
		{Name: "gl/outcome"},
		{Name: "feat/loader", AheadBase: 4, Upstream: "origin/feat/loader", Ahead: 1},
		{Name: "main", Upstream: "origin/main", Ahead: 2},
	}
	owners := []Owner{
		{Branch: "feat/loader", Card: "FD-001", Landed: true, Cleanable: true},
		{Branch: "pr/412", Card: "FD-002", Adopted: true},
		{Branch: "gl/outcome", Card: "GL-001", Goal: true},
		// a card whose branch was never cut claims nothing that exists
		{Branch: "feat/unstarted", Card: "FD-003"},
		// a card claiming the base (a session in the main checkout's
		// branch) does not make the base a card's
		{Branch: "main", Card: "FF-001"},
	}
	forks := []Fork{{Branch: "release", Card: "FD-004"}, {Branch: "release", Card: "FD-005"}}
	rows := Rows("main", branches, owners, forks)

	type want struct {
		name   string
		group  Group
		card   domain.FeatureID
		delete Delete
		why    string
		push   string
	}
	wants := []want{
		{"main", GroupBase, "", DeleteNo, "", "git push origin main"},
		{"feat/loader", GroupCards, "FD-001", DeleteNo, "", "git push origin feat/loader"},
		{"pr/412", GroupHeld, "FD-002", DeleteNo, "", ""},
		{"gl/outcome", GroupGoals, "GL-001", DeleteNo, "", ""},
		{"out", GroupUnowned, "", DeleteNo, "checked out at /ws/elsewhere", ""},
		{"release", GroupUnowned, "", DeleteNo, "FD-004, FD-005 forks from it", ""},
		{"spike/old", GroupUnowned, "", DeleteOK, "", ""},
		{"wip/simon", GroupUnowned, "", DeleteConfirm, "", ""},
	}
	if len(rows) != len(wants) {
		t.Fatalf("got %d rows, want %d", len(rows), len(wants))
	}
	for i, w := range wants {
		r := rows[i]
		var card domain.FeatureID
		if r.Owner != nil {
			card = r.Owner.Card
		}
		if r.Name != w.name || r.Group != w.group || card != w.card || r.Delete != w.delete || r.Why != w.why || r.Push != w.push {
			t.Errorf("row %d = %s group=%s card=%q delete=%q why=%q push=%q, want %+v", i, r.Name, r.Group, card, r.Delete, r.Why, r.Push, w)
		}
	}
}

// Only a branch nobody holds is ever offered for deletion: the rule the
// view's one destructive button rests on.
func TestOnlyAnUnownedBranchIsDeletable(t *testing.T) {
	branches := []worktree.BranchInfo{{Name: "main"}, {Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	owners := []Owner{{Branch: "a", Card: "FD-001"}, {Branch: "b", Card: "FD-002", Adopted: true}, {Branch: "c", Card: "GL-001", Goal: true}}
	for _, r := range Rows("main", branches, owners, nil) {
		if (r.Delete != DeleteNo) != (r.Name == "d") {
			t.Errorf("%s (%s): delete = %q", r.Name, r.Group, r.Delete)
		}
	}
}

func TestADetachedCheckoutHasNoBase(t *testing.T) {
	rows := Rows("", []worktree.BranchInfo{{Name: "main"}}, nil, nil)
	if len(rows) != 1 || rows[0].Group != GroupUnowned {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPushCommandOnlyWhereAPlainPushWorks(t *testing.T) {
	for _, c := range []struct {
		b    worktree.BranchInfo
		want string
	}{
		{worktree.BranchInfo{Name: "x", Upstream: "origin/x", Ahead: 1}, "git push origin x"},
		{worktree.BranchInfo{Name: "x", Upstream: "fork/y", Ahead: 1}, "git push fork x:y"},
		{worktree.BranchInfo{Name: "x", Upstream: "origin/x"}, ""},
		{worktree.BranchInfo{Name: "x", Upstream: "origin/x", Ahead: 1, Behind: 1}, ""},
		{worktree.BranchInfo{Name: "x", Upstream: "origin/x", UpstreamGone: true}, ""},
		{worktree.BranchInfo{Name: "x", AheadBase: 3}, ""},
	} {
		if got := pushCommand(c.b); got != c.want {
			t.Errorf("pushCommand(%+v) = %q, want %q", c.b, got, c.want)
		}
	}
}
