package agent

import (
	"testing"
	"time"
)

func TestParseUsageLimit(t *testing.T) {
	now := time.Date(2026, 10, 26, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"You've hit your session limit · resets 3:20am (UTC)", time.Date(2026, 10, 27, 3, 20, 0, 0, time.UTC), true},
		{"You've hit your session limit · resets 11pm (UTC)", time.Date(2026, 10, 26, 23, 0, 0, 0, time.UTC), true},
		{"You've hit your weekly limit · resets 12am (UTC)", time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), true},
		{"You've hit your usage limit · resets Oct 30, 3am (UTC)", time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), true},
		{"You've hit your session limit · resets 3pm (Europe/Berlin)", time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC), true},
		{"I think we hit a limit; it resets later", time.Time{}, false},
		{"turn failed", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := ParseUsageLimit(c.in, now)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("%q: got %v %v, want %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestADatedResetReadJustAfterNewYearIsLastYears: a reset of Dec 31 read
// in the first minutes of January passed moments ago; this year's Dec 31
// would have the session sleep a year.
func TestADatedResetReadJustAfterNewYearIsLastYears(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC)
	got, ok := ParseUsageLimit("You've hit your usage limit · resets Dec 31, 11pm (UTC)", now)
	if want := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Fatalf("reset %s, %v; want %s", got, ok, want)
	}
}
