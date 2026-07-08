package domain

import "testing"

func approx(a, b float64) bool { return a-b < 0.001 && b-a < 0.001 }

func TestDefaultPlanSumsToOne(t *testing.T) {
	p := DefaultPlan(300)
	var sum float64
	for _, f := range p.Alloc {
		sum += f
	}
	sum += p.Reserve
	if !approx(sum, 1.0) {
		t.Errorf("allocation + reserve = %v, want 1.0", sum)
	}
}

func TestStageBudgetRollover(t *testing.T) {
	p := DefaultPlan(300) // plan 30, implement 135, review 45, verify 30, reserve 15
	// entering plan with nothing spent: cap through plan = 15% + 10% = 25%
	// of 300 = 75.
	if got := p.StageBudget(StagePlan, 0, false); !approx(got, 75) {
		t.Errorf("plan budget with 0 spent = %v, want 75", got)
	}
	// spec+brainstorm were cheap: only 20 spent entering plan → plan gets
	// its 30 plus the 25 rolled over from the 45 (15%) spec allocation.
	if got := p.StageBudget(StagePlan, 20, false); !approx(got, 55) {
		t.Errorf("plan budget with 20 spent = %v, want 55 (rollover)", got)
	}
	// entering implement having spent 40 so far: cap through implement =
	// 70% of 300 = 210; available = 210 − 40 = 170 (its 135 + 35 rollover).
	if got := p.StageBudget(StageImplement, 40, false); !approx(got, 170) {
		t.Errorf("implement budget = %v, want 170", got)
	}
}

func TestStageBudgetProtectedFloor(t *testing.T) {
	p := DefaultPlan(300)
	// implementation cannot borrow review/verify/reserve: even having spent
	// nothing, implement's cap is 210 (70%), never the full 300. The 90
	// held back = review 45 + verify 30 + reserve 15.
	if got := p.StageBudget(StageImplement, 0, false); !approx(got, 210) {
		t.Errorf("implement cap = %v, want 210 (review/verify/reserve protected)", got)
	}
	// even an implement that overspent its own share can't eat review: with
	// 210 already spent, implement is dry (0), but review still has its cap.
	if got := p.StageBudget(StageImplement, 210, false); !approx(got, 0) {
		t.Errorf("over-budget implement = %v, want 0", got)
	}
	if got := p.StageBudget(StageReview, 210, false); !approx(got, 45) {
		t.Errorf("review budget after implement drained = %v, want 45 (protected)", got)
	}
}

func TestStageBudgetTopUpReleasesReserve(t *testing.T) {
	p := DefaultPlan(300)
	// verify normally caps at 95% = 285; spent 285 → dry.
	if got := p.StageBudget(StageVerify, 285, false); !approx(got, 0) {
		t.Errorf("verify at cap = %v, want 0", got)
	}
	// top up releases the 5% reserve (15 credits) → verify can continue.
	if got := p.StageBudget(StageVerify, 285, true); !approx(got, 15) {
		t.Errorf("verify after top-up = %v, want 15 (reserve released)", got)
	}
}

func TestEstimateEnvelope(t *testing.T) {
	// no history → no estimate (caller keeps its default)
	if env, n := EstimateEnvelope(nil); env != 0 || n != 0 {
		t.Errorf("no history = (%v,%d), want (0,0)", env, n)
	}
	// features that never metered anything are ignored
	if env, n := EstimateEnvelope([]Spend{{}, {}}); env != 0 || n != 0 {
		t.Errorf("zero-spend history = (%v,%d), want (0,0)", env, n)
	}
	// median 100 × 1.25 = 125 → round up to 130; a runaway 900 doesn't
	// drag the median (that's why median, not mean).
	hist := []Spend{
		{Credits: 80}, {Credits: 100}, {Credits: 120}, {Credits: 900},
	}
	env, n := EstimateEnvelope(hist)
	if n != 4 {
		t.Errorf("samples = %d, want 4", n)
	}
	// median of [80,100,120,900] = (100+120)/2 = 110 → ×1.25 = 137.5 → 140
	if env != 140 {
		t.Errorf("estimate = %v, want 140", env)
	}
	// BYOK token-only spend converts to credits before estimating
	tok := []Spend{{OutputTokens: 200000}} // 200k tok × 0.5/1k = 100 credits
	if env, _ := EstimateEnvelope(tok); env != 130 {
		t.Errorf("byok estimate = %v, want 130 (100 credits × 1.25 → 130)", env)
	}
}

func TestCreditEquivalentAt(t *testing.T) {
	tok := Spend{OutputTokens: 10000}
	if got := tok.CreditEquivalentAt(2.0); got != 20 { // 10000/1000 × 2.0
		t.Errorf("rate 2.0 = %v, want 20", got)
	}
	if got := tok.CreditEquivalentAt(0); got != 5 { // default 0.5
		t.Errorf("default rate = %v, want 5", got)
	}
	if got := tok.CreditEquivalent(); got != 5 {
		t.Errorf("CreditEquivalent = %v, want 5 (default)", got)
	}
	// hosted credits ignore the token rate
	if got := (Spend{Credits: 7}).CreditEquivalentAt(2.0); got != 7 {
		t.Errorf("hosted = %v, want 7", got)
	}
}

func TestStageBudgetUnbudgeted(t *testing.T) {
	p := DefaultPlan(0)
	if got := p.StageBudget(StageImplement, 0, false); got != 0 {
		t.Errorf("unbudgeted plan = %v, want 0 (no cap)", got)
	}
}

func TestBlendEstimate(t *testing.T) {
	if got := BlendEstimate(100, 200); got != 150 { // avg → 150
		t.Errorf("blend(100,200) = %v, want 150", got)
	}
	if got := BlendEstimate(0, 175); got != 180 { // scribe only, round up to 10
		t.Errorf("blend(0,175) = %v, want 180", got)
	}
	if got := BlendEstimate(130, 0); got != 130 { // historical only
		t.Errorf("blend(130,0) = %v, want 130", got)
	}
	if got := BlendEstimate(0, 0); got != 0 {
		t.Errorf("blend(0,0) = %v, want 0", got)
	}
}

func TestBugPlanSumsToOneAndProtectsFloor(t *testing.T) {
	p := PlanFor(KindBug, 200)
	var sum float64
	for _, f := range p.Alloc {
		sum += f
	}
	sum += p.Reserve
	if !approx(sum, 1.0) {
		t.Errorf("bug allocation + reserve = %v, want 1.0", sum)
	}
	// Fix's cap is its own share only (0.55·200 = 110) despite feature
	// stages sharing the Stages order — capThrough must skip their zeros.
	if got := p.StageBudget(StageFix, 0, false); !approx(got, 110) {
		t.Errorf("fix budget = %v, want 110", got)
	}
	// Spending all of fix's share cannot borrow into review (protected floor).
	if got := p.StageBudget(StageFix, 110, false); !approx(got, 0) {
		t.Errorf("exhausted fix budget = %v, want 0", got)
	}
	// Review still has its own 0.20·200 = 40 after fix is spent out.
	if got := p.StageBudget(StageReview, 110, false); !approx(got, 40) {
		t.Errorf("review budget after fix spent = %v, want 40", got)
	}
}

func TestFeaturePlanUnaffectedByBugStages(t *testing.T) {
	// Adding triage/diagnose/fix to domain.Stages must not shift a
	// feature's cumulative caps: implement is still 0.70·300 = 210.
	p := DefaultPlan(300)
	if got := p.StageBudget(StageImplement, 0, false); !approx(got, 210) {
		t.Errorf("feature implement budget = %v, want 210", got)
	}
}

func TestFormatDollars(t *testing.T) {
	cases := []struct {
		credits float64
		want    string
	}{
		{0, "$0.00"},
		{-5, "$0.00"},          // clamped, never negative
		{420, "$4.20"},         // whole total
		{1, "$0.01"},           // exactly one cent
		{425, "$4.25"},         // two-place total
		{0.3, "$0.003"},        // sub-cent gains a place
		{0.05, "0.05 credits"}, // below a tenth of a cent falls back to credits
	}
	for _, c := range cases {
		if got := FormatDollars(c.credits); got != c.want {
			t.Errorf("FormatDollars(%v) = %q, want %q", c.credits, got, c.want)
		}
	}
}
