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

func TestStageBudgetUnbudgeted(t *testing.T) {
	p := DefaultPlan(0)
	if got := p.StageBudget(StageImplement, 0, false); got != 0 {
		t.Errorf("unbudgeted plan = %v, want 0 (no cap)", got)
	}
}
