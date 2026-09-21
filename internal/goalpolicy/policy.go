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
//     6a. A blocked card — one whose verify said the environment cannot run
//     its verification plan — goes to the lead once and is never dropped
//     for it: it waits, and is started again when there is a reason to
//     think the environment changed.
//  7. Pending lead wake reasons (kickoff, your notes, a rework note) run a
//     lead turn.
//  8. Waiting cards whose dependencies have landed start, up to the lanes.
//  9. When nothing is left to run, land or decide, the goal finishes: its
//     review of the combined branch starts.
//  10. When nothing can move and a blocked card is why, the goal stalls:
//     it stops, drops nothing, and says what it is waiting on.
//  14. A tranche — budget a plan held for cards nobody could name yet —
//     goes to the lead once what it waited for has settled, and is closed
//     after that look: what it did not give to cards returns to the goal.
//  15. A question only the owner can answer — a finding that would change
//     what "done" means — freezes the cards that serve only the items it
//     is about, leaves everything else running, and stops the goal when
//     nothing else can move. It is never answered by more turns.
//  12. While work is still in flight the goal finds out early: when cards
//     have landed on heads no conclusive run is about, and the substrate
//     is idle, it makes an integration run — from what is left above the
//     runs held back for being judged. Something that held before and no
//     longer does is a regression: the landings since it held are
//     bisected, and the lead is told which one broke it.
//  13. A card marked live proves itself on the substrate before it lands.
//  11. A goal whose items are proved by an experiment does not finish on
//     heads no conclusive run is about: it makes the run first. A blocked
//     card does not hold that run up — only the goal's runs take the
//     substrate, so a card blocked waiting for one would otherwise be
//     waiting on the goal that is waiting on it — but it does hold up
//     finishing, because blocked is unfinished rather than dropped. A run that
//     failed goes to the lead once before the goal goes on to be judged
//     on it; runs that keep judging nothing, or a rig that fails its own
//     control, stall the goal — evidence that cannot be believed is not
//     something more turns can fix.
package goalpolicy

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

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
	// Blocked: its verify said the environment cannot run its verification
	// plan. That is a statement about the environment, not about the work:
	// gatepolicy keeps it apart from a failed verify so that it "never
	// burns a corrective round against a problem no retry fixes", and the
	// goal keeps it apart from Stuck for the same reason. Read as stuck it
	// cost two lead turns and then the card's branch — the quota outage
	// that was read as the lead failing, one noun over.
	Blocked
)

func (s CardState) String() string {
	return [...]string{"waiting", "running", "verified", "landed", "dropped", "exhausted", "stuck", "blocked"}[s]
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
	// Discoveries counts what a verified card says it found out about the
	// system the goal is building on (FINDING: lines in its spec) — things
	// other cards need to know, which only the lead can put where they
	// will see them.
	Discoveries int
	// LeadTries counts lead turns that have already seen this card's
	// current problem and left it as it was.
	LeadTries int
	// Reason says why a Stuck card is stuck, or what a Blocked one is
	// waiting on.
	Reason string
	// Live marks a card that proves itself on the substrate before it
	// lands — one whose whole point is live behaviour, where landing it on
	// the strength of unit tests is landing it unproven. LiveProof is how
	// far that has got for the head the card has now, and LiveWhy says what
	// a failed or abandoned proof found.
	Live      bool
	LiveProof LiveProof
	LiveWhy   string
	// LiveExperiment is the experiment that proves it.
	LiveExperiment string
	// Frozen marks a card that serves only items the owner has been asked
	// about. It keeps everything it has — its branch, its spend, its place —
	// and nothing is done to it or for it until the owner answers: building
	// on towards a "done" that may be about to change is spending on a
	// guess.
	Frozen bool
	// Retry marks a Blocked card there is a reason to try again: something
	// happened since it stopped that may have changed its environment. A
	// retry is a verify session and costs what one costs, so the goal
	// never retries on a timer.
	Retry bool
}

// Tranche is part of the budget a plan held for cards it could not name
// yet (a `tbd` row). While open it is held against the ledger as a waiting
// card's envelope is, less what cards created from it have been given.
type Tranche struct {
	Title    string
	Envelope int
	Given    float64 // envelopes of the cards created from it
	// Ready: everything it waited for has landed or been dropped.
	// LeadSaw: a lead turn has run since.
	Ready, LeadSaw bool
}

// Held is what the tranche still holds against the goal budget.
func (t Tranche) Held() float64 { return math.Max(0, float64(t.Envelope)-t.Given) }

// Experiment is one experiment the goal's done-when items name, read
// against the heads the goal has now.
type Experiment struct {
	Name string
	// Problem is set when the experiment cannot be run at all.
	Problem string
	// Running: a run of it is in flight.
	Running bool
	// Proven: a conclusive run — pass or fail — is about the current heads.
	Proven bool
	// Failed: that run failed. LeadSaw: a lead turn has run since it did.
	Failed, LeadSaw bool
	// FailedWhy is the sentence the lead is woken with.
	FailedWhy string
	// Inconclusive counts the runs about the current heads, since the last
	// conclusive one and since a person was last here, that judged
	// nothing; Why is the newest of them saying why.
	Inconclusive int
	Why          string
	// ControlFailed: the newest of those failed the rig's own control.
	ControlFailed bool
	// Held: someone else has the substrate right now.
	Held bool
	// TrunkChecked: a conclusive run on the trunk exists — the negative
	// control. An experiment that has only ever been seen to pass has not
	// been seen to be able to fail, and a pass from it says less than it
	// looks: the item may have held before the goal did anything, or the
	// experiment may not observe it at all. It is the rule a repaired
	// check is held to (it must fail on main), applied to a run.
	TrunkChecked bool
	// LandedSince counts the cards landed on the goal's heads since the
	// newest conclusive run (all of them, before the first).
	LandedSince int
	// Regressed names what held in an earlier run and does not in the one
	// about the current heads. It is the strongest signal on the code a
	// substrate gives: something that never held failing is expected,
	// something that did is a landing's doing.
	Regressed []string
	// Candidates is how many landings lie between the run where it held and
	// the one where it did not. BisectNext is the one to try next (-1 when
	// there is nothing to try), Culprit the card found to have broken it,
	// and BisectStuck reports verdicts that contradict each other.
	Candidates  int
	BisectNext  int
	Culprit     domain.FeatureID
	BisectStuck bool
	// RegressionWhy is the sentence the lead is woken with, and
	// RegressionSeen reports that a lead turn has run since it was known.
	RegressionWhy  string
	RegressionSeen bool
}

// SubstrateBudget is a goal's second ledger: the experiment runs it may
// make and the minutes it may hold a substrate for. It is kept apart from
// the credit ledger on purpose. An exchange rate between a cluster's
// minutes and a model's tokens would be the fiction the per-stage credit
// shares were (DESIGN §5.1), and it would let a goal buy verification by
// starving its agents, or agents by starving its proof. Each is a ceiling
// only a person raises.
type SubstrateBudget struct {
	Runs    int // the ceiling on runs; 0 with Minutes 0 means none was agreed
	Minutes int // the ceiling on substrate minutes; 0 = not bounded by time
	// RunsSpent counts the runs that took the substrate, conclusive or not
	// — a run that judged nothing still used it. MinutesSpent is how long
	// they held it.
	RunsSpent    int
	MinutesSpent float64
	// TypicalMinutes is what one run has cost so far, 0 before any has.
	TypicalMinutes float64
}

// ReserveRuns is how many runs a goal holds back for being judged: the
// proof of its final heads, and that proof once more after a rework round.
// Everything a goal does to find out EARLY is bought from what is left
// above it, so that no amount of finding out early can spend the run the
// hand-over needs.
const ReserveRuns = 2

// Agreed reports that the goal has a substrate budget at all.
func (b SubstrateBudget) Agreed() bool { return b.Runs > 0 || b.Minutes > 0 }

func (b SubstrateBudget) reserve() (runs int, minutes float64) {
	runs = ReserveRuns
	if b.Runs > 0 && b.Runs/2 < runs {
		runs = b.Runs / 2 // a small budget keeps half of itself, not more than it has
	}
	return runs, float64(runs) * b.TypicalMinutes
}

// left is what remains under the ceilings, reserve included. A dimension
// with no ceiling is never what runs out.
func (b SubstrateBudget) left() (runs int, minutes float64) {
	runs, minutes = math.MaxInt32, math.MaxFloat64
	if b.Runs > 0 {
		runs = b.Runs - b.RunsSpent
	}
	if b.Minutes > 0 {
		minutes = float64(b.Minutes) - b.MinutesSpent
	}
	return runs, minutes
}

// CanProve reports whether the goal can afford a run it needs in order to
// be judged. It may spend the reserve: that is what the reserve is for.
func (b SubstrateBudget) CanProve() bool {
	if !b.Agreed() {
		return true
	}
	runs, minutes := b.left()
	return runs >= 1 && minutes >= b.TypicalMinutes && minutes > 0
}

// CanExplore reports whether the goal can afford a run it merely wants —
// an integration run, a bisect step — without touching the reserve.
func (b SubstrateBudget) CanExplore() bool {
	if !b.Agreed() {
		return true
	}
	runs, minutes := b.left()
	rr, rm := b.reserve()
	return runs-rr >= 1 && minutes-rm >= b.TypicalMinutes && minutes-rm > 0
}

// MaxInconclusive is how many runs in a row may judge nothing before the
// goal stops making them. Each is substrate time spent learning only that
// the substrate is not to be trusted, and the third says what the first
// two did.
const MaxInconclusive = 3

// LiveProof is how far a live card's proof has got.
type LiveProof int

const (
	// LiveNone: no conclusive run is about the card's head.
	LiveNone LiveProof = iota
	// LiveRunning: one is in flight.
	LiveRunning
	// LivePassed and LiveFailed: what the conclusive run said.
	LivePassed
	LiveFailed
	// LiveGaveUp: runs kept judging nothing. The card lands without its
	// proof and the log says so; the goal's own proof still stands between
	// it and the hand-over.
	LiveGaveUp
)

// Bisect narrows which of n ordered landings broke something that held
// before the first of them and does not hold after the last. verdicts maps
// a landing's index to what a run on the state just after it showed. It
// returns the index to try next, or the culprit once two neighbours
// disagree; both are -1 when the verdicts contradict each other — held
// after a landing it failed before — which is a rig that cannot be
// believed, not a landing to blame.
func Bisect(n int, verdicts map[int]bool) (next, culprit int) {
	lo, hi := -1, n-1
	for i, held := range verdicts {
		if i < 0 || i >= n {
			continue
		}
		if held && i > lo {
			lo = i
		}
		if !held && i < hi {
			hi = i
		}
	}
	switch {
	case n <= 0 || lo >= hi:
		return -1, -1
	case hi-lo == 1:
		return -1, hi
	}
	return lo + (hi-lo)/2, -1
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
	// LeadFailures counts consecutive failed lead turns — the lead's own
	// failures. A turn that failed because the backend could not serve it
	// is not one of them; it is LeadOutage.
	LeadFailures int
	// LeadOutage carries the backend's own words when the newest lead
	// turn could not run at all and nothing has succeeded since. The goal
	// stops on it and keeps everything.
	LeadOutage string
	// LeadPending lists wake reasons no lead turn has handled yet.
	LeadPending []string
	// Reviewing reports that the goal's own review or verify session is in
	// flight; nothing is conducted meanwhile.
	Reviewing bool
	// Experiments lists the experiments the goal's items are proved by.
	Experiments []Experiment
	// Substrate is the goal's substrate budget and what its runs have
	// spent of it.
	Substrate SubstrateBudget
	// Tranches lists the open tranches.
	Tranches []Tranche
	// NeedOwner is the standing question only the owner can answer, empty
	// when there is none.
	NeedOwner string
	// IntegrateEvery is how many landings pile up before an integration
	// run; 0 reads as 1 — whenever the substrate is idle and the heads are
	// unproven, because an idle scarce resource is pure waste.
	IntegrateEvery int
	// Quiet is how long it has been since anything happened in this goal —
	// a landing, a lead turn, a card event, a run. It is the backstop's
	// only input, and zero (an unsupplied clock) disarms it.
	Quiet time.Duration
	// QuietCeiling is how long that may go on before the goal says so.
	// Zero means MaxQuiet. A caller that has given its stages longer than
	// MaxQuiet to be silent in must raise this to match, or the backstop
	// fires inside a window the operator explicitly allowed — see
	// QuietCeilingFor.
	QuietCeiling time.Duration
}

// QuietCeilingFor is how long a goal may produce nothing when its stages
// are allowed to be silent for stageTimeout.
//
// MaxQuiet is longer than the DEFAULT stage timeout on purpose, so a
// stage that is genuinely stuck is timed out by its driver — which is an
// event — before the backstop is reached. But the timeout is a flag and
// MaxQuiet was a constant, so `--stage-timeout 45m` put a stage the
// operator had given 45 quiet minutes inside a 30-minute backstop. The
// ceiling therefore keeps the margin the constant was chosen for rather
// than the number: comfortably past whatever a stage is allowed, and
// never below MaxQuiet.
//
// A zero timeout disables the driver's own cut-off, so there is nothing
// to stay clear of and MaxQuiet stands as the only backstop there is.
func QuietCeilingFor(stageTimeout time.Duration) time.Duration {
	if stageTimeout <= 0 {
		return MaxQuiet
	}
	return max(MaxQuiet, stageTimeout+stageTimeout/2)
}

// MaxQuiet is the floor under how long a goal may produce nothing at all
// — no landing, no lead turn, no card event, no run heartbeat — before it
// says so. QuietCeilingFor raises it for a caller whose stages are
// allowed to be silent for longer.
//
// It deliberately does NOT require that nothing is running. A card the
// conductor reads as running is the one thing no rule here will touch, so
// a card that is running only as far as the snapshot knows is invisible to
// every other rule and to every stop: that is precisely how a goal came to
// tick silently for the better part of three hours. What tells a card that
// is working from one that only looks like it is, is that a card which is
// working produces events.
//
// Longer than the stage timeout (20 minutes by default) on purpose, so a
// stage that is genuinely silent is timed out by its driver — which is an
// event — before this is reached. A run in flight is covered twice over:
// its heartbeat is one of the times this measures, and anyRunning holds
// the goal open besides.
const MaxQuiet = 30 * time.Minute

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
	// Stall stops the goal because its agent backend could not serve it —
	// a provider quota, a rate limit, an overload. Reason is the
	// backend's own words, which generally say when it will serve again.
	//
	// Card is set when what cannot serve the goal is a card's environment
	// rather than the agent backend: every card that could move is
	// blocked, and Card is the first of them.
	//
	// It is the answer to a lead that is not failing at its job but
	// cannot run at all, and it drops nothing: MaxLeadFailures exists for
	// a lead that keeps getting the work wrong, and counting an outage
	// towards it spent a goal's whole tolerance in two seconds — three
	// identical "you've hit your session limit" replies, one per tick,
	// and every card dropped with a working branch. Which work to abandon
	// is no more the goal's decision here than it is when the budget runs
	// out (NeedBudget); waiting is not abandoning.
	Stall
	// NeedBudget stops the goal and asks for more. Card cannot go on
	// without credits the envelope cannot give, and To is what it asks
	// for — only a person raises a goal's envelope.
	//
	// It replaces dropping a card for want of money. Which work to
	// abandon when the budget runs out is not a decision the goal is
	// entitled to make on its own: it had been deciding it by accident,
	// dropping whichever card happened to exhaust first — reliably the
	// most ambitious one — and then reviewing itself against the work it
	// had just defunded. Verified cards still land; the waiting card
	// keeps its branch and its spend, and a top-up continues it.
	NeedBudget
	// Run makes a run of the experiment named in Experiment, for the
	// purpose in Reason.
	Run
	// NeedSubstrate stops the goal and asks for more substrate budget: it
	// cannot afford the run of Experiment it needs in order to be judged.
	// NeedBudget's twin, for the same reason — which proof to go without is
	// no more the goal's decision than which work to abandon.
	NeedSubstrate
	// CloseTranche closes the tranche titled Reason: what it did not give
	// to cards returns to the goal.
	CloseTranche
	// NeedOwner stops the goal on a question only its owner can answer.
	// Nothing is dropped. It is the one reason a goal that is otherwise
	// silent until it is ready speaks first, and it exists because the
	// alternative is a lead that, unable to meet what "done" says, quietly
	// decides what "done" should have said.
	NeedOwner
)

func (k Kind) String() string {
	return [...]string{"wrap-up", "drop", "land", "raise", "lead", "start", "finish", "shrink", "stall", "need-budget", "run", "need-substrate", "close-tranche", "need-owner"}[k]
}

// MinCardEnvelope is the smallest envelope worth giving a card. Below it a
// card cannot finish its design stage and that stage's critique on any
// repository measured — the lxd autopilot drive's cheapest plan cost 25
// credits for the architect and 33 for its reviewer, and its dearest 112
// and 66 — so funding a card under this is buying a guaranteed exhaustion
// rather than a chance at the work.
const MinCardEnvelope = 100

// MinRaise is the smallest top-up worth giving an exhausted card: less
// than one turn's worth buys another immediate exhaustion rather than any
// work, so below it the goal stops and asks instead.
const MinRaise = float64(domain.TurnReserveCredits)

// Action is one step for the engine to execute.
type Action struct {
	Kind    Kind
	Card    domain.FeatureID
	To      int
	Reason  string
	Reasons []string // Lead only
	// Experiment names the experiment a Run makes, or the one a Stall is
	// waiting to be able to believe.
	Experiment string
	// Landing is the landing a bisect Run tries: the run is about the
	// goal's heads as they were just after it.
	Landing int
}

func (a Action) String() string {
	var b strings.Builder
	b.WriteString(a.Kind.String())
	if a.Card != "" {
		b.WriteString(" " + string(a.Card))
	}
	if a.Experiment != "" {
		b.WriteString(" " + a.Experiment)
	}
	if a.Kind == Run && a.Reason == "bisect" {
		fmt.Fprintf(&b, " @%d", a.Landing)
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
	for _, t := range in.Tranches {
		l.Given += t.Held()
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
	// An outage outranks every other reading of the snapshot: a goal
	// whose backend cannot serve a turn cannot start a card, land one, or
	// ask its lead anything, and the one thing it must not do is decide
	// that the work is at fault. Nothing is dropped, nothing is started,
	// and the goal waits to be picked back up.
	if !wrap && in.LeadOutage != "" {
		return []Action{{Kind: Stall, Reason: in.LeadOutage}}
	}
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

	// needBudget: the goal has asked a person to raise the ceiling.
	// Nothing new starts meanwhile — another card started now would
	// exhaust on its first session and ask the same question twice —
	// but everything already in hand carries on, and a verified card
	// still lands, because landing it banks both the work and the
	// credits it did not spend.
	needBudget := false

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
		// What a verified card did not spend comes back the moment it
		// lands, and this tick is about to land one. A goal short of
		// credits it is already holding is not short: it dropped two
		// cards for want of 16 credits in the same tick it banked 240,
		// which is the arithmetic done in the wrong order rather than a
		// goal out of money.
		if ledger.Available < 0 {
			ledger.Available += unlandedReturn(cards)
		}
		if ledger.Available < 0 {
			// Still short. Which work to abandon is not the goal's
			// decision (§17.4a), and a wrap-up here took that decision:
			// it dropped every unfinished card, finished work included,
			// without anybody having been offered the raise that would
			// have kept it. Worse, it did so or did not depending on
			// nothing but how many cards happened to be running at that
			// tick, since only a waiting card can be shrunk — the same
			// goal, the same budget and the same shortfall shrank and
			// carried on with two cards waiting, and abandoned all three
			// with two of them running.
			//
			// So it stops and asks, keeping everything, exactly as an
			// exhausted card does. The card named is the one whose
			// allocation the goal cannot cover — the largest holder —
			// and what it asks for is that allocation plus the
			// shortfall, so a top-up sized from it clears the ledger.
			// Unless there is no work to keep. A goal holding nothing
			// but unstarted allocations loses nothing by stopping, and
			// a card funded below the floor buys a guaranteed
			// exhaustion rather than a chance at the work — so that
			// case keeps the wrap-up it always had, and only a goal
			// with work in flight is worth a person's attention.
			if c, ok := largestHolder(cards); ok && workInFlight(cards) {
				short := int(math.Ceil(-ledger.Available))
				out = append(out, Action{Kind: NeedBudget, Card: c.ID, To: c.Envelope + short,
					Reason: fmt.Sprintf("the goal's cards hold %.0f credits more than it has left and it needs about %d more to go on",
						-ledger.Available, short)})
				needBudget = true
			} else {
				// Nothing live to keep, so there is nothing to ask for
				// and nothing to lose by stopping.
				wrap, wrapReason = true, "the budget reached the reserve"
				out = append(out, Action{Kind: WrapUp, Reason: wrapReason})
			}
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
			case Waiting, Running, Exhausted, Stuck, Blocked:
				out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "the goal is wrapping up: " + wrapReason})
				state[c.ID] = Dropped
			}
		}
	}

	// land one verified card per tick
	idle := substrateIdle(in)
	for _, c := range cards {
		if c.State != Verified || (c.Frozen && !wrap) {
			continue
		}
		if c.Findings > 0 && c.LeadTries == 0 && in.LeadAvailable && !wrap {
			addLead(fmt.Sprintf("%s verified with %d open reviewer finding(s) to settle", c.ID, c.Findings))
			break
		}
		if c.Discoveries > 0 && c.LeadTries == 0 && in.LeadAvailable && !wrap {
			addLead(fmt.Sprintf("%s verified and reports %d thing(s) it found out about the system (FINDING: lines in its spec) — "+
				"record what holds in the notebook (notebook_finding) so the cards after it build on it", c.ID, c.Discoveries))
			break
		}
		if c.Live && !wrap && !c.TakenOver {
			// A live card's verify proved it compiles. Whether it WORKS is
			// only visible on the substrate, and the cheapest moment to
			// find out is before it is on the branch everything else forks
			// from.
			switch c.LiveProof {
			case LiveRunning:
				continue // another verified card may land meanwhile
			case LiveFailed:
				if in.LeadAvailable && c.LeadTries == 0 {
					addLead(fmt.Sprintf("%s verified, and its run on the substrate failed: %s", c.ID, c.LiveWhy))
					break
				}
				// the lead looked and left it: it lands, as a card with
				// findings the lead left open does, and the goal's own proof
				// is what stands between it and the hand-over
			case LiveNone:
				if in.Substrate.CanExplore() {
					if idle {
						out = append(out, Action{Kind: Run, Card: c.ID, Experiment: c.LiveExperiment, Reason: "card"})
						idle = false
					}
					continue
				}
				// it cannot be afforded: landing it unproven is the honest
				// remainder, and the log says that is what happened
			}
		}
		out = append(out, Action{Kind: Land, Card: c.ID})
		break
	}

	available := ledger.Available
	if !wrap && !needBudget {
		for _, c := range cards {
			if c.TakenOver || c.Frozen {
				continue
			}
			switch c.State {
			case Exhausted:
				if in.LeadAvailable && c.LeadTries == 0 {
					addLead(fmt.Sprintf("%s ran out of budget (spent %.0f of %d)", c.ID, c.Spent, c.Envelope))
					continue
				}
				ask := int(domain.Budget{Envelope: c.Envelope}.RaisedEnvelope(c.Spent))
				if ask <= c.Envelope {
					break
				}
				// Raise to what is there, not all or nothing. The full ask
				// is itself an estimate; a card refused it entirely stops
				// the goal, while the same card given what the pool holds
				// keeps working and comes back for the rest once its
				// siblings have landed and returned what they did not
				// spend.
				to := min(ask, c.Envelope+int(available))
				if float64(to-c.Envelope) >= MinRaise {
					out = append(out, Action{Kind: Raise, Card: c.ID, To: to, Reason: "raised from the goal budget"})
					available -= float64(to - c.Envelope)
					continue
				}
				// Nothing left worth giving: the envelope is spent, and
				// only a person raises it. The goal stops and says what it
				// needs — it does not choose work to abandon.
				out = append(out, Action{Kind: NeedBudget, Card: c.ID, To: ask,
					Reason: fmt.Sprintf("%s has spent %.0f of %d and needs about %d to go on", c.ID, c.Spent, c.Envelope, ask)})
				needBudget = true
			case Stuck:
				if in.LeadAvailable && c.LeadTries < MaxLeadTriesStuck {
					addLead(fmt.Sprintf("%s is stuck: %s", c.ID, c.Reason))
					continue
				}
				out = append(out, Action{Kind: Drop, Card: c.ID, Reason: "stuck and not recoverable: " + c.Reason})
				state[c.ID] = Dropped
			case Blocked:
				// One look, because the lead can sometimes do something
				// about it — a step that belongs under [CI-only], a plan
				// that asks for more than it needs. Never a second, and
				// never a drop: more turns cannot make an environment
				// appear, they can only be spent on its absence.
				if in.LeadAvailable && c.LeadTries == 0 && !c.Retry {
					addLead(fmt.Sprintf("%s cannot be verified in this environment: %s", c.ID, c.Reason))
				}
			}
		}
	}
	if wrap {
		// a wrap-up — yours, or the lead having failed too often — drops
		// the rest of the unfinished work. Running out of budget is NOT
		// one of its causes: that stops and asks (NeedBudget) instead of
		// choosing work to abandon.
		for _, c := range cards {
			if st := state[c.ID]; st == Waiting || st == Running || st == Exhausted || st == Stuck || st == Blocked {
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

	// tranches: one look for the lead once what they waited for has
	// settled, then closed — and closed at once by a wrap-up
	for _, tr := range in.Tranches {
		switch {
		case wrap, tr.Ready && (tr.LeadSaw || !in.LeadAvailable):
			out = append(out, Action{Kind: CloseTranche, Reason: tr.Title})
		case tr.Ready:
			addLead(fmt.Sprintf("what %q was waiting for has settled: create the cards it calls for (card_create with from: %q) within the %.0f credits held for it — what you do not use returns to the goal after this turn",
				tr.Title, tr.Title, tr.Held()))
		}
	}

	// finding out early: regressions first, then heads nobody has proven
	if !wrap && !needBudget {
		acts, reasons := integrate(in, idle && !hasKind(out, Run), settled(cards, state))
		out = append(out, acts...)
		for _, r := range reasons {
			addLead(r)
		}
	}

	if in.LeadAvailable && !wrap {
		leadReasons = append(leadReasons, in.LeadPending...)
	}
	if len(leadReasons) > 0 {
		out = append(out, Action{Kind: Lead, Reasons: leadReasons})
	}

	if !wrap && !needBudget {
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
			if c.TakenOver || c.Frozen {
				continue
			}
			switch {
			case state[c.ID] == Waiting && depsLanded(c, state):
			case state[c.ID] == Blocked && c.Retry:
			default:
				continue
			}
			out = append(out, Action{Kind: Start, Card: c.ID})
			state[c.ID] = Running
			running++
		}
	}

	// Nothing to do and nothing in flight, and a blocked card is why: the
	// goal stops and says what it is waiting on. It drops nothing, which
	// is the whole difference from the wrap-up this used to end in.
	//
	// Unless the goal still owes itself a run. Only the goal's runs and
	// live cards take the substrate, so a card blocked for want of one is
	// blocked on this goal — and stalling here would stop it holding the
	// thing the card is waiting for. Make the run; if the card is still
	// blocked afterwards, the next tick stalls with the same words.
	if !wrap && len(out) == 0 && len(leadReasons) == 0 && count(state, Running) == 0 && !anyRunning(in) {
		owed := false
		if provable(cards, state) && !openTranche(in, wrap) {
			if acts, _ := proveFirst(in); hasKind(acts, Run) {
				owed = true
			}
		}
		for _, c := range cards {
			if state[c.ID] == Blocked && !owed {
				return []Action{{Kind: Stall, Card: c.ID,
					Reason: fmt.Sprintf("%s cannot be verified in this environment: %s", c.ID, c.Reason)}}
			}
		}
	}

	// A question for the owner stops the goal once nothing else can move —
	// including at the point it would otherwise finish, because a goal that
	// goes on to be judged with the question open has answered it by default.
	if !wrap && in.NeedOwner != "" && len(out) == 0 && len(leadReasons) == 0 && count(state, Running) == 0 && !anyRunning(in) {
		return []Action{{Kind: NeedOwner, Reason: in.NeedOwner}}
	}

	if provable(cards, state) && len(leadReasons) == 0 && !hasKind(out, Land) && !hasKind(out, CloseTranche) && !openTranche(in, wrap) {
		// The work has settled. Before it is judged, the items an experiment
		// proves need evidence about the heads the goal has NOW — and a goal
		// wrapping up gets the same, because a partial result still has to
		// say truthfully which of its items hold.
		if hasKind(out, Run) {
			return out // a run was just asked for; what it says comes first
		}
		if acts, wait := proveFirst(in); wait {
			return append(out, acts...)
		}
		if !settled(cards, state) {
			// A card is blocked. The run has been made — that is what
			// getting here means — so the thing it was waiting on now
			// exists, and its retry can have it. What must not happen is
			// finishing: blocked is unfinished, not dropped.
			return out
		}
		reason := ""
		if wrap {
			reason = wrapReason
		}
		if n := count(state, Dropped); n > 0 && reason == "" {
			reason = fmt.Sprintf("%d card(s) dropped", n)
		}
		out = append(out, Action{Kind: Finish, Reason: reason})
	}
	// The backstop. Every rule above has had its turn and none of them
	// found anything to do; nothing is running, no run is in flight and no
	// lead turn is pending. A goal in that state is finished, stalled or
	// waiting for a person — it is never simply quiet, and a goal that IS
	// simply quiet is a goal nobody will ever be told about. Saying so
	// costs a stop a resume clears; not saying so cost one goal 2h49m.
	ceiling := in.QuietCeiling
	if ceiling <= 0 {
		ceiling = MaxQuiet
	}
	if !wrap && len(out) == 0 && in.Quiet > ceiling && !anyRunning(in) {
		return []Action{{Kind: Stall, Reason: fmt.Sprintf(
			"nothing has happened in this goal for %s: no landing, no lead turn, no card event, "+
				"no run — and the conductor found nothing to do. A card that is working produces "+
				"events, so something this goal is holding is not what it reads as",
			in.Quiet.Round(time.Minute))}}
	}
	return out
}

// substrateIdle reports that none of the goal's experiments has a run in
// flight and nobody else holds a substrate one of them needs: the goal
// makes one run at a time, because its experiments as good as always share
// a substrate and would only queue behind each other.
func substrateIdle(in Input) bool {
	for _, x := range in.Experiments {
		if x.Running || x.Held {
			return false
		}
	}
	return true
}

func anyRunning(in Input) bool {
	for _, x := range in.Experiments {
		if x.Running {
			return true
		}
	}
	return false
}

// canBisect reports that a regression can still be narrowed by a run.
func (x Experiment) canBisect() bool {
	return len(x.Regressed) > 0 && x.Culprit == "" && !x.BisectStuck && x.Candidates > 1 && x.BisectNext >= 0
}

// integrate is how a goal finds out early. For each experiment, in order:
// a regression is bisected while a run can still narrow it and then told to
// the lead, once; otherwise heads that enough cards have landed on since
// anything was proven get an integration run. At most one run, only on an
// idle substrate, and only from what is left above the runs held back for
// the goal being judged.
//
// Settled work is not explored, it is proven: that run is proveFirst's, and
// may spend what is held back.
func integrate(in Input, idle, isSettled bool) (acts []Action, leadReasons []string) {
	every := max(1, in.IntegrateEvery)
	for _, x := range in.Experiments {
		if x.Problem != "" || x.Running {
			continue
		}
		if len(x.Regressed) > 0 {
			if x.RegressionSeen {
				continue
			}
			if x.canBisect() && in.Substrate.CanExplore() {
				if idle {
					acts = append(acts, Action{Kind: Run, Experiment: x.Name, Reason: "bisect", Landing: x.BisectNext})
					idle = false
				}
				continue // the lead is told once there is a name to tell it
			}
			leadReasons = append(leadReasons, x.RegressionWhy)
			continue
		}
		if x.Proven || x.ControlFailed || x.Inconclusive >= MaxInconclusive {
			continue
		}
		if idle && !isSettled && x.LandedSince >= every && in.Substrate.CanExplore() {
			acts = append(acts, Action{Kind: Run, Experiment: x.Name, Reason: "integration"})
			idle = false
		}
	}
	return acts, leadReasons
}

// proveFirst is what stands between settled work and the goal's review:
// the runs its experiment items still need. wait reports that the goal is
// not ready to finish; the actions are what to do about it (none, while a
// run is in flight or someone else has the substrate).
func proveFirst(in Input) (acts []Action, wait bool) {
	for _, x := range in.Experiments {
		switch {
		case x.Problem != "":
			// nothing a run could be made of: the item goes to verify as it
			// is, and is reported not checked, with this as the reason
			continue
		case x.Running, x.Held && !x.Proven:
			wait = true
		case x.Proven && x.Failed && !x.LeadSaw && in.LeadAvailable && !in.WrapUp:
			// One look before the goal is judged on it. The lead can turn a
			// failed run into a card while that still costs a card; after
			// the goal's verify it costs a rework round as well.
			return []Action{{Kind: Lead, Reasons: []string{x.FailedWhy}}}, true
		case x.Proven && !x.Failed && !x.TrunkChecked && in.Substrate.CanProve():
			// it passes; has it ever been seen to fail? One run on the
			// trunk, once per goal, before a pass is handed over as proof.
			acts = append(acts, Action{Kind: Run, Experiment: x.Name, Reason: "negative-control"})
			wait = true
		case x.Proven:
		case x.ControlFailed:
			return []Action{{Kind: Stall, Experiment: x.Name,
				Reason: fmt.Sprintf("%s cannot judge anything: %s", x.Name, x.Why)}}, true
		case x.Inconclusive >= MaxInconclusive:
			return []Action{{Kind: Stall, Experiment: x.Name,
				Reason: fmt.Sprintf("%s judged nothing %d times running, most recently: %s", x.Name, x.Inconclusive, x.Why)}}, true
		case !in.Substrate.CanProve():
			return []Action{{Kind: NeedSubstrate, Experiment: x.Name,
				Reason: fmt.Sprintf("the goal has spent %d of %d runs and %.0f of %d substrate minutes, and cannot afford the run of %s it has to make to be judged",
					in.Substrate.RunsSpent, in.Substrate.Runs, in.Substrate.MinutesSpent, in.Substrate.Minutes, x.Name)}}, true
		default:
			acts = append(acts, Action{Kind: Run, Experiment: x.Name, Reason: "verify"})
			wait = true
		}
	}
	// one run at a time per tick is plenty: experiments that share a
	// substrate would only queue behind each other
	if len(acts) > 1 {
		acts = acts[:1]
	}
	return acts, wait
}

// openTranche reports that a tranche still stands between the goal and
// finishing: work it was held for may yet be created.
func openTranche(in Input, wrap bool) bool {
	return !wrap && len(in.Tranches) > 0
}

func depsLanded(c Card, state map[domain.FeatureID]CardState) bool {
	for _, d := range c.DependsOn {
		if st, ok := state[d]; ok && st != Landed {
			return false
		}
	}
	return true
}

// provable reports that no card is going to change what a run would say:
// each one has landed, been dropped, or is blocked on its environment.
//
// Blocked is the difference between this and settled, and the reason it
// has to be: a card whose verification wants a substrate run can never
// pass on its own, because only the goal's runs and `live:` cards take
// the substrate. Waiting for it to land before taking the run leaves the
// goal holding the very thing the card is blocked on. So the run is
// taken, and the card's retry can have it.
//
// Finishing still needs settled. Blocked is unfinished, not dropped.
func provable(cards []Card, state map[domain.FeatureID]CardState) bool {
	for _, c := range cards {
		if st := state[c.ID]; st != Landed && st != Dropped && st != Blocked {
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

// unlandedReturn is what the goal gets back when the cards that have
// already verified land: the part of each of their envelopes they did
// not spend. A verified card's work is done and its allocation is no
// longer a promise about anything — it is credits in transit.
func unlandedReturn(cards []Card) float64 {
	var back float64
	for _, c := range cards {
		if c.State != Verified {
			continue
		}
		if spare := float64(c.Envelope) - c.Spent; spare > 0 {
			back += spare
		}
	}
	return back
}

// workInFlight reports that some live card has spent credits or already
// verified: work a raise would preserve and a wrap-up would throw away.
// It is the difference between a goal that should stop and ask and one
// that has nothing to lose by stopping.
func workInFlight(cards []Card) bool {
	for _, c := range cards {
		if c.State == Landed || c.State == Dropped {
			continue
		}
		if c.Spent > 0 || c.State == Verified {
			return true
		}
	}
	return false
}

// largestHolder is the unfinished card holding most of the goal's
// budget: the allocation a goal that has run short cannot cover, and
// therefore the one to name when it asks for more. Ties go to the first
// id, so the same shortfall always names the same card and the ask is
// not re-opened every tick.
//
// A verified card is not a candidate. It is about to land and stop
// holding anything, and its unspent envelope has already been counted
// as coming back — naming it would ask for credits to fund work that is
// finished.
func largestHolder(cards []Card) (Card, bool) {
	var best Card
	var found bool
	for _, c := range cards {
		if c.State == Landed || c.State == Dropped || c.State == Verified {
			continue
		}
		if !found || c.Held() > best.Held() {
			best, found = c, true
		}
	}
	return best, found
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
