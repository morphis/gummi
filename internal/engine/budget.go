package engine

import (
	"context"
	"fmt"

	"github.com/morphia/gummi/internal/domain"
)

// capHeadroom sets the enforced cap this fraction below the stage budget,
// so the soft stop (the in-flight response completes) can overrun without
// exceeding the budget (DESIGN §5.1 layer 1).
const capHeadroom = 0.90

// budgetThresholds are the spend fractions at which the model is nudged
// (DESIGN §5.1 layer 2). 100% is the exhaustion checkpoint.
var budgetThresholds = []int{50, 80, 95}

// creditEquiv returns a usage total in credits: the metered credits for a
// hosted session, or a token-derived equivalent for a BYOK session (which
// reports tokens, never credits). This is what budget math compares —
// enforcement for BYOK is gummi-side since --max-ai-credits never fires
// for credit-free sessions (DESIGN §5.1).
func creditEquiv(credits float64, inTok, outTok int64) float64 {
	return domain.Spend{Credits: credits, InputTokens: inTok, OutputTokens: outTok}.CreditEquivalent()
}

// stageBudget returns the credit budget for a feature's current stage.
// A feature with a spend-plan envelope (layer 3) gets its per-stage
// allocation with rollover and the protected review/verify floor; one
// without falls back to the flat config value (layer 1/2 behavior).
func (e *Engine) stageBudget(f domain.Feature) float64 {
	if f.Budget.Envelope > 0 {
		return domain.DefaultPlan(float64(f.Budget.Envelope)).
			StageBudget(f.Stage, e.featureSpent(f), e.reserveReleased(f.ID))
	}
	return e.cfg.StageBudget
}

// featureSpent returns the feature's credit-equivalent spend so far,
// reading the store's authoritative running total (updated on every
// usage event) so rollover math sees spend from prior stages.
func (e *Engine) featureSpent(f domain.Feature) float64 {
	sp := f.Spend
	if e.cfg.Store != nil {
		if cur, err := e.cfg.Store.GetFeature(context.Background(), f.ID); err == nil {
			sp = cur.Spend
		}
	}
	return sp.CreditEquivalent()
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
