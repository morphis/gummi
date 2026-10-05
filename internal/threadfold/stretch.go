package threadfold

// The autopilot stretch: one period a card spent being driven without
// you, derived from the card's own event log so a thread can draw it
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
// life. Five things end one, whichever lands first: an explicit handback
// row, a park, a pause a person made, a gate a person crossed, or a turn
// a person typed. All five are rows: a period ends when the log says it ended, and the one
// judgement made outside the log (CloseOrphaned) exists only for the
// stop that by definition wrote nothing down.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
)

// StretchClose names how a period ended, which is the whole of what the
// closing rule says. The four are not decoration: they answer different
// questions. parked means it stopped short and something is waiting;
// finished means it carried the card as far as it is allowed to, to the
// landing gate it may never cross itself; taken back means you did not
// wait for either; orphaned means none of the above ever happened — the
// process driving it is simply gone.
type StretchClose string

const (
	StretchRunning   StretchClose = ""
	StretchParked    StretchClose = "parked"
	StretchFinished  StretchClose = "finished"
	StretchTakenBack StretchClose = "taken-back"
	// StretchOrphaned is a period the log never closes because nothing
	// closed it: a crash, an OOM kill, a container restart — the driving
	// process exited without parking, handing back, or anyone taking the
	// card back by hand. Distinct from the other three because it is not
	// derived from any row in the log; it is a query-time judgement
	// (CloseOrphaned) applied on top of a stretch the log alone still
	// calls Running().
	StretchOrphaned StretchClose = "orphaned"
)

// humanGateActors are the ways a person crosses a gate themselves: "user"
// is the TUI's g, "caller" is the headless driver's attended mode.
// Everything else is a crossing made without a person present.
//
// It is written this way round deliberately. The machine actors are
// open-ended — "auto" is the driver's unattended loop, "review" is the
// automatic review→fix→verify chain, "autopilot" is the switch, and any
// future loop names itself — so enumerating those is how a reader
// silently starts miscounting the day one is added. The human set is
// bounded by how a person can actually reach a gate, and that is the
// smaller, more stable thing to name. A person the web face names
// ("user:Simon", state.PersonActor) is the TUI's "user" with a name on it.
var humanGateActors = map[string]bool{"caller": true}

// HumanGateActor reports whether a gate crossing's actor is a person
// crossing it themselves (see humanGateActors for why the set is named
// this way round).
func HumanGateActor(actor string) bool {
	return humanGateActors[actor] || state.IsPersonActor(actor)
}

// GateCrossing is one design gate autopilot crossed on its own.
type GateCrossing struct {
	From domain.Stage
	At   time.Time
}

// AskAnswer is one ask_user question autopilot answered on its own.
type AskAnswer struct {
	Answer string
	At     time.Time
}

// Stretch is one such period. From and To bound it as a half-open range
// of indices into the event slice it was derived from: To is the index of
// the event that closed it (that event is the boundary, not part of the
// period), or len(events) while it is still running.
type Stretch struct {
	From, To int
	OpenedAt time.Time
	ClosedAt time.Time
	Closed   StretchClose
	// Reason is the sentence the closing event carried — a park's own
	// Detail, kept verbatim the way the park line keeps it, or the
	// handback's. Empty when the close was inferred from a person simply
	// acting, which explains itself.
	Reason string
	Mode   string
	// Gates and Answers are what autopilot decided inside this period,
	// and they are the whole of the tally. Credits are not here because
	// the masthead already carries them, and corrective rounds are not
	// here because they are a whole-card count that no store can slice by
	// period — printing either under a heading that names a bounded
	// window would be the same lie in a smaller font.
	Gates   []GateCrossing
	Answers []AskAnswer
}

// Running reports whether the period is still open — autopilot has the
// card right now, so a thread draws an opening rule and no closing one.
func (st Stretch) Running() bool { return st.Closed == StretchRunning }

// DecidedNothing reports whether autopilot crossed no gate and answered
// no question in this period. The stretch still draws: "it ran implement
// while you were out" is worth saying, and the folded receipt inside the
// rules says which stage. Only the tally line is withheld, which is the
// same per-row restraint the block this replaces applied to itself —
// moved down from the whole block to the one row that would otherwise
// read as a row of zeroes.
func (st Stretch) DecidedNothing() bool {
	return len(st.Gates) == 0 && len(st.Answers) == 0
}

// Stretches walks a card's event log once and returns every period it
// ran itself, in order. events must be in seq order, which is what
// Store.Events returns.
func Stretches(events []state.CardEvent) []Stretch {
	var out []Stretch
	cur := -1 // index into out of the open period, -1 when none

	closeWith := func(i int, at time.Time, how StretchClose, reason string) {
		if cur < 0 {
			return
		}
		out[cur].To = i
		out[cur].ClosedAt = at
		out[cur].Closed = how
		out[cur].Reason = reason
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
				out = append(out, Stretch{
					From: i, To: len(events), OpenedAt: ev.At, Mode: p.Mode,
				})
				cur = len(out) - 1
			case state.AutopilotHandedBack:
				closeWith(i, ev.At, StretchTakenBack, p.Reason)
			}

		case state.EventPause:
			// a person stopping the run by hand takes the card back, and
			// the rule says who did (its own receipt is not drawn twice)
			var p state.PausePayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			closeWith(i, ev.At, StretchTakenBack, PauseLine(p))

		case state.EventPark:
			if cur < 0 {
				continue
			}
			var p state.ParkPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			how := StretchParked
			verdict, ran := StageExitVerdict(events[:i], ev.Stage)
			if LandingGate(ev.Stage) && ran && verdict != state.StatusFail &&
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
				how = StretchFinished
			}
			closeWith(i, ev.At, how, p.Detail)

		case state.EventGate:
			var p state.GatePayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if HumanGateActor(p.Actor) {
				closeWith(i, ev.At, StretchTakenBack, "")
				continue
			}
			if cur >= 0 {
				out[cur].Gates = append(out[cur].Gates, GateCrossing{
					From: domain.Stage(p.From), At: ev.At,
				})
			}
			if domain.Stage(p.To) == domain.StageDone {
				// A card inside a goal never parks at the landing gate —
				// its conductor lands it — so the close that reads
				// "finished" for every other card was never written, and
				// the page of a card that landed cleanly opened on
				// "autopilot stopped without saying so". Crossing to done
				// unattended IS getting as far as a card is allowed to go
				// on its own.
				closeWith(i, ev.At, StretchFinished, "")
			}

		case state.EventAsk:
			var p state.AskPayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if state.IsPersonActor(AskedBy(p)) {
				closeWith(i, ev.At, StretchTakenBack, "")
				continue
			}
			if cur >= 0 && AskedBy(p) == state.ActorAutopilot {
				out[cur].Answers = append(out[cur].Answers, AskAnswer{
					Answer: p.Answer, At: ev.At,
				})
			}

		case state.EventMessage:
			var p MessagePayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				continue
			}
			if p.Author == string(engine.AuthorUser) {
				// A turn you typed is you back in the room, and it is the
				// backstop that keeps a period from running forever when
				// whatever should have closed it never got written — a
				// process killed mid-run parks nothing.
				closeWith(i, ev.At, StretchTakenBack, "")
			}
		}
	}
	return out
}

// CloseOrphaned downgrades the newest stretch from running to orphaned
// when the process that opened it is no longer driving the card. At most
// one stretch is ever open (Stretches' own invariant, per Driving's doc
// comment), so only the last element can need this, and only when it is
// still running.
//
// live answers "is a session — this process's own or another's — driving
// this card right now" (state.CardIsLive). It is threaded in by the
// caller rather than looked up here so this stays a pure function of its
// inputs, the same way the rest of this file is: a query-time judgement
// applied on top of the log-derived stretches, never folded into their
// derivation.
func CloseOrphaned(stretches []Stretch, events []state.CardEvent, live bool) []Stretch {
	if live || len(stretches) == 0 {
		return stretches
	}
	last := &stretches[len(stretches)-1]
	if !last.Running() {
		return stretches
	}
	last.Closed = StretchOrphaned
	// Dated from the last thing the run managed to write. The stop
	// itself was never recorded — that is what this closing means — so
	// there is no exact moment to read, and an undated rule was the
	// wrong answer to that: this is the one closing whose rule is the
	// only record the run ended at all, which makes "when" the thing the
	// reader most needs from it.
	if len(events) > 0 {
		last.ClosedAt = events[len(events)-1].At
	}
	return stretches
}

// LiveStretches is Stretches with the render-time liveness judgement
// folded in — what every rendering call site should call instead of
// Stretches directly. A period still open per the log renders as still
// running only if a process is actually driving the card right now; a
// process killed mid-run writes nothing on its way out, so the log can
// never say this by itself (BG-059).
//
// There was a second judgement here, and it is gone: a card resting at a
// stage autopilot may not drive used to be read as handed over (BG-085),
// because back then the design stage was interactive and autopilot's
// arrival there was a stop nothing wrote down. Every stage is autonomous
// now, and every way a card comes to rest writes its own row — a gate
// waiting on a person raises the card's needs-you item, and that raise
// records a park (the TUI shell's parkAttentionItem), which closes the
// period with the gate's own sentence as the reason. What survived the
// stage merge was the question asked of the wrong graph: "is the card at
// plan", answered yes by every card in the design phase, running or not.
// So pressing the switch on a working card drew the opening rule and the
// closing one in the same breath while the stage went on writing
// (BG-105).
func LiveStretches(f domain.Feature, events []state.CardEvent, ws state.Workspace) []Stretch {
	return CloseOrphaned(Stretches(events), events, state.CardIsLive(ws, f.ID))
}

// AskedBy names who answered an ask. By is what the caller stated
// outright and is what this trusts; Actor is the older field, inferred
// from the card's stored mode, and stands in only for rows written
// before By existed (AskPayload's own doc comment).
func AskedBy(p state.AskPayload) string {
	if p.By != "" {
		return p.By
	}
	return p.Actor
}

// StageSequence derives the ordered stage list a card walks, read out of
// the workflow package rather than written down as a string. One graph
// serves every kind now, so the sequence no longer varies by card — it
// takes no feature and callers pass none. At each stage it picks the
// same edge engine.Engine.nextStage (advance.go) would resolve g into:
// workflow.Next lists the primary forward edge first and the rerun
// bounces after it, so the first entry is always the one to take.
func StageSequence() []domain.Stage {
	cur := workflow.Initial()
	seq := []domain.Stage{cur}
	// A backward edge picked here would walk in a circle, and this runs
	// on every frame: stop the first time a stage repeats rather than
	// trusting the edge tables to stay acyclic under this rule.
	seen := map[domain.Stage]bool{cur: true}
	for !workflow.Terminal(cur) {
		nexts := workflow.Next(cur)
		if len(nexts) == 0 {
			break
		}
		// nexts[0] is the forward edge; the rerun bounces follow it.
		next := nexts[0]
		if seen[next] {
			break
		}
		seen[next] = true
		seq = append(seq, next)
		cur = next
	}
	return seq
}

// LandingGate reports whether stage is the last decision on the workflow
// — the one autopilot never crosses, because landing on main stays a
// person's call under every mode. It is read from the graph's own
// sequence rather than hardcoded to verify, so moving the last stage
// moves this with it.
func LandingGate(stage domain.Stage) bool {
	seq := StageSequence()
	for len(seq) > 0 && seq[len(seq)-1] == domain.StageDone {
		seq = seq[:len(seq)-1]
	}
	return len(seq) > 0 && seq[len(seq)-1] == stage
}

// StageExitVerdict is the verdict the given stage finished on, and
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
func StageExitVerdict(events []state.CardEvent, stage domain.Stage) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != state.EventStageExit || events[i].Stage != stage {
			continue
		}
		var p StageExitPayload
		_ = json.Unmarshal([]byte(events[i].Payload), &p)
		return p.Verdict, true
	}
	return "", false
}

// Driving reports whether the card is inside an open period right now —
// the board's version of the question Running answers for the thread. At
// most one period is ever open at a time (Stretches closes the previous
// one before opening the next), so checking the last element is
// equivalent to scanning the whole slice for an open one.
func Driving(stretches []Stretch) bool {
	return len(stretches) > 0 && stretches[len(stretches)-1].Running()
}

// StretchAt reports the period covering event index i, and whether there
// is one. The rendering side asks this per event to decide whether a
// machine crossing is autopilot's — inside a period it is, and says so;
// outside one it keeps the actor's own name, because the review→fix loop
// crosses gates unattended on cards nobody ever handed over.
func StretchAt(stretches []Stretch, i int) (Stretch, bool) {
	for _, st := range stretches {
		if i >= st.From && i < st.To {
			return st, true
		}
	}
	return Stretch{}, false
}

// InStretch is StretchAt reduced to the one question a renderer asks
// most: did this event happen while autopilot had the card.
func InStretch(stretches []Stretch, i int) bool {
	_, in := StretchAt(stretches, i)
	return in
}

// UnseenStretch is the newest period that both ended and ended after the
// reader last looked at this card — the one a thread opens on instead of
// on its newest line.
//
// A period still running is never it. The thread's normal anchor is the
// end of the conversation, which is where a card being worked on right
// now should open; jumping backwards there would take the reader away
// from the thing that is still moving.
func UnseenStretch(stretches []Stretch, events []state.CardEvent, seen int64) (Stretch, bool) {
	for i := len(stretches) - 1; i >= 0; i-- {
		st := stretches[i]
		if st.Running() || st.To >= len(events) {
			continue
		}
		if events[st.To].Seq > seen {
			return st, true
		}
	}
	return Stretch{}, false
}

// StretchOpenLabel is what the rule opening a period says.
const StretchOpenLabel = "autopilot took over"

// StretchLabel names a close the way the rule says it. The wordings
// answer different questions and are not interchangeable: a card that
// parked has something waiting on you, one that finished got as far as
// it is allowed to go on its own, one you took back never reached
// either, and one orphaned never got a chance to say anything at all —
// its driving process is simply gone.
func StretchLabel(how StretchClose) string {
	switch how {
	case StretchFinished:
		return "autopilot finished"
	case StretchTakenBack:
		return "you took back control"
	case StretchOrphaned:
		return "autopilot stopped without saying so"
	default:
		return "autopilot parked it"
	}
}

// Tally counts what autopilot decided, in the two dimensions the event
// log can slice exactly: "2 gates · 1 answer". Empty when it decided
// nothing (DecidedNothing).
func (st Stretch) Tally() string {
	parts := make([]string, 0, 2)
	if n := len(st.Gates); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" gate"+plural(n))
	}
	if n := len(st.Answers); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" answer"+plural(n))
	}
	return strings.Join(parts, " · ")
}

// plural is "" for exactly one, "s" otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
