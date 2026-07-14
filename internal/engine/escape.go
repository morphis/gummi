// Escape guard: agents run CWD-pinned to their feature worktree, but the
// main checkout shares the same filesystem and .git, so nothing stops an
// absolute-path write or a `cd`-and-commit from landing work on main
// directly. This file is the backstop: snapshot the main checkout before
// each autonomous turn is dispatched, compare at turn end, and undo the
// delta when it is unambiguously the agent's.
//
// Accepted limit: detection runs at turn end, so a mid-turn escape is
// undone after the turn, not prevented — nothing reaches main's history
// before a human gate regardless (the squash merge is the only sanctioned
// path). Preventing the write itself needs an isolated clone or an OS
// sandbox; that is a future escalation, not this mechanism.
package engine

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/worktree"
)

// armEscapeGuard snapshots the main checkout on a session as its turn is
// dispatched. Interactive sessions are exempt: design-phase chats
// legitimately run in the main checkout (against ignored .gummi drafts)
// and are human-paced and human-watched. A snapshot failure disables the
// guard for the turn, loudly, rather than blocking the run.
func (e *Engine) armEscapeGuard(s *Session) {
	if s.Interactive {
		return
	}
	// Read the generation before the git state: a sanctioned mutation (a
	// land) racing these two reads then leaves the recorded generation
	// behind the git state, and the post-turn check — which reads the
	// generation after — sees it moved and stands down, instead of
	// judging against a torn snapshot.
	gen := e.cfg.Worktrees.MainGen()
	snap, err := e.cfg.Worktrees.MainSnapshot(context.Background())
	if err != nil {
		s.appendActivity("main-checkout escape guard unavailable this turn: " + err.Error())
		return
	}
	s.armMainGuard(snap, gen)
}

// checkEscape compares the main checkout against the turn's pre-dispatch
// snapshot. On an escape it reverts the delta when that is safe (see
// below), fails the run, and raises EventEscape — visible, never silent.
// Returns true when the turn escaped, so the caller skips the normal
// turn-settled path.
func (e *Engine) checkEscape(s *Session) bool {
	snap, gen, armed := s.takeMainGuard()
	if !armed {
		return false
	}
	ctx := context.Background()
	cur, err := e.cfg.Worktrees.MainSnapshot(ctx)
	if err != nil {
		s.appendActivity("main-checkout escape check failed: " + err.Error())
		return false
	}
	// Generation read after the git state (mirror of armEscapeGuard): any
	// move means gummi itself mutated the main checkout mid-turn (a land,
	// or another session's revert), so the delta cannot be attributed to
	// this agent — stand down for this turn.
	if e.cfg.Worktrees.MainGen() != gen {
		return false
	}
	if cur.Equal(snap) {
		return false
	}

	// Escape confirmed. Revert only when the delta is unambiguously the
	// agent's: the checkout was clean before the turn (so every new edit
	// and untracked file is the agent's) and history only grew on top of
	// the snapshot HEAD on the same branch (so a reset drops exactly the
	// agent's commits). Anything else — the user's uncommitted work in
	// place, a switched branch, rewritten history — is left untouched and
	// reported instead: never destroy user work to undo an agent.
	verdict := "not auto-reverted: " + describeAmbiguity(snap, cur)
	if snap.Clean() && cur.Branch == snap.Branch {
		if chains, err := e.cfg.Worktrees.MainChainsFrom(ctx, snap.Head); err == nil && chains {
			if rerr := e.cfg.Worktrees.RestoreMain(ctx, snap, gen); rerr == nil {
				verdict = "reverted"
			} else {
				verdict = "revert failed: " + rerr.Error()
			}
		}
	}
	err = fmt.Errorf("agent wrote outside its worktree (%s) — main checkout %s", describeEscape(snap, cur), verdict)
	s.appendActivity(err.Error())
	s.setError(err)
	if !s.Interactive {
		s.setState(StatePaused)
	}
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventEscape, Err: err})
	e.freeSlot(s)
	return true
}

// describeEscape says what moved, for the failure message.
func describeEscape(snap, cur worktree.MainState) string {
	switch {
	case cur.Branch != snap.Branch:
		return "checked-out branch changed"
	case cur.Head != snap.Head:
		return fmt.Sprintf("HEAD moved %.8s → %.8s", snap.Head, cur.Head)
	default:
		return "uncommitted changes appeared"
	}
}

// describeAmbiguity says why an escape was not auto-reverted.
func describeAmbiguity(snap, cur worktree.MainState) string {
	switch {
	case !snap.Clean():
		return "it had uncommitted changes before the turn; inspect and clean up manually"
	case cur.Branch != snap.Branch:
		return "the checked-out branch changed; inspect and clean up manually"
	default:
		return "history diverged from the pre-turn snapshot; inspect and clean up manually"
	}
}
