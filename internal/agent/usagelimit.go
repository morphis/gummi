package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// usageLimitRE reads the line Claude Code prints when a subscription's
// window is spent: "You've hit your session limit · resets 3:20am (UTC)",
// also "usage limit", "weekly limit" and a reset that names a day
// ("resets Oct 27, 3am (Europe/Berlin)"). Only the fixed words are matched,
// so a model's prose that mentions a limit is not mistaken for it.
var usageLimitRE = regexp.MustCompile(
	`(?i)hit your (?:[a-z0-9 -]{0,20} )?limit\b.{0,8}resets\s+(?:([a-z]{3,9})\s+(\d{1,2}),?\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\s*(?:\(([^)]+)\))?`)

// ParseUsageLimit reports when a backend's usage limit lifts, read from the
// text it failed with. ok is false when text is not that message. The time
// is the next such clock time after now, in the zone the message names (the
// local zone when it names none or one that cannot be loaded); a message
// that names a day is read as that day, the next year if it already passed.
func ParseUsageLimit(text string, now time.Time) (reset time.Time, ok bool) {
	m := usageLimitRE.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[3])
	min := 0
	if m[4] != "" {
		min, _ = strconv.Atoi(m[4])
	}
	if hour < 1 || hour > 12 || min > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}
	loc := now.Location()
	if z := strings.TrimSpace(m[6]); z != "" {
		if l, err := time.LoadLocation(z); err == nil {
			loc = l
		}
	}
	at := now.In(loc)
	if m[1] != "" {
		month, found := parseMonth(m[1])
		day, _ := strconv.Atoi(m[2])
		if !found || day < 1 || day > 31 {
			return time.Time{}, false
		}
		reset = time.Date(at.Year(), month, day, hour, min, 0, 0, loc)
		// the year that puts the day nearest: last year's when it passed
		// within the day (a reset read just after the year turned, which
		// this year's date would put a year away), next year's when this
		// year's passed longer ago
		grace := now.Add(-24 * time.Hour)
		if prev := reset.AddDate(-1, 0, 0); !prev.Before(grace) {
			reset = prev
		} else if reset.Before(grace) {
			reset = reset.AddDate(1, 0, 0)
		}
		return reset, true
	}
	reset = time.Date(at.Year(), at.Month(), at.Day(), hour, min, 0, 0, loc)
	if !reset.After(now) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset, true
}

func parseMonth(s string) (time.Month, bool) {
	s = strings.ToLower(s)
	for m := time.January; m <= time.December; m++ {
		name := strings.ToLower(m.String())
		if s == name || (len(s) == 3 && strings.HasPrefix(name, s)) {
			return m, true
		}
	}
	return 0, false
}
