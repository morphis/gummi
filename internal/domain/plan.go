package domain

import (
	"fmt"
	"math"
	"sort"
)

// The layer-3 budget (DESIGN §5.1) is one envelope per work item, spent
// by every stage — interactive or autonomous — until it runs dry and a
// human gate offers a top-up. There are no per-stage allocations: review
// and verify need no protected share because the workflow cannot land a
// feature without them, so an empty envelope defers them behind a top-up
// rather than skipping them.

// Remaining returns the credits left in the envelope given the work
// item's credit-equivalent spend so far, clamped at 0 (an unbudgeted
// envelope has nothing to remain).
func (b Budget) Remaining(spent float64) float64 {
	if b.Envelope <= 0 {
		return 0
	}
	return math.Max(0, float64(b.Envelope)-spent)
}

// ByokCreditsPer1KTokens converts a BYOK session's tokens into a credit-
// equivalent so one credit-denominated plan governs both hosted and BYOK
// spend (DESIGN §5.1). A single default rate; per-provider rates are a
// later refinement. 0.5 credits/1K ≈ $0.005/1K, a mid local rate.
const ByokCreditsPer1KTokens = 0.5

// CreditsToDollars converts AI credits to USD: 1 credit = $0.01, the
// Copilot usage-based rate in effect since 2026-06-01.
const CreditsToDollars = 0.01

// FormatDollars renders a credit figure as adaptive-precision USD. Whole
// totals read as "$4.20"; a sub-cent figure that would collapse to "$0.00"
// gains decimal places, and one below a tenth of a cent falls back to a
// credit count so a stage's real cost never reads as free.
func FormatDollars(credits float64) string {
	d := credits * CreditsToDollars
	switch {
	case d <= 0:
		return "$0.00"
	case d >= 0.01:
		return fmt.Sprintf("$%.2f", d)
	case d >= 0.001:
		return fmt.Sprintf("$%.3f", d)
	default:
		return fmt.Sprintf("%.2g credits", credits)
	}
}

// CreditEquivalent returns the spend as a credit figure at the default
// rate. See CreditEquivalentAt for a per-provider rate.
func (s Spend) CreditEquivalent() float64 { return s.CreditEquivalentAt(0) }

// CreditEquivalentAt returns the spend as a credit figure: the metered
// credits for hosted usage, or a token-derived equivalent for BYOK (which
// reports tokens, never credits) at ratePer1K credits per 1000 tokens. A
// non-positive rate falls back to the default ByokCreditsPer1KTokens.
func (s Spend) CreditEquivalentAt(ratePer1K float64) float64 {
	if s.Credits > 0 {
		return s.Credits
	}
	if ratePer1K <= 0 {
		ratePer1K = ByokCreditsPer1KTokens
	}
	return float64(s.InputTokens+s.OutputTokens) / 1000 * ratePer1K
}

// estimateHeadroom pads the historical median so an estimated envelope
// isn't sized right at the typical cost — a feature a bit above the median
// still finishes without a top-up.
const estimateHeadroom = 1.25

// TurnReserveCredits is one agent turn's worth of credits (sized to a
// mid-tier agentic turn — a whole tool-using loop, not one completion).
// Budget enforcement runs between turns, so a cap below this cannot be
// held: the first turn blows through it anyway. Stage budgets are
// floored at it and top-ups raise by at least it. Overridable per engine
// (Config.TurnReserve); this is the default.
const TurnReserveCredits = 30

// MinEnvelope floors every estimated envelope. Estimation signals skew
// low — a scribe guesses from the spec without seeing the agent's real
// exploration cost, and a thin history medians over unrepresentative
// features — and an undersized envelope gates a stage instantly, which
// costs a human round-trip. Estimates only; an explicit envelope the
// user chose is honored as given, and (0,0) still means unbudgeted.
const MinEnvelope = 150

// EstimateEnvelope proposes a feature's credit envelope from the actual
// spend of previously completed features — the historical-spend signal
// of plan-time estimation (DESIGN §5.1). It takes the median
// credit-equivalent spend of the samples (robust to the odd runaway
// feature), adds headroom, and rounds up to a tidy 10, so a repo's
// features get budgets sized to what work there actually costs rather
// than a flat default. Returns (0, 0) when there is no spend to learn
// from, leaving the caller's existing envelope untouched.
func EstimateEnvelope(history []Spend) (envelope float64, samples int) {
	vals := make([]float64, 0, len(history))
	for _, s := range history {
		if c := s.CreditEquivalent(); c > 0 {
			vals = append(vals, c)
		}
	}
	if len(vals) == 0 {
		return 0, 0
	}
	sort.Float64s(vals)
	med := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		med = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	return math.Max(roundUpTo10(med*estimateHeadroom), MinEnvelope), len(vals)
}

// roundUpTo10 rounds a credit figure up to a tidy multiple of 10.
func roundUpTo10(v float64) float64 { return math.Ceil(v/10) * 10 }

// BlendEstimate combines the historical-spend envelope with a scribe
// agent's plan-time estimate (DESIGN §5.1). With both signals it averages
// them (the history grounds the guess, the scribe reflects this specific
// plan); with one, it uses that; with neither, 0. Rounded to a tidy 10
// and floored at MinEnvelope — any non-zero blend is an estimate.
func BlendEstimate(historical, scribe float64) float64 {
	switch {
	case historical > 0 && scribe > 0:
		return math.Max(roundUpTo10((historical+scribe)/2), MinEnvelope)
	case scribe > 0:
		return math.Max(roundUpTo10(scribe), MinEnvelope)
	case historical > 0:
		return math.Max(roundUpTo10(historical), MinEnvelope)
	default:
		return 0
	}
}

// topUpResumeTurns sizes RaisedEnvelope's resume floor: a top-up
// guarantees the gated stage at least this many agent turns of fresh
// budget. One turn is not enough — enforcement runs between turns, so a
// stage resumed with a single turn's worth does that one turn and
// re-gates immediately, asking the human again after every turn.
const topUpResumeTurns = 2

// EnvelopeFloor is the smallest envelope worth setting given a work
// item's spend so far: spend plus the top-up resume headroom. An
// envelope below it gates the next agent turn immediately (or asks
// again after every turn), so explicit raises are validated against it.
func EnvelopeFloor(spent float64) float64 {
	return spent + topUpResumeTurns*TurnReserveCredits
}

// RaisedEnvelope returns the envelope a top-up should raise the work
// item to, given its total spend so far. It takes the largest of three
// corrections:
//
//   - rederive: what the envelope would have been estimated at had the
//     spend been foreseen — spent padded with the estimate headroom, so
//     an envelope that proved undersized grows in proportion to the
//     real cost.
//   - resume floor: spend plus topUpResumeTurns agent turns, so the
//     resumed stage has real multi-turn headroom even when the rederive
//     padding amounts to less than a turn.
//   - at least one agent turn (TurnReserveCredits) over the old
//     envelope — a raise smaller than a turn cannot be held anyway.
//
// The result never shrinks the envelope and is rounded up to a tidy 10.
func (b Budget) RaisedEnvelope(spent float64) float64 {
	if b.Envelope <= 0 {
		return float64(b.Envelope) // unbudgeted stays unbudgeted
	}
	env := float64(b.Envelope)
	raised := math.Max(spent*estimateHeadroom, EnvelopeFloor(spent))
	return roundUpTo10(math.Max(raised, env+TurnReserveCredits))
}
