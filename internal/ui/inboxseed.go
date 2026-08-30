package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// openDecisionsMsg carries the startup query's result: every card's still
// open decisions (DESIGN §10.18), keyed by feature, or the error from a
// failed read.
type openDecisionsMsg struct {
	decisions map[domain.FeatureID][]state.OpenDecision
	err       error
}

// fetchOpenDecisions runs Store.OpenDecisions once, at startup: a database
// read, so — per the no-IO-in-Update contract attachChat documents — it
// has to happen inside a dispatched command rather than inline in Init or
// Update.
func (m *Shell) fetchOpenDecisions() tea.Msg {
	decisions, err := m.store.OpenDecisions(context.Background())
	return openDecisionsMsg{decisions: decisions, err: err}
}

// rankOpenDecision picks the single decision a card's inbox row shows
// when Store.OpenDecisions reports more than one at once — a verify gate
// and a budget stop genuinely co-exist (DESIGN §6.3). Rather than invent
// a close event for the loser, it ranks: the decision that would stop the
// card *first* wins, in the same order the card thread's own pinned
// control already uses to pick its one decision (decision.go's
// openDecision) — ask > budget > verify > gate > idle — so the inbox and
// the thread can never disagree about what a card is waiting on.
//
// A rebase conflict has no slot in that order (the thread never renders
// one — decisionKind has no conflict case), so it is ranked beside ask,
// ahead of everything else: like an ask, it is a hard stop nothing else
// on the card can get past on its own, whereas a budget or a gate can
// still be looked at once the more urgent thing is dealt with. idle is
// never a real contender — it means nobody is waiting on anyone — and is
// dropped rather than ranked.
func rankOpenDecision(decs []state.OpenDecision) (state.OpenDecision, bool) {
	rank := map[string]int{
		state.DecisionKindAsk:      0,
		state.DecisionKindConflict: 0,
		state.DecisionKindBudget:   1,
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

// attnForDecision maps a decision's closed-vocabulary Kind (state's
// DecisionKind*) onto the inbox's own attnKind, per this change's mapping
// (DESIGN §10.18 becoming the queue's primary source, without replacing
// attnKind's own vocabulary — that is separate work): an ask is a
// question, a budget stays a budget, a gate stays a gate. verify is only
// ever raised by an autonomous loop giving up rather than finishing clean
// (see decisionKindForStage), so it reads as an escalated gate — the same
// tint raiseEscalation gives a human-judgment stop nothing settled on its
// own. A rebase conflict is an environment stop, which is the failure
// lane, not the review-and-advance one. idle (and anything unrecognized)
// reports false: an idle card is not waiting on anyone, and nothing
// writes one today regardless.
func attnForDecision(kind string) (k attnKind, escalated bool, ok bool) {
	switch kind {
	case state.DecisionKindAsk:
		return attnQuestion, false, true
	case state.DecisionKindBudget:
		return attnBudget, false, true
	case state.DecisionKindGate:
		return attnGate, false, true
	case state.DecisionKindVerify:
		return attnGate, true, true
	case state.DecisionKindConflict:
		return attnFailure, false, true
	default:
		return "", false, false
	}
}

// seedInboxFromDecisions seeds the needs-attention queue from the durable
// decision_open records the startup query read — the primary source
// DESIGN §10.18 exists for, with reconstructInbox's session inference
// (called right after this, in the openDecisionsMsg handler) as the
// fallback for whatever it doesn't cover.
//
// Every add goes through inbox.seed, never put/add: a live engine event
// can raise a feature's item before this message lands (the query, and
// the round trip back to Update, both take time a running engine doesn't
// wait for), and that live item is truer than a snapshot taken before
// Init even asked.
func (m *Shell) seedInboxFromDecisions(decisions map[domain.FeatureID][]state.OpenDecision) {
	for id, decs := range decisions {
		dec, ok := rankOpenDecision(decs)
		if !ok {
			continue
		}
		kind, escalated, ok := attnForDecision(dec.Kind)
		if !ok {
			continue
		}
		m.inbox.seed(attnItem{
			Feature: id, Kind: kind, Text: dec.Question,
			Escalated: escalated, At: dec.At,
		})
	}
}
