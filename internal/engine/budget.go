package engine

import "fmt"

// capHeadroom sets the enforced cap this fraction below the stage budget,
// so the soft stop (the in-flight response completes) can overrun without
// exceeding the budget (DESIGN §5.1 layer 1).
const capHeadroom = 0.90

// budgetThresholds are the spend fractions at which the model is nudged
// (DESIGN §5.1 layer 2). 100% is the exhaustion checkpoint.
var budgetThresholds = []int{50, 80, 95}

// byokCreditsPer1KTokens converts a BYOK session's token usage into a
// credit-equivalent so the one credit-denominated budget governs both
// hosted and BYOK sessions (DESIGN §5.1: "unified spend … each
// convertible to display-dollars via per-provider rates"). This is a
// single default rate; the per-provider rate table lands with the spend
// plan in Phase 20. 0.5 credits/1K tokens ≈ $0.005/1K, a mid local rate.
const byokCreditsPer1KTokens = 0.5

// creditEquiv returns a usage total in credits: the metered credits for a
// hosted session, or a token-derived equivalent for a BYOK session (which
// reports tokens, never credits). This is what budget math compares —
// enforcement for BYOK is gummi-side since --max-ai-credits never fires
// for credit-free sessions (DESIGN §5.1).
func creditEquiv(credits float64, inTok, outTok int64) float64 {
	if credits > 0 {
		return credits
	}
	return float64(inTok+outTok) / 1000 * byokCreditsPer1KTokens
}

// stageBudget returns the credit budget for a feature's current stage.
// M3-c uses a flat config value for every autonomous stage; Phase 20
// replaces it with per-feature spend-plan allocations.
func (e *Engine) stageBudget() float64 { return e.cfg.StageBudget }

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
