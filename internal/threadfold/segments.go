// Package threadfold folds a card's event log into the shape a thread is
// drawn from.
//
// A fold is a pure function of a card's card_events rows (state.CardEvent,
// as Store.Events returns them, oldest first): it reconstructs the stage
// sessions the log describes (Segments), the decisions it has already
// answered (AnsweredDecisions), and the periods the card spent running
// itself (Stretches), and says each of them in the words the thread uses
// for them. Nothing here does IO, holds state, or knows how anything is
// drawn.
//
// Two faces read it. The TUI's card thread (internal/ui) renders its
// folded receipts, live stage block and autopilot rules from Segments and
// Stretches, and takes its receipt wording from the helpers here. The web
// face reads Items, a UI-free item list derived from the same segment
// fold, so a page in a browser says what the terminal says about the same
// card rather than a second reading of the log that could disagree with
// it.
package threadfold

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
)

// StageEnterPayload, StageExitPayload and MessagePayload mirror the JSON
// shapes the engine writes into card_events (see
// internal/engine/persist.go's mirrorEvents). They live with the fold
// rather than in the store because they are a reading-side concern, not
// something the store needs typed; a tool row's payload is the store's
// own state.ToolPayload.
type (
	StageEnterPayload struct {
		Role   string `json:"role"`
		Model  string `json:"model"`
		Flavor string `json:"flavor"`
	}
	StageExitPayload struct {
		Verdict string  `json:"verdict"`
		Credits float64 `json:"credits"`
	}
	MessagePayload struct {
		Author  string `json:"author"`
		Content string `json:"content"`
		// By is who typed a user line (state.PersonActor), when a
		// person's name came with it.
		By string `json:"by,omitempty"`
		// Images are the attachments a user turn carried, in order.
		Images []state.AttachmentRef `json:"images,omitempty"`
	}
)

// Segment is one generation of a stage session, reconstructed from its
// stage_enter/stage_exit event pair — or, for a stage that never opened a
// session at all, from the first event recorded at it.
//
// An unclosed segment (Exited == false) has no stage_exit yet. That is
// usually the last segment in the slice, the live stage; it is not a
// rule. The interactive stages never earn a stage_exit on an ordinary
// approval, and a stage whose session was refused never earns one either,
// so an unclosed segment can be folded like any other — a folded receipt
// dates those from when they opened.
type Segment struct {
	Stage   domain.Stage
	Role    string
	Model   string
	Flavor  string // "stage", "critique" or "rebase"; "" on rows that predate it
	EnterAt time.Time
	Exited  bool
	Verdict string
	Credits float64
	ExitAt  time.Time
	Events  []state.CardEvent // messages/tools recorded within this segment
	// EnterIdx is where this segment's stage_enter sat in the event slice
	// it was reconstructed from, and EvIdx holds the same index for each
	// entry of Events. Folding loses position — a segment becomes one
	// receipt line — and the autopilot stretches drawn around those
	// receipts are bounded by event indices, so without a way back to the
	// original position there is no way to say which side of a boundary a
	// folded stage fell on.
	EnterIdx int
	EvIdx    []int
	// ExitIdx is the index of the stage_exit that closed the segment, or
	// -1 while it is open.
	ExitIdx int
}

// Segments reconstructs a card's session history from its event log, in
// seq order: each stage_enter opens a segment, the matching stage_exit
// (same stage, still open) closes it, and every other event but a consult
// turn belongs to the open segment for its own stage. Nil input (events not loaded yet, or
// none recorded) yields no segments — the caller degrades to omitting the
// folded receipts and the live-stage fallback, exactly as required.
//
// "for its own stage" is the part that is easy to lose. Every event
// carries the stage it happened at, and most of the time that is the
// stage whose session is open, so appending to the newest segment and
// never looking is right — until a stage never opens a session at all. A
// stage the agent was refused entry to (the backend cannot run it) writes
// its park receipt and nothing else: no stage_enter, so no segment, so the
// receipt landed under the previous stage's heading, naming that stage's
// role and model as the ones that failed. It looked right while the
// failed session was still in memory, because the card page renders a
// live block for that instead, and then moved on the next restart. A
// stage with something to say opens a segment for it.
func Segments(events []state.CardEvent) []Segment {
	var segs []Segment
	for i, ev := range events {
		switch ev.Kind {
		case state.EventStageEnter:
			var p StageEnterPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			segs = append(segs, Segment{
				Stage: ev.Stage, Role: p.Role, Model: p.Model, Flavor: p.Flavor,
				EnterAt: ev.At, EnterIdx: i, ExitIdx: -1,
			})
		case state.EventStageExit:
			if len(segs) == 0 {
				continue
			}
			last := &segs[len(segs)-1]
			if last.Exited || last.Stage != ev.Stage {
				continue
			}
			var p StageExitPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			last.Exited, last.Verdict, last.Credits, last.ExitAt = true, p.Verdict, p.Credits, ev.At
			last.ExitIdx = i
		case state.EventConsult:
			// the consult conversation is no stage session's: it sits in
			// the log where it was asked, in no segment (Items places it)
			continue
		default:
			if len(segs) == 0 {
				// nothing has started yet: the todo→first-stage crossing
				// is recorded before any session exists and has no block
				// of its own to sit in.
				continue
			}
			if ev.Stage != "" && segs[len(segs)-1].Stage != ev.Stage {
				// a stage that never opened a session — see the doc
				// comment. EnterAt/EnterIdx come from the event itself,
				// which is the only moment this stage is known to have
				// been reached; role and model stay empty because no
				// session was ever chosen for it, and a folded receipt
				// already renders a segment that has neither.
				segs = append(segs, Segment{Stage: ev.Stage, EnterAt: ev.At, EnterIdx: i, ExitIdx: -1})
			}
			last := &segs[len(segs)-1]
			last.Events = append(last.Events, ev)
			last.EvIdx = append(last.EvIdx, i)
		}
	}
	return segs
}

// Turns counts the conversation turns recorded in the segment — its
// message events, whoever wrote them.
func (seg Segment) Turns() int {
	turns := 0
	for _, ev := range seg.Events {
		if ev.Kind == state.EventMessage {
			turns++
		}
	}
	return turns
}

// Outcome is the mark a finished segment's receipt carries:
// state.StatusOK, state.StatusFail, or "" for the neutral mark.
//
// The reviewer's own sessions — a stage's critique, and verify — are the
// ones that submit a verdict. Keyed on the ROLE rather than the stage,
// because a work stage now hosts both: its writer (implementer, no
// verdict, ✓ on a clean exit) and its critique (reviewer, a real
// verdict). Keying on the stage would have made every finished implement
// look unverdicted and lose its check.
func (seg Segment) Outcome() string {
	if !seg.Exited {
		return ""
	}
	switch seg.Role {
	case string(agent.RoleReviewer):
		// a critique and a verify carry a real pass/changes/fail/blocked
		// verdict (internal/verdict); only a resolved "pass" earns ✓, and
		// only "fail" earns ✗ — anything else (including "", the shape
		// left behind by a session that exited without ever calling
		// submit_verdict) stays neutral rather than defaulting to a pass
		// that was never recorded.
		switch seg.Verdict {
		case verdict.Pass.String():
			return state.StatusOK
		case verdict.Fail.String():
			return state.StatusFail
		}
		return ""
	default:
		// every other session never calls submit_verdict, so verdict==""
		// is its only possible value and isn't itself a negative signal —
		// exited and not failed still reads ✓.
		if seg.Verdict == state.StatusFail {
			return state.StatusFail
		}
		return state.StatusOK
	}
}

// ReceiptCredits is what a folded segment's receipt says it cost.
//
// stage_spend's primary key is (feature, stage, model, role): it rolls
// every session of a stage into one number, so it can answer "what did
// fix cost this card" but not "what did this fix session cost" — a card
// that bounced through review→fix four times has four segments and one
// rollup between them, and printing that rollup on each of their receipts
// is how a ~172-credit card reads as 53.5 (all four print the same
// total). The stage_exit event payload is the only per-session record
// there is, so it wins whenever there is more than one segment to tell
// apart; the rollup (spend, from SpendByStage) is kept as a fallback for
// a stage that only ran once (stageSegs == 1), in case its payload
// predates the credits field. remainder (Remainder) is the last resort:
// the stage's unaccounted spend, when this is the one segment of it that
// does not know what it cost.
func ReceiptCredits(seg Segment, spend map[domain.Stage]float64, stageSegs int, remainder float64) float64 {
	credits := seg.Credits
	if credits == 0 && stageSegs == 1 {
		credits = spend[seg.Stage]
	}
	if credits == 0 {
		credits = remainder
	}
	return credits
}

// AnsweredDecisions returns the set of decision ids this card's event log
// has already answered: every GatePayload.ID and AskPayload.ID that shows
// up on a gate or ask event anywhere in events. Those two fields are the
// correlation EventDecisionOpen's own doc comment describes
// (state/cardevents.go) — a decision_open row and the gate/ask row that
// answers it share one id — so a decision whose id appears here has
// collapsed into that answer's row per DESIGN §6.3, and a reader uses
// this set to say nothing for it rather than saying the same stop twice.
//
// A gate crossing also answers every gate decision still open at the
// stage it leaves (DecisionAnswers): one stage can hold two stops raised
// by two drivers — a headless run's --until stop, then the board's own —
// and the one crossing passes both.
//
// Callers compute this once per render rather than once per line: a line
// renderer only ever sees the single event it is asked to render, and a
// card's history can carry many decision_open rows, so re-scanning the
// whole event log inside it would redo the same work once per line for
// nothing.
func AnsweredDecisions(events []state.CardEvent) map[string]bool {
	answered := map[string]bool{}
	for _, ev := range events {
		var id string
		switch ev.Kind {
		case state.EventGate:
			var p state.GatePayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			id = p.ID
		case state.EventAsk:
			var p state.AskPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			id = p.ID
		default:
			continue
		}
		if id != "" {
			answered[id] = true
		}
	}
	for _, opened := range DecisionAnswers(events) {
		for _, i := range opened {
			var p state.DecisionPayload
			_ = json.Unmarshal([]byte(events[i].Payload), &p)
			if p.ID != "" {
				answered[p.ID] = true
			}
		}
	}
	return answered
}

// DecisionAnswers maps each gate or ask event that answers a decision to
// the decision_open rows before it that it answers, by index into events:
// the one its payload id names, and — for a gate crossing — every gate
// decision still unanswered at the stage it leaves. A stage the card has
// crossed out of can hold no stop (the store's OpenDecisions abandons
// one the same way), so a stop raised there is passed by the crossing,
// whichever driver raised it and whichever id the crossing carried.
func DecisionAnswers(events []state.CardEvent) map[int][]int {
	out := map[int][]int{}
	byID := map[string]int{}
	done := map[int]bool{}
	openGates := map[domain.Stage][]int{}
	take := func(at, i int) {
		if !done[i] {
			done[i] = true
			out[at] = append(out[at], i)
		}
	}
	for i, ev := range events {
		switch ev.Kind {
		case state.EventDecisionOpen:
			var p state.DecisionPayload
			if json.Unmarshal([]byte(ev.Payload), &p) != nil {
				continue
			}
			if p.ID != "" {
				byID[p.ID] = i
			}
			if p.Kind == state.DecisionKindGate {
				openGates[ev.Stage] = append(openGates[ev.Stage], i)
			}
		case state.EventGate:
			var p state.GatePayload
			if json.Unmarshal([]byte(ev.Payload), &p) != nil {
				continue
			}
			if k, ok := byID[p.ID]; ok && p.ID != "" {
				take(i, k)
			}
			from := domain.Stage(p.From)
			for _, k := range openGates[from] {
				take(i, k)
			}
			delete(openGates, from)
			slices.Sort(out[i])
		case state.EventAsk:
			var p state.AskPayload
			if json.Unmarshal([]byte(ev.Payload), &p) != nil {
				continue
			}
			if k, ok := byID[p.ID]; ok && p.ID != "" {
				take(i, k)
			}
		}
	}
	return out
}

// SpendByStage rolls the per-stage/model spend rows up to one total per
// stage, the shape a folded receipt needs. stage_spend is the meter of
// record for credits; the event log only carries a copy.
func SpendByStage(rows []state.StageSpend) map[domain.Stage]float64 {
	if len(rows) == 0 {
		return nil
	}
	out := make(map[domain.Stage]float64, len(rows))
	for _, r := range rows {
		out[r.Stage] += r.Credits
	}
	return out
}

// Remainder returns the spend a segment may claim as its own when its own
// receipt carries none: the stage's total less what its other segments
// accounted for, and only when this is the single segment of that stage
// without a figure. With two such segments there is no honest way to
// split the remainder between them, so neither takes it — a wrong
// attribution is worse than a missing one on a page a person reads to
// answer "what did this cost me, and on what".
//
// unclaimed and unknown are what Unclaimed returns for the same folded
// segments.
func Remainder(seg Segment, unclaimed map[domain.Stage]float64, unknown map[domain.Stage]int) float64 {
	if seg.Credits > 0 || unknown[seg.Stage] != 1 {
		return 0
	}
	if r := unclaimed[seg.Stage]; r > 0 {
		return r
	}
	return 0
}

// Unclaimed reports what each stage spent that no segment claims, and how
// many segments of each stage carry no figure of their own. A segment's
// own figure comes from its stage_exit payload, and a session that ended
// without one leaves a receipt with no credits at all — on the lxd
// autopilot drive's case B the card's first plan session, 35.1 credits of
// it, printed as "plan · architect · 5 turns" and nothing else, while the
// masthead counted the money. stage_spend knows the stage's true total,
// so the remainder — the total less what the segments do account for — is
// exactly what is missing, and when one segment is missing its figure the
// remainder IS that figure.
func Unclaimed(spend map[domain.Stage]float64, folded []Segment) (unclaimed map[domain.Stage]float64, unknown map[domain.Stage]int) {
	unclaimed = map[domain.Stage]float64{}
	unknown = map[domain.Stage]int{}
	for st, total := range spend {
		unclaimed[st] = total
	}
	for _, seg := range folded {
		if seg.Credits > 0 {
			unclaimed[seg.Stage] -= seg.Credits
			continue
		}
		unknown[seg.Stage]++
	}
	return unclaimed, unknown
}

// SessionlessSpend groups the stage_spend rows whose role never ran as a
// session of that stage — the one-shot passes (check discovery and its
// baseline, on the scribe) and the backend's own side-model spend (the
// helper role). Both are booked against the card and neither folds to a
// receipt, so without this they are money the page cannot explain.
func SessionlessSpend(rows []state.StageSpend, segs []Segment) map[domain.Stage][]state.StageSpend {
	if len(rows) == 0 {
		return nil
	}
	ran := map[domain.Stage]map[string]bool{}
	for _, seg := range segs {
		if ran[seg.Stage] == nil {
			ran[seg.Stage] = map[string]bool{}
		}
		ran[seg.Stage][seg.Role] = true
	}
	out := map[domain.Stage][]state.StageSpend{}
	for _, r := range rows {
		if r.Credits <= 0 || ran[r.Stage][r.Role] {
			continue
		}
		out[r.Stage] = append(out[r.Stage], r)
	}
	return out
}
