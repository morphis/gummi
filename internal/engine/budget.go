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

// budgetThresholds are the spend fractions at which the model is nudged
// (DESIGN §5.1 layer 2). 100% is the exhaustion checkpoint.
var budgetThresholds = []int{50, 80, 95}

// stageBudget returns the credit budget for a feature's current stage:
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
func (e *Engine) stageBudget(f domain.Feature, creditRate float64) float64 {
	if f.Budget.Envelope > 0 {
		cur := e.currentFeature(f)
		b := cur.Budget.Remaining(cur.Spend.CreditEquivalentAt(creditRate))
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

// diffReviewHints turns a feature's open diff annotations into system
// hints for an implement run (DESIGN §6.1). Empty when the store is
// absent or there is nothing open.
func (e *Engine) diffReviewHints(ctx context.Context, id domain.FeatureID) []string {
	if e.cfg.Store == nil {
		return nil
	}
	anns, err := e.cfg.Store.ListDiffAnnotations(ctx, id)
	if err != nil {
		return nil
	}
	turn := CompileDiffComments(anns, e.ClientTools())
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

// budgetHint is the session-start system instruction telling the model
// its budget (DESIGN §5.1 layer 2, "at session start"). Tailored for
// stages that edit files (implement, fix).
func budgetHint(budget float64) string {
	return fmt.Sprintf(`You have a budget of about %.0f credits (≈$%.2f) for this stage. `+
		`Work budget-consciously: prefer targeted reads over broad exploration, `+
		`batch related edits, and avoid speculative refactors. If you estimate the `+
		`task cannot finish within budget, stop early and write a checkpoint `+
		`(what's done, what's left, where to resume) into the spec's progress `+
		`section rather than running dry mid-edit.`, budget, budget*0.01)
}

// budgetHintReadMostly is the variant used by stages that don't edit
// files (plan critique, verify). The "batch edits / avoid refactors"
// clause is noise there, and critique specifically needs breadth to
// walk closure tables and cited rules — a read-restriction hint
// pulls in the wrong direction.
func budgetHintReadMostly(budget float64) string {
	return fmt.Sprintf(`You have a budget of about %.0f credits (≈$%.2f) for this stage. `+
		`Work budget-consciously. If you estimate you cannot finish within budget, `+
		`stop early and write a checkpoint (what's done, what's left, where to resume) `+
		`into the artifact rather than running dry.`, budget, budget*0.01)
}

// nudge builds the mid-session budget update injected at a threshold.
func nudge(pct int, spent, budget float64) string {
	left := budget - spent
	if left < 0 {
		left = 0
	}
	if pct >= 95 {
		return fmt.Sprintf("[budget] %d%% consumed, ~%.0f credits left — checkpoint now: "+
			"write what's done, what's left, and where to resume, then stop.", pct, left)
	}
	return fmt.Sprintf("[budget] %d%% consumed, ~%.0f credits left — wrap up or checkpoint soon.", pct, left)
}
