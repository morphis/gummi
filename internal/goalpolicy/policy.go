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
	// Shrink lowers Card's envelope to To, freeing the difference back to
	// the goal. The opposite of Raise, and the step that was missing
	// between "the ledger went negative" and "drop a card".
	Shrink
)

func (k Kind) String() string {
	return [...]string{"wrap-up", "drop", "land", "raise", "lead", "start", "finish", "shrink"}[k]
}

// MinCardEnvelope is the smallest envelope worth giving a card. Below it a
// card cannot finish its design stage and that stage's critique on any
// repository measured — the lxd autopilot drive's cheapest plan cost 25
// credits for the architect and 33 for its reviewer, and its dearest 112
// and 66 — so funding a card under this is buying a guaranteed exhaustion
// rather than a chance at the work.
const MinCardEnvelope = 100

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
	cards := append([]Card(nil), in.Cards...)
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	state := map[domain.FeatureID]CardState{}
	for _, c := range cards {
		state[c.ID] = c.State
	}

	if !wrap && ledger.Available < 0 {
		// Before giving up on the work, try making room for it.
		//
		// The ledger counts what cards HOLD, not what they have spent
		// (Card.Held), so a card that has not started yet holds its whole
		// allocation — and when that allocation is what tipped the ledger
		// negative, the card the goal then drops is the one whose unspent
		// credits made the number negative in the first place. On the lxd
		// autopilot drive that arithmetic dropped a one-paragraph doc card
		// while 330 of the goal's 1,400 credits had never been spent:
		//
		//     Available = 1400 − Own 470 − Given (490 + 416) − Reserve 140 = −116
		//
		// Shrinking that card's envelope by 117 would have balanced the
		// ledger and left it 299 credits to work with. So a waiting card is
		// shrunk to what the goal can actually afford, and dropped only
		// when that is less than a card can do anything with.
		if shrinks, freed := reclaimFromWaiting(cards, -ledger.Available); freed {
			out = append(out, shrinks...)
			for _, sh := range shrinks {
				for i := range cards {
					if cards[i].ID == sh.Card {
						ledger.Available += float64(cards[i].Envelope - sh.To)
						cards[i].Envelope = sh.To
					}
				}
			}
		}
		if ledger.Available < 0 {
			wrap, wrapReason = true, "the budget reached the reserve"
			out = append(out, Action{Kind: WrapUp, Reason: wrapReason})
		}
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

// StartEnvelopes sizes the envelopes cards are MINTED at. want holds each
// row's estimate from the plan (0 = no estimate given). A card starts at
// the lesser of its estimate and an even share of pool, never below
// domain.MinEnvelope; whatever is left over is not handed out at all. When
// the pool cannot give every card MinEnvelope it refuses: a goal that
// cannot fund its own card list has to be re-planned or given more budget,
// not started starved.
//
// It used to hand out the whole pool up front, in proportion to the
// plan's estimates. An estimate is the one number in a goal plan that
// nothing grounds — the architect is guessing what work it has not done
// yet will cost — and committing the budget to it made the guess
// load-bearing. A card that has not started holds its whole envelope
// against the ledger (Card.Held), so one overestimated card took the
// goal's room to give with it: on a real drive one card was handed 65% of
// the budget before it ran a single turn, spent half of that, and was
// dropped for want of the credits its two cheap siblings were sitting on
// and never used. The goal then reviewed itself against the work it had
// just defunded.
//
// Sizing the START instead leaves the difference in the pool, where the
// raise machinery already spends it on evidence: a card that exhausts
// gets a lead turn and then a raise from what is available, and a card
// that lands or is dropped returns what it did not spend. The estimate
// stays in the goal doc as what the plan expected, which is what an
// estimate can honestly be.
func StartEnvelopes(want []int, pool float64) ([]int, error) {
	out := make([]int, len(want))
	if len(want) == 0 {
		return out, nil
	}
	if pool < float64(domain.MinEnvelope*len(want)) {
		return nil, fmt.Errorf("the goal budget leaves %.0f credits for %d cards, less than %d each", pool, len(want), domain.MinEnvelope)
	}
	share := pool / float64(len(want))
	for i, w := range want {
		start := share
		if w > 0 && float64(w) < share {
			// under its share: the estimate is the whole ask, and the
			// difference stays in the pool rather than padding a card
			// that did not ask for it
			start = float64(w)
		}
		if start < float64(domain.MinEnvelope) {
			start = float64(domain.MinEnvelope)
		}
		out[i] = int(start)
	}
	return out, nil
}

// reclaimFromWaiting frees `need` credits by lowering the envelopes of
// cards that have not started, largest allocation first, never below
// MinCardEnvelope. It returns the shrinks to apply and whether they cover
// the whole shortfall — a partial reclaim is no use, because the goal
// wraps up either way, so nothing is emitted unless the ledger balances.
//
// Only Waiting cards are touched. A running card's envelope is a promise
// its session is already spending against, and an exhausted one has
// proved it needs more rather than less.
func reclaimFromWaiting(cards []Card, need float64) ([]Action, bool) {
	if need <= 0 {
		return nil, false
	}
	type room struct {
		id    domain.FeatureID
		env   int
		spare int
	}
	var rooms []room
	for _, c := range cards {
		if c.State != Waiting || c.Envelope <= MinCardEnvelope {
			continue
		}
		rooms = append(rooms, room{c.ID, c.Envelope, c.Envelope - MinCardEnvelope})
	}
	sort.Slice(rooms, func(i, j int) bool {
		if rooms[i].spare != rooms[j].spare {
			return rooms[i].spare > rooms[j].spare
		}
		return rooms[i].id < rooms[j].id
	})
	var total int
	for _, r := range rooms {
		total += r.spare
	}
	if float64(total) < need {
		return nil, false // even at the floor there is not enough room
	}
	var out []Action
	left := int(math.Ceil(need))
	for _, r := range rooms {
		if left <= 0 {
			break
		}
		take := min(r.spare, left)
		out = append(out, Action{Kind: Shrink, Card: r.id, To: r.env - take,
			Reason: "shrunk to what the goal can still fund"})
		left -= take
	}
	return out, true
}
