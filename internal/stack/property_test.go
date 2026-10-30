package stack

// Property tests of the stack policy, driven by hegel (https://hegel.dev).
// Each draws a stack of up to eight members — positions bottom first, with
// gaps where a member left — each landed or not, with a branch or not,
// stale, running and dirty at random, and states one rule of the package
// doc over it, for every member and for a card the stack does not hold.

import (
	"fmt"
	"slices"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
)

const propCases = 2000

func props(t *testing.T, check func(ht *hegel.T, v View, id domain.FeatureID)) {
	t.Helper()
	hegel.Test(t, func(ht *hegel.T) {
		v := drawView(ht)
		ids := []domain.FeatureID{"FF-999"} // a card the stack does not hold
		for _, m := range v.Members {
			ids = append(ids, m.ID)
		}
		id := hegel.Draw(ht, hegel.SampledFrom(ids))
		check(ht, v, id)
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

func drawView(ht *hegel.T) View {
	v := View{ID: "ST-1", Name: "s", Repo: "r", Base: "main"}
	n := hegel.Draw(ht, hegel.Integers(0, 8))
	gaps := hegel.Draw(ht, hegel.Booleans())
	pos := 0
	for i := range n {
		if gaps {
			skip := hegel.Draw(ht, hegel.Integers(0, 2))
			pos += skip
		}
		landed := hegel.Draw(ht, hegel.Booleans())
		tree := hegel.Draw(ht, hegel.Booleans())
		stale := hegel.Draw(ht, hegel.Booleans())
		running := hegel.Draw(ht, hegel.Booleans())
		dirty := hegel.Draw(ht, hegel.Booleans())
		v.Members = append(v.Members, Member{
			ID: domain.FeatureID(fmt.Sprintf("FF-%d", i+1)), Pos: pos, Branch: fmt.Sprintf("ff/%d", i+1),
			HasTree: tree, Landed: landed, Stale: stale, Running: running, Dirty: dirty,
		})
		pos++
	}
	ht.Note(fmt.Sprintf("members: %+v", v.Members))
	return v
}

func member(v View, id domain.FeatureID) (Member, bool) {
	for _, m := range v.Members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// TestPropertyBaseForIsTheBranchOfTheCardBelow: BaseFor and Below give
// the same answer, one as a branch and one as a card: the branch of the
// card Below names, else the stack's base.
func TestPropertyBaseForIsTheBranchOfTheCardBelow(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		want := v.Base
		if below, ok := Below(v, id); ok {
			m, _ := member(v, below)
			want = m.Branch
		}
		if got := BaseFor(v, id); got != want {
			ht.Fatalf("BaseFor(%s) = %q, Below says %q", id, got, want)
		}
	})
}

// TestPropertyBelowIsTheNearestForkableCardBeneath: Below names the
// nearest card beneath id that has a branch and has not landed, and says
// none when there is no such card.
func TestPropertyBelowIsTheNearestForkableCardBeneath(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		var want domain.FeatureID
		if self, ok := member(v, id); ok {
			for _, m := range v.Members {
				if m.Pos < self.Pos && m.HasTree && !m.Landed {
					want = m.ID // members are bottom first: the last one is nearest
				}
			}
		}
		got, ok := Below(v, id)
		if ok != (want != "") || got != want {
			ht.Fatalf("Below(%s) = %q, %v; want %q", id, got, ok, want)
		}
	})
}

// TestPropertyBelowDeclaredIsTheNearestLiveCardBeneath: BelowDeclared
// names the nearest card beneath id that has not landed, branch or not.
func TestPropertyBelowDeclaredIsTheNearestLiveCardBeneath(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		var want domain.FeatureID
		if self, ok := member(v, id); ok {
			for _, m := range v.Members {
				if m.Pos < self.Pos && !m.Landed {
					want = m.ID
				}
			}
		}
		got, ok := BelowDeclared(v, id)
		if ok != (want != "") || got != want {
			ht.Fatalf("BelowDeclared(%s) = %q, %v; want %q", id, got, ok, want)
		}
	})
}

// TestPropertyBelowNeverSitsAboveBelowDeclared: what git forks from is the
// declared card, or one further down when the declared one has no branch
// yet — never one above it.
func TestPropertyBelowNeverSitsAboveBelowDeclared(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		below, ok := Below(v, id)
		declared, dok := BelowDeclared(v, id)
		if ok && !dok {
			ht.Fatalf("Below(%s) = %s with nothing declared", id, below)
		}
		if !ok || !dok {
			return
		}
		b, _ := member(v, below)
		d, _ := member(v, declared)
		if b.Pos > d.Pos || (b.Pos < d.Pos && d.HasTree) {
			ht.Fatalf("Below(%s) = %s at %d, declared %s at %d (branch %v)", id, below, b.Pos, declared, d.Pos, d.HasTree)
		}
	})
}

// TestPropertyACardMayLandOnceEveryCardBelowHas: LandBlocker names a card
// beneath id that has not landed, and names none exactly when every card
// beneath has landed.
func TestPropertyACardMayLandOnceEveryCardBelowHas(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		self, held := member(v, id)
		pending := false
		for _, m := range v.Members {
			pending = pending || (held && m.Pos < self.Pos && !m.Landed)
		}
		blocker, ok := LandBlocker(v, id)
		if ok != pending {
			ht.Fatalf("LandBlocker(%s) = %q, %v; a card below has not landed: %v", id, blocker, ok, pending)
		}
		if ok {
			if b, _ := member(v, blocker); b.Landed || b.Pos >= self.Pos {
				ht.Fatalf("LandBlocker(%s) = %s, which is %+v", id, blocker, b)
			}
		}
	})
}

// TestPropertyTheBottomIsTheCardWhoseTurnItIsToLand: Bottom is the one
// live member nothing blocks from landing, and there is none exactly when
// Live counts none.
func TestPropertyTheBottomIsTheCardWhoseTurnItIsToLand(t *testing.T) {
	props(t, func(ht *hegel.T, v View, _ domain.FeatureID) {
		bottom, ok := Bottom(v)
		if ok != (Live(v) > 0) {
			ht.Fatalf("Bottom ok=%v with %d live", ok, Live(v))
		}
		for _, m := range v.Members {
			if m.Landed {
				continue
			}
			_, blocked := LandBlocker(v, m.ID)
			if blocked == (m.ID == bottom.ID) {
				ht.Fatalf("%s blocked=%v, bottom is %s", m.ID, blocked, bottom.ID)
			}
		}
	})
}

// TestPropertyLiveCountsTheMembersNotLanded: Live is the number of
// members that have not landed.
func TestPropertyLiveCountsTheMembersNotLanded(t *testing.T) {
	props(t, func(ht *hegel.T, v View, _ domain.FeatureID) {
		want := 0
		for _, m := range v.Members {
			if !m.Landed {
				want++
			}
		}
		if got := Live(v); got != want {
			ht.Fatalf("Live = %d, want %d", got, want)
		}
	})
}

// TestPropertyStaleListsTheCardsOnMovedCommits: Stale is every member
// with a branch, not landed, sitting on commits that moved, bottom first.
func TestPropertyStaleListsTheCardsOnMovedCommits(t *testing.T) {
	props(t, func(ht *hegel.T, v View, _ domain.FeatureID) {
		var want []domain.FeatureID
		for _, m := range v.Members {
			if m.Stale && m.HasTree && !m.Landed {
				want = append(want, m.ID)
			}
		}
		if got := Stale(v); !slices.Equal(got, want) {
			ht.Fatalf("Stale = %v, want %v", got, want)
		}
	})
}

// TestPropertyDecideTakesOneStepAtTheFirstCardThatNeedsOne: Decide returns
// at most one action, for the lowest card with a branch and not landed
// that is running, or stale: a wait on a running or dirty one, else its
// restack onto BaseFor.
func TestPropertyDecideTakesOneStepAtTheFirstCardThatNeedsOne(t *testing.T) {
	props(t, func(ht *hegel.T, v View, _ domain.FeatureID) {
		var want []Action
		for _, m := range v.Members {
			if m.Landed || !m.HasTree || (!m.Running && !m.Stale) {
				continue
			}
			switch {
			case m.Running || m.Dirty:
				want = []Action{{Kind: ActionWait, Card: m.ID}}
			default:
				want = []Action{{Kind: ActionRestack, Card: m.ID, Base: BaseFor(v, m.ID)}}
			}
			break
		}
		got := Decide(v)
		for i := range got {
			got[i].Reason = ""
		}
		if !slices.Equal(got, want) {
			ht.Fatalf("Decide = %+v, want %+v", got, want)
		}
	})
}

// TestPropertyAStackNeverBlocksWork: nothing Decide returns is about a
// card that is not live, branched and either running or stale — a stack
// is topology, never scheduling: a card nothing is wrong with is never
// waited on.
func TestPropertyAStackNeverBlocksWork(t *testing.T) {
	props(t, func(ht *hegel.T, v View, _ domain.FeatureID) {
		for _, a := range Decide(v) {
			m, _ := member(v, a.Card)
			if m.Landed || !m.HasTree || (!m.Running && !m.Stale) {
				ht.Fatalf("Decide acts on %+v: %+v", m, a)
			}
		}
	})
}

// TestPropertyLandingTheBottomMovesTheNextCardDown: landing the bottom
// card makes the cards that forked from it fork from what it forked from
// — the cascade falls out of skipping landed cards.
func TestPropertyLandingTheBottomMovesTheNextCardDown(t *testing.T) {
	props(t, func(ht *hegel.T, v View, id domain.FeatureID) {
		bottom, ok := Bottom(v)
		if !ok || id == bottom.ID {
			return
		}
		before := BaseFor(v, id)
		landed := View{ID: v.ID, Base: v.Base, Members: slices.Clone(v.Members)}
		for i := range landed.Members {
			if landed.Members[i].ID == bottom.ID {
				landed.Members[i].Landed = true
			}
		}
		after := BaseFor(landed, id)
		want := before
		if bottom.HasTree && before == bottom.Branch {
			want = BaseFor(v, bottom.ID)
		}
		if after != want {
			ht.Fatalf("landing %s: BaseFor(%s) %q → %q, want %q", bottom.ID, id, before, after, want)
		}
	})
}
