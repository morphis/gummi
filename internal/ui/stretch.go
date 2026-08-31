package ui

// The autopilot stretch: one period a card spent being driven without
// you, derived from the card's own event log so the thread can draw it
// where it happened instead of summarising it in a block pinned to the
// end of the page.
//
// The block this replaces was a rollup over the whole log, rebuilt every
// frame and appended after the live stage, so every line that arrived
// afterwards — the agent's output, and your own turns once you took the
// card back — was inserted above it. It stayed the newest thing on the
// page for as long as the card existed, describing a period that had
// been over for hours. A period has two ends, and the fix is to find
// both of them.
//
// Nothing here guesses where a period began. A stretch opens only on a
// row that says so (state.AutopilotTookOver), written by the switch and
// by the headless driver, because every available heuristic gets the
// same case wrong: a person drives a stage by hand, hands the card over
// at its gate, and any rule that walks backwards from the first machine
// crossing swallows the stage the person drove into the machine's
// stretch. For a record whose whole purpose is saying honestly what ran
// without you, over-claiming is the one failure that matters.
//
// Closing is derived, and deliberately generous about what counts,
// because a period that never closes swallows the rest of the card's
// life. Four things end one, whichever lands first: an explicit handback
// row, a park, a gate a person crossed, or a turn a person typed.

import (
	"encoding/json"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// stretchClose names how a period ended, which is the whole of what the
// closing rule says. The three are not decoration: they answer different
// questions. parked means it stopped short and something is waiting;
// finished means it carried the card as far as it is allowed to, to the
// landing gate it may never cross itself; taken back means you did not
// wait for either.
type stretchClose string

const (
	stretchRunning   stretchClose = ""
	stretchParked    stretchClose = "parked"
	stretchFinished  stretchClose = "finished"
	stretchTakenBack stretchClose = "taken-back"
)

// autopilotStretch is one such period. from and to bound it as a half-
// open range of indices into the event slice it was derived from: to is
// the index of the event that closed it (that event is the boundary, not
// part of the period), or len(events) while it is still running.
type autopilotStretch struct {
	from, to int
	openedAt time.Time
	closedAt time.Time
	closed   stretchClose
	// reason is the sentence the closing event carried — a park's own
	// Detail, kept verbatim the way the park line keeps it, or the
	// handback's. Empty when the close was inferred from a person simply
	// acting, which explains itself.
	reason string
	mode   string
	// gates and answers are what autopilot decided inside this period,
	// and they are the whole of the tally. Credits are not here because
	// the masthead already carries them, and corrective rounds are not
	// here because they are a whole-card count that no store can slice by
	// period — printing either under a heading that names a bounded
	// window would be the same lie in a smaller font.
	gates   []receiptGate
	answers []receiptAnswer
}

// running reports whether the period is still open — autopilot has the
// card right now, so the thread draws an opening rule and no closing
// one.
func (st autopilotStretch) running() bool { return st.closed == stretchRunning }

// decidedNothing reports whether autopilot crossed no gate and answered
// no question in this period. The stretch still draws: "it ran implement
// while you were out" is worth saying, and the folded receipt inside the
// rules says which stage. Only the tally line is withheld, which is the
// same per-row restraint the block this replaces applied to itself —
// moved down from the whole block to the one row that would otherwise
// read as a row of zeroes.
func (st autopilotStretch) decidedNothing() bool {
	return len(st.gates) == 0 && len(st.answers) == 0
}

// autopilotStretches walks a card's event log once and returns every
// period it ran itself, in order. events must be in seq order, which is
// what Store.Events returns.
//
// f is needed only to tell a period that finished from one that parked:
// "finished" means the card reached the last decision on its own
// workflow, and which stage that is depends on the card.
func autopilotStretches(f domain.Feature, events []state.CardEvent) []autopilotStretch {
	var out []autopilotStretch
	cur := -1 // index into out of the open period, -1 when none

	closeWith := func(i int, at time.Time, how stretchClose, reason string) {
		if cur < 0 {
			return
		}
		out[cur].to = i
		out[cur].closedAt = at
		out[cur].closed = how
		out[cur].reason = reason
		cur = -1
	}

	for i, ev := range events {
		switch ev.Kind {
		case state.EventAutopilot:
			var p state.AutopilotPayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			switch p.Event {
			case state.AutopilotTookOver:
				if cur >= 0 {
					// Already driving. One uninterrupted period is one
					// period however many times the row was written inside
					// it — the headless driver writes one per process and
					// deliberately does not dedupe, on the grounds that a
					// duplicate is something the reader can collapse and a
					// missing row is a period that can never open at all.
					// This is that collapse.
					continue
				}
				out = append(out, autopilotStretch{
					from: i, to: len(events), openedAt: ev.At, mode: p.Mode,
				})
				cur = len(out) - 1
			case state.AutopilotHandedBack:
				closeWith(i, ev.At, stretchTakenBack, p.Reason)
			}

		case state.EventPark:
			if cur < 0 {
				continue
			}
			var p state.ParkPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			how := stretchParked
			if landingGate(f, ev.Stage) && newestExitVerdict(events[:i]) != state.StatusFail {
				// It got the card as far as a card is allowed to go on its
				// own. The verdict guard matters: autopilot parks at the
				// landing gate whether verification passed or failed, and
				// calling a failed verify "finished" would be the closing
				// rule congratulating itself over a card that is worse off
				// than when it started.
				how = stretchFinished
			}
			closeWith(i, ev.At, how, p.Detail)

		case state.EventGate:
			var p state.GatePayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if humanGateActors[p.Actor] {
				closeWith(i, ev.At, stretchTakenBack, "")
				continue
			}
			if cur >= 0 {
				out[cur].gates = append(out[cur].gates, receiptGate{
					from: domain.Stage(p.From), at: ev.At,
				})
			}

		case state.EventAsk:
			var p state.AskPayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if askedBy(p) == state.ActorUser {
				closeWith(i, ev.At, stretchTakenBack, "")
				continue
			}
			if cur >= 0 && askedBy(p) == state.ActorAutopilot {
				out[cur].answers = append(out[cur].answers, receiptAnswer{
					answer: p.Answer, at: ev.At,
				})
			}

		case state.EventMessage:
			var p messagePayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if p.Author == string(engine.AuthorUser) {
				// A turn you typed is you back in the room, and it is the
				// backstop that keeps a period from running forever when
				// whatever should have closed it never got written — a
				// process killed mid-run parks nothing.
				closeWith(i, ev.At, stretchTakenBack, "")
			}
		}
	}
	return out
}

// askedBy names who answered an ask. By is what the caller stated
// outright and is what this trusts; Actor is the older field, inferred
// from the card's stored mode, and stands in only for rows written
// before By existed (AskPayload's own doc comment).
func askedBy(p state.AskPayload) string {
	if p.By != "" {
		return p.By
	}
	return p.Actor
}

// landingGate reports whether stage is the last decision on this card's
// own workflow — the one autopilot never crosses, because landing on
// main stays a person's call under every mode. It is read from the
// card's own sequence rather than hardcoded to verify, since a bug and a
// research card end somewhere else.
func landingGate(f domain.Feature, stage domain.Stage) bool {
	seq := stageSequence(f)
	for len(seq) > 0 && seq[len(seq)-1] == domain.StageDone {
		seq = seq[:len(seq)-1]
	}
	return len(seq) > 0 && seq[len(seq)-1] == stage
}

// newestExitVerdict is the verdict on the most recent stage_exit in
// events, or "" when nothing has finished yet.
func newestExitVerdict(events []state.CardEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != state.EventStageExit {
			continue
		}
		var p stageExitPayload
		_ = json.Unmarshal([]byte(events[i].Payload), &p)
		return p.Verdict
	}
	return ""
}

// stretchAt reports the period covering event index i, and whether there
// is one. The rendering side asks this per event to decide whether a
// machine crossing is autopilot's — inside a period it is, and says so;
// outside one it keeps the actor's own name, because the review→fix loop
// crosses gates unattended on cards nobody ever handed over.
func stretchAt(stretches []autopilotStretch, i int) (autopilotStretch, bool) {
	for _, st := range stretches {
		if i >= st.from && i < st.to {
			return st, true
		}
	}
	return autopilotStretch{}, false
}
