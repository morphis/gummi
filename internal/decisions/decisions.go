// Package decisions holds the pure rules for a card's open decisions:
// which of several a card shows (Rank), which attention lane it belongs
// in (Attention), how an ask_user question is offered as options
// (AskOptions), and what answering it with a picked option says
// (AnswerText, GateAnswerCrosses).
//
// They are the same rules whichever face asks. The TUI's inbox and its
// pinned decision control read them, and the web face reads them to offer
// the same decision with the same options — a card cannot be waiting on
// one thing in the terminal and another in the browser.
package decisions

import (
	"strings"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// Rank picks the single decision a card shows when Store.OpenDecisions
// reports more than one at once — a verify gate and a budget stop
// genuinely co-exist (DESIGN §6.3). Rather than invent a close event for
// the loser, it ranks: the decision that would stop the card *first*
// wins, in the same order the card thread's own pinned control already
// uses to pick its one decision (the TUI's openDecision) — ask > budget >
// verify > gate > idle — so the inbox and the thread can never disagree
// about what a card is waiting on.
//
// A rebase conflict has no slot in that order (the thread never renders
// one), so it is ranked beside ask, ahead of everything else: like an
// ask, it is a hard stop nothing else on the card can get past on its
// own, whereas a budget or a gate can still be looked at once the more
// urgent thing is dealt with. idle is never a real contender — it means
// nobody is waiting on anyone — and is dropped rather than ranked. A
// stage that failed to run ranks beside a budget stop: both are a stage
// that stopped short and resolve only by running it again.
func Rank(decs []state.OpenDecision) (state.OpenDecision, bool) {
	rank := map[string]int{
		state.DecisionKindAsk:      0,
		state.DecisionKindConflict: 0,
		state.DecisionKindBudget:   1,
		state.DecisionKindFailure:  1,
		state.DecisionKindVerify:   2,
		state.DecisionKindGate:     3,
	}
	best := -1
	var winner state.OpenDecision
	for _, d := range decs {
		r, ok := rank[d.Kind]
		if !ok {
			continue // DecisionKindIdle, or an unknown kind: never the winner
		}
		if best == -1 || r < best {
			best, winner = r, d
		}
	}
	return winner, best != -1
}

// Lane is the needs-attention queue's own vocabulary for why a card is
// waiting: a gate to review, a failure, a question, a budget stop. It is
// deliberately not the decision vocabulary — a verify stop and a gate
// share the gate lane, and a conflict is a failure.
type Lane string

const (
	// LaneGate: an autonomous stage finished and awaits your decision.
	LaneGate Lane = "gate"
	// LaneFailure: a session errored, or the environment stopped it.
	LaneFailure Lane = "failure"
	// LaneQuestion: the agent asked something and is waiting.
	LaneQuestion Lane = "question"
	// LaneBudget: a stage hit its budget and awaits a top-up/park decision.
	LaneBudget Lane = "budget"
)

// Attention maps a decision's closed-vocabulary Kind (state's
// DecisionKind*) onto the attention queue's Lane (DESIGN §10.18 being the
// queue's primary source, without replacing the lane vocabulary): an ask
// is a question, a budget stays a budget, a gate stays a gate. verify is
// only ever raised by an autonomous loop giving up rather than finishing
// clean, so it reads as an escalated gate — the same tint an escalation
// gives a human-judgment stop nothing settled on its own. A rebase
// conflict is an environment stop, which is the failure lane, not the
// review-and-advance one, and so is a stage that failed to run. idle (and
// anything unrecognized) reports false:
// an idle card is not waiting on anyone, and nothing writes one today
// regardless.
func Attention(kind string) (lane Lane, escalated bool, ok bool) {
	switch kind {
	case state.DecisionKindAsk:
		return LaneQuestion, false, true
	case state.DecisionKindBudget:
		return LaneBudget, false, true
	case state.DecisionKindGate:
		return LaneGate, false, true
	case state.DecisionKindVerify:
		return LaneGate, true, true
	case state.DecisionKindConflict, state.DecisionKindFailure:
		return LaneFailure, false, true
	default:
		return "", false, false
	}
}

// Option is one answer a decision offers: what the choice is, and the
// detail that says what it does.
//
// There is no key field. There used to be — the board accelerator (g, s,
// A, b, d, v…) — but that accelerator only fires from the backlog list,
// where a decision's options never show: on the card page the same letter
// reaches the composer and types. An option that names a key which does
// something else entirely one keystroke later is worse than one that
// names no key at all.
type Option struct {
	Label  string
	Detail string
	// Danger marks an option that cannot be taken back.
	Danger bool
	// Chat marks the synthetic "Chat about this" row AskOptions appends:
	// the answer is the person's own words rather than the row itself, so
	// it is never ticked in a multi-pick and never contributes a label to
	// AnswerText.
	Chat bool
}

// ChatLabel and ChatDetail are what the synthetic chat row says.
const (
	ChatLabel  = "Chat about this"
	ChatDetail = "reply with your own words instead of picking an option"
)

// AskOptions shapes a live ask_user question as options. Every ask gains
// a synthetic "Chat about this" row after the real options, so the
// free-form channel is visible before the person ever types anything.
//
// EVERY ask, not only one that declared allow_free_form: the answer path
// has never honoured that flag — a prose line at any open question is
// delivered as its answer, because the ask is blocking the agent's turn
// from inside a client tool and a second turn is refused. So withholding
// the row withheld nothing but the knowledge that talking was allowed,
// from exactly the questions whose options were too narrow to say what
// the reader meant. The flag is gone from Ask for the same reason.
//
// The row's index is always len(ask.Options): appended last and never
// reordered, so every caller that bounds a cursor or a pick against the
// real options agrees on it without a shared constant.
func AskOptions(ask *engine.Ask) []Option {
	options := make([]Option, 0, len(ask.Options)+1)
	for _, option := range ask.Options {
		options = append(options, Option{Label: option.Label, Detail: option.Detail})
	}
	detail := ChatDetail
	if ask.Restored && len(ask.Options) == 0 {
		// a question re-armed after the process that asked it ended: the
		// agent's options ended with it (decision rows never store them),
		// and a bare chat row would read as all it ever offered
		detail = ChatDetailRestored
	}
	return append(options, Option{Label: ChatLabel, Detail: detail, Chat: true})
}

// ChatDetailRestored is the chat row's detail on a question restored
// without its options (engine.Ask.Restored).
const ChatDetailRestored = "the options the agent offered did not survive the restart of the run that asked — answer in your own words"

// GateAnswerCrosses reports whether answering ask with this text is the
// gate crossing itself, rather than an ordinary answer the stage then
// acts on. Only a gate ask can cross, and only its advance option does —
// "not yet" and any free-form reply are answers that leave the card
// exactly where it is.
func GateAnswerCrosses(ask *engine.Ask, answer string) bool {
	return ask != nil && ask.Gate && answer == engine.GateAdvanceLabel
}

// AnswerText is the answer an ask receives when the option at cursor is
// taken — or, for a multi-pick ask with anything picked, the picked
// options' labels joined by ", ". The synthetic chat row (and any index
// past the real options) is never an answer by itself: it yields "", and
// its words are the answer instead.
func AnswerText(ask *engine.Ask, cursor int, picked map[int]bool) string {
	if ask.MultiPick {
		var labels []string
		for i, option := range ask.Options {
			if picked[i] {
				labels = append(labels, option.Label)
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
	}
	if cursor >= 0 && cursor < len(ask.Options) {
		return ask.Options[cursor].Label
	}
	return ""
}
