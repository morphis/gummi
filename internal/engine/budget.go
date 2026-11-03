package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// capHeadroom sets the enforced cap this fraction below the stage budget,
// so the soft stop (the in-flight response completes) can overrun without
// exceeding the budget (DESIGN §5.1 layer 1).
const capHeadroom = 0.90

// sessionBudget returns the credit budget for a card's current session:
// what's left of its envelope (layer 3), or the flat config value for a
// feature without one (layer 1/2 behavior). Every stage draws from the
// same envelope — there are no per-stage allocations. Review and verify
// are guaranteed by the workflow (they can never be skipped), not by a
// protected budget share: a drained envelope defers them behind the
// top-up gate.
//
// A positive envelope-derived budget is floored at one agent turn:
// enforcement runs between turns (a turn is a whole agentic loop), so a
// smaller cap cannot be held anyway — the turn overshoots it either way.
// Exhaustion semantics are unchanged: an envelope with nothing left
// still returns 0 and gates.
func (e *Engine) sessionBudget(f domain.Feature, creditRate float64) float64 {
	if e.stepOf(context.Background(), f) != nil {
		// a step spends from its session's pool (§25.7)
		return e.stepBudget(context.Background(), f)
	}
	if f.Budget.Envelope > 0 {
		cur := e.currentFeature(f)
		spent := cur.Spend.CreditEquivalentAt(creditRate)
		if cur.IsFreeform() && !cur.IsChild() {
			// a session and its workflow runs draw from one envelope (§24.7a)
			spent += e.legacyCrewsSpent(context.Background(), cur.ID) + e.sessionRunsSpent(context.Background(), cur.ID)
		}
		b := cur.Budget.Remaining(spent)
		if reserve := e.turnReserve(); b > 0 && b < reserve {
			b = reserve
		}
		return b
	}
	return e.cfg.StageBudget
}

// turnReserve is the one-turn credit reserve stage caps are floored at.
func (e *Engine) turnReserve() float64 {
	if e.cfg.TurnReserve > 0 {
		return e.cfg.TurnReserve
	}
	return domain.TurnReserveCredits
}

// currentFeature returns the store's authoritative row for a feature —
// its running spend total (updated on every usage event) and its
// current envelope (a top-up may have raised it) — so the budget math
// sees spend from prior stages. Falls back to the caller's copy without
// a store.
func (e *Engine) currentFeature(f domain.Feature) domain.Feature {
	if e.cfg.Store != nil {
		if cur, err := e.cfg.Store.GetFeature(context.Background(), f.ID); err == nil {
			return cur
		}
	}
	return f
}

// seedCardSpend stamps a spawning session with the card's spend as the
// store holds it right now, priced at the session's own adapter rate —
// the same figure sessionBudget just subtracted from the envelope. From
// here recordUsage moves it by each booked sample, so a render path has
// the store's running total without a read of its own (Session.cardSpent).
func (e *Engine) seedCardSpend(s *Session) {
	s.seedCardSpent(e.currentFeature(s.Feature).Spend.CreditEquivalentAt(s.rate()))
}

// diffReviewHints turns a feature's open diff annotations into system
// hints for an implement run (DESIGN §6.1). Empty when the store is
// absent or there is nothing open.
func (e *Engine) diffReviewHints(ctx context.Context, id domain.FeatureID, resolveTool bool) []string {
	if e.cfg.Store == nil {
		return nil
	}
	anns, err := e.cfg.Store.ListDiffAnnotations(ctx, id)
	if err != nil {
		return nil
	}
	turn := CompileDiffComments(anns, resolveTool)
	if turn == "" {
		return nil
	}
	return []string{turn}
}

// CompileDiffComments builds the fix-up instruction listing a feature's
// open diff annotations — folded into a fresh implement/fix run's hints
// (diffReviewHints) and sent by the UI as a live turn to a running
// session. With resolveTool, each comment carries its [id] and the agent
// is told to mark it resolved via the resolve_annotation client tool
// (DESIGN §6.1's resolve event for diffs); without, the ids are omitted
// and resolution stays manual. Empty when nothing is open.
func CompileDiffComments(anns []domain.DiffAnnotation, resolveTool bool) string {
	var lines []string
	for _, a := range anns {
		if a.Resolved {
			continue
		}
		loc := a.File
		if a.Repo != "" {
			loc = a.Repo + ":" + loc
		}
		if a.Excerpt != "" {
			loc += " — " + a.Excerpt
		}
		if resolveTool {
			lines = append(lines, fmt.Sprintf("- [%d] %s: %s", a.ID, loc, a.Comment))
		} else {
			lines = append(lines, fmt.Sprintf("- %s: %s", loc, a.Comment))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	turn := "Address these diff review comments; make the edits and keep " +
		"the change minimal:\n" + strings.Join(lines, "\n")
	if resolveTool {
		turn += "\nAfter addressing each comment, call the resolve_annotation " +
			"tool with its [id]; unresolved comments keep the gate blocked."
	}
	return turn
}

// backendCap is the credit cap a freeform card's backend enforces itself:
// a stop the backend makes ends the turn where it stands. A step's card
// gets none — its session's pool is held across the run's steps by
// checkRunPool, which pauses the run and carries the step on after a
// top-up, where a backend's stop would end the step on a cut-off reply.
func backendCap(f domain.Feature, budget float64) float64 {
	if f.IsChild() {
		return 0
	}
	return budget * capHeadroom
}

// budgetHintStep is the budget instruction for a workflow step. It names
// no figure: a step cannot top its pool up or spend it any better for
// knowing it, and a figure shown is one a model rations against and
// argues about instead of working.
const budgetHintStep = `Money is not yours to manage in this step. gummi holds the session's
budget across the whole run: if it runs out, the run pauses for the person
to top it up and you carry on where you were. Do not estimate your spend,
ration your work, cut the task short or report on budget: do the whole
task your brief sets. Work efficiently: prefer targeted reads over broad
exploration, batch related edits, and avoid speculative refactors.`

// budgetHintFreeform is the budget instruction for a freeform card's
// session. It is a third variant beside budgetHint and
// budgetHintReadMostly for one concrete reason: both of those tell the
// model to write a checkpoint "into the spec's progress section", and a
// freeform card has no spec. It also has no nudges — gummi stops the turn
// itself when the envelope runs out (exhaustFreeform) and the person tops
// it up — so the model is told the figure and that it is not its to judge.
func budgetHintFreeform(budget float64) string {
	return fmt.Sprintf(`This card has %s of budget left. gummi meters what you
spend and stops the turn itself if it runs out; the person then decides
whether to top it up. You cannot see your own spend, so do not estimate it,
ration your work against it, or stop early on a guess about it. Work
efficiently: prefer targeted reads over broad exploration, batch related
edits, and avoid speculative refactors. Whatever the worktree holds when a
turn ends is what the next turn starts from.`, domain.FormatDollars(budget))
}
