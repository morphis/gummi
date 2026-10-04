package schedule

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func mustParse(t *testing.T, expr string) Cron {
	t.Helper()
	c, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return c
}

func TestParse(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 0 * * 0",
		"0 0 * * 7", // 7 is Sunday's alias, not an eighth day
		"30 2 1,15 * *",
		"0 9-17 * * 1-5",
		"5,35 */2 * * *",
		"0 0 29 2 *",   // leap day: some years have it
		"0 0 31 1 *",   // some months do
		"0 0 30 2 0",   // infeasible date, live weekday: either-day rule
		"1/15 * * * *", // a/step walks from a to the field's top
		"0 0 1-31/7 1 *",
		"  */5   *   *   *   *  ", // extra whitespace is not syntax
	}
	for _, expr := range valid {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q) = %v, want ok", expr, err)
		}
	}
	invalid := []string{
		"",
		"* * * *",     // four fields
		"* * * * * *", // six: no seconds field here
		"60 * * * *",
		"-1 * * * *",
		"* 24 * * *",
		"* * 0 * *", // day of month starts at 1
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"*/-5 * * * *",
		"a * * * *",
		"5/a * * * *",
		"1- * * * *",
		"10-5 * * * *",
		"0 0 30 2 *",   // no February has a 30th
		"0 0 31 2,4 *", // neither of those months does
		"0 0 31 4,6,9,11 *",
		"mon * * * *", // no names: numeric only
		"@daily",      // presets compile (Compile), they do not parse
	}
	for _, expr := range invalid {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) = ok, want refused", expr)
		}
	}
	// The refusal for the never-matching shapes names the expression, so
	// the face that shows the error says what was wrong.
	if _, err := Parse("0 0 30 2 *"); err == nil || !strings.Contains(err.Error(), "0 0 30 2 *") {
		t.Errorf("Parse never-matching = %v, want an error naming the expression", err)
	}
}

func TestCompile(t *testing.T) {
	for _, tc := range []struct{ preset, cron string }{
		{"5m", "*/5 * * * *"},
		{"15m", "*/15 * * * *"},
		{"1m", "* * * * *"}, // a step of 1 normalizes to *
		{"1h", "0 * * * *"},
		{"2h", "0 */2 * * *"},
		{"6h", "0 */6 * * *"},
		{"24h", "0 */24 * * *"},
		{"@hourly", "0 * * * *"},
		{"@daily", "0 0 * * *"},
		{"@midnight", "0 0 * * *"},
		{"@weekly", "0 0 * * 0"},
		{"5M", "*/5 * * * *"}, // case does not matter
	} {
		t.Run(tc.preset, func(t *testing.T) {
			got, err := Compile(tc.preset)
			if err != nil {
				t.Fatalf("Compile(%q): %v", tc.preset, err)
			}
			if got != tc.cron {
				t.Fatalf("Compile(%q) = %q, want %q", tc.preset, got, tc.cron)
			}
			// What Compile produces must itself parse — the stored
			// definition is always the compiled string.
			if _, err := Parse(got); err != nil {
				t.Fatalf("Compile(%q) = %q, which does not parse: %v", tc.preset, got, err)
			}
		})
	}
	for _, preset := range []string{
		"", "now", "hourly", "30d", "7m", "13h", "0m", "90m", "5s", "m", "h", "-5m",
	} {
		if _, err := Compile(preset); err == nil {
			t.Errorf("Compile(%q) = ok, want refused", preset)
		}
	}
}

func TestCheck(t *testing.T) {
	if err := Check("*/5 * * * *", ""); err != nil {
		t.Errorf("Check with no zone: %v", err)
	}
	if err := Check("30 2 * * *", "America/New_York"); err != nil {
		t.Errorf("Check with a zone: %v", err)
	}
	for _, tc := range []struct{ cron, tz string }{
		{"", ""},
		{"bad", ""},
		{"0 0 30 2 *", ""}, // never matches
		{"0 0 31 4 *", ""}, // April has 30
		{"*/5 * * * *", "Nowhere/Nothing"},
	} {
		if err := Check(tc.cron, tc.tz); err == nil {
			t.Errorf("Check(%q, %q) = ok, want refused", tc.cron, tc.tz)
		}
	}
}

func TestNext(t *testing.T) {
	utc := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, time.UTC)
	}
	for _, tc := range []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{"same hour, later minute", "*/15 * * * *", utc(2026, 10, 3, 10, 7), utc(2026, 10, 3, 10, 15)},
		{"same minute is not after", "*/15 * * * *", utc(2026, 10, 3, 10, 15), utc(2026, 10, 3, 10, 30)},
		{"across the hour", "45 * * * *", utc(2026, 10, 3, 10, 50), utc(2026, 10, 3, 11, 45)},
		{"across midnight", "0 0 * * *", utc(2026, 10, 3, 23, 59), utc(2026, 10, 4, 0, 0)},
		{"across the month", "0 0 1 * *", utc(2026, 10, 3, 0, 0), utc(2026, 11, 1, 0, 0)},
		{"across the year", "0 0 1 1 *", utc(2026, 12, 31, 12, 0), utc(2027, 1, 1, 0, 0)},
		{"leap day", "0 0 29 2 *", utc(2027, 3, 1, 0, 0), utc(2028, 2, 29, 0, 0)},
		{"leap day past a century year", "0 0 29 2 *", utc(2097, 3, 1, 0, 0), utc(2104, 2, 29, 0, 0)},
		{"list of minutes", "5,35 * * * *", utc(2026, 10, 3, 10, 6), utc(2026, 10, 3, 10, 35)},
		{"hour range", "0 9-17 * * *", utc(2026, 10, 3, 18, 0), utc(2026, 10, 4, 9, 0)},
		{"weekday", "0 0 * * 1", utc(2026, 10, 3, 12, 0), utc(2026, 10, 5, 0, 0)},
		{"7 means sunday too", "0 0 * * 7", utc(2026, 10, 3, 12, 0), utc(2026, 10, 4, 0, 0)},
		{"monthly", "0 0 1 * *", utc(2026, 1, 15, 0, 0), utc(2026, 2, 1, 0, 0)},
		{"list crossing the day", "59 23 28,31 * *", utc(2026, 10, 28, 23, 59), utc(2026, 10, 31, 23, 59)},
	} {
		c := mustParse(t, tc.expr)
		got := c.Next(tc.after, time.UTC)
		if got != tc.want {
			t.Errorf("%s: Next(%q, %s) = %s, want %s", tc.name, tc.expr, tc.after, got, tc.want)
		}
	}
}

// TestNextEitherDayRule: when both day fields are restricted, a day
// matches if EITHER does (Vixie cron). October 2026 starts Thu Oct 1;
// Friday Oct 2 and Friday Oct 9 sit before Tuesday Oct 13, the month's
// first 13th after the second.
func TestNextEitherDayRule(t *testing.T) {
	c := mustParse(t, "0 0 13 * 5")
	after := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, want := range []time.Time{
		time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),  // Friday
		time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), // the 13th
		time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), // Friday again
	} {
		got := c.Next(after, time.UTC)
		if got != want {
			t.Errorf("Next after %s = %s, want %s", after, got, want)
		}
		after = got
	}
	// With the day-of-month left alone, the weekday carries the rule by
	// itself; with the weekday left alone, the date does.
	if got := mustParse(t, "0 0 * * 5").Next(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.UTC); got.Day() != 9 {
		t.Errorf("weekday-only rule: got %s, want Oct 9", got)
	}
	if got := mustParse(t, "0 0 13 * *").Next(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.UTC); got.Day() != 13 {
		t.Errorf("date-only rule: got %s, want Oct 13", got)
	}
}

// TestNextDST handles the two clock shapes a year hands the walk: the
// hour that does not exist (spring forward — skipped) and the hour that
// happens twice (fall back — fired once, at its first occurrence).
func TestNextDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata on this host:", err)
	}
	at := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, ny)
	}
	// US DST 2026: spring forward Sun Mar 8, fall back Sun Nov 1.
	c := mustParse(t, "30 2 * * *")
	t.Run("spring forward skips the hour that does not exist", func(t *testing.T) {
		// 02:30 does not exist on Mar 8; the next one is Mar 9's.
		if got := c.Next(at(2026, 3, 7, 3, 0), ny); got != at(2026, 3, 9, 2, 30) {
			t.Errorf("got %s, want %s", got, at(2026, 3, 9, 2, 30))
		}
		// The day before, the ordinary Mar 7 02:30 is next.
		if got := c.Next(at(2026, 3, 6, 12, 0), ny); got != at(2026, 3, 7, 2, 30) {
			t.Errorf("ordinary day: got %s, want %s", got, at(2026, 3, 7, 2, 30))
		}
	})
	t.Run("fall back fires once, at the first occurrence", func(t *testing.T) {
		// 01:00 happens twice (EDT then EST). From midnight, the first
		// occurrence; from the first occurrence, the next day's 01:00 —
		// never the same wall minute a second time.
		hourly := mustParse(t, "0 1 * * *")
		if got := hourly.Next(at(2026, 11, 1, 0, 0), ny); got != at(2026, 11, 1, 1, 0) {
			t.Errorf("got %s, want the first 01:00 (%s)", got, at(2026, 11, 1, 1, 0))
		}
		if got := hourly.Next(at(2026, 11, 1, 1, 0), ny); got != at(2026, 11, 2, 1, 0) {
			t.Errorf("got %s, want %s (not 01:00 a second time)", got, at(2026, 11, 2, 1, 0))
		}
	})
	t.Run("a minute cron walks over the repeat without doubling", func(t *testing.T) {
		if got := mustParse(t, "* * * * *").Next(at(2026, 11, 1, 1, 30), ny); got.Minute() != 31 || got.Hour() != 1 {
			t.Errorf("got %s, want the next 01:31", got)
		}
	})
}

// TestNextStrictlyAfterInsideFallBackRepeat pins the absolute half of
// strictly-after: when after sits inside the second pass of a fall-back
// repeated hour, the walk's wall anchor re-derives to the hour's first
// pass — a real instant an hour before after — and a match there reads
// fine on the wall clock while being in after's own past. Every answer
// below is therefore both the right wall minute and strictly later than
// after in absolute time. The engine consequence this guards: Advance
// from such a now used to arm a past next_run, and Due then refired the
// row on every poll for the whole repeated hour.
func TestNextStrictlyAfterInsideFallBackRepeat(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata on this host:", err)
	}
	// 2026-11-01: 01:00–02:00 EDT is 05:00–06:00Z, then the clocks fall
	// back and 01:00–02:00 EST is 06:00–07:00Z, so 06:00Z is the second
	// pass of the wall minute 01:00.
	at := func(h, m int) time.Time { return time.Date(2026, 11, 1, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		// The sub-hourly matches in the first pass are after's own past;
		// the answers are their second-pass twins.
		{"*/1 * * * *", at(6, 0), at(6, 1)},   // 01:01 EST, not 01:01 EDT
		{"*/15 * * * *", at(6, 0), at(6, 15)}, // 01:15 EST, not 01:15 EDT
		{"5,35 * * * *", at(6, 0), at(6, 5)},  // 01:05 EST, not 01:05 EDT
		// A sub-minute after holds the whole 01:00 wall minute against
		// itself: the first match strictly after is the next cadence mark.
		{"*/15 * * * *", at(6, 0).Add(30 * time.Second), at(6, 15)},
		// Hourly's next mark after the second 01:00 is the unambiguous
		// 02:00 EST: the repeat is skipped by the once rule, and its
		// first pass was before after anyway.
		{"0 * * * *", at(6, 0), at(7, 0)},
		// From inside the first pass, the old answers still hold: the
		// guard must not skip a match that is genuinely later.
		{"*/15 * * * *", at(5, 0), at(5, 15)},
		{"*/15 * * * *", at(5, 30), at(5, 45)},
	} {
		c := mustParse(t, tc.expr)
		got := c.Next(tc.after, ny)
		if !got.Equal(tc.want) {
			t.Errorf("Next(%q, %s) = %s, want %s", tc.expr, tc.after, got, tc.want)
		}
		if !got.After(tc.after) {
			t.Errorf("Next(%q, %s) = %s, which is not after it", tc.expr, tc.after, got)
		}
	}
}

// TestNextStrictlyAfterSweep walks a net of cadences across instants at
// fifteen-minute resolution over both of 2026's US transition days and
// asserts the one line of the contract the fall-back repeat broke: the
// answer is always strictly after its question, in absolute time.
func TestNextStrictlyAfterSweep(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata on this host:", err)
	}
	for _, day := range []struct {
		name  string
		start time.Time
	}{
		{"spring forward", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)},
		{"fall back", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(day.name, func(t *testing.T) {
			for _, expr := range []string{
				"*/1 * * * *", "*/5 * * * *", "*/15 * * * *", "0 * * * *",
				"5,35 * * * *", "30 2 * * *", "0 0 * * *", "0 0 * * 0",
			} {
				c := mustParse(t, expr)
				end := day.start.Add(24 * time.Hour)
				for at := day.start; at.Before(end); at = at.Add(15 * time.Minute) {
					if got := c.Next(at, ny); !got.After(at) {
						t.Fatalf("Next(%q, %s) = %s, which is not after it", expr, at, got)
					}
				}
			}
		})
	}
}

func TestNextRunAndZone(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 7, 30, 0, time.UTC)
	got, err := NextRun("*/15 * * * *", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// Local on a UTC host is UTC; the minute is what the answer turns on.
	if !got.UTC().Equal(time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)) {
		t.Errorf("NextRun = %s, want 10:15Z", got)
	}
	// A zone is not decoration: 10:07Z is 15:37 in India (+5:30), whose
	// next hour mark is 16:00 IST — 10:30Z, half an hour later than the
	// same expression in UTC.
	got, err = NextRun("0 * * * *", "Asia/Kolkata", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UTC().Equal(time.Date(2026, 10, 3, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("NextRun in India = %s, want 10:30Z", got)
	}
	if _, err := NextRun("0 0 30 2 *", "", now); err == nil {
		t.Error("NextRun accepted a never-matching expression")
	}
	if _, err := NextRun("* * * * *", "Nowhere/Nothing", now); err == nil {
		t.Error("NextRun accepted an unknown zone")
	}
}

// TestNextCoalescesMissedFires: Advance returns Next(now), never
// Next(next_run) — however many cadence marks went by while no board was
// up, one tick fires once and the next run lands after now.
func TestAdvanceCoalescesMissedFires(t *testing.T) {
	s := domain.Schedule{
		ID: "hourly", Kind: domain.ScheduleHeartbeat, Cron: "0 * * * *",
		Target: "FF-001", Prompt: "check CI, keep going",
		Enabled: true,
		NextRun: time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 10, 3, 5, 30, 0, 0, time.UTC)
	t.Run("one missed mark advances from now", func(t *testing.T) {
		if got := Advance(s, now); !got.Equal(time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC)) {
			t.Errorf("Advance = %s, want 06:00 (advanced from now, not from next_run)", got)
		}
	})
	// Three missed hours is still one fire: the same answer, not a list.
	t.Run("three missed hours coalesce into one", func(t *testing.T) {
		s.NextRun = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		if got := Advance(s, now); !got.Equal(time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC)) {
			t.Errorf("Advance after a long gap = %s, want 06:00", got)
		}
	})
	// A definition whose cron cannot be re-parsed (a corrupt row the
	// store's checks should have kept out) advances to zero, which the
	// engine treats as a failure rather than a cadence.
	broken := s
	broken.Cron = "not a cron"
	if got := Advance(broken, now); !got.IsZero() {
		t.Errorf("Advance on a broken cron = %s, want zero", got)
	}
}

// TestDueRequiresEnabled: a disabled row is never due, whatever
// run_requested says — a CLI run-now on a disabled row is refused at the
// face, and the only forced fire of a disabled row is RunSchedule, which
// bypasses Due entirely.
func TestDueRequiresEnabled(t *testing.T) {
	past := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	s := domain.Schedule{
		ID: "hourly", Kind: domain.ScheduleHeartbeat, Cron: "0 * * * *",
		Target: "FF-001", Prompt: "check CI",
	}
	if Due(s, now) {
		t.Error("an unenabled row with no next run is due")
	}
	s.NextRun = past
	if Due(s, now) {
		t.Error("a disabled row with a past next run is due")
	}
	s.RunRequested = true
	if Due(s, now) {
		t.Error("a disabled row with run_requested is due")
	}
	s.Enabled = true
	if !Due(s, now) {
		t.Error("an enabled row with run_requested is not due")
	}
	s.RunRequested = false
	if !Due(s, now) {
		t.Error("an enabled row with a past next run is not due")
	}
	s.NextRun = now.Add(time.Hour)
	if Due(s, now) {
		t.Error("an enabled row with a future next run is due")
	}
	if Due(s, now.Add(2*time.Hour)) == false {
		t.Error("an enabled row at exactly its next run is not due")
	}
}

// TestDueExactlyAtNextRun pins the boundary: !now.Before(next_run) fires
// at the very minute, not one past it.
func TestDueExactlyAtNextRun(t *testing.T) {
	s := domain.Schedule{
		ID: "hourly", Kind: domain.ScheduleHeartbeat, Cron: "0 * * * *",
		Target: "FF-001", Prompt: "check CI", Enabled: true,
		NextRun: time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC),
	}
	if !Due(s, s.NextRun) {
		t.Error("Due is false at exactly next_run")
	}
	if Due(s, s.NextRun.Add(-time.Minute)) {
		t.Error("Due is true one minute before next_run")
	}
}

// TestAdvanceMatchesNextRun pins that enable's next run and the tick's
// advance are the same computation.
func TestAdvanceMatchesNextRun(t *testing.T) {
	s := domain.Schedule{ID: "n", Name: "n", Kind: domain.ScheduleMint, Cron: "30 2 * * *", Prompt: "p", Envelope: 10}
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	want, err := NextRun(s.Cron, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := Advance(s, now); !got.Equal(want) {
		t.Errorf("Advance = %s, want NextRun = %s", got, want)
	}
}
