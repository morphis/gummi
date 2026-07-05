package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/morphia/gummi/internal/domain"
)

// capHeadroom sets the enforced cap this fraction below the stage budget,
// so the soft stop (the in-flight response completes) can overrun without
// exceeding the budget (DESIGN §5.1 layer 1).
const capHeadroom = 0.90

// budgetThresholds are the spend fractions at which the model is nudged
// (DESIGN §5.1 layer 2). 100% is the exhaustion checkpoint.
var budgetThresholds = []int{50, 80, 95}

// stageBudget returns the credit budget for a feature's current stage.
// A feature with a spend-plan envelope (layer 3) gets its per-stage
// allocation with rollover and the protected review/verify floor; one
// without falls back to the flat config value (layer 1/2 behavior).
func (e *Engine) stageBudget(f domain.Feature, byokRate float64) float64 {
	if f.Budget.Envelope > 0 {
		return domain.PlanFor(f.Kind, float64(f.Budget.Envelope)).
			StageBudget(f.Stage, e.featureSpent(f, byokRate), e.reserveReleased(f.ID))
	}
	return e.cfg.StageBudget
}

// featureSpent returns the feature's credit-equivalent spend so far,
// reading the store's authoritative running total (updated on every usage
// event) so rollover math sees spend from prior stages, priced at the
// current session's provider rate.
func (e *Engine) featureSpent(f domain.Feature, byokRate float64) float64 {
	sp := f.Spend
	if e.cfg.Store != nil {
		if cur, err := e.cfg.Store.GetFeature(context.Background(), f.ID); err == nil {
			sp = cur.Spend
		}
	}
	return sp.CreditEquivalentAt(byokRate)
}

// reserveReleased reports whether a top-up has released the feature's
// held reserve into its stage caps.
func (e *Engine) reserveReleased(id domain.FeatureID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.released[id]
}

// ReserveReleased reports whether a feature's reserve has been released by
// a top-up, so the UI can show its stage cap with the extra headroom.
func (e *Engine) ReserveReleased(id domain.FeatureID) bool { return e.reserveReleased(id) }

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
	var lines []string
	for _, a := range anns {
		if a.Resolved {
			continue
		}
		loc := a.File
		if a.Excerpt != "" {
			loc += " — " + a.Excerpt
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", loc, a.Comment))
	}
	if len(lines) == 0 {
		return nil
	}
	return []string{
		"Address these diff review comments from the last review; make the " +
			"edits and keep the change minimal:\n" + strings.Join(lines, "\n"),
	}
}

// budgetHint is the session-start system instruction telling the model
// its budget (DESIGN §5.1 layer 2, "at session start").
func budgetHint(budget float64) string {
	return fmt.Sprintf(`You have a budget of about %.0f credits (≈$%.2f) for this stage. `+
		`Work budget-consciously: prefer targeted reads over broad exploration, `+
		`batch related edits, and avoid speculative refactors. If you estimate the `+
		`task cannot finish within budget, stop early and write a checkpoint `+
		`(what's done, what's left, where to resume) into the spec's progress `+
		`section rather than running dry mid-edit.`, budget, budget*0.01)
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
