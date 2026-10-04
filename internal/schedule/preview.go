package schedule

// The cadence preview: what a person sees in a schedule form before
// anything is stored. It is pure composition over the package's own
// primitives — Compile, Parse, Next — so the faces cannot drift from the
// write boundary on what a cadence means: the previewed expression is
// the stored one, and a cadence the package refuses is refused in the
// form, not at the first fire.

import (
	"fmt"
	"strings"
	"time"
)

// previewFires is how many coming fires a preview shows: enough to see a
// cadence's shape (hourly is three hours, daily is three days) without
// making a dialog tall.
const previewFires = 3

// Result is what a cadence preview answers: the expression as the store
// would hold it, why the cadence is refused (nil when it is not), and —
// only when it is valid — the coming fires in the zone it named.
type Result struct {
	// Cron is the canonical 5-field expression the write boundary would
	// store: the preset compiled, or the named expression trimmed. Empty
	// when no cadence was given at all.
	Cron string
	// Err says why the cadence is refused: no cadence given, a preset
	// this package cannot state exactly, an expression that does not
	// parse or can never fire, or a zone it does not know. Nil is valid.
	Err error
	// Next are the coming fires, soonest first, in the asked zone. Empty
	// whenever Err is not nil.
	Next []time.Time
}

// Valid reports whether the previewed cadence would store and fire.
func (r Result) Valid() bool { return r.Err == nil }

// Preview is what both faces show before a schedule is stored: given the
// cadence inputs a form holds — a preset, an expression, either optional
// when the other is present — and a timezone, it answers with the
// expression the store would hold, whether the cadence is one the
// package accepts, and the next few fires in the zone named.
//
// It adds no cron logic of its own. The precedence mirrors the write
// boundary: when both inputs are present the expression wins and the
// preset is ignored, so a both-filled form previews the cadence it
// would save; an empty timezone is the host's zone, the same reading
// every stored row gets. The zero error and three fires a valid cadence
// answers with are computed off the now the caller hands in, so the
// same inputs always answer the same preview.
func Preview(every, cronExpr, tz string, now time.Time) Result {
	expr := strings.TrimSpace(cronExpr)
	if expr == "" {
		preset := strings.TrimSpace(every)
		if preset == "" {
			return Result{Err: fmt.Errorf("give the cadence: cron %q or every 5m", "0 * * * *")}
		}
		compiled, err := Compile(preset)
		if err != nil {
			return Result{Err: err}
		}
		expr = compiled
	}
	c, err := Parse(expr)
	if err != nil {
		return Result{Cron: expr, Err: err}
	}
	loc, err := location(tz)
	if err != nil {
		return Result{Cron: expr, Err: err}
	}
	next := make([]time.Time, 0, previewFires)
	t := now
	for range previewFires {
		n := c.Next(t, loc)
		if n.IsZero() {
			break
		}
		next = append(next, n)
		t = n
	}
	return Result{Cron: expr, Next: next}
}
