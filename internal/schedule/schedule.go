// Package schedule is the pure cron policy behind freeform sessions that
// come back on a clock (DESIGN §19.9): parse a 5-field cron expression,
// compile a cadence preset to one, decide whether a stored definition is
// due, and advance its next run. It has no clock, no IO, and no store —
// the caller hands it a `now` and gets an answer — so the same rules hold
// for the board's poll, a forced run-now, and a test.
//
// Two time-driven primitives share this engine: a schedule that mints a
// new freeform card on a cadence, and a heartbeat that sends a recurring
// turn into one existing freeform session. Which of the two a row is does
// not matter here; both are `domain.Schedule` rows keyed by cron.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// Cron is a parsed 5-field cron expression: minute, hour, day-of-month,
// month, day-of-week. The fields are expanded bit sets, so a match check
// is a bit test and no field string survives to be reinterpreted later.
type Cron struct {
	minute, hour, dom, month, dow cronField
}

// cronField is one expanded cron field. any is the plain `*` — carried
// separately because it changes the day rule: a restricted day-of-month
// beside an unrestricted day-of-week matches on the day-of-month alone,
// while two restricted day fields match on either (the Vixie rule).
type cronField struct {
	any  bool
	bits uint64 // bit v set = value v allowed
}

func (f cronField) has(v int) bool { return f.bits&(1<<uint(v)) != 0 }

func (f cronField) withRange(lo, hi int) cronField {
	for v := lo; v <= hi; v++ {
		f.bits |= 1 << uint(v)
	}
	return f
}

// fieldBounds is one field's inclusive numeric range, in expression order.
type fieldBounds struct{ min, max int }

var cronBounds = [5]fieldBounds{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 7},  // day of week: 0 and 7 both mean Sunday
}

// Parse parses a standard 5-field cron expression. It accepts `*`, lists,
// ranges, and `/step` in any field, and nothing else: no seconds field, no
// `L`/`W`/`#` extensions, no month or weekday names — the stored form is
// numeric-only, and the presets that read better compile to it (Compile).
//
// An expression that can never match any wall-clock minute is refused
// here rather than at the first fire: `0 0 30 2 *` names a date no
// February has, so every tick would compute a zero next run and never
// fire. The day-of-week escape (`0 0 30 2 0`) is still accepted, because
// under the either-day rule its Sundays are real.
func Parse(expr string) (Cron, error) {
	var c Cron
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return c, fmt.Errorf("invalid cron %q: want 5 fields (minute hour day-of-month month day-of-week)", expr)
	}
	var err error
	if c.minute, err = parseField(fields[0], cronBounds[0], false); err != nil {
		return c, fmt.Errorf("invalid cron %q, minute: %w", expr, err)
	}
	if c.hour, err = parseField(fields[1], cronBounds[1], false); err != nil {
		return c, fmt.Errorf("invalid cron %q, hour: %w", expr, err)
	}
	if c.dom, err = parseField(fields[2], cronBounds[2], false); err != nil {
		return c, fmt.Errorf("invalid cron %q, day of month: %w", expr, err)
	}
	if c.month, err = parseField(fields[3], cronBounds[3], false); err != nil {
		return c, fmt.Errorf("invalid cron %q, month: %w", expr, err)
	}
	if c.dow, err = parseField(fields[4], cronBounds[4], true); err != nil {
		return c, fmt.Errorf("invalid cron %q, day of week: %w", expr, err)
	}
	if err := c.checkFeasible(); err != nil {
		return c, fmt.Errorf("invalid cron %q: %w", expr, err)
	}
	return c, nil
}

// parseField expands one field into its bit set. sundaySeven widens the
// numeric range to 7 and folds 7 back to 0, so `0-7` and `5-7` parse.
func parseField(s string, b fieldBounds, sundaySeven bool) (cronField, error) {
	max := b.max
	if sundaySeven {
		max = 7
	}
	if s == "*" {
		return cronField{any: true}.withRange(b.min, max), nil
	}
	var f cronField
	for _, part := range strings.Split(s, ",") {
		body, step, err := cutStep(part)
		if err != nil {
			return f, err
		}
		lo, hi := b.min, max
		if body != "*" {
			var ranged bool
			lo, hi, ranged, err = parseRange(body, b.min, max)
			if err != nil {
				return f, err
			}
			// `a/step` walks from a to the top of the field, cronie-style;
			// a real range steps within itself.
			if step > 1 && !ranged {
				hi = max
			}
		}
		for v := lo; v <= hi; v += step {
			n := v
			if sundaySeven && n == 7 {
				n = 0
			}
			f.bits |= 1 << uint(n)
		}
	}
	return f, nil
}

// cutStep splits a trailing /step off one field part. A bare `/n` is a
// step over the whole field; a step of 1 parses and changes nothing.
func cutStep(part string) (body string, step int, err error) {
	i := strings.IndexByte(part, '/')
	if i < 0 {
		return part, 1, nil
	}
	step, err = strconv.Atoi(part[i+1:])
	if err != nil || step < 1 {
		return "", 0, fmt.Errorf("bad step %q", part[i+1:])
	}
	return part[:i], step, nil
}

// parseRange parses `a` or `a-b`. ranged reports whether the body was a
// real range. The weekday alias 7 is NOT folded here — a range like
// `5-7` would fold into a descending 5..0 and match nothing; the field
// loop maps each value instead.
func parseRange(body string, min, max int) (lo, hi int, ranged bool, err error) {
	loStr, hiStr := body, body
	if i := strings.IndexByte(body, '-'); i >= 0 {
		loStr, hiStr = body[:i], body[i+1:]
		ranged = true
	}
	lo, err = strconv.Atoi(loStr)
	if err != nil {
		return 0, 0, false, fmt.Errorf("bad value %q", loStr)
	}
	hi, err = strconv.Atoi(hiStr)
	if err != nil {
		return 0, 0, false, fmt.Errorf("bad value %q", hiStr)
	}
	if lo < min || lo > max || hi < min || hi > max {
		return 0, 0, false, fmt.Errorf("%d-%d out of range %d-%d", lo, hi, min, max)
	}
	if lo > hi {
		return 0, 0, false, fmt.Errorf("descending range %d-%d", lo, hi)
	}
	return lo, hi, ranged, nil
}

// maxDays is the number of days each month can have, with February's leap
// ceiling at 29 — a February 30 can never exist, so a cron naming it
// (with no weekday escape) never matches and Parse refuses it.
var maxDays = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// checkFeasible refuses a day-of-month no month in the month set has.
// The day-of-week half of the either-day rule is left out: every weekday
// recurs, so a restricted day-of-week always has real days to match.
func (c Cron) checkFeasible() error {
	if c.dom.any || !c.dow.any {
		return nil
	}
	for m := 1; m <= 12; m++ {
		if !c.month.has(m) {
			continue
		}
		for d := maxDays[m]; d >= 1; d-- {
			if c.dom.has(d) {
				return nil
			}
		}
	}
	return fmt.Errorf("the day-of-month never exists in the months given")
}

// dayMatches applies the day rule to one wall-clock day: an unrestricted
// day-of-month matches on the month alone, an unrestricted day-of-week on
// the weekday alone, two restricted fields on either one matching.
func (c Cron) dayMatches(t time.Time) bool {
	switch {
	case c.dom.any && c.dow.any:
		return true
	case c.dow.any:
		return c.dom.has(t.Day())
	case c.dom.any:
		return c.dow.has(int(t.Weekday()))
	default:
		return c.dom.has(t.Day()) || c.dow.has(int(t.Weekday()))
	}
}

// Next returns the first wall-clock minute the expression matches,
// strictly after after, evaluated in loc (time.Local when nil). Strictly
// after is absolute time, not only the wall clock: every matched
// candidate is held against after itself, so the answer is never at or
// before the instant it was asked from.
//
// The walk is minute by minute over absolute instants, so the two DST
// shapes fall out of the clock rather than out of special cases: a wall
// time that does not exist (spring-forward) is never visited, and a wall
// time that repeats (fall-back) keeps its cadence — every matched wall
// minute inside the repeated hour is returned at both of its occurrences
// (eight fires for a */15 expression), as in classic cron. The once rule
// covers the one minute the ask sits in: the fall-back repeat of after's
// own wall minute is skipped, so a schedule set for the hour that happens
// twice a year never sees the same wall time twice in a row — it fires
// once, at the first of the two.
//
// The once rule is not the whole of strictly-after, though. The walk's
// anchor re-derives after's wall minute with time.Date, and Go resolves
// an ambiguous wall time to its FIRST occurrence — so when after sits
// inside the second pass of a fall-back repeated hour, the anchor is a
// real instant an hour in after's past, and a match inside that first
// pass (01:15 EDT, say, when after is 01:00 EST) reads as a fine answer
// on the wall clock while being forty-five minutes before after. The
// matched candidate is therefore held against after in absolute time
// too: one in after's past is skipped and the walk continues to the
// first match that is genuinely later.
//
// The bound is ten years of daily jumps, which covers a leap day that
// waits out a non-leap century year (Feb 29 2100 does not exist; the
// next one is 2104). Next returns the zero time only past that bound,
// which Parse's feasibility check keeps unreachable; the zero is a
// defensive answer, not a result.
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	start := after.In(loc)
	// Start at after's own minute boundary, then one minute on: matches
	// are whole minutes, and strictly after means after's minute does
	// not count even when it matches.
	wall := time.Date(start.Year(), start.Month(), start.Day(),
		start.Hour(), start.Minute(), 0, 0, loc)
	t := wall.Add(time.Minute)
	limit := wall.AddDate(10, 0, 0)
	for t.Before(limit) {
		if !c.month.has(int(t.Month())) {
			t = jump(firstOfNextMonth(t, loc), t, 24*time.Hour)
			continue
		}
		if !c.dayMatches(t) {
			t = jump(time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc), t, 24*time.Hour)
			continue
		}
		if !c.hour.has(t.Hour()) {
			t = jump(time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc), t, time.Hour)
			continue
		}
		if !c.minute.has(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		if sameWallMinute(t, wall) {
			// A fall-back repeat of after's own minute: same wall clock,
			// later instant. The first occurrence was after itself.
			t = t.Add(time.Minute)
			continue
		}
		if !t.After(after) {
			// A match inside the first pass of a repeated hour that
			// after sits in the second pass of: same wall clock as a
			// fine answer, but an instant in after's own past. The
			// first pass already fired before after was asked; skip it
			// and keep walking to the first match that is genuinely
			// later.
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// jump returns target when it strictly moves past from, else from plus
// one absolute step. It exists for the DST gap: time.Date normalizes a
// nonexistent wall time to the instant BEFORE the gap (02:00 on a
// spring-forward morning reads back as 01:00), so a jump that asks for
// the skipped hour would return from unchanged and the walk would spin
// on the same minute forever. The absolute step cannot fail to move,
// and wherever it lands, the loop's own checks take it from there.
func jump(target, from time.Time, step time.Duration) time.Time {
	if target.After(from) {
		return target
	}
	return from.Add(step)
}

// sameWallMinute reports whether a and b read as the same minute on the
// same day in their own locations — the test that separates a fall-back
// repeat from a later day's matching minute.
func sameWallMinute(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day() &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

// firstOfNextMonth is midnight on the first day of the month after t.
func firstOfNextMonth(t time.Time, loc *time.Location) time.Time {
	if t.Month() == time.December {
		return time.Date(t.Year()+1, time.January, 1, 0, 0, 0, 0, loc)
	}
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
}

// Compile turns a cadence preset into the cron expression that is stored
// for it. Cron is canonical: the stored definition is always the compiled
// expression, so what `list` shows and what fires agree by construction.
//
// `Nm` needs an N that divides 60 (`*/N * * * *`); `Nh` one that divides
// 24 (`0 */N * * *`). A step of 1 normalizes to `*` — `1m` is every
// minute, which `*/1` would say in two characters that mean nothing
// extra. `@hourly`, `@daily` (and its `@midnight` alias) and `@weekly`
// compile to their classic forms. Anything else is refused: a preset
// this package cannot state exactly is a preset it will not guess at.
func Compile(preset string) (string, error) {
	p := strings.TrimSpace(strings.ToLower(preset))
	switch p {
	case "@hourly":
		return "0 * * * *", nil
	case "@daily", "@midnight":
		return "0 0 * * *", nil
	case "@weekly":
		return "0 0 * * 0", nil
	}
	for _, u := range []struct {
		suffix string
		unit   int
	}{{"m", 60}, {"h", 24}} {
		n, ok := strings.CutSuffix(p, u.suffix)
		if !ok {
			continue
		}
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 || u.unit%v != 0 {
			return "", fmt.Errorf("invalid cadence %q: want a number dividing %d with %q (e.g. 5m, 15m, 1h, 6h), @hourly, @daily or @weekly",
				preset, u.unit, u.suffix)
		}
		// An Nm preset's step lives in the minute field and its hour is
		// `*`; an Nh preset's hour carries the step and its minute is 0.
		// A step of 1 normalizes to `*`, so 1m is every minute and 1h is
		// every hour on the hour.
		step := fmt.Sprintf("*/%d", v)
		if v == 1 {
			step = "*"
		}
		minuteField, hourField := step, "*"
		if u.suffix == "h" {
			minuteField, hourField = "0", step
		}
		return fmt.Sprintf("%s %s * * *", minuteField, hourField), nil
	}
	return "", fmt.Errorf("invalid cadence %q: want <N>m or <N>h, @hourly, @daily or @weekly", preset)
}

// location resolves a stored timezone name; empty is the host's local
// zone, the same reading every other stored timestampless field follows.
func location(tz string) (*time.Location, error) {
	if strings.TrimSpace(tz) == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", tz)
	}
	return loc, nil
}

// Check is the one validity rule a stored definition must satisfy: the
// expression parses (so it can never be a definition that silently never
// fires), the zone loads, and Next off a fixed reference instant is
// non-zero. Every write boundary calls it, so no face can store a cron
// this package would refuse.
func Check(cron, tz string) error {
	c, err := Parse(cron)
	if err != nil {
		return err
	}
	loc, err := location(tz)
	if err != nil {
		return err
	}
	ref := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if c.Next(ref, loc).IsZero() {
		return fmt.Errorf("cron %q never matches", cron)
	}
	return nil
}

// NextRun returns the first matching minute strictly after now, in the
// zone a row names — the value an enable writes into next_run. Callers
// that only have the row's fields use this; a caller with a parsed Cron
// calls Next directly.
func NextRun(cron, tz string, now time.Time) (time.Time, error) {
	c, err := Parse(cron)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := location(tz)
	if err != nil {
		return time.Time{}, err
	}
	return c.Next(now, loc), nil
}

// Due reports whether a stored definition should fire at now. A disabled
// row is never due — not even one with run_requested set: run-now is
// refused on a disabled row at the face, and the one forced fire of a
// disabled row is Engine.RunSchedule, which bypasses Due entirely. A
// zero next_run (never enabled) is likewise not due; a run_requested on
// an enabled row is, because run-now's whole point is firing off-cadence.
func Due(s domain.Schedule, now time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.RunRequested {
		return true
	}
	return !s.NextRun.IsZero() && !now.Before(s.NextRun)
}

// Advance returns a definition's next run after now, in its own zone.
// It advances from now, never from the row's stored next_run — that is
// how missed fires coalesce into one catch-up fire: however many cadence
// marks went by while no board was up, one tick fires once and the next
// run lands strictly after now. The zero time means the expression could
// not be re-parsed; the store validated it at write, so the zero is a
// defensive answer the engine treats as a failure, not a cadence.
func Advance(s domain.Schedule, now time.Time) time.Time {
	next, err := NextRun(s.Cron, s.Timezone, now)
	if err != nil {
		return time.Time{}
	}
	return next
}
