package domain

import (
	"fmt"
	"math"
	"sort"
)

// SpendPlan is a feature's layer-3 budget (DESIGN §5.1): an envelope of
// credits split into per-stage allocations, plus an orchestrator-held
// reserve. Allocations are consumed in workflow order, and two rules make
// the plan real rather than decorative:
//
//   - Rollover forward: a stage's cap is the cumulative allocation up to
//     and including it, minus everything spent so far — so budget a stage
//     doesn't use inflates the next stage's headroom.
//   - Protected quality floor: because a stage's cap sums allocations only
//     up to itself, it can never reach into a later stage's share. Review
//     and verify allocations cannot be borrowed by implementation.
//
// The reserve is never included in any stage cap until a human releases it
// at an exhaustion gate ("top up").
type SpendPlan struct {
	Envelope float64 // total credits for the feature; 0 = unbudgeted
	// Alloc is each stage's share as a fraction of the envelope. Stages
	// absent from the map (todo, done) consume no allocation. The
	// fractions plus Reserve should sum to 1.
	Alloc   map[Stage]float64
	Reserve float64 // fraction held back, released only at a human gate
}

// defaultAlloc is the v1 static allocation (DESIGN §5.1). brainstorm+spec
// share 15%; reserve is held separately. Estimation-driven allocation is
// an M4+ refinement.
var defaultAlloc = map[Stage]float64{
	StageBrainstorm: 0.075,
	StageSpec:       0.075,
	StagePlan:       0.10,
	StageImplement:  0.45,
	StageReview:     0.15,
	StageVerify:     0.10,
}

// defaultBugAlloc is the v1 static allocation for the bug workflow.
// Triage and diagnose are interactive (uncapped, like brainstorm/spec),
// so they carry no allocation; the autonomous fix/review/verify stages
// share the envelope. Feature-only stages are absent (0), so the shared
// capThrough math over domain.Stages stays correct for both kinds.
var defaultBugAlloc = map[Stage]float64{
	StageFix:    0.55,
	StageReview: 0.20,
	StageVerify: 0.15,
}

// DefaultPlan returns the standard feature plan for an envelope (0 =
// unbudgeted). See PlanFor for the kind-aware entry point.
func DefaultPlan(envelope float64) SpendPlan {
	return SpendPlan{Envelope: envelope, Alloc: defaultAlloc, Reserve: 0.05}
}

// PlanFor returns the standard plan for a work kind's workflow.
func PlanFor(kind Kind, envelope float64) SpendPlan {
	if kind == KindBug {
		return SpendPlan{Envelope: envelope, Alloc: defaultBugAlloc, Reserve: 0.10}
	}
	return DefaultPlan(envelope)
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
// held: the first turn blows through it, and the overshoot then poisons
// every downstream stage-cap and top-up computation. Stage caps are
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

// fracThrough returns the fraction of the envelope allocated to every
// stage at or before s in workflow order — the cumulative-allocation
// share behind capThrough and RaisedEnvelope.
func (p SpendPlan) fracThrough(s Stage) float64 {
	var frac float64
	for _, st := range Stages {
		frac += p.Alloc[st]
		if st == s {
			break
		}
	}
	return frac
}

// capThrough returns the cumulative credit cap available up to and
// including stage s: the envelope times the sum of allocations for every
// stage at or before s in workflow order. Later stages (the protected
// floor) and the reserve are excluded. When reserveReleased is true the
// reserve is folded in, lifting the cap for a topped-up stage.
func (p SpendPlan) capThrough(s Stage, reserveReleased bool) float64 {
	if p.Envelope <= 0 {
		return 0
	}
	frac := p.fracThrough(s)
	if reserveReleased {
		frac += p.Reserve
	}
	return p.Envelope * frac
}

// RaisedEnvelope returns the envelope a top-up at stage s should raise
// the plan to, given the feature's total spend so far. It takes the
// larger of two corrections:
//
//   - refill: the spend to date plus the original shares of stage s and
//     everything after it — restores the current and remaining stages to
//     their as-planned size on top of what's already gone.
//   - rederive: what the envelope would have been estimated at had the
//     spend been foreseen — spent, padded with headroom, scaled up by
//     the share of the envelope stage s's cumulative cap represents.
//
// The rederive term guarantees the raised cumulative cap through s is
// at least spent × estimateHeadroom, so the resumed stage's budget is
// ≥ (estimateHeadroom−1) × spent and a top-up can never re-gate on the
// spot. The raise is at least one agent turn (TurnReserveCredits) — a
// sliver of a raise would just re-gate after the next turn — the result
// never shrinks the envelope, and is rounded up to a tidy 10.
func (p SpendPlan) RaisedEnvelope(s Stage, spent float64) float64 {
	if p.Envelope <= 0 {
		return p.Envelope // unbudgeted stays unbudgeted
	}
	through := p.fracThrough(s)
	before := through - p.Alloc[s]
	refill := spent + p.Envelope*(1-before)
	raised := refill
	if through > 0 {
		raised = math.Max(raised, spent*estimateHeadroom/through)
	}
	raised = math.Max(raised, p.Envelope+TurnReserveCredits)
	return roundUpTo10(math.Max(p.Envelope, raised))
}

// StageBudget returns the credits available to stage s given the credits
// already spent across the feature so far. This is the per-stage cap the
// orchestrator enforces: cumulative allocation through s (with rollover),
// minus prior spend. reserveReleased folds the reserve in after a top-up.
// The result is clamped at 0 (an over-budget feature gets no more).
func (p SpendPlan) StageBudget(s Stage, spentSoFar float64, reserveReleased bool) float64 {
	if p.Envelope <= 0 {
		return 0
	}
	b := p.capThrough(s, reserveReleased) - spentSoFar
	if b < 0 {
		return 0
	}
	return b
}
