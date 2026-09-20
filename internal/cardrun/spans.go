package cardrun

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/morphis/gummi/internal/state"
)

// Span is one interval on a card's record. A zero To means the interval
// is still open — the thing it spans has not ended yet, and every
// consumer clamps it to whatever its own right edge is.
type Span struct {
	From, To time.Time
}

// DecisionSpans derives the card's waiting-on-you intervals from its
// log, raw: each span runs from the decision_open event to the gate or
// ask that answered it, and a span with a zero To is a decision nobody
// has answered yet.
//
// This is the one derivation behind every waiting figure a surface
// shows. Clock.OnYou is these spans clamped to the card's own life
// (openDecisionTime); the workspace timeline draws them clamped to its
// window instead. One record, one correlation rule, two clamps — which
// is what keeps a timeline from ever showing a wait the card's own
// clock would deny, short of the right edge its window ends at.
//
// The correlation rule is exactly the one state.OpenDecisions applies,
// restated over the event slice because this package takes no store: a
// decision opens with an EventDecisionOpen carrying an id and closes
// when a later gate or ask event carries the same id. An answer
// recorded at or before its own question is a clock skew, not a
// negative wait, and the span is dropped rather than inverted.
//
// The spans are unioned rather than left as written because two
// decisions can stand open together — an ask raised while a gate waits
// — and a card cannot wait on two people for twice the time.
func DecisionSpans(evs []state.CardEvent) []Span {
	answeredAt := map[string]time.Time{}
	for _, ev := range evs {
		if ev.Kind != state.EventGate && ev.Kind != state.EventAsk {
			continue
		}
		id := correlatingID(ev)
		if id == "" {
			continue
		}
		if at, seen := answeredAt[id]; !seen || ev.At.Before(at) {
			answeredAt[id] = ev.At
		}
	}

	var spans []Span
	for _, ev := range evs {
		if ev.Kind != state.EventDecisionOpen {
			continue
		}
		var p state.DecisionPayload
		if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.ID == "" {
			continue
		}
		var to time.Time
		if at, ok := answeredAt[p.ID]; ok {
			if !at.After(ev.At) {
				continue
			}
			to = at
		}
		spans = append(spans, Span{From: ev.At, To: to})
	}
	return unionSpans(spans)
}

// WaitSpans is DecisionSpans at drawing grain: the decision spans
// clamped to [from,to], an unanswered one running to its end. A surface
// that draws the waits uses this; a surface that sums them uses
// WaitTime — the two are the same clamp, which is what keeps a drawn
// wait and a summed one from disagreeing.
func WaitSpans(evs []state.CardEvent, from, to time.Time) []Span {
	if !to.After(from) {
		return nil
	}
	return clampSpans(DecisionSpans(evs), from, to)
}

// WaitTime is the card's waiting-on-you total over [from,to]: the same
// spans WaitSpans returns, summed as a union. openDecisionTime — the
// per-card clock's charge to a person — is this at the card's own life;
// the workspace window calls it with its own horizon.
func WaitTime(evs []state.CardEvent, from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	return unionDuration(clampSpans(DecisionSpans(evs), from, to))
}

// Buckets turns a name→credits map into the largest-first slice every
// display wants, ties broken by name so the order never wobbles. It is
// the exported form of the fold's own builder, so the workspace report
// — which folds the same shape at fleet scale — cannot grow a second
// ordering that drifts from the one the card's page prints.
func Buckets(m map[string]float64) []Bucket { return buckets(m) }

// spanLater returns the later of two span ends, where a zero end reads
// as unbounded: an open span swallows any closed one it overlaps.
func spanLater(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return time.Time{}
	case b.IsZero():
		return time.Time{}
	case a.After(b):
		return a
	default:
		return b
	}
}

// unionSpans merges overlapping and touching spans. An open span (zero
// To) stays open for as long as nothing closed after it.
func unionSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if len(out) == 0 {
			out = append(out, s)
			continue
		}
		cur := &out[len(out)-1]
		// A still-open current span swallows everything; a closed one
		// only merges a successor that starts before it ends.
		if !cur.To.IsZero() && s.From.After(cur.To) {
			out = append(out, s)
			continue
		}
		cur.To = spanLater(cur.To, s.To)
	}
	return out
}

// clampSpans clips spans to [first,last], an open span running to last.
// A span that ends up empty — it began after the window closed, or its
// answer predates the window — is dropped rather than inverted.
func clampSpans(spans []Span, first, last time.Time) []Span {
	var out []Span
	for _, s := range spans {
		from, to := s.From, s.To
		if from.Before(first) {
			from = first
		}
		if to.IsZero() || to.After(last) {
			to = last
		}
		if to.After(from) {
			out = append(out, Span{From: from, To: to})
		}
	}
	return out
}

// unionDuration sums a span slice after merging whatever clipping left
// adjacent.
func unionDuration(spans []Span) time.Duration {
	var total time.Duration
	for _, s := range unionSpans(spans) {
		total += s.To.Sub(s.From)
	}
	return total
}
