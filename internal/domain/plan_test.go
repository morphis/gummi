package domain

import "testing"

func approx(a, b float64) bool { return a-b < 0.001 && b-a < 0.001 }

func TestRemaining(t *testing.T) {
	b := Budget{Envelope: 150}
	if got := b.Remaining(113.6); !approx(got, 36.4) {
		t.Errorf("remaining(150, 113.6) = %v, want 36.4", got)
	}
	// overspend clamps at 0 — an over-budget item gets no more
	if got := b.Remaining(200); got != 0 {
		t.Errorf("remaining(150, 200) = %v, want 0", got)
	}
	// unbudgeted has nothing to remain (0 means "no envelope", not "dry")
	if got := (Budget{}).Remaining(0); got != 0 {
		t.Errorf("remaining(unbudgeted) = %v, want 0", got)
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
	// a runaway 900 doesn't drag the median (that's why median, not mean);
	// the padded median lands under MinEnvelope, so the floor applies.
	hist := []Spend{
		{Credits: 80}, {Credits: 100}, {Credits: 120}, {Credits: 900},
	}
	env, n := EstimateEnvelope(hist)
	if n != 4 {
		t.Errorf("samples = %d, want 4", n)
	}
	// median of [80,100,120,900] = (100+120)/2 = 110 → ×1.25 = 137.5 →
	// 140, floored at MinEnvelope 150
	if env != 150 {
		t.Errorf("estimate = %v, want 150 (MinEnvelope floor)", env)
	}
	// a history rich enough to clear the floor is used as-is
	rich := []Spend{{Credits: 200}, {Credits: 240}, {Credits: 280}}
	if env, _ := EstimateEnvelope(rich); env != 300 { // 240 × 1.25 = 300
		t.Errorf("rich estimate = %v, want 300", env)
	}
	// BYOK token-only spend converts to credits before estimating
	tok := []Spend{{OutputTokens: 200000}} // 200k tok × 0.5/1k = 100 credits
	if env, _ := EstimateEnvelope(tok); env != 150 {
		t.Errorf("byok estimate = %v, want 150 (100 × 1.25 = 125 → floor)", env)
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

func TestBlendEstimate(t *testing.T) {
	if got := BlendEstimate(100, 200); got != 150 { // avg → 150
		t.Errorf("blend(100,200) = %v, want 150", got)
	}
	if got := BlendEstimate(0, 175); got != 180 { // scribe only, round up to 10
		t.Errorf("blend(0,175) = %v, want 180", got)
	}
	// every non-zero blend is floored at MinEnvelope — an undersized
	// estimate gates a stage instantly
	if got := BlendEstimate(0, 40); got != 150 { // scribe only, under the floor
		t.Errorf("blend(0,40) = %v, want 150 (MinEnvelope floor)", got)
	}
	if got := BlendEstimate(130, 0); got != 150 { // historical only, under the floor
		t.Errorf("blend(130,0) = %v, want 150 (MinEnvelope floor)", got)
	}
	if got := BlendEstimate(60, 80); got != 150 { // both signals, under the floor
		t.Errorf("blend(60,80) = %v, want 150 (MinEnvelope floor)", got)
	}
	if got := BlendEstimate(300, 0); got != 300 { // historical only, over the floor
		t.Errorf("blend(300,0) = %v, want 300", got)
	}
	if got := BlendEstimate(0, 0); got != 0 { // unbudgeted stays unbudgeted
		t.Errorf("blend(0,0) = %v, want 0", got)
	}
}

func TestRaisedEnvelope(t *testing.T) {
	// spend near the envelope: rederive 285 × 1.25 = 356.25 → 360. The
	// rederive term wins when the spend shows the envelope was undersized.
	if got := (Budget{Envelope: 300}).RaisedEnvelope(285); got != 360 {
		t.Errorf("raised(300, 285) = %v, want 360", got)
	}
	// a 3× underestimate: resume floor 120 + 60 = 180 beats rederive 150.
	if got := (Budget{Envelope: 40}).RaisedEnvelope(120); got != 180 {
		t.Errorf("raised(40, 120) = %v, want 180", got)
	}
	// never shrinks: barely-any-spend still raises by at least one turn
	if got := (Budget{Envelope: 300}).RaisedEnvelope(10); got != 330 {
		t.Errorf("raised(300, 10) = %v, want 330 (one-turn minimum raise)", got)
	}
	// guarantee: whatever the shortfall, the raised envelope gives the
	// gated stage at least topUpResumeTurns agent turns — a one-turn
	// sliver would just re-gate after that turn.
	for _, tc := range []struct {
		env   int
		spent float64
	}{
		{300, 285},
		{40, 120},
		{40, 200},
		{150, 149},
		{10, 500},
		{150, 113.6}, // premium-priced interactive stages ate most of the envelope
	} {
		b := Budget{Envelope: tc.env}
		raised := Budget{Envelope: int(b.RaisedEnvelope(tc.spent))}
		if got := raised.Remaining(tc.spent); got < topUpResumeTurns*TurnReserveCredits {
			t.Errorf("raised envelope (env %v, spent %v) leaves %v, want >= %v",
				tc.env, tc.spent, got, topUpResumeTurns*TurnReserveCredits)
		}
	}
	// unbudgeted stays unbudgeted
	if got := (Budget{}).RaisedEnvelope(100); got != 0 {
		t.Errorf("raised(0, ...) = %v, want 0", got)
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
