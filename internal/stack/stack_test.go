package stack

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// chain builds a three-card stack on main, every card with a tree and
// nothing stale — the resting state the other tests perturb.
func chain() View {
	return View{
		ID: "rule-engine", Name: "rule engine", Base: "main",
		Members: []Member{
			{ID: "FD-101", Pos: 0, Branch: "feat/FD-101-parser", HasTree: true},
			{ID: "FD-104", Pos: 1, Branch: "feat/FD-104-eval", HasTree: true},
			{ID: "FD-103", Pos: 2, Branch: "feat/FD-103-cli", HasTree: true},
		},
	}
}

func TestBaseForChainsTheStack(t *testing.T) {
	v := chain()
	for _, tc := range []struct {
		id   domain.FeatureID
		want string
	}{
		{"FD-101", "main"},               // the bottom forks from the stack's base
		{"FD-104", "feat/FD-101-parser"}, // and everything above from the one below
		{"FD-103", "feat/FD-104-eval"},
	} {
		if got := BaseFor(v, tc.id); got != tc.want {
			t.Errorf("BaseFor(%s) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestBaseForUnknownCardGetsTheStackBase(t *testing.T) {
	if got := BaseFor(chain(), "FD-999"); got != "main" {
		t.Errorf("BaseFor(stranger) = %q, want the stack base", got)
	}
}

// A landed card stops being something to fork from: its commits are in
// the base already. This is the whole landing cascade.
func TestBaseForSkipsLandedCards(t *testing.T) {
	v := chain()
	v.Members[0].Landed = true
	if got := BaseFor(v, "FD-104"); got != "main" {
		t.Errorf("after the bottom landed, BaseFor(FD-104) = %q, want main", got)
	}
	if got := BaseFor(v, "FD-103"); got != "feat/FD-104-eval" {
		t.Errorf("BaseFor(FD-103) = %q, want the card below it", got)
	}
	// Two landed in a row: keep walking down to the base.
	v.Members[1].Landed = true
	if got := BaseFor(v, "FD-103"); got != "main" {
		t.Errorf("with two landed below, BaseFor(FD-103) = %q, want main", got)
	}
}

// A card that has not cut its branch yet has no ref to fork from, so the
// cards above it look past it.
func TestBaseForSkipsCardsWithNoBranchYet(t *testing.T) {
	v := chain()
	v.Members[1].HasTree = false
	if got := BaseFor(v, "FD-103"); got != "feat/FD-101-parser" {
		t.Errorf("BaseFor(FD-103) = %q, want the nearest card that has a branch", got)
	}
}

func TestBelowNamesTheCardNotTheBranch(t *testing.T) {
	v := chain()
	if id, ok := Below(v, "FD-104"); !ok || id != "FD-101" {
		t.Errorf("Below(FD-104) = %q,%v, want FD-101,true", id, ok)
	}
	if _, ok := Below(v, "FD-101"); ok {
		t.Error("the bottom card should have nothing below it")
	}
}

func TestDecideRestacksTheLowestStaleCard(t *testing.T) {
	v := chain()
	v.Members[1].Stale = true
	v.Members[2].Stale = true
	got := Decide(v)
	if len(got) != 1 {
		t.Fatalf("Decide returned %d actions, want exactly 1", len(got))
	}
	if got[0].Kind != ActionRestack || got[0].Card != "FD-104" {
		t.Fatalf("Decide = %v on %s, want a restack of FD-104", got[0].Kind, got[0].Card)
	}
	if got[0].Base != "feat/FD-101-parser" {
		t.Errorf("restack base = %q, want the branch below", got[0].Base)
	}
}

func TestDecideIsQuietWhenNothingIsStale(t *testing.T) {
	if got := Decide(chain()); len(got) != 0 {
		t.Errorf("Decide on a settled stack = %v, want nothing", got)
	}
}

// A replay must never race a session: the walk waits rather than
// rewriting a branch an agent is committing to.
func TestDecideWaitsForARunningSession(t *testing.T) {
	v := chain()
	v.Members[0].Running = true
	v.Members[1].Stale = true
	got := Decide(v)
	if len(got) != 1 || got[0].Kind != ActionWait || got[0].Card != "FD-101" {
		t.Fatalf("Decide = %+v, want a wait on FD-101", got)
	}
}

func TestDecideWaitsOnADirtyWorktree(t *testing.T) {
	v := chain()
	v.Members[1].Stale = true
	v.Members[1].Dirty = true
	got := Decide(v)
	if len(got) != 1 || got[0].Kind != ActionWait {
		t.Fatalf("Decide = %+v, want a wait", got)
	}
}

func TestDecideIgnoresLandedAndTreelessCards(t *testing.T) {
	v := chain()
	v.Members[0].Landed, v.Members[0].Stale = true, true
	v.Members[1].HasTree, v.Members[1].Stale = false, true
	if got := Decide(v); len(got) != 0 {
		t.Errorf("Decide = %+v, want nothing to do", got)
	}
}

func TestStaleListsOnlyReplayableCards(t *testing.T) {
	v := chain()
	v.Members[0].Stale, v.Members[0].Landed = true, true
	v.Members[1].Stale = true
	v.Members[2].Stale, v.Members[2].HasTree = true, false
	got := Stale(v)
	if len(got) != 1 || got[0] != "FD-104" {
		t.Errorf("Stale = %v, want just FD-104", got)
	}
}

func TestLandBlockerOrdersLandingOnly(t *testing.T) {
	v := chain()
	if _, ok := LandBlocker(v, "FD-101"); ok {
		t.Error("the bottom card is never blocked from landing")
	}
	if id, ok := LandBlocker(v, "FD-104"); !ok || id != "FD-101" {
		t.Errorf("LandBlocker(FD-104) = %q,%v, want FD-101,true", id, ok)
	}
	// It names the LOWEST unlanded card, which is the one to land next.
	if id, ok := LandBlocker(v, "FD-103"); !ok || id != "FD-101" {
		t.Errorf("LandBlocker(FD-103) = %q, want the lowest unlanded card", id)
	}
	v.Members[0].Landed = true
	if _, ok := LandBlocker(v, "FD-104"); ok {
		t.Error("FD-104 should be free to land once FD-101 has")
	}
	if id, ok := LandBlocker(v, "FD-103"); !ok || id != "FD-104" {
		t.Errorf("LandBlocker(FD-103) = %q, want FD-104", id)
	}
}

// THE guarantee the whole design rests on: a stack orders landing and
// nothing else. There is no state a member can be in that makes the
// policy report another member as unable to run — if there were, a card
// could not be worked on until the one below it had landed, which is the
// serialized waiting a stack exists to remove.
//
// Asserted structurally rather than by inspection: whatever the stack
// looks like, Decide only ever asks for a restack or a wait-for-settle,
// and never reports a card as blocked.
func TestAStackNeverBlocksWork(t *testing.T) {
	v := chain()
	// The bottom card is parked in review with an open PR while the two
	// above it are mid-implement — the exact shape the feature is for.
	v.Members[0].Running = false
	v.Members[1].Running, v.Members[2].Running = true, true
	for _, a := range Decide(v) {
		if a.Kind != ActionRestack && a.Kind != ActionWait {
			t.Fatalf("Decide produced %v — a stack must never gate work", a.Kind)
		}
	}
	// And a card high in the stack is free to land as soon as the ones
	// below it have, with no reference to what stage any of them is at.
	v.Members[0].Landed, v.Members[1].Landed = true, true
	if _, ok := LandBlocker(v, "FD-103"); ok {
		t.Error("FD-103 should be free to land once everything below it has")
	}
}

func TestBottomAndLiveTrackTheLandingCascade(t *testing.T) {
	v := chain()
	if m, ok := Bottom(v); !ok || m.ID != "FD-101" {
		t.Errorf("Bottom = %q, want FD-101", m.ID)
	}
	if got := Live(v); got != 3 {
		t.Errorf("Live = %d, want 3", got)
	}
	v.Members[0].Landed = true
	if m, ok := Bottom(v); !ok || m.ID != "FD-104" {
		t.Errorf("after a landing Bottom = %q, want FD-104", m.ID)
	}
	if got := Live(v); got != 2 {
		t.Errorf("Live = %d, want 2", got)
	}
	for i := range v.Members {
		v.Members[i].Landed = true
	}
	if _, ok := Bottom(v); ok {
		t.Error("a fully landed stack has no bottom")
	}
}

// A stack of one is legal and sits on its own base — the state a stack
// passes through when every card above the bottom has landed.
func TestSingleMemberStack(t *testing.T) {
	v := View{ID: "solo", Base: "release-2.1", Members: []Member{
		{ID: "FD-200", Pos: 0, Branch: "feat/FD-200-x", HasTree: true, Stale: true},
	}}
	if got := BaseFor(v, "FD-200"); got != "release-2.1" {
		t.Errorf("BaseFor = %q, want the stack base", got)
	}
	got := Decide(v)
	if len(got) != 1 || got[0].Base != "release-2.1" {
		t.Fatalf("Decide = %+v, want a restack onto release-2.1", got)
	}
}

// BelowDeclared is the display answer and differs from Below on purpose:
// a card whose predecessor has not cut its branch yet will still fork
// from it, so the board names it rather than the stack's base.
func TestBelowDeclaredNamesTheChainNotTheInstant(t *testing.T) {
	v := chain()
	v.Members[1].HasTree = false
	// Below skips it — there is no ref to fork from yet.
	if id, _ := Below(v, "FD-103"); id != "FD-101" {
		t.Errorf("Below(FD-103) = %q, want FD-101 (the nearest card with a branch)", id)
	}
	// BelowDeclared names it anyway.
	if id, ok := BelowDeclared(v, "FD-103"); !ok || id != "FD-104" {
		t.Errorf("BelowDeclared(FD-103) = %q,%v, want FD-104,true", id, ok)
	}
	// A landed card is skipped by both: its work is in the base now.
	v.Members[1].Landed = true
	if id, _ := BelowDeclared(v, "FD-103"); id != "FD-101" {
		t.Errorf("BelowDeclared past a landed card = %q, want FD-101", id)
	}
}
