// Package stack decides, purely, what a stack of cards looks like and
// what has to happen to it. Given a snapshot of one stack's members and
// their git state it answers three questions — what each card forks
// from, which cards are sitting on commits that have moved, and whether a
// card may land yet — and it never touches git, the store, a clock or an
// agent.
//
// The engine builds the snapshot and executes (Engine.StackTick); the
// worktree manager asks BaseFor to resolve a card's base; the board
// renders the same View. Keeping the rules here is what lets the TUI and
// the headless driver conduct a stack identically, the way gatepolicy
// lets both cross a gate identically and cardrun lets three surfaces
// agree on what a card cost.
//
// The one rule worth stating twice, because getting it wrong defeats the
// feature: a stack is TOPOLOGY, never scheduling. Nothing here reports a
// card as blocked from running. Only landing is ordered, because git
// itself orders it — a card's branch contains the commits of every card
// below it, so landing out of order would land work nobody reviewed under
// that card's name.
package stack

import (
	"github.com/morphis/gummi/internal/domain"
)

// Member is one card in a stack, as far as the policy cares.
type Member struct {
	ID domain.FeatureID
	// Pos is the card's place in the stack, 0 at the bottom.
	Pos int
	// Branch is the card's own git branch name.
	Branch string
	// HasTree reports whether the card has cut its worktree and branch
	// yet. A card that has not is still a member with a base — it just
	// has nothing to replay, and its base is applied when Create runs.
	HasTree bool
	// Landed reports whether the card's work has reached the stack's own
	// base branch, by gummi's squash merge or by a merged PR. A landed
	// card stops being something for the cards above it to fork from:
	// its commits are in the base now, so they fork from the base
	// directly. This is what makes "land the bottom one and the rest
	// move down a rung" fall out rather than need arranging.
	Landed bool
	// Stale reports that the tip of what this card forks from is no
	// longer in the card's own history — someone changed the card below
	// it (review feedback, a rework pass), so this card is built on
	// commits that have moved. It is worktree.RebasedOnBase, inverted.
	Stale bool
	// Running reports a live session on the card. A restack never fights
	// one: it skips the card and the tick comes back when the session
	// settles.
	Running bool
	// Dirty reports uncommitted changes in the card's worktree. A replay
	// would refuse anyway, so the policy does not ask for one.
	Dirty bool
}

// View is a snapshot of one stack: its identity and its members, ordered
// bottom first.
type View struct {
	ID   domain.StackID
	Name string
	Repo string
	// Base is the branch the whole stack sits on — the bottom card's own
	// chosen base, empty for the managed checkout's HEAD.
	Base    string
	Members []Member
}

// BaseFor returns the revision the named card forks from: the branch of
// the nearest card below it that has not landed, else the stack's own
// base.
//
// Skipping landed cards is the whole of the landing cascade. When the
// bottom card lands, the card above it stops forking from a branch whose
// commits are now in the base anyway and starts forking from the base
// directly — so the replay that follows is a fast-forward rather than a
// re-application of work that is already there.
//
// An unknown card, or the bottom of the stack, gets the stack's base.
func BaseFor(v View, id domain.FeatureID) string {
	pos := -1
	for _, m := range v.Members {
		if m.ID == id {
			pos = m.Pos
			break
		}
	}
	if pos <= 0 {
		return v.Base
	}
	for i := len(v.Members) - 1; i >= 0; i-- {
		m := v.Members[i]
		if m.Pos >= pos || m.Landed || !m.HasTree {
			// A card with no branch yet cannot be forked from either —
			// there is no ref. Keep walking down.
			continue
		}
		return m.Branch
	}
	return v.Base
}

// Below returns the card the named one forks from, and whether there is
// one. It is BaseFor's companion for the surfaces that name the card
// rather than the branch ("← FD-101").
func Below(v View, id domain.FeatureID) (domain.FeatureID, bool) {
	pos := -1
	for _, m := range v.Members {
		if m.ID == id {
			pos = m.Pos
			break
		}
	}
	if pos <= 0 {
		return "", false
	}
	for i := len(v.Members) - 1; i >= 0; i-- {
		m := v.Members[i]
		if m.Pos >= pos || m.Landed || !m.HasTree {
			continue
		}
		return m.ID, true
	}
	return "", false
}

// BelowDeclared returns the card the named one is *meant* to fork from:
// the nearest card below it that has not landed, whether or not it has
// cut its branch yet.
//
// This is the display answer, and it differs from Below on purpose.
// Below answers "what ref does git fork this from right now", so it skips
// a card with no branch — there is nothing to fork from. A reader looking
// at a board wants the chain they declared: a card whose predecessor has
// not started yet will still fork from it once it does, and showing
// "← main" in the meantime describes a moment rather than the plan.
func BelowDeclared(v View, id domain.FeatureID) (domain.FeatureID, bool) {
	pos := -1
	for _, m := range v.Members {
		if m.ID == id {
			pos = m.Pos
			break
		}
	}
	if pos <= 0 {
		return "", false
	}
	for i := len(v.Members) - 1; i >= 0; i-- {
		m := v.Members[i]
		if m.Pos >= pos || m.Landed {
			continue
		}
		return m.ID, true
	}
	return "", false
}

// Action is one thing the tick should do.
type Action struct {
	Kind ActionKind
	Card domain.FeatureID
	// Base is the revision to replay Card onto, for ActionRestack.
	Base string
	// Reason is a short phrase for the log and the board, in the voice
	// the reader gets ("the card below it moved").
	Reason string
}

// ActionKind enumerates what a tick can do to a stack.
type ActionKind int

const (
	// ActionRestack replays a card's own commits onto its current base.
	ActionRestack ActionKind = iota
	// ActionWait reports that something must settle before the walk can
	// continue — a live session, or a dirty worktree. The stack is left
	// alone and the tick returns.
	ActionWait
)

// String names the kind for logs.
func (k ActionKind) String() string {
	if k == ActionWait {
		return "wait"
	}
	return "restack"
}

// Decide returns what to do with the stack now, bottom first.
//
// It returns at most ONE restack, deliberately. A replay rewrites the
// branch every card above it forks from, so their staleness — and even
// whether they conflict — is not knowable until it has happened. Deciding
// one step at a time and re-snapshotting is what keeps the policy pure
// and the walk honest; the engine ticks again when the step is done.
//
// The walk stops at the first card it cannot move: a card with a live
// session or a dirty worktree yields ActionWait rather than being
// skipped over, because replaying a card above it would fork it from
// commits that are about to change again.
func Decide(v View) []Action {
	for _, m := range v.Members {
		if m.Landed || !m.HasTree {
			continue
		}
		if m.Running {
			return []Action{{Kind: ActionWait, Card: m.ID, Reason: "a session is running on it"}}
		}
		if !m.Stale {
			continue
		}
		if m.Dirty {
			return []Action{{Kind: ActionWait, Card: m.ID, Reason: "its worktree has uncommitted changes"}}
		}
		return []Action{{
			Kind:   ActionRestack,
			Card:   m.ID,
			Base:   BaseFor(v, m.ID),
			Reason: "the card below it moved",
		}}
	}
	return nil
}

// Stale lists the cards sitting on commits that have moved, bottom
// first. It is what the board badges; Decide is what the tick acts on.
func Stale(v View) []domain.FeatureID {
	var out []domain.FeatureID
	for _, m := range v.Members {
		if m.Stale && m.HasTree && !m.Landed {
			out = append(out, m.ID)
		}
	}
	return out
}

// LandBlocker reports the card that must land before id may, and whether
// there is one. A card may land once every card below it has.
//
// This is the ONLY ordering a stack imposes, and it is not a policy
// choice: a card's branch contains the commits of every card beneath it,
// so landing it early would land their work too, under this card's
// message and without their review.
func LandBlocker(v View, id domain.FeatureID) (domain.FeatureID, bool) {
	pos := -1
	for _, m := range v.Members {
		if m.ID == id {
			pos = m.Pos
			break
		}
	}
	if pos <= 0 {
		return "", false
	}
	for _, m := range v.Members {
		if m.Pos < pos && !m.Landed {
			return m.ID, true
		}
	}
	return "", false
}

// Bottom returns the lowest member that has not landed — the card whose
// turn it is to land, and the one the board leads the stack with.
func Bottom(v View) (Member, bool) {
	for _, m := range v.Members {
		if !m.Landed {
			return m, true
		}
	}
	return Member{}, false
}

// Live counts the members that have not landed.
func Live(v View) int {
	n := 0
	for _, m := range v.Members {
		if !m.Landed {
			n++
		}
	}
	return n
}
