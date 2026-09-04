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
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/workflow"
)

// stretchClose names how a period ended, which is the whole of what the
// closing rule says. The four are not decoration: they answer different
// questions. parked means it stopped short and something is waiting;
// finished means it carried the card as far as it is allowed to, to the
// landing gate it may never cross itself; taken back means you did not
// wait for either; orphaned means none of the above ever happened — the
// process driving it is simply gone.
type stretchClose string

const (
	stretchRunning   stretchClose = ""
	stretchParked    stretchClose = "parked"
	stretchFinished  stretchClose = "finished"
	stretchTakenBack stretchClose = "taken-back"
	// stretchOrphaned is a period the log never closes because nothing
	// closed it: a crash, an OOM kill, a container restart — the driving
	// process exited without parking, handing back, or anyone taking the
	// card back by hand. Distinct from the other three because it is not
	// derived from any row in the log; it is a query-time judgement
	// (closeOrphaned) applied on top of a stretch the log alone still
	// calls running().
	stretchOrphaned stretchClose = "orphaned"
	// stretchHandedOver is a period that ended because autopilot carried
	// the card into a stage it is not allowed to drive — an interactive
	// one, which needs a person at the keyboard. Nothing in the log says
	// so: autopilot writes a machine gate crossing (not a closing event)
	// and then simply has nothing further it may do, so it parks nothing
	// and hands back nothing. Like stretchOrphaned this is a query-time
	// judgement (closeHandedOver), and it must be distinguished from it:
	// orphaned says the driver died, this says it finished its turn and
	// the card is waiting for you.
	stretchHandedOver stretchClose = "handed-over"
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
			verdict, ran := stageExitVerdict(events[:i], ev.Stage)
			if landingGate(f, ev.Stage) && ran && verdict != state.StatusFail &&
				p.Reason != state.ParkReasonGaveUp {
				// It got the card as far as a card is allowed to go on its
				// own. Both guards matter, and they answer different
				// questions: autopilot parks at the landing gate whether
				// verification passed or failed, and calling a failed
				// verify "finished" would be the closing rule
				// congratulating itself over a card that is worse off than
				// when it started.
				//
				// The verdict alone cannot carry that. A stage that could
				// not reach a verdict at all — the environment could not
				// run the checks, the loop hit its cap — exits with an
				// empty one, which is not StatusFail and so reads here as
				// success. The park's own reason is the field that says
				// what happened: gave-up is by its own definition a stop at
				// a decision only a person can take, which is the opposite
				// of finishing.
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

// closeOrphaned downgrades the newest stretch from running to orphaned
// when the process that opened it is no longer driving the card. At most
// one stretch is ever open (autopilotStretches' own invariant, per
// autopilotDriving's doc comment), so only the last element can need
// this, and only when it is still running.
//
// live answers "is a session — this process's own or another's — driving
// this card right now" (state.CardIsLive). It is threaded in by the
// caller rather than looked up here so this stays a pure function of its
// inputs, the same way the rest of this file is: a query-time judgement
// applied on top of the log-derived stretches, never folded into their
// derivation.
func closeOrphaned(stretches []autopilotStretch, live bool) []autopilotStretch {
	if live || len(stretches) == 0 {
		return stretches
	}
	last := &stretches[len(stretches)-1]
	if last.running() {
		last.closed = stretchOrphaned
	}
	return stretches
}

// closeHandedOver closes a still-open period whose card has come to rest
// at a stage autopilot is not allowed to drive. Autopilot's last act was
// to cross into it; an interactive stage needs a person, so from that
// crossing on the card was the reader's, not the machine's.
//
// The log cannot say this on its own. A machine gate crossing is not one
// of the four closing events (it is autopilot working, not stopping),
// and autopilot writes no park and no handback here because it did not
// give up on anything — it arrived where it was always going to stop.
// closeOrphaned cannot cover it either, and must not: the process that
// opened the period is usually still alive and holding the card, so the
// liveness test correctly says "running" and the period would otherwise
// stay open for the rest of the card's life, claiming a machine drove
// every stage the reader went on to work by hand (BG-085).
//
// Ordered before the liveness judgement by liveStretches, because it is
// the more specific answer: a card resting at an interactive stage got
// there by being handed over, whether or not the driver has since gone.
func closeHandedOver(f domain.Feature, stretches []autopilotStretch, events []state.CardEvent) []autopilotStretch {
	if len(stretches) == 0 || !workflow.Interactive(f.Stage) {
		return stretches
	}
	last := &stretches[len(stretches)-1]
	if !last.running() {
		return stretches
	}
	// dated from the crossing that brought the card here, so the rule
	// carries the moment autopilot actually stopped rather than now.
	at := last.openedAt
	for i := len(events) - 1; i >= last.from; i-- {
		if events[i].Kind == state.EventGate {
			at = events[i].At
			break
		}
	}
	last.closed = stretchHandedOver
	last.closedAt = at
	return stretches
}

// liveStretches is autopilotStretches with the render-time judgements
// folded in — what every rendering call site should call instead of
// autopilotStretches directly.
//
// Two things can close a period that the log itself never closes, and
// they are asked in order of specificity. A card resting at a stage
// autopilot may not drive was handed over, which is a normal ending and
// says so (BG-085). Otherwise, a period still open per the log renders
// as still running only if a process is actually driving the card right
// now; a process killed mid-run writes nothing on its way out, so the
// log can never say this by itself (BG-059).
func liveStretches(f domain.Feature, events []state.CardEvent, ws state.Workspace) []autopilotStretch {
	sts := closeHandedOver(f, autopilotStretches(f, events), events)
	return closeOrphaned(sts, state.CardIsLive(ws, f.ID))
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

// stageExitVerdict is the verdict the given stage finished on, and
// whether it finished at all.
//
// Both halves are load-bearing, and the second one is the subtle one. A
// stage can park without ever exiting — quitting the board stops a
// running session where it stands and records the park with no
// stage_exit behind it — so "the newest exit anywhere in the log" is not
// the same question as "how did this stage end". Asking the looser
// question let an interrupted verify borrow the pass of some earlier
// stage and close its period as "autopilot finished", which is the
// closing rule congratulating itself over a card that never got a
// verdict at all. A stage with no exit of its own has not finished, and
// says so.
func stageExitVerdict(events []state.CardEvent, stage domain.Stage) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != state.EventStageExit || events[i].Stage != stage {
			continue
		}
		var p stageExitPayload
		_ = json.Unmarshal([]byte(events[i].Payload), &p)
		return p.Verdict, true
	}
	return "", false
}

// autopilotDriving reports whether the card is inside an open period right
// now — the board's version of the question running() answers for the
// thread. At most one period is ever open at a time (autopilotStretches
// closes the previous one before opening the next), so checking the last
// element is equivalent to scanning the whole slice for an open one.
func autopilotDriving(stretches []autopilotStretch) bool {
	return len(stretches) > 0 && stretches[len(stretches)-1].running()
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

// --- rendering ---------------------------------------------------------

// stretchLabel names a close the way the rule says it. The wordings
// answer different questions and are not interchangeable: a card that
// parked has something waiting on you, one that finished got as far as
// it is allowed to go on its own, one you took back never reached
// either, and one orphaned never got a chance to say anything at all —
// its driving process is simply gone.
func stretchLabel(how stretchClose) string {
	switch how {
	case stretchFinished:
		return "autopilot finished"
	case stretchTakenBack:
		return "you took back control"
	case stretchOrphaned:
		return "autopilot stopped without saying so"
	case stretchHandedOver:
		return "autopilot handed it to you"
	default:
		return "autopilot parked it"
	}
}

// stretchOpenLine is the rule that opens a period. It is drawn at
// s.Subtle where an ordinary rule is s.Faint — one step brighter and
// nothing more. The period needs to be findable while paging back
// through a long card, and two rules a shade above the furniture do that
// without spending a colour on it, which is what makes the whole thing
// affordable: no new hue, no pinned region, nothing to clean up.
func stretchOpenLine(s *theme.Styles, st autopilotStretch, w int) string {
	return s.Subtle.Render(stretchRule("autopilot took over", st.openedAt, w))
}

// stretchCloseLines are the rule that closes a period and the two lines
// under it. The reason and the tally each take their own row rather than
// riding the rule: the full wording measures 85 columns against an
// 84-column pane before any narrow terminal is considered, so putting
// them on the rule would mean one wording at wide widths and another at
// narrow ones — two things to render, two to test, and a rule that reads
// differently depending on the window.
//
// Either row is withheld when it has nothing to say. A period that
// crossed no gate and answered no question still draws its rules, since
// "it ran implement while you were out" is worth saying; it is only the
// tally that would be a row of zeroes, and that row alone goes.
func stretchCloseLines(s *theme.Styles, st autopilotStretch, w int) []string {
	out := []string{s.Subtle.Render(stretchRule(stretchLabel(st.closed), st.closedAt, w))}
	if st.reason != "" {
		out = append(out, "  "+s.Subtle.Render(ansi.Truncate(sanitize(st.reason), max(w-2, 8), "…")))
	}
	if !st.decidedNothing() {
		out = append(out, "  "+s.Subtle.Render(stretchTally(st)))
	}
	return out
}

// stretchTally counts what autopilot decided, in the two dimensions the
// event log can slice exactly.
func stretchTally(st autopilotStretch) string {
	parts := make([]string, 0, 2)
	if n := len(st.gates); n > 0 {
		parts = append(parts, itoa(n)+" gate"+plural(n))
	}
	if n := len(st.answers); n > 0 {
		parts = append(parts, itoa(n)+" answer"+plural(n))
	}
	return strings.Join(parts, " · ")
}

// stretchRule is boundaryRule's dash-fill shape (thread.go) for a
// stretch boundary: a label on the left, the time on the right, dashes
// between. It carries no role or model, because a period is not a
// session — it can span several.
func stretchRule(label string, at time.Time, w int) string {
	head := "── " + label + " "
	tail := "──"
	if !at.IsZero() {
		tail = " " + at.Format("15:04") + " ──"
	}
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail), 0)
	return head + strings.Repeat("─", fill) + tail
}

// stretchDecisionLine renders one crossing or answer autopilot made,
// pulled out of the fold so it keeps the position it happened at.
// Folding a stage to a receipt drops everything inside it, which is
// exactly why the block this replaces existed: with the decisions gone
// from the history, the only place left to report them was a rollup at
// the end. Keeping them here is what makes the rollup unnecessary.
//
// who comes from whether the event fell inside a period, never from the
// actor alone. Inside one it is autopilot's, whatever name the crossing
// was filed under — the review loop's own actor reaches this path for
// cards started by the switch before that was corrected. Outside one it
// keeps the actor's own name, because an unattended crossing on a card
// nobody handed over is the review loop's work and saying otherwise
// would claim a handover that never happened.
func stretchDecisionLine(s *theme.Styles, ev state.CardEvent, inStretch bool, w int) string {
	switch ev.Kind {
	case state.EventGate:
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || humanGateActors[p.Actor] {
			return ""
		}
		who := p.Actor
		if inStretch {
			who = "autopilot"
		}
		line := who + " crossed " + p.From + " → " + p.To
		return stampedLine(s, line, ev.At, w)
	case state.EventAsk:
		var p state.AskPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || askedBy(p) != state.ActorAutopilot {
			return ""
		}
		line := "autopilot answered “" + sanitize(p.Question) + "”"
		if p.Answer != "" {
			line += " — " + sanitize(p.Answer)
		}
		return stampedLine(s, line, ev.At, w)
	}
	return ""
}

// stampedLine is one pulled decision: a tick, the sentence, and the time
// pushed to the right margin so a run of them reads as a column. The
// stamp is dropped rather than truncated when the sentence needs the
// room — the time is the least of what the line is saying.
func stampedLine(s *theme.Styles, line string, at time.Time, w int) string {
	body := s.Success.Render("✓ ") + s.Subtle.Render(sanitize(line))
	stamp := ""
	if !at.IsZero() {
		stamp = at.Format("15:04")
	}
	pad := w - 2 - ansi.StringWidth(sanitize(line)) - ansi.StringWidth(stamp) - 1
	if stamp == "" || pad < 1 {
		return ansi.Truncate(body, w, "…")
	}
	return body + strings.Repeat(" ", pad) + s.Faint.Render(stamp)
}

// inStretch is stretchAt reduced to the one question the renderer asks
// most: did this event happen while autopilot had the card.
func inStretch(stretches []autopilotStretch, i int) bool {
	_, in := stretchAt(stretches, i)
	return in
}

// unseenStretch is the newest period that both ended and ended after the
// reader last looked at this card — the one the thread opens on instead
// of on its newest line.
//
// A period still running is never it. The thread's normal anchor is the
// end of the conversation, which is where a card being worked on right
// now should open; jumping backwards there would take the reader away
// from the thing that is still moving.
func unseenStretch(stretches []autopilotStretch, events []state.CardEvent, seen int64) (autopilotStretch, bool) {
	for i := len(stretches) - 1; i >= 0; i-- {
		st := stretches[i]
		if st.running() || st.to >= len(events) {
			continue
		}
		if events[st.to].Seq > seen {
			return st, true
		}
	}
	return autopilotStretch{}, false
}

// newestSeq is the high-water mark of an event slice, or 0 for an empty
// one — what gets recorded as read once a card has been looked at.
func newestSeq(events []state.CardEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}
