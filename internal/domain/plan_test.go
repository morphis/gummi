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

func TestParseDollars(t *testing.T) {
	for in, want := range map[string]int{
		"5": 500, "$5": 500, " $ 5.50 ": 550, "0": 0, "1,200": 120000, "0.01": 1, "12.3": 1230,
		// exact at the largest envelope, where a float lost the cent
		"21,474,836.47": 2147483647, "9336891.04": 933689104, "5.500": 550, ".5": 50,
	} {
		if got, err := ParseDollars(in); err != nil || got != want {
			t.Errorf("ParseDollars(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// a decimal comma is refused, never read as a hundred times the figure;
	// exponents, hex and signs are not dollar figures
	for _, in := range []string{"", "$", "five", "-1", "0.005", "NaN", "Inf", "12,50", "1,2", "1e3", "0x10", "+5", "21474836.48"} {
		if got, err := ParseDollars(in); err == nil {
			t.Errorf("ParseDollars(%q) = %d, want an error", in, got)
		}
	}
}
