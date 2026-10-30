package schedule

// Property tests of the cron policy, driven by hegel (https://hegel.dev).
// Expressions are drawn field by field — `*`, values, ranges, lists and
// steps, the weekday alias 7 — and read against an oracle that expands
// each field from its written form, as Parse's doc states it. Instants are
// drawn across the zones whose clocks misbehave: DST at midnight
// (Santiago, Havana), half-hour DST (Lord Howe), quarter-hour offsets
// (Kathmandu, Chatham), a date-line jump (Apia), weighted toward the
// months clocks change in.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
)

const propCases = 2000

var propZones = []string{
	"UTC", "America/New_York", "Europe/London", "Australia/Lord_Howe", "Asia/Kathmandu",
	"Pacific/Chatham", "America/Santiago", "America/Havana", "Asia/Tehran", "America/St_Johns",
	"Pacific/Apia", "Europe/Moscow", "Africa/Casablanca",
}

func props(t *testing.T, fn func(ht *hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// part is one comma-separated part of a field as written.
type part struct {
	star   bool // the body is *
	bare   bool // a star written as nothing: a bare /step
	lo, hi int  // the body's value, or range
	ranged bool
	step   int // 0: no /step written
}

func (p part) String() string {
	body := strconv.Itoa(p.lo)
	switch {
	case p.bare:
		body = ""
	case p.star:
		body = "*"
	case p.ranged:
		body = fmt.Sprintf("%d-%d", p.lo, p.hi)
	}
	if p.step > 0 {
		body += "/" + strconv.Itoa(p.step)
	}
	return body
}

// field is one field as written: plain `*`, or parts.
type field struct {
	any   bool
	parts []part
}

// plain reports the field written as `*` alone, which Parse reads as
// unrestricted (the day rule turns on it).
func (f field) plain() bool { return f.String() == "*" }

func (f field) String() string {
	if f.any {
		return "*"
	}
	var s []string
	for _, p := range f.parts {
		s = append(s, p.String())
	}
	return strings.Join(s, ",")
}

// set expands a field the way Parse's doc states: `*` is the whole field,
// `a` one value, `a-b` a range, `/step` steps within the body, and
// `a/step` walks from a to the top of the field; 7 in day-of-week is
// Sunday.
func (f field) set(b fieldBounds, sundaySeven bool) map[int]bool {
	top := b.max
	if sundaySeven {
		top = 7
	}
	out := map[int]bool{}
	add := func(v int) {
		if sundaySeven && v == 7 {
			v = 0
		}
		out[v] = true
	}
	if f.plain() {
		for v := b.min; v <= top; v++ {
			add(v)
		}
		return out
	}
	for _, p := range f.parts {
		lo, hi, step := p.lo, p.hi, max(p.step, 1)
		switch {
		case p.star:
			lo, hi = b.min, top
		case !p.ranged && p.step > 0:
			hi = top
		case !p.ranged:
			hi = lo
		}
		for v := lo; v <= hi; v += step {
			add(v)
		}
	}
	return out
}

type expr struct{ f [5]field }

func (e expr) String() string {
	var s []string
	for _, f := range e.f {
		s = append(s, f.String())
	}
	return strings.Join(s, " ")
}

// matcher is the oracle's reading of a wall-clock minute, its fields
// expanded once.
func (e expr) matcher() func(t time.Time) bool {
	sets := [5]map[int]bool{}
	for i, f := range e.f {
		sets[i] = f.set(cronBounds[i], i == 4)
	}
	domAny, dowAny := e.f[2].plain(), e.f[4].plain()
	return func(t time.Time) bool {
		if !sets[0][t.Minute()] || !sets[1][t.Hour()] || !sets[3][int(t.Month())] {
			return false
		}
		dom, dow := sets[2][t.Day()], sets[4][int(t.Weekday())]
		switch {
		case domAny && dowAny:
			return true
		case dowAny:
			return dom
		case domAny:
			return dow
		}
		return dom || dow
	}
}

// matches is matcher for one minute.
func (e expr) matches(t time.Time) bool { return e.matcher()(t) }

// feasible is the oracle for Parse's refusal: a restricted day-of-month
// beside a plain `*` day-of-week must name a day some given month has.
func (e expr) feasible() bool {
	if e.f[2].plain() || !e.f[4].plain() {
		return true
	}
	doms, months := e.f[2].set(cronBounds[2], false), e.f[3].set(cronBounds[3], false)
	for m := range months {
		for d := range doms {
			if d <= maxDays[m] {
				return true
			}
		}
	}
	return false
}

// drawField draws a field, plain `*` most often so expressions stay dense
// enough to fire within days.
func drawField(ht *hegel.T, b fieldBounds, sundaySeven bool) field {
	top := b.max
	if sundaySeven {
		top = 7
	}
	if hegel.Draw(ht, hegel.WeightedBooleans(0.5)) {
		return field{any: true}
	}
	var f field
	n := hegel.Draw(ht, hegel.Integers(1, 3))
	for range n {
		kind := hegel.Draw(ht, hegel.Integers(0, 3))
		var p part
		switch kind {
		case 0:
			p.star = true
			p.bare = hegel.Draw(ht, hegel.WeightedBooleans(0.2))
		case 1:
			p.lo = hegel.Draw(ht, hegel.Integers(b.min, top))
		default:
			lo := hegel.Draw(ht, hegel.Integers(b.min, top))
			hi := hegel.Draw(ht, hegel.Integers(lo, top))
			p.lo, p.hi, p.ranged = lo, hi, true
		}
		if kind == 3 || p.bare || hegel.Draw(ht, hegel.WeightedBooleans(0.3)) {
			step := hegel.Draw(ht, hegel.Integers(1, top-b.min+1))
			p.step = step
		}
		f.parts = append(f.parts, p)
	}
	return f
}

func drawExpr(ht *hegel.T) expr {
	var e expr
	for i := range e.f {
		e.f[i] = drawField(ht, cronBounds[i], i == 4)
	}
	ht.Note("cron: " + e.String())
	return e
}

// drawInstant draws a zone and an instant in it, most often in a month
// clocks change in.
func drawInstant(ht *hegel.T) (*time.Location, time.Time) {
	zone := hegel.Draw(ht, hegel.SampledFrom(propZones))
	loc, err := time.LoadLocation(zone)
	if err != nil {
		ht.Fatalf("zone %s: %v", zone, err)
	}
	year := hegel.Draw(ht, hegel.Integers(2000, 2105))
	month := hegel.Draw(ht, hegel.Integers(1, 12))
	if hegel.Draw(ht, hegel.WeightedBooleans(0.7)) {
		month = hegel.Draw(ht, hegel.SampledFrom([]int{3, 4, 9, 10, 11}))
	}
	day := hegel.Draw(ht, hegel.Integers(1, 31))
	hour := hegel.Draw(ht, hegel.Integers(0, 23))
	minute := hegel.Draw(ht, hegel.Integers(0, 59))
	second := hegel.Draw(ht, hegel.Integers(0, 59))
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, loc)
	ht.Note(fmt.Sprintf("after: %s (%s)", t.Format(time.RFC3339), zone))
	return loc, t
}

func sameWall(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd && a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

// TestPropertyParseReadsEachFieldAsWritten: an expression the oracle
// finds feasible parses, and each field allows exactly the values its
// written form names.
func TestPropertyParseReadsEachFieldAsWritten(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		c, err := Parse(e.String())
		if err != nil {
			if e.feasible() {
				ht.Fatalf("Parse(%q): %v", e, err)
			}
			return
		}
		got := [5]cronField{c.minute, c.hour, c.dom, c.month, c.dow}
		for i, f := range e.f {
			want := f.set(cronBounds[i], i == 4)
			hi := cronBounds[i].max
			if i == 4 {
				hi = 6 // 7 is Sunday's alias: no weekday reads its bit
			}
			for v := cronBounds[i].min; v <= hi; v++ {
				if got[i].has(v) != want[v] {
					ht.Fatalf("Parse(%q) field %d: value %d allowed=%v, written form says %v", e, i, v, got[i].has(v), want[v])
				}
			}
			if got[i].any != f.plain() {
				ht.Fatalf("Parse(%q) field %d: any=%v, written %q", e, i, got[i].any, f)
			}
		}
	})
}

// TestPropertyParseRefusesAnInfeasibleDay: a day-of-month no given month
// has, beside a plain `*` day-of-week, is refused.
func TestPropertyParseRefusesAnInfeasibleDay(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		if _, err := Parse(e.String()); err == nil && !e.feasible() {
			ht.Fatalf("Parse(%q) accepted a day no month in it has", e)
		}
	})
}

// TestPropertyParseRefusesWhatIsNotCron: a value out of its field's
// range, a descending range, a zero or missing step, an empty part, a
// name, a sign or the wrong number of fields is refused.
func TestPropertyParseRefusesWhatIsNotCron(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		fields := strings.Fields(e.String())
		i := hegel.Draw(ht, hegel.Integers(0, 4))
		b := cronBounds[i]
		top := b.max
		if i == 4 {
			top = 7
		}
		bad := hegel.Draw(ht, hegel.SampledFrom([]string{
			strconv.Itoa(top + 1), strconv.Itoa(b.min - 1), fmt.Sprintf("%d-%d", top, b.min),
			"*/0", "1/", "*/-1", "", ",", "1,", "mon", "jan", "+1", "1-", "-1", "L", "?", "1.5", "0x1",
		}))
		if b.min == 0 && bad == "-1" {
			bad = "-2"
		}
		if top == b.min && bad == fmt.Sprintf("%d-%d", top, b.min) {
			return
		}
		count := hegel.Draw(ht, hegel.Booleans())
		var expr string
		if count {
			fields[i] = bad
			expr = strings.Join(fields, " ")
		} else if n := hegel.Draw(ht, hegel.SampledFrom([]int{0, 4, 6})); n < 5 {
			expr = strings.Join(fields[:n], " ")
		} else {
			expr = strings.Join(append(fields, "0"), " ")
		}
		if bad == "" && count {
			return // an empty field is just one fewer field: the count case covers it
		}
		if _, err := Parse(expr); err == nil {
			ht.Fatalf("Parse(%q) accepted", expr)
		}
	})
}

// TestPropertyNextIsAMatchingMinuteStrictlyAfter: Next is a whole minute,
// strictly after the instant asked from, that the expression matches on
// the zone's wall clock.
func TestPropertyNextIsAMatchingMinuteStrictlyAfter(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		c, err := Parse(e.String())
		if err != nil {
			return
		}
		loc, after := drawInstant(ht)
		got := c.Next(after, loc)
		if got.IsZero() {
			return // TestPropertyAParsedExpressionAlwaysFiresAgain's to judge
		}
		local := got.In(loc)
		if !got.After(after) || local.Second() != 0 || local.Nanosecond() != 0 || !e.matches(local) {
			ht.Fatalf("Next = %s (%s): after=%v whole=%v matches=%v", local.Format(time.RFC3339), loc, got.After(after), local.Second() == 0, e.matches(local))
		}
	})
}

// TestPropertyNextIsTheFirstMatch: no minute between the instant asked
// from and Next matches — walked minute by minute over absolute instants,
// skipping only the fall-back repeat of the asking minute's own wall time.
func TestPropertyNextIsTheFirstMatch(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		c, err := Parse(e.String())
		if err != nil {
			return
		}
		loc, after := drawInstant(ht)
		got := c.Next(after, loc)
		const window = 3 * 24 * 60
		matches := e.matcher()
		asked := after.In(loc)
		m := after.Truncate(time.Minute)
		for range window {
			m = m.Add(time.Minute)
			local := m.In(loc)
			if !m.After(after) || sameWall(local, asked) || !matches(local) {
				continue
			}
			if !got.Equal(m) {
				ht.Fatalf("Next = %s, but %s matches first", got.In(loc).Format(time.RFC3339), local.Format(time.RFC3339))
			}
			return
		}
		if !got.IsZero() && !got.After(m) {
			ht.Fatalf("Next = %s, which the walk of the first %d minutes did not match", got.In(loc).Format(time.RFC3339), window)
		}
	})
}

// TestPropertyAParsedExpressionAlwaysFiresAgain: whatever Parse accepts,
// Next finds a next fire from any instant — Parse's feasibility check
// keeps the zero answer unreachable — and Check accepts it.
func TestPropertyAParsedExpressionAlwaysFiresAgain(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		c, err := Parse(e.String())
		if err != nil {
			return
		}
		loc, after := drawInstant(ht)
		if c.Next(after, loc).IsZero() {
			ht.Fatalf("Parse accepted %q, and Next from %s finds no fire", e, after.Format(time.RFC3339))
		}
		if err := Check(e.String(), loc.String()); err != nil {
			ht.Fatalf("Check(%q): %v", e, err)
		}
	})
}

// TestPropertyACompiledCadenceFiresEveryN: `Nm` and `Nh` compile to
// expressions that fire every N minutes, or every N hours on the hour, in
// UTC; a number that does not divide its unit is refused.
func TestPropertyACompiledCadenceFiresEveryN(t *testing.T) {
	props(t, func(ht *hegel.T) {
		unit := hegel.Draw(ht, hegel.SampledFrom([]string{"m", "h"}))
		top := map[string]int{"m": 60, "h": 24}[unit]
		n := hegel.Draw(ht, hegel.Integers(-1, top+1))
		preset := strconv.Itoa(n) + unit
		cron, err := Compile(preset)
		if n < 1 || top%n != 0 {
			if err == nil {
				ht.Fatalf("Compile(%q) = %q", preset, cron)
			}
			return
		}
		if err != nil {
			ht.Fatalf("Compile(%q): %v", preset, err)
		}
		c, err := Parse(cron)
		if err != nil {
			ht.Fatalf("Compile(%q) = %q, which Parse refuses: %v", preset, cron, err)
		}
		_, after := drawInstant(ht)
		every := time.Duration(n) * time.Minute
		if unit == "h" {
			every = time.Duration(n) * time.Hour
		}
		prev := c.Next(after, time.UTC)
		if prev.Sub(after) > every || prev.UTC().Unix()%int64(every/time.Second) != 0 {
			ht.Fatalf("%s (%q): first fire %s after %s", preset, cron, prev.UTC(), after.UTC())
		}
		for range 5 {
			next := c.Next(prev, time.UTC)
			if next.Sub(prev) != every {
				ht.Fatalf("%s (%q): %s then %s", preset, cron, prev.UTC(), next.UTC())
			}
			prev = next
		}
	})
}

// TestPropertyAdvanceCoalescesMissedFires: Advance answers from now
// alone — whatever next_run the row stored, however many fires it missed
// — the first fire strictly after now.
func TestPropertyAdvanceCoalescesMissedFires(t *testing.T) {
	props(t, func(ht *hegel.T) {
		e := drawExpr(ht)
		if _, err := Parse(e.String()); err != nil {
			return
		}
		loc, now := drawInstant(ht)
		back := hegel.Draw(ht, hegel.Integers(0, 100000))
		s := domain.Schedule{Cron: e.String(), Timezone: loc.String(), Enabled: true, NextRun: now.Add(-time.Duration(back) * time.Minute)}
		want, _ := NextRun(s.Cron, s.Timezone, now)
		if got := Advance(s, now); !got.Equal(want) || !got.After(now) {
			ht.Fatalf("Advance = %s, want %s, after now %s", got, want, now)
		}
	})
}

// TestPropertyDueOnceItsNextRunIsReached: an enabled row is due when run
// now was asked for, or once now reaches a next_run it has; a disabled
// row never is.
func TestPropertyDueOnceItsNextRunIsReached(t *testing.T) {
	props(t, func(ht *hegel.T) {
		enabled := hegel.Draw(ht, hegel.Booleans())
		requested := hegel.Draw(ht, hegel.Booleans())
		_, now := drawInstant(ht)
		var next time.Time
		if hegel.Draw(ht, hegel.WeightedBooleans(0.8)) {
			delta := hegel.Draw(ht, hegel.Integers(-120, 120))
			next = now.Add(time.Duration(delta) * time.Second)
		}
		s := domain.Schedule{Enabled: enabled, RunRequested: requested, NextRun: next}
		want := enabled && (requested || (!next.IsZero() && !now.Before(next)))
		if got := Due(s, now); got != want {
			ht.Fatalf("Due(%+v, %s) = %v", s, now, got)
		}
	})
}
