package agent

// Property tests of reading a backend's usage-limit message, driven by
// hegel (https://hegel.dev). A session that hit its limit sleeps until
// the time read here and then resumes by itself: read too early it fails
// again, read too late it sleeps for nothing. The oracle walks the clock
// minute by minute in the zone the message names.

import (
	"fmt"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

const usageCases = 2000

var usageZones = []string{"", "UTC", "America/New_York", "Europe/Berlin", "Australia/Lord_Howe", "Asia/Kathmandu", "Pacific/Chatham", "America/Santiago"}

type usageMsg struct {
	text      string
	loc       *time.Location
	hour, min int // 24-hour
	month     time.Month
	day       int // 0: the message names no day
}

func drawUsage(ht *hegel.T, now time.Time) usageMsg {
	zone := hegel.Draw(ht, hegel.SampledFrom(usageZones))
	loc := now.Location()
	if zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			ht.Fatalf("zone %s: %v", zone, err)
		}
		loc = l
	}
	// the message is a real instant shown on the zone's clock: one a few
	// hours to a year ahead, or — read late — up to a day and a half past
	delta := hegel.Draw(ht, hegel.Integers(-36*60, 370*24*60))
	r := now.Add(time.Duration(delta) * time.Minute).In(loc)
	hour, m := r.Hour(), r.Minute()
	h12 := hour % 12
	if h12 == 0 {
		h12 = 12
	}
	pm := hour >= 12
	clock := fmt.Sprint(h12)
	if m != 0 || hegel.Draw(ht, hegel.Booleans()) {
		clock += fmt.Sprintf(":%02d", m)
	}
	ampm := map[bool]string{false: "am", true: "pm"}[pm]
	if hegel.Draw(ht, hegel.Booleans()) {
		ampm = map[bool]string{false: "AM", true: "PM"}[pm]
	}
	msg := usageMsg{loc: loc, hour: hour, min: m}
	when := clock + ampm
	if hegel.Draw(ht, hegel.Booleans()) {
		msg.month, msg.day = r.Month(), r.Day()
		name := msg.month.String()
		if hegel.Draw(ht, hegel.Booleans()) {
			name = name[:3]
		}
		when = fmt.Sprintf("%s %d, %s", name, msg.day, when)
	}
	if zone != "" {
		when += " (" + zone + ")"
	}
	kind := hegel.Draw(ht, hegel.SampledFrom([]string{"session", "usage", "weekly", "5-hour"}))
	msg.text = fmt.Sprintf("You've hit your %s limit · resets %s", kind, when)
	ht.Note(fmt.Sprintf("now %s; %q", now.Format(time.RFC3339), msg.text))
	return msg
}

func drawNow(ht *hegel.T) time.Time {
	zone := hegel.Draw(ht, hegel.SampledFrom([]string{"UTC", "America/New_York", "Asia/Tokyo"}))
	loc, _ := time.LoadLocation(zone)
	sec := hegel.Draw(ht, hegel.Integers(int64(946684800), int64(4102444800)))
	if hegel.Draw(ht, hegel.WeightedBooleans(0.5)) {
		// around a year's turn, where a dated reset can be last year's
		year := hegel.Draw(ht, hegel.Integers(2001, 2099))
		off := hegel.Draw(ht, hegel.Integers(-3*86400, 3*86400))
		sec = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix() + int64(off)
	}
	return time.Unix(sec, 0).In(loc)
}

// exists reports whether the wall time names a real instant in loc that
// day: a message is a real instant shown on that clock, so it never names
// one the clock skips.
func exists(y int, mo time.Month, d, h, mi int, loc *time.Location) bool {
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	return t.Day() == d && t.Hour() == h && t.Minute() == mi
}

// TestPropertyAResetTimeIsTheNextSuchClockTime: a reset naming only a
// clock time is the first instant after now at which the named zone's
// clock reads that time.
func TestPropertyAResetTimeIsTheNextSuchClockTime(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		now := drawNow(ht)
		msg := drawUsage(ht, now)
		if msg.day != 0 {
			return
		}
		at := now.In(msg.loc)
		for i := range 3 {
			d := at.AddDate(0, 0, i)
			if !exists(d.Year(), d.Month(), d.Day(), msg.hour, msg.min, msg.loc) {
				return
			}
		}
		got, ok := ParseUsageLimit(msg.text, now)
		if !ok {
			ht.Fatalf("not read as a usage limit")
		}
		var want time.Time
		for m := now.Truncate(time.Minute); ; m = m.Add(time.Minute) {
			if l := m.In(msg.loc); m.After(now) && l.Hour() == msg.hour && l.Minute() == msg.min {
				want = m
				break
			}
		}
		if !got.Equal(want) {
			ht.Fatalf("reset %s, want %s", got.In(msg.loc).Format(time.RFC3339), want.In(msg.loc).Format(time.RFC3339))
		}
	}, hegel.WithTestCases(usageCases), hegel.WithDatabase(""))
}

// TestPropertyADatedResetIsThatDayNotLongPast: a reset naming a day is
// that day's clock time in the year that puts it nearest ahead — the
// next year once it has passed — and never a year off: one that passed
// within the last day reads as passed, ready now.
func TestPropertyADatedResetIsThatDayNotLongPast(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		now := drawNow(ht)
		msg := drawUsage(ht, now)
		if msg.day == 0 {
			return
		}
		y := now.In(msg.loc).Year()
		var want time.Time
		for _, year := range []int{y - 1, y, y + 1} {
			if !exists(year, msg.month, msg.day, msg.hour, msg.min, msg.loc) {
				return
			}
			c := time.Date(year, msg.month, msg.day, msg.hour, msg.min, 0, 0, msg.loc)
			if want.IsZero() && !c.Before(now.Add(-24*time.Hour)) {
				want = c
			}
		}
		got, ok := ParseUsageLimit(msg.text, now)
		if !ok {
			ht.Fatalf("not read as a usage limit")
		}
		if !got.Equal(want) {
			ht.Fatalf("reset %s, want %s", got.In(msg.loc).Format(time.RFC3339), want.In(msg.loc).Format(time.RFC3339))
		}
	}, hegel.WithTestCases(usageCases), hegel.WithDatabase(""))
}

// TestPropertyAClockThatIsNoClockIsNotAReset: an hour past 12 or a minute
// past 59 is not a reset time.
func TestPropertyAClockThatIsNoClockIsNotAReset(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		h := hegel.Draw(ht, hegel.Integers(0, 99))
		m := hegel.Draw(ht, hegel.Integers(0, 99))
		if h >= 1 && h <= 12 && m <= 59 {
			return
		}
		text := fmt.Sprintf("You've hit your usage limit · resets %d:%02dam (UTC)", h, m)
		if got, ok := ParseUsageLimit(text, time.Now()); ok {
			ht.Fatalf("%q read as a reset at %s", text, got)
		}
	}, hegel.WithTestCases(usageCases), hegel.WithDatabase(""))
}
