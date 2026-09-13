// Package goalpolicy decides what a goal does next. It is pure: given a
// snapshot of a goal and its cards, Decide returns the actions to take, in
// order, and never touches git, the store, or an agent. The engine builds
// the snapshot and executes the actions (Engine.GoalTick); the board and
// the headless driver only start the cards it says to start. Keeping the
// rules here is what lets both drivers conduct a goal identically, the way
// gatepolicy lets both cross a gate identically.
//
// The rules, in the order Decide applies them:
//
//  1. Only a goal at implement is conducted, and never while its own
//     review or verify is running.
//  2. A lead that keeps failing, or a budget already past its reserve,
//     wraps the goal up.
//  3. Wrapping up drops every card that is not verified or landed.
//  4. One verified card lands per tick — after the lead has had one look
//     at the reviewer findings it still carries.
//  5. An exhausted card goes to the lead once; otherwise it is raised from
//     the not-yet-given pool when that is affordable, and the goal wraps
//     up when it is not.
//  6. A stuck card (escalated, failed, blocked on a dropped dependency)
//     goes to the lead up to twice; then it is dropped.
//  7. Pending lead wake reasons (kickoff, your notes, a rework note) run a
//     lead turn.
//  8. Waiting cards whose dependencies have landed start, up to the lanes.
//  9. When nothing is left to run, land or decide, the goal finishes: its
//     review of the combined branch starts.
package goalpolicy

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// CardState is where a goal card stands, as far as the conductor cares.
type CardState int

const (
	// Waiting: not started yet (todo).
	Waiting CardState = iota
	// Running: started and working, or waiting on a synchronous lead hook.
	Running
	// Verified: verified on its own branch, not yet landed on the goal.
	Verified
	// Landed: landed on the goal branch (done).
	Landed
	// Dropped: the goal dropped it.
	Dropped
	// Exhausted: stopped with its envelope dry.
	Exhausted
	// Stuck: stopped at a decision the autopilot could not take (a critique
	// or verify cap, an unclear verdict, a failed run, a sandbox refusal).
	Stuck
)

func (s CardState) String() string {
	return [...]string{"waiting", "running", "verified", "landed", "dropped", "exhausted", "stuck"}[s]
}

// Card is one goal card in a snapshot.
type Card struct {
	ID        domain.FeatureID
	State     CardState
	Envelope  int
	Spent     float64
	DependsOn []domain.FeatureID // other cards of the same goal
	// TakenOver marks a card you are driving yourself: the goal never
	// starts, raises or re-plans it, but lands it once it verifies.
	TakenOver bool
	// Findings counts reviewer findings still open on a verified card.
	Findings int
	// LeadTries counts lead turns that have already seen this card's
	// current problem and left it as it was.
	LeadTries int
	// Reason says why a Stuck card is stuck.
	Reason string
}

// Input is a goal snapshot.
type Input struct {
	Stage      domain.Stage
	Envelope   int     // the goal budget; the hard ceiling
	OwnSpent   float64 // the goal card's own spend (plan, lead, review, verify)
	Reserve    int     // credits held back for finishing
	Lanes      int     // cards that may run at once
	WrapUp     bool    // the goal has been told to finish now
	WrapReason string  // why, when WrapUp
	Cards      []Card
	// LeadAvailable reports that a lead turn can run: a lead role resolves
	// and the goal has not given up on it.
	LeadAvailable bool
	// LeadFailures counts consecutive failed lead turns.
	LeadFailures int
	// LeadPending lists wake reasons no lead turn has handled yet.
	LeadPending []string
	// Reviewing reports that the goal's own review or verify session is in
	// flight; nothing is conducted meanwhile.
	Reviewing bool
}

// MaxLeadFailures is how many lead turns in a row may fail before the goal
// stops relying on its lead and wraps up.
const MaxLeadFailures = 3

// MaxLeadTriesStuck is how many lead turns may leave a stuck card as it
// was before the conductor drops it.
const MaxLeadTriesStuck = 2

// Kind is what an action does.
type Kind int

const (
	// WrapUp stamps the goal as finishing now, with Reason.
	WrapUp Kind = iota
	// Drop drops Card, with Reason.
	Drop
	// Land lands Card on the goal branch.
	Land
	// Raise raises Card's envelope to To.
	Raise
	// Lead runs one lead turn over Reasons.
	Lead
	// Start starts Card.
	Start
	// Finish settles the goal's work and starts its review; Reason is why
	// the result is partial, empty when whole.
	Finish
)

func (k Kind) String() string {
	return [...]string{"wrap-up", "drop", "land", "raise", "lead", "start", "finish"}[k]
}

// Action is one step for the engine to execute.
type Action struct {
	Kind    Kind
	Card    domain.FeatureID
	To      int
	Reason  string
	Reasons []string // Lead only
}

func (a Action) String() string {
	var b strings.Builder
	b.WriteString(a.Kind.String())
	if a.Card != "" {
		b.WriteString(" " + string(a.Card))
	}
	if a.To != 0 {
		fmt.Fprintf(&b, " → %d", a.To)
	}
	if a.Reason != "" {
		b.WriteString(": " + a.Reason)
	}
	if len(a.Reasons) > 0 {
		b.WriteString(" [" + strings.Join(a.Reasons, "; ") + "]")
	}
	return b.String()
}

// Ledger is a goal's budget, split the way every surface shows it.
type Ledger struct {
	Envelope int
	Own      float64 // the goal card's own spend
	Given    float64 // what the cards hold: envelopes of live cards, spend of ended ones
	Reserve  int
	// Available is the not-yet-given pool: Envelope − Own − Given −
	// Reserve. Negative means the goal is already into its reserve.
	Available float64
}

// OwnBudget is what the goal's own stages (its review and verify) may
// spend: everything not held by its cards, reserve included — the reserve
// exists for exactly those turns.
func (l Ledger) OwnBudget() float64 { return float64(l.Envelope) - l.Own - l.Given }

// Held is what a card holds against the goal budget: its envelope while it
// can still spend (or its spend, when it has overrun), its spend once it
// has landed or been dropped — the unspent part comes back to the goal.
func (c Card) Held() float64 {
	if c.State == Landed || c.State == Dropped {
		return c.Spent
	}
	return math.Max(float64(c.Envelope), c.Spent)
}

// ComputeLedger totals a snapshot's budget.
func ComputeLedger(in Input) Ledger {
	l := Ledger{Envelope: in.Envelope, Own: in.OwnSpent, Reserve: in.Reserve}
	for _, c := range in.Cards {
		l.Given += c.Held()
	}
	l.Available = float64(in.Envelope) - l.Own - l.Given - float64(in.Reserve)
	return l
}

// Decide returns the actions for one tick, in execution order.
func Decide(in Input) []Action {
	if in.Stage != domain.StageImplement || in.Reviewing {
		return nil
	}
	var out []Action
	wrap := in.WrapUp
	wrapReason := in.WrapReason

	ledger := ComputeLedger(in)
	if !wrap && in.LeadFailures >= MaxLeadFailures {
		wrap, wrapReason = true, "the lead kept failing"
		out = append(out, Action{Kind: WrapUp, Reason: wrapReason})
	}
	if !wrap && ledger.Available < 0 {
		wrap, wrapReason = true, "the budget reached the reserve"
		out = append(out, Action{Kind: WrapUp, Reason: wrapReason})
	}

	cards := append([]Card(nil), in.Cards...)
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	state := map[domain.FeatureID]CardState{}
	for _, c := range cards {
		state[c.ID] = c.State
	}

	var leadReasons []string
	addLead := func(r string) {
		if in.LeadAvailable {
			leadReasons = append(leadReasons, r)
		}
	}

	if wrap {
		for _, c := range cards {
			switch c.State {
			case Waiting, Running, Exhausted, Stuck:
				out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "the goal is wrapping up: " + wrapReason})
				state[c.ID] = Dropped
			}
		}
	}

	// land one verified card per tick
	for _, c := range cards {
		if c.State != Verified {
			continue
		}
		if c.Findings > 0 && c.LeadTries == 0 && in.LeadAvailable && !wrap {
			addLead(fmt.Sprintf("%s verified with %d open reviewer finding(s) to settle", c.ID, c.Findings))
			break
		}
		out = append(out, Action{Kind: Land, Card: c.ID})
		break
	}

	available := ledger.Available
	if !wrap {
		for _, c := range cards {
			if c.TakenOver {
				continue
			}
			switch c.State {
			case Exhausted:
				if in.LeadAvailable && c.LeadTries == 0 {
					addLead(fmt.Sprintf("%s ran out of budget (spent %.0f of %d)", c.ID, c.Spent, c.Envelope))
					continue
				}
				to := int(domain.Budget{Envelope: c.Envelope}.RaisedEnvelope(c.Spent))
				delta := float64(to - c.Envelope)
				if to > c.Envelope && delta <= available {
					out = append(out, Action{Kind: Raise, Card: c.ID, To: to, Reason: "raised from the goal budget"})
					available -= delta
					continue
				}
				wrap, wrapReason = true, fmt.Sprintf("%s needs more budget than the goal has left", c.ID)
				out = append(out, Action{Kind: WrapUp, Reason: wrapReason})
				out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "the goal is wrapping up: " + wrapReason})
				state[c.ID] = Dropped
			case Stuck:
				if in.LeadAvailable && c.LeadTries < MaxLeadTriesStuck {
					addLead(fmt.Sprintf("%s is stuck: %s", c.ID, c.Reason))
					continue
				}
				out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "stuck and not recoverable: " + c.Reason})
				state[c.ID] = Dropped
			}
		}
	}
	if wrap {
		// a wrap-up triggered by an exhausted card above still has to drop
		// the rest of the unfinished work
		for _, c := range cards {
			if st := state[c.ID]; st == Waiting || st == Running || st == Exhausted || st == Stuck {
				if !hasDrop(out, c.ID) {
					out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "the goal is wrapping up: " + wrapReason})
				}
				state[c.ID] = Dropped
			}
		}
	}

	// cards waiting on a dependency that was dropped can never start
	if !wrap {
		for _, c := range cards {
			if c.State != Waiting || c.TakenOver {
				continue
			}
			for _, d := range c.DependsOn {
				if st, ok := state[d]; ok && st == Dropped {
					if in.LeadAvailable && c.LeadTries < MaxLeadTriesStuck {
						addLead(fmt.Sprintf("%s depends on %s, which was dropped", c.ID, d))
					} else {
						out = append(out, Action{Kind: Drop, Card: c.ID, Reason: fmt.Sprintf("depends on %s, which was dropped", d)})
						state[c.ID] = Dropped
					}
					break
				}
			}
		}
	}

	if in.LeadAvailable && !wrap {
		leadReasons = append(leadReasons, in.LeadPending...)
	}
	if len(leadReasons) > 0 {
		out = append(out, Action{Kind: Lead, Reasons: leadReasons})
	}

	if !wrap {
		running := 0
		for _, c := range cards {
			if state[c.ID] == Running && !c.TakenOver {
				running++
			}
		}
		lanes := in.Lanes
		if lanes <= 0 {
			lanes = domain.DefaultGoalLanes
		}
		for _, c := range cards {
			if running >= lanes {
				break
			}
			if state[c.ID] != Waiting || c.TakenOver {
				continue
			}
			if !depsLanded(c, state) {
				continue
			}
			out = append(out, Action{Kind: Start, Card: c.ID})
			state[c.ID] = Running
			running++
		}
	}

	if settled(cards, state) && len(leadReasons) == 0 && !hasKind(out, Land) {
		reason := ""
		if wrap {
			reason = wrapReason
		}
		if n := count(state, Dropped); n > 0 && reason == "" {
			reason = fmt.Sprintf("%d card(s) dropped", n)
		}
		out = append(out, Action{Kind: Finish, Reason: reason})
	}
	return out
}

func depsLanded(c Card, state map[domain.FeatureID]CardState) bool {
	for _, d := range c.DependsOn {
		if st, ok := state[d]; ok && st != Landed {
			return false
		}
	}
	return true
}

// settled reports that every card has landed or been dropped: nothing is
// left to run, land, raise or decide.
func settled(cards []Card, state map[domain.FeatureID]CardState) bool {
	for _, c := range cards {
		if st := state[c.ID]; st != Landed && st != Dropped {
			return false
		}
	}
	return true
}

func count(state map[domain.FeatureID]CardState, want CardState) int {
	n := 0
	for _, st := range state {
		if st == want {
			n++
		}
	}
	return n
}

func hasKind(acts []Action, k Kind) bool {
	for _, a := range acts {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func hasDrop(acts []Action, id domain.FeatureID) bool {
	for _, a := range acts {
		if a.Kind == Drop && a.Card == id {
			return true
		}
	}
	return false
}

// SplitEnvelopes sizes the envelopes of cards about to be minted. want
// holds each row's requested envelope (0 = let the goal split). Requested
// envelopes are kept; the rest share what is left of pool evenly. When the
// total overflows pool, every envelope is scaled down in proportion. A
// card never gets less than domain.MinEnvelope — when the pool cannot give
// every card that much, it refuses: a goal that cannot fund its own card
// list has to be re-planned or given more budget, not started starved.
func SplitEnvelopes(want []int, pool float64) ([]int, error) {
	out := make([]int, len(want))
	if len(want) == 0 {
		return out, nil
	}
	if pool < float64(domain.MinEnvelope*len(want)) {
		return nil, fmt.Errorf("the goal budget leaves %.0f credits for %d cards, less than %d each", pool, len(want), domain.MinEnvelope)
	}
	fixed, unset := 0, 0
	for _, w := range want {
		if w > 0 {
			fixed += w
		} else {
			unset++
		}
	}
	share := 0.0
	if unset > 0 {
		share = (pool - float64(fixed)) / float64(unset)
		if share < float64(domain.MinEnvelope) {
			share = float64(domain.MinEnvelope)
		}
	}
	total := 0.0
	for i, w := range want {
		if w > 0 {
			out[i] = w
		} else {
			out[i] = int(share)
		}
		total += float64(out[i])
	}
	if total > pool {
		scale := pool / total
		for i := range out {
			out[i] = int(float64(out[i]) * scale)
			if out[i] < domain.MinEnvelope {
				out[i] = domain.MinEnvelope
			}
		}
	}
	return out, nil
}
