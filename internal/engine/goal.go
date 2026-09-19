package engine

// Goals (domain.KindGoal): a card whose work is other cards. This file is
// the goal's conductor — the engine half of goalpolicy. It builds a goal
// snapshot from the store and the live sessions, asks goalpolicy.Decide
// what to do, and executes the git and store side of every action:
// minting and attaching the goal's cards at the plan gate, landing
// verified cards on the goal branch, raising and dropping cards, wrapping
// up, and starting the goal's own review once its work is settled.
//
// It never drives a card through its stages. The board and the headless
// driver already know how to run an autopilot card; GoalTick hands them
// the cards to start (GoalTickResult.Start) and they do.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/substrate"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
)

// ActorGoal is the transition actor for moves the goal's conductor makes
// on its own cards (a landing, a send-back).
const ActorGoal = "goal"

// goalIdleGrace is how long a started goal card may sit with no session,
// no park and no progress before the conductor restarts it. It covers the
// moment between one step of a driving loop and the next, so a card that
// is merely between steps is never restarted under the loop's feet.
//
// It was 90 seconds, which is shorter than the gaps a real repository
// produces: check discovery alone ran 4.6 minutes on canonical/lxd, and
// the conductor duly declared a working card stuck and paid a lead turn
// to restart it. That particular gap is now visible on its own
// (OneShotRunning, oneshotpresence.go); the grace is widened as well
// because it is the backstop for every OTHER between-steps pause on a
// large tree — a worktree add, a rebase, a session spawning — and the
// cost of waiting too long on a card that really is stuck is one more
// tick, while the cost of restarting one that is not is a lead turn and a
// duplicated pass.
const goalIdleGrace = 5 * time.Minute

// goalOutageRetry is how long a goal waits before asking a backend that
// could not serve it to try again. A rate limit lasts minutes to hours
// and every attempt inside it costs a process spawn and another failure
// in the log, so the conductor sits still between tries rather than
// retrying on its poll interval — while still recovering on its own,
// which is what a person expects to come back to.
const goalOutageRetry = 2 * time.Minute

// goalRetryRef marks the log entry of a blocked card started again, so the
// retries a goal makes on its own can be counted.
const goalRetryRef = "retry-blocked"

// maxSubstrateRetries is how many times a goal retries a blocked card on
// the strength of a substrate probing ready, between one visit from a
// person and the next. A retry is a verify session and costs what one
// costs; a card that blocks twice against a substrate whose probe passes
// is waiting for something the probe cannot see, and asking a third time
// is spending on it.
const maxSubstrateRetries = 2

// goalLock serializes everything the conductor does for one goal: two
// ticks of the same goal must never land two cards on its branch at once
// or read a snapshot the other is changing.
func (e *Engine) goalLock(id domain.FeatureID) *sync.Mutex {
	e.goalLocksMu.Lock()
	defer e.goalLocksMu.Unlock()
	if e.goalLocks == nil {
		e.goalLocks = map[domain.FeatureID]*sync.Mutex{}
	}
	m := e.goalLocks[id]
	if m == nil {
		m = &sync.Mutex{}
		e.goalLocks[id] = m
	}
	return m
}

// --- snapshot ------------------------------------------------------------

// GoalCard is one card of a goal as the goal sees it.
type GoalCard struct {
	Feature   domain.Feature
	State     goalpolicy.CardState
	Reason    string             // why a stuck card is stuck, why a dropped card was dropped
	Envelope  int                // the envelope that counts against the goal
	Spent     float64            // the spend that counts against the goal
	DependsOn []domain.FeatureID // other cards of this goal
	Findings  int                // open reviewer findings on a verified card
	// Discoveries counts the FINDING: lines in a verified card's spec.
	Discoveries int
	LeadTries   int
	TakenOver   bool
	Retry       bool     // a blocked card there is a reason to try again
	Frozen      bool     // serves only an item the owner has been asked about
	WaitsOn     []string // the substrates a blocked card's plan cites
	// Live marks a card that proves itself on the substrate before it lands
	// (its row's `live: true`); LiveExperiment is the experiment that does,
	// LiveHead the commit a proof has to be about, LiveProof how far that
	// has got and LiveWhy what a failed proof found.
	Live           bool
	LiveExperiment string
	LiveHead       string
	LiveProof      goalpolicy.LiveProof
	LiveWhy        string
	Serves         []string // done-when ids, from the goal doc's card row
}

// GoalView is a goal at one moment: its card, its cards, its budget, its
// log, and the policy input they make.
type GoalView struct {
	Goal     domain.Feature
	Cards    []GoalCard
	Ledger   goalpolicy.Ledger
	Log      []state.GoalEntry
	DoneWhen []domain.DoneWhen
	Rows     []domain.GoalCardRow
	Input    goalpolicy.Input
	// DocPath is where the goal doc lives right now ("" when missing).
	DocPath string
	// NeedsBudget is the goal's standing request for more budget, derived
	// from the log: which card is waiting and what it asked for.
	NeedsBudget GoalNeedsBudget
	// Experiments lists the experiments the goal's items are proved by,
	// read against the heads the goal has now.
	Experiments []GoalExperiment
	// Substrate is the goal's substrate budget and what its runs have spent.
	Substrate goalpolicy.SubstrateBudget
	// NeedsSubstrate is the goal's standing request for more of it.
	NeedsSubstrate GoalNeedsSubstrate
	// Notebook is the index of what the goal knows that no card owns.
	Notebook string
	// Tranches lists the budget held for cards the plan could not name yet.
	Tranches []GoalTranche
	// NeedsOwner is the standing question only the owner can answer.
	NeedsOwner GoalNeedsOwner
}

// GoalTranche is one tbd row of the plan and what has become of it.
type GoalTranche struct {
	Title    string
	Serves   []string
	Envelope int
	Given    float64            // envelopes of the cards created from it
	Cards    []domain.FeatureID // those cards
	Closed   bool
	// Ready: every row it waited for has landed or been dropped, at
	// SettledAt.
	Ready     bool
	SettledAt time.Time
}

// GoalNeedsOwner is a question only the goal's owner can answer: a finding
// that would change what an agreed done-when item means.
type GoalNeedsOwner struct {
	Item     string `json:"item,omitempty"`
	Question string `json:"question,omitempty"`
	Proposal string `json:"proposal,omitempty"`
	Finding  string `json:"finding,omitempty"`
}

// Waiting reports whether the goal has a question open.
func (n GoalNeedsOwner) Waiting() bool { return n.Question != "" }

// needsOwnerFrom derives the standing question from the log: the newest one
// the owner has not spoken after. Anything the owner says answers it — a
// note, a send-back, a reversal — because what they say is theirs to choose,
// and the lead reads it on its next turn either way.
func needsOwnerFrom(log []state.GoalEntry) GoalNeedsOwner {
	var out GoalNeedsOwner
	for _, en := range log {
		switch en.Action {
		case state.GoalNeedOwner:
			out = GoalNeedsOwner{Item: en.Item, Question: en.Detail, Proposal: en.Alternative, Finding: en.Ref}
		case state.GoalNote, state.GoalReversed:
			out = GoalNeedsOwner{}
		case state.GoalRework:
			if en.By == "user" {
				out = GoalNeedsOwner{}
			}
		}
	}
	return out
}

// Card returns the goal card with id, and whether it belongs to the goal.
func (v GoalView) Card(id domain.FeatureID) (GoalCard, bool) {
	for _, c := range v.Cards {
		if c.Feature.ID == id {
			return c, true
		}
	}
	return GoalCard{}, false
}

// GoalView reads a goal's current view.
func (e *Engine) GoalView(ctx context.Context, goalID domain.FeatureID) (GoalView, error) {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return GoalView{}, err
	}
	if !goal.IsGoal() {
		return GoalView{}, fmt.Errorf("%s is not a goal", goalID)
	}
	return e.goalView(ctx, goal)
}

func (e *Engine) goalView(ctx context.Context, goal domain.Feature) (GoalView, error) {
	v := GoalView{Goal: goal}
	cards, err := e.cfg.Store.ListGoalCards(ctx, goal.ID)
	if err != nil {
		return v, err
	}
	if v.Log, err = e.cfg.Store.GoalLog(ctx, goal.ID); err != nil {
		return v, err
	}
	v.NeedsBudget = needsBudgetFrom(v.Log)
	v.NeedsOwner = needsOwnerFrom(v.Log)
	v.Notebook = e.goalNotebook(goal.ID).Index()
	v.DocPath = e.artifactFile(&goal)
	if v.DocPath != "" {
		if raw, rerr := os.ReadFile(v.DocPath); rerr == nil {
			v.DoneWhen, _, _ = spec.ParseDoneWhen(string(raw))
			v.Rows, _, _ = spec.ParseGoalCards(string(raw), v.DoneWhen)
		}
	}
	serves := map[domain.FeatureID][]string{}
	live := map[domain.FeatureID]bool{}
	for _, r := range v.Rows {
		if r.ID != "" {
			serves[r.ID] = r.Serves
			live[r.ID] = r.Live
		}
	}
	itemExperiment := map[string]string{}
	for _, d := range v.DoneWhen {
		itemExperiment[d.ID] = strings.TrimSpace(d.Experiment)
	}
	runs := e.ExperimentRuns(goal.ID)
	e.reapExperimentTrees(ctx, runs)
	landedAt := map[domain.FeatureID]time.Time{}
	open, err := e.cfg.Store.OpenDecisions(ctx)
	if err != nil {
		return v, err
	}
	members := map[domain.FeatureID]bool{}
	for _, c := range cards {
		members[c.ID] = true
	}

	// Lead failures older than the goal's latest arrival at implement are
	// history. Every way a person puts a goal back to work ends in that
	// transition — a send-back through its verify arm, a line the
	// composer routes there, a reversed decision, a verify that failed —
	// and each of them used to leave the failures that had wrapped the
	// goal up standing as the newest lead entries in its log. The goal
	// then re-wrapped on its first tick, bounced straight back to its
	// hand-over, and said "the lead kept failing" about turns that had
	// happened before the person intervened. Twice on the measured drive,
	// at the cost of a full critique and verify round each time.
	var leadSince int64
	if marks, merr := e.cfg.Store.LatestCardMarks(ctx, goal.ID); merr == nil {
		// the newest of the two, because a stage the goal was moved into
		// leaves a gate mark and one it started a session in leaves a
		// stage-enter; a send-back is the first without the second
		leadSince = max(marks.Gate.Seq, marks.StageEnter.Seq)
	}

	// the last log entry that touched each card, and the pre-goal spend of
	// attached cards (it does not count against the goal)
	lastTouch := map[domain.FeatureID]int64{}
	lastTouchAt := map[domain.FeatureID]time.Time{}
	attachSpent := map[domain.FeatureID]int{}
	dropReason := map[domain.FeatureID]string{}
	leadSeen := map[domain.FeatureID][]int64{}
	var lastLeadOK, lastLeadAny, retrySince int64
	var lastLeadAt, visitedAt time.Time
	autoRetries := map[domain.FeatureID]int{}
	failures := 0
	outage, stalled := "", ""
	var stalledAt time.Time
	for _, en := range v.Log {
		switch en.Action {
		case state.GoalStarted, state.GoalBounced, state.GoalRaised, state.GoalAnswered,
			state.GoalPlanApproved, state.GoalAttached, state.GoalMinted:
			lastTouch[en.Card] = en.Seq
			if en.Action != state.GoalMinted && en.Action != state.GoalAttached {
				lastTouchAt[en.Card] = en.At
			}
		case state.GoalLanded:
			if en.Card != "" {
				landedAt[en.Card] = en.At
			}
		case state.GoalDropped:
			dropReason[en.Card] = en.Detail
		case state.GoalLeadTurn:
			outage, stalled = "", ""
			// only a wake turn names the cards it was woken over in Ref;
			// a turn answering a card's question or checking its plan
			// carries the card in Card and is not a try at unsticking it
			lastLeadOK, lastLeadAny, lastLeadAt = en.Seq, en.Seq, en.At
			failures = 0
			for _, id := range strings.Split(en.Ref, ",") {
				if id = strings.TrimSpace(id); id != "" {
					leadSeen[domain.FeatureID(id)] = append(leadSeen[domain.FeatureID(id)], en.Seq)
				}
			}
		case state.GoalRework:
			// Work is owed again — a send-back, or a verify that failed —
			// so the lead's tolerance starts over. Without this a goal
			// that once wrapped up for lead failures could never be
			// recovered: ClearGoalWrapUp lifted the wrap-up, the failures
			// that caused it were still the newest lead entries in the
			// log, and the next tick re-wrapped it in the same second.
			// Only a successful lead turn cleared them, and the conductor
			// will not run one while it is wrapping up.
			//
			// Resetting is self-limiting: a lead that really is broken
			// fails MaxLeadFailures times again and the goal wraps up
			// again, having cost one more round of turns.
			failures, outage, stalled = 0, "", ""
		case state.GoalLeadFailed:
			if en.Seq < leadSince {
				continue // before this stage began; not this run's evidence
			}
			lastLeadAny = en.Seq
			if en.Outage {
				// not the lead's failure: the backend could not serve the
				// turn. It neither counts against the lead nor clears a
				// previous count — nothing was learned either way.
				outage = en.Detail
				continue
			}
			outage = ""
			failures++
		case state.GoalStalled:
			if en.Card != "" || en.Ref != "" {
				// stopped on a card's environment or on evidence it cannot
				// believe, not on the backend: the lead can run, and
				// nothing here is an outage to wait out
				continue
			}
			// the goal has already stopped on this outage and said so
			outage, stalled, stalledAt = "", en.Detail, en.At
		case state.GoalNote, state.GoalResumed:
			// a person acted: whatever a blocked card was waiting for may
			// be there now
			retrySince, visitedAt = en.Seq, en.At
			autoRetries = map[domain.FeatureID]int{}
		}
		if en.Action == state.GoalStarted && en.Ref == goalRetryRef {
			autoRetries[en.Card]++
		}
		if en.Action == state.GoalAttached {
			attachSpent[en.Card] = en.From
		}
	}

	now := e.now()
	// newest is when anything last happened in this goal — its own log, any
	// card's events, any run. Decide's backstop is the only reader: a goal
	// that produces nothing for MaxQuiet with nothing running is a goal
	// something is wrong with, and it must say so rather than tick on.
	var newest time.Time
	for _, en := range v.Log {
		if en.At.After(newest) {
			newest = en.At
		}
	}
	if outage == "" && stalled != "" && now.Sub(stalledAt) < goalOutageRetry {
		// Already reported, and the backend is unlikely to have come back
		// in the last few seconds. Staying stopped keeps the goal from
		// spawning a doomed session on every tick; after the window one
		// tick tries again, and either carries on or stalls afresh.
		outage = stalled
	}
	for _, c := range cards {
		gc := GoalCard{Feature: c, Serves: serves[c.ID], TakenOver: c.GateMode() == domain.GateAttended}
		before := attachSpent[c.ID]
		gc.Spent = c.Spend.CreditEquivalent() - float64(before)
		if gc.Spent < 0 {
			gc.Spent = 0
		}
		gc.Envelope = c.Budget.Envelope - before
		if gc.Envelope < 0 {
			gc.Envelope = 0
		}
		if deps, derr := e.cfg.Store.ListDependencies(ctx, c.ID); derr == nil {
			for _, d := range deps {
				if members[d] {
					gc.DependsOn = append(gc.DependsOn, d)
				}
			}
		}
		marks, merr := e.cfg.Store.LatestCardMarks(ctx, c.ID)
		if marks.Last.At.After(newest) {
			newest = marks.Last.At
		}
		if merr != nil {
			return v, merr
		}
		gc.State, gc.Reason = e.goalCardState(ctx, c, marks, open[c.ID], lastTouch[c.ID], lastTouchAt[c.ID], now)
		if gc.State == goalpolicy.Dropped && gc.Reason == "" {
			gc.Reason = dropReason[c.ID]
		}
		if gc.State == goalpolicy.Verified {
			gc.Findings = e.openReviewerFindings(&c)
			gc.Discoveries = e.reportedDiscoveries(&c)
		}
		if gc.State == goalpolicy.Verified && live[c.ID] {
			for _, item := range gc.Serves {
				if x := itemExperiment[item]; x != "" {
					gc.Live, gc.LiveExperiment = true, x
					break
				}
			}
			if gc.Live {
				e.readLiveProof(ctx, &gc, runs, visitedAt)
			}
		}
		if gc.State == goalpolicy.Blocked {
			gc.Retry = retrySince > marks.Park.Seq
			gc.WaitsOn = e.citedSubstrates(&c)
			if !gc.Retry && autoRetries[c.ID] < maxSubstrateRetries {
				gc.Retry = e.substratesReady(ctx, gc.WaitsOn)
			}
		}
		// lead turns that saw this card since it last crossed a stage: a
		// send-back or a restart is the lead acting on the same problem, not
		// the card getting past it, so neither resets the count — otherwise
		// a lead that keeps sending a card back would never let it be dropped
		since := marks.Gate.Seq
		if gc.State == goalpolicy.Verified {
			// a verified card's problem is its findings, first seen at verify
			since = max(since, marks.StageEnter.Seq)
		}
		for _, seq := range leadSeen[c.ID] {
			if seq > since {
				gc.LeadTries++
			}
		}
		v.Cards = append(v.Cards, gc)
	}

	droppedAt := map[domain.FeatureID]time.Time{}
	for _, en := range v.Log {
		if en.Action == state.GoalDropped {
			droppedAt[en.Card] = en.At
		}
	}
	v.Tranches = goalTranches(v.Rows, v.Log, v.Cards, landedAt, droppedAt)

	in := goalpolicy.Input{
		Stage:        goal.Stage,
		Envelope:     goal.Budget.Envelope,
		OwnSpent:     goal.Spend.CreditEquivalent(),
		Reserve:      goal.ReserveCredits(),
		Lanes:        goal.Goal.LaneCount(),
		WrapUp:       goal.Goal.WrappingUp(),
		WrapReason:   e.goalWrapReason(v.Log),
		LeadFailures: failures,
		LeadOutage:   outage,
		Reviewing:    e.Get(goal.ID) != nil,
	}
	for _, r := range e.ExperimentRuns(goal.ID) {
		for _, at := range []time.Time{r.Started, r.Ended, r.Heartbeat} {
			if at.After(newest) {
				newest = at
			}
		}
	}
	if !newest.IsZero() {
		in.Quiet = now.Sub(newest)
	}
	in.QuietCeiling = goalpolicy.QuietCeilingFor(e.cfg.StageTimeout)
	in.NeedOwner = v.NeedsOwner.Question
	for _, t := range v.Tranches {
		if !t.Closed {
			in.Tranches = append(in.Tranches, goalpolicy.Tranche{Title: t.Title, Envelope: t.Envelope, Given: t.Given,
				Ready: t.Ready, LeadSaw: t.Ready && lastLeadAt.After(t.SettledAt)})
		}
	}
	for i := range v.Cards {
		v.Cards[i].Frozen = v.NeedsOwner.Waiting() && servesOnly(v.Cards[i].Serves, v.NeedsOwner.Item)
	}
	for _, gc := range v.Cards {
		in.Cards = append(in.Cards, goalpolicy.Card{
			Frozen: gc.Frozen,
			ID:     gc.Feature.ID, State: gc.State, Envelope: gc.Envelope, Spent: gc.Spent,
			DependsOn: gc.DependsOn, TakenOver: gc.TakenOver, Findings: gc.Findings, Discoveries: gc.Discoveries,
			LeadTries: gc.LeadTries, Reason: gc.Reason, Retry: gc.Retry,
			Live: gc.Live, LiveExperiment: gc.LiveExperiment, LiveProof: gc.LiveProof, LiveWhy: gc.LiveWhy,
		})
	}
	if goal.Stage == domain.StageImplement || goal.Stage == domain.StageVerify {
		var landings []goalLanding
		for _, gc := range v.Cards {
			if at, ok := landedAt[gc.Feature.ID]; ok && gc.State == goalpolicy.Landed && gc.Feature.LandedSHA != "" {
				landings = append(landings, goalLanding{Card: gc.Feature.ID, Repo: gc.Feature.Repo, SHA: gc.Feature.LandedSHA, At: at})
			}
		}
		sort.Slice(landings, func(i, j int) bool { return landings[i].At.Before(landings[j].At) })
		v.Experiments = e.goalExperiments(ctx, goal, v.DoneWhen, visitedAt.UnixNano(), landings)
	}
	if v.DocPath != "" {
		if raw, rerr := os.ReadFile(v.DocPath); rerr == nil {
			_, in.IntegrateEvery, _ = spec.ParseGoalSubstrate(string(raw))
		}
	}
	v.Substrate = substrateBudgetFrom(v.Log, e.ExperimentRuns(goal.ID), e.now())
	v.NeedsSubstrate = needsSubstrateFrom(v.Log)
	in.Substrate = v.Substrate
	for _, x := range v.Experiments {
		px := goalpolicy.Experiment{
			Name: x.Name, Problem: x.Problem, Running: x.Running != nil, Proven: x.Evidence != nil,
			Inconclusive: x.Inconclusive, Why: x.LastReason, ControlFailed: x.ControlFailed,
			TrunkChecked: x.Trunk != nil || trunkGaveUp(x),
		}
		if x.Evidence != nil && x.Evidence.Outcome == experiment.Fail {
			px.Failed, px.LeadSaw = true, lastLeadAt.After(x.Evidence.Ended)
			px.FailedWhy = fmt.Sprintf("the %s experiment failed on the goal's current heads — %s. Its evidence is in %s. "+
				"Turn what it shows into a card (card_create, card_send_back) or, if the item cannot be met, say so (done_when_not_met); "+
				"leave it and the goal goes on to be judged on this result.", x.Name, describeEvidence(*x.Evidence, nil), x.Evidence.Dir)
		}
		if !px.Running && x.Problem == "" {
			px.Held = e.substrateHeld(ctx, x.Substrate)
		}
		px.LandedSince = len(x.LandedSince)
		if len(x.Regressed) > 0 || x.WholeRegressed {
			px.Regressed = x.Regressed
			if x.WholeRegressed {
				px.Regressed = []string{"the run"}
			}
			px.Candidates, px.BisectNext, px.Culprit, px.BisectStuck = len(x.Suspects), x.BisectNext, x.Culprit, x.BisectStuck
			px.RegressionWhy, px.RegressionSeen = x.regressionWhy(), lastLeadAt.After(x.knownAt)
		} else {
			px.BisectNext = -1
		}
		in.Experiments = append(in.Experiments, px)
	}
	v.Ledger = goalpolicy.ComputeLedger(in)
	// a lead turn spends from what the goal has left to give; with less
	// than one turn's worth there, the conductor's own rules decide
	in.LeadAvailable = e.leadAvailable(goal) && v.Ledger.Available >= e.turnReserve()
	if in.LeadAvailable {
		if lastLeadAny == 0 && goal.Stage == domain.StageImplement {
			in.LeadPending = append(in.LeadPending, "kickoff: the goal's cards are minted — read the goal doc and set the goal up")
		}
		for _, en := range v.Log {
			if en.Seq <= lastLeadOK {
				continue
			}
			switch en.Action {
			case state.GoalNote:
				in.LeadPending = append(in.LeadPending, "your note: "+en.Detail)
			case state.GoalRework:
				in.LeadPending = append(in.LeadPending, "rework: "+en.Detail)
			case state.GoalReversed:
				in.LeadPending = append(in.LeadPending, "reverse "+en.Ref+": "+en.Detail)
			}
		}
	}
	v.Input = in
	return v, nil
}

// goalWrapReason is the newest wrap-up entry's reason.
func (e *Engine) goalWrapReason(log []state.GoalEntry) string {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Action == state.GoalWrapUp {
			return log[i].Detail
		}
	}
	return ""
}

// goalCardState classifies one goal card for the conductor. lastTouch is
// the seq of the newest goal log entry that acted on the card.
func (e *Engine) goalCardState(ctx context.Context, c domain.Feature, marks state.CardMarks, open []state.OpenDecision, lastTouch int64, touchedAt, now time.Time) (goalpolicy.CardState, string) {
	if c.GoalDropped() {
		return goalpolicy.Dropped, ""
	}
	if c.Stage == domain.StageDone {
		if c.HandedOff() {
			return goalpolicy.Dropped, "handed off instead of landing on the goal"
		}
		return goalpolicy.Landed, ""
	}
	if s := e.Get(c.ID); s != nil {
		switch s.State() {
		case StateRunning, StateQueued:
			return goalpolicy.Running, ""
		case StateInteractive:
			// An interactive session is a conversation, and a conversation
			// nobody is having is not a card that is working. A process
			// killed mid-turn leaves its session persisted, and the next
			// gummi rehydrates it exactly as it was — interactive — so a
			// goal read the card as running and never acted on it again:
			// no lead turn, no drop, no stall, no exit, across every
			// resume, because each resume restores the same snapshot.
			// CardIsLive asks the question the log cannot answer — is any
			// process driving this card right now — and its pid check is
			// what a killed process fails.
			if e.cfg.Workspace.Root == "" || state.CardIsLive(e.cfg.Workspace, c.ID) {
				return goalpolicy.Running, ""
			}
		}
	}
	// A card between sessions may still be working: check discovery and its
	// baseline hold a feature, not a Session, so e.Get sees nothing for the
	// minutes they run. That is how this conductor came to declare a
	// perfectly healthy child card "stuck: stopped with nothing running"
	// 90 seconds into a discovery pass that takes three times that on a
	// repo of any size — and then spent a lead turn restarting it.
	if e.OneShotRunning(c.ID) {
		return goalpolicy.Running, ""
	}
	for _, d := range open {
		if d.Kind == state.DecisionKindBudget {
			return goalpolicy.Exhausted, d.Question
		}
	}
	// verified wins over a park: the board parks a card at its landing gate
	// the moment verify passes, and for a goal card that gate is the goal's
	if c.Stage == domain.StageVerify && !c.VerifiedAt.IsZero() &&
		(marks.StageEnter.Stage != domain.StageVerify || !c.VerifiedAt.Before(marks.StageEnter.At)) {
		return goalpolicy.Verified, ""
	}
	reason, detail := marks.ParkReason()
	if marks.Park.Seq > max(marks.StageEnter.Seq, marks.Gate.Seq, lastTouch) {
		if reason == state.ParkReasonQuit {
			// a quit stopped it; nothing resumes itself after a quit without
			// being asked, so the goal waits for that resume
			return goalpolicy.Running, ""
		}
		if reason == state.ParkReasonBlocked {
			// the environment could not run its verification plan: no
			// verdict on the work, so nothing to give up on
			if detail == "" {
				detail = "the environment cannot run its verification plan"
			}
			return goalpolicy.Blocked, detail
		}
		if detail == "" {
			detail = "stopped at a decision"
		}
		return goalpolicy.Stuck, detail
	}
	if c.Stage == domain.StageTodo {
		if touchedAt.IsZero() {
			return goalpolicy.Waiting, ""
		}
		// told to start and not started yet: the driving loop has it
		if now.Sub(touchedAt) > goalIdleGrace {
			return goalpolicy.Stuck, "was started but never began its plan"
		}
		return goalpolicy.Running, ""
	}
	// started, nothing running, not parked, not verified: between two steps
	// of the driving loop — or dropped on the floor. Past the grace it is
	// restarted rather than waited on forever.
	last := marks.StageEnter.At
	if marks.Gate.At.After(last) {
		last = marks.Gate.At
	}
	if touchedAt.After(last) {
		last = touchedAt
	}
	// a card that just ended a turn is between steps however long ago it
	// entered its stage: the grace runs from its newest event
	if marks.Last.At.After(last) {
		last = marks.Last.At
	}
	if !last.IsZero() && now.Sub(last) > goalIdleGrace {
		return goalpolicy.Stuck, "stopped with nothing running"
	}
	return goalpolicy.Running, ""
}

// citedSubstrates lists the substrates a card's verification plan cites
// with [env: <name>] — what a card blocked at verify is, most likely,
// waiting for.
func (e *Engine) citedSubstrates(f *domain.Feature) []string {
	path := e.artifactFile(f)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m, err := e.Substrates()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range spec.EnvTags(string(raw), f.Kind) {
		if m.Has(name) {
			out = append(out, name)
		}
	}
	return out
}

// substratesReady reports that every named substrate is ready now. None
// named is not "ready": a card that cites no substrate is waiting for
// something gummi cannot probe, and only a person knows when that changed.
func (e *Engine) substratesReady(ctx context.Context, names []string) bool {
	if len(names) == 0 {
		return false
	}
	m, err := e.Substrates()
	if err != nil {
		return false
	}
	for _, name := range names {
		if st, err := e.substrateStatus(ctx, m, name); err != nil || st.State != substrate.Ready {
			return false
		}
	}
	return true
}

// openReviewerFindings counts the reviewer findings still open in a card's
// artifact.
func (e *Engine) openReviewerFindings(f *domain.Feature) int {
	path := e.artifactFile(f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range spec.Parse(string(raw)).OpenQuestions() {
		for _, m := range t.Markers {
			if m.Author == "reviewer" && !m.Resolved {
				n++
				break
			}
		}
	}
	return n
}

// --- tick ----------------------------------------------------------------

// typicalCardCredits is what a card has actually cost in this workspace:
// the median spend of completed cards, padded, or 0 when there is no
// history to learn from. It is the same signal plan-time estimation uses
// for an ordinary card (DESIGN §5.1), read here for the goal rows whose
// plan gave no estimate of their own.
func (e *Engine) typicalCardCredits(ctx context.Context) int {
	feats, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		return 0
	}
	var hist []domain.Spend
	for _, x := range feats {
		if x.Stage == domain.StageDone && !x.IsGoal() {
			hist = append(hist, x.Spend)
		}
	}
	env, n := domain.EstimateEnvelope(hist)
	if n == 0 || env <= 0 {
		return 0
	}
	return int(env)
}

// sizeUnestimatedRows fills in the rows whose plan gave no envelope with
// what a card typically costs here, in place.
//
// StartEnvelopes gives a card min(estimate, an even share of the pool),
// and leaves the difference ungiven where the raise machinery can spend
// it on evidence. A row with no estimate has nothing to take the lesser
// of, so it takes the share — and on a one-card goal the share is the
// whole pool. A goal with a 6,000-credit envelope minted its single card
// at 4,569 and the card spent 115: 76% of the budget committed before it
// ran a turn, and the card told it had forty times what the work costs.
//
// That is the arithmetic StartEnvelopes was written to prevent, in the
// case its fix does not reach — the estimate being ABSENT rather than
// wrong. An absent estimate is not a claim that the work is unbounded,
// so it is read as the ordinary claim instead: this card costs about
// what cards here cost.
//
// With no history there is nothing to read and the rows keep their even
// share, which is the old behaviour. A workspace that has never finished
// a card cannot be told what a card costs, and guessing a constant at it
// would be the invented number this avoids.
func sizeUnestimatedRows(want []int, typical int) {
	if typical <= 0 {
		return
	}
	for i, w := range want {
		if w <= 0 {
			want[i] = typical
		}
	}
}

// SetStageTimeout tells the engine how long its caller lets a stage go
// silent before cutting it off. The engine does not enforce it — the
// driver does — but a goal's quiet backstop has to stay clear of it:
// MaxQuiet is longer than the DEFAULT stage timeout on purpose, and a
// caller that raised the timeout past it would otherwise have its
// goals stopped inside a window it explicitly allowed.
//
// A setter rather than a Config field because the caller that knows
// the timeout is the driver, which is handed an engine somebody else
// built (SetExperimentSpawner is here for the same reason).
func (e *Engine) SetStageTimeout(d time.Duration) { e.cfg.StageTimeout = d }

// GoalStart is a card the driving loop must start, with the note to start
// it with (a send-back's reason), empty for a plain start.
type GoalStart struct {
	ID   domain.FeatureID
	Note string
}

// GoalTickResult reports what one tick did.
type GoalTickResult struct {
	Goal    domain.Feature
	Actions []goalpolicy.Action
	// Start lists the cards the driving loop must start now.
	Start []GoalStart
	// Again reports that the tick changed something another tick may act
	// on straight away (a landing unblocks dependents).
	Again bool
	// Finished reports that the goal's work settled and its review started.
	Finished bool
	// NeedsBudget is set when the tick stopped the goal on a card it
	// cannot fund: the driving loop reports it to whoever is listening
	// rather than carrying on.
	NeedsBudget GoalNeedsBudget
	// Stalled carries the backend's own words when the tick stopped
	// because the agent backend could not serve the goal at all. Nothing
	// was dropped: the driving loop reports it and stops, and picking the
	// goal back up once the backend is available carries on.
	Stalled string
	// StalledOn names the card whose environment the goal is waiting for,
	// when that and not the backend is what Stalled is about.
	StalledOn domain.FeatureID
	// StalledExperiment names the experiment whose evidence the goal has
	// stopped being able to believe, when that is what Stalled is about.
	StalledExperiment string
	// NeedsSubstrate is set when the tick stopped the goal on a run it
	// cannot afford.
	NeedsSubstrate GoalNeedsSubstrate
	// NeedsOwner is the goal's standing question for its owner, set on
	// every tick while it stands: a board says so at once, because it is
	// the one thing a running goal has to say before it is ready. OwnerStop
	// reports that nothing else can move and the goal has stopped on it.
	NeedsOwner GoalNeedsOwner
	OwnerStop  bool
}

// GoalNeedsSubstrate is a goal's standing request for more substrate
// budget: the run it cannot afford, and the sentence saying why.
type GoalNeedsSubstrate struct {
	Experiment string `json:"experiment,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Waiting reports whether the goal is stopped on a substrate question.
func (n GoalNeedsSubstrate) Waiting() bool { return n.Experiment != "" }

// needsSubstrateFrom derives the standing request from the log: the newest
// one not answered by a raise of the substrate budget since.
func needsSubstrateFrom(log []state.GoalEntry) GoalNeedsSubstrate {
	var out GoalNeedsSubstrate
	for _, en := range log {
		switch en.Action {
		case state.GoalNeedSubstrate:
			out = GoalNeedsSubstrate{Experiment: en.Item, Reason: en.Detail}
		case state.GoalSubstrateBudget:
			out = GoalNeedsSubstrate{}
		}
	}
	return out
}

// substrateBudgetFrom reads the goal's substrate ledger: the ceilings from
// the newest budget entry in its log, and what has been spent from the runs
// on disk. Spend is derived, never stored — a run that another gummi made,
// or that finished while nobody was looking, counts the moment it is read.
func substrateBudgetFrom(log []state.GoalEntry, runs []experiment.Result, now time.Time) goalpolicy.SubstrateBudget {
	var b goalpolicy.SubstrateBudget
	for _, en := range log {
		if en.Action == state.GoalSubstrateBudget {
			b.Runs, b.Minutes = en.To, en.Minutes
		}
	}
	var finished int
	var finishedMinutes float64
	for _, r := range runs {
		switch {
		case r.State == experiment.StateRunning:
			b.RunsSpent++
			b.MinutesSpent += now.Sub(r.Started).Minutes()
		case r.Outcome == experiment.NotRun:
			// it never took the substrate
		default:
			b.RunsSpent++
			b.MinutesSpent += r.Seconds / 60
			finished++
			finishedMinutes += r.Seconds / 60
		}
	}
	if finished > 0 {
		b.TypicalMinutes = finishedMinutes / float64(finished)
	}
	return b
}

// GoalNeedsBudget is a goal's standing request for more budget: which
// card is waiting, what it asks for, and the sentence saying why.
type GoalNeedsBudget struct {
	Card   domain.FeatureID `json:"card,omitempty"`
	Needs  int              `json:"needs,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

// Waiting reports whether the goal is stopped on a budget question.
func (n GoalNeedsBudget) Waiting() bool { return n.Card != "" }

// GoalTick conducts a goal one step: it reads the goal, decides, and
// executes. It is safe to call often and from any event; a goal that is
// not at implement, or whose own review is running, does nothing.
func (e *Engine) GoalTick(ctx context.Context, goalID domain.FeatureID) (GoalTickResult, error) {
	mu := e.goalLock(goalID)
	mu.Lock()
	defer mu.Unlock()

	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return GoalTickResult{}, err
	}
	res := GoalTickResult{Goal: goal}
	if !goal.IsGoal() {
		return res, fmt.Errorf("%s is not a goal", goalID)
	}
	if goal.Stage != domain.StageImplement {
		return res, nil
	}
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return res, err
	}
	acts := goalpolicy.Decide(view.Input)
	res.Actions, res.NeedsOwner = acts, view.NeedsOwner
	for _, a := range acts {
		if err := e.goalExecute(ctx, view, a, &res); err != nil {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadNote, Card: a.Card, By: ActorGoal,
				Detail: fmt.Sprintf("%s failed: %v", a.Kind, err)})
			return res, err
		}
	}
	if len(acts) > 0 {
		e.send(Event{Feature: goal.ID, Stage: goal.Stage, Kind: EventUpdated})
	}
	return res, nil
}

func (e *Engine) goalExecute(ctx context.Context, view GoalView, a goalpolicy.Action, res *GoalTickResult) error {
	goal := view.Goal
	switch a.Kind {
	case goalpolicy.WrapUp:
		return e.goalWrapUp(ctx, goal.ID, a.Reason, ActorGoal)
	case goalpolicy.Drop:
		gc, _ := view.Card(a.Card)
		res.Again = true
		return e.goalDrop(ctx, goal, gc.Feature, a.Reason, ActorGoal)
	case goalpolicy.Land:
		gc, _ := view.Card(a.Card)
		if gc.Live && gc.LiveProof != goalpolicy.LivePassed {
			// honest reporting over optimistic: the card was meant to be
			// proven before it landed, and was not
			why := "the goal could not afford the run"
			switch gc.LiveProof {
			case goalpolicy.LiveFailed:
				why = "its run failed and the lead left it: " + gc.LiveWhy
			case goalpolicy.LiveGaveUp:
				why = "its runs kept judging nothing: " + gc.LiveWhy
			}
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadNote, Card: a.Card, By: ActorGoal,
				Detail: fmt.Sprintf("%s lands without its proof on the substrate — %s", a.Card, why)})
		}
		starts, err := e.goalLand(ctx, goal, gc.Feature)
		res.Start = append(res.Start, starts...)
		res.Again = true
		return err
	case goalpolicy.Raise:
		gc, _ := view.Card(a.Card)
		if err := e.goalRaise(ctx, goal, gc, a.To, a.Reason, ActorGoal); err != nil {
			return err
		}
		res.Start = append(res.Start, GoalStart{ID: a.Card})
		return nil
	case goalpolicy.Shrink:
		// The step between "the ledger went negative" and "drop a card":
		// a card that has not started holds its whole allocation against
		// the goal, so handing part of it back can be the difference
		// between the work happening and the goal coming back partial
		// with credits unspent. It does not start the card — the normal
		// Start action does that on this or a later tick.
		gc, _ := view.Card(a.Card)
		if err := e.goalShrink(ctx, goal, gc, a.To, a.Reason, ActorGoal); err != nil {
			return err
		}
		return nil
	case goalpolicy.Stall:
		e.goalStall(ctx, goal.ID, a.Card, a.Experiment, a.Reason)
		res.Stalled, res.StalledOn, res.StalledExperiment = a.Reason, a.Card, a.Experiment
		return nil
	case goalpolicy.CloseTranche:
		for _, t := range view.Tranches {
			if t.Title == a.Reason && !t.Closed {
				back := max(0, float64(t.Envelope)-t.Given)
				e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalTrancheClosed, Ref: t.Title, To: int(back), By: ActorGoal,
					Detail: fmt.Sprintf("%d card(s) created from it; %.0f credits return to the goal", len(t.Cards), back)})
			}
		}
		res.Again = true
		return nil
	case goalpolicy.NeedOwner:
		res.OwnerStop = true
		return nil
	case goalpolicy.NeedSubstrate:
		res.NeedsSubstrate = GoalNeedsSubstrate{Experiment: a.Experiment, Reason: a.Reason}
		if last := needsSubstrateFrom(view.Log); last.Experiment != a.Experiment {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalNeedSubstrate, Item: a.Experiment, Detail: a.Reason, By: ActorGoal})
			e.send(Event{Feature: goal.ID, Stage: domain.StageImplement, Kind: EventGoal})
		}
		return nil
	case goalpolicy.Run:
		start := ExperimentStart{Name: a.Experiment, Purpose: a.Reason, Control: e.needsControl(goal.ID, a.Experiment)}
		switch a.Reason {
		case PurposeNegativeControl:
			start.Trunk, start.Control = true, false
		case PurposeBisect:
			for _, x := range view.Experiments {
				if x.Name == a.Experiment && a.Landing >= 0 && a.Landing < len(x.Suspects) {
					start.Heads = x.headsAfter(a.Landing)
				}
			}
			if start.Heads == nil {
				return nil // the snapshot moved on; the next tick decides again
			}
		case PurposeCard:
			gc, ok := view.Card(a.Card)
			if !ok || gc.LiveHead == "" {
				return nil
			}
			start.Card, start.Heads = a.Card, map[string]string{gc.Feature.Repo: gc.LiveHead}
		}
		run, err := e.StartExperiment(ctx, goal, start)
		if err != nil {
			// not the conductor's failure to pass up: the run is on record
			// as not run, and the next tick reads that like any other
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadNote, By: ActorGoal,
				Detail: fmt.Sprintf("a %s run of %s could not start: %v", a.Reason, a.Experiment, err)})
			return nil
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalRun, Card: a.Card, Ref: run.ID, Item: a.Experiment, By: ActorGoal,
			Detail: fmt.Sprintf("%s run of %s on %s", run.Purpose, a.Experiment, describeHeads(run.Heads))})
		return nil
	case goalpolicy.Lead:
		starts, err := e.runLeadTurn(ctx, view, a.Reasons)
		res.Start = append(res.Start, starts...)
		res.Again = true
		if err != nil {
			e.logLeadFailure(ctx, goal.ID, "", err)
		}
		return nil
	case goalpolicy.Start:
		gc, _ := view.Card(a.Card)
		note := ""
		switch gc.State {
		case goalpolicy.Stuck:
			note = "The goal restarted this card after it stopped: " + gc.Reason
		case goalpolicy.Blocked:
			note = "This card's verify could not run in the environment it had (" + gc.Reason +
				"). Someone has been at the goal since, so try the verification plan again; if it still cannot run, say so with VERDICT: blocked."
		}
		started := state.GoalPayload{Action: state.GoalStarted, Card: a.Card, By: ActorGoal}
		if gc.State == goalpolicy.Blocked {
			started.Ref, started.Detail = goalRetryRef, "its verify is tried again"
		}
		e.goalLog(ctx, goal.ID, started)
		res.Start = append(res.Start, GoalStart{ID: a.Card, Note: note})
		return nil
	case goalpolicy.Finish:
		res.Finished = true
		return e.goalFinish(ctx, goal, a.Reason)
	case goalpolicy.NeedBudget:
		res.NeedsBudget = GoalNeedsBudget{Card: a.Card, Needs: a.To, Reason: a.Reason}
		return e.goalNeedBudget(ctx, goal, a.Card, a.To, a.Reason)
	}
	return nil
}

// RaiseGoalSubstrate raises a goal's substrate budget. Like its envelope,
// only a person does: neither ceiling is one an agent can move. A zero
// leaves that dimension as it is; a value below the present one is refused,
// because lowering a ceiling under a goal that has already spent past it is
// a way to stop a goal that has a verb of its own.
func (e *Engine) RaiseGoalSubstrate(ctx context.Context, goalID domain.FeatureID, runs, minutes int) error {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return err
	}
	if !goal.IsGoal() {
		return fmt.Errorf("%s is not a goal", goalID)
	}
	cur := substrateBudgetFrom(mustGoalLog(ctx, e, goalID), nil, e.now())
	if runs == 0 {
		runs = cur.Runs
	}
	if minutes == 0 {
		minutes = cur.Minutes
	}
	if runs < cur.Runs || minutes < cur.Minutes {
		return fmt.Errorf("%s's substrate budget is %d runs and %d minutes; a raise does not lower it", goalID, cur.Runs, cur.Minutes)
	}
	if runs == cur.Runs && minutes == cur.Minutes {
		return nil
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalSubstrateBudget, From: cur.Runs, To: runs, Minutes: minutes, By: "user",
		Detail: fmt.Sprintf("raised from %d runs / %d minutes", cur.Runs, cur.Minutes)})
	e.send(Event{Feature: goalID, Stage: goal.Stage, Kind: EventGoal})
	return nil
}

func mustGoalLog(ctx context.Context, e *Engine, id domain.FeatureID) []state.GoalEntry {
	log, _ := e.cfg.Store.GoalLog(ctx, id)
	return log
}

// trancheRef marks the log entry of a card created from a tranche.
const trancheRef = "tranche:"

// goalTranches reads the plan's tbd rows against the log: what each holds,
// what it has given to cards, whether what it waited for has settled.
func goalTranches(rows []domain.GoalCardRow, log []state.GoalEntry, cards []GoalCard, landedAt, droppedAt map[domain.FeatureID]time.Time) []GoalTranche {
	byTitle := map[string]domain.FeatureID{}
	for _, r := range rows {
		if r.ID != "" {
			byTitle[strings.ToLower(strings.TrimSpace(r.Title))] = r.ID
			byTitle[strings.ToLower(string(r.ID))] = r.ID
		}
	}
	stateOf := map[domain.FeatureID]goalpolicy.CardState{}
	for _, c := range cards {
		stateOf[c.Feature.ID] = c.State
	}
	var out []GoalTranche
	for _, r := range rows {
		if !r.IsTBD() {
			continue
		}
		t := GoalTranche{Title: r.Title, Serves: r.Serves, Envelope: r.Envelope, Ready: true}
		opened := false
		for _, en := range log {
			switch {
			case en.Action == state.GoalTranche && en.Ref == r.Title:
				opened = true
			case en.Action == state.GoalTrancheClosed && en.Ref == r.Title:
				t.Closed = true
			case en.Action == state.GoalMinted && en.Ref == trancheRef+r.Title:
				t.Given += float64(en.To)
				t.Cards = append(t.Cards, en.Card)
			}
		}
		if !opened {
			continue // the plan has not been approved yet
		}
		for _, d := range r.DependsOn {
			id := byTitle[strings.ToLower(strings.TrimSpace(d))]
			switch stateOf[id] {
			case goalpolicy.Landed:
				if at := landedAt[id]; at.After(t.SettledAt) {
					t.SettledAt = at
				}
			case goalpolicy.Dropped:
				if at := droppedAt[id]; at.After(t.SettledAt) {
					t.SettledAt = at
				}
			default:
				t.Ready = false
			}
		}
		out = append(out, t)
	}
	return out
}

// servesOnly reports that every item a card serves is item: the card has no
// other reason to go on while item is in question.
func servesOnly(serves []string, item string) bool {
	if len(serves) == 0 || item == "" {
		return false
	}
	for _, s := range serves {
		if s != item {
			return false
		}
	}
	return true
}

// goalNeedBudget stops the goal on a card it cannot fund and records what
// it needs, so the one thing left is a person's answer.
//
// It is the ending that used to be a drop. A goal that hit its ceiling
// dropped whichever card had exhausted — reliably the most ambitious one,
// since that is the card that runs out first — and carried on to review
// itself against work it had just defunded. Which work to abandon when
// the money runs out is not the goal's call: the envelope is a hard
// ceiling only a person raises, so the goal says what it has spent, what
// the card needs, and stops. Nothing is dropped, the card keeps its
// branch and its spend, and a top-up continues it.
func (e *Engine) goalNeedBudget(ctx context.Context, goal domain.Feature, card domain.FeatureID, needs int, reason string) error {
	if last := e.goalNeedsBudget(ctx, goal.ID); last.Card == card && last.Needs == needs {
		return nil // already asked, and nothing has changed since
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalNeedBudget, Card: card, To: needs, Detail: reason, By: ActorGoal})
	e.send(Event{Feature: goal.ID, Stage: domain.StageImplement, Kind: EventGoal})
	return nil
}

// goalNeedsBudget reads the goal's standing request for more budget: the
// newest one not answered by a raise of the goal's envelope since. Zero
// when the goal is not waiting on money.
//
// Derived from the log rather than stored on the card, because it is a
// fact about a moment the log already records, and a raise answers it
// without anything having to remember to clear a flag.
func (e *Engine) goalNeedsBudget(ctx context.Context, goalID domain.FeatureID) GoalNeedsBudget {
	log, err := e.cfg.Store.GoalLog(ctx, goalID)
	if err != nil {
		return GoalNeedsBudget{}
	}
	return needsBudgetFrom(log)
}

// needsBudgetFrom is the derivation itself, over a log already read.
func needsBudgetFrom(log []state.GoalEntry) GoalNeedsBudget {
	var out GoalNeedsBudget
	for _, en := range log {
		switch en.Action {
		case state.GoalNeedBudget:
			out = GoalNeedsBudget{Card: en.Card, Needs: en.To, Reason: en.Detail}
		case state.GoalRaised:
			if en.Card == "" {
				// the goal's own envelope went up: the question is answered
				out = GoalNeedsBudget{}
			}
		}
	}
	return out
}

// goalLog appends to a goal's log, best-effort: the action it records has
// already happened.
func (e *Engine) goalLog(ctx context.Context, goal domain.FeatureID, p state.GoalPayload) state.GoalEntry {
	en, _ := e.cfg.Store.AppendGoalEvent(ctx, goal, p, e.now())
	return en
}

// --- executors -----------------------------------------------------------

func (e *Engine) goalWrapUp(ctx context.Context, goal domain.FeatureID, reason, by string) error {
	if err := e.cfg.Store.SetGoalWrapUp(ctx, goal, e.now()); err != nil {
		return err
	}
	e.goalLog(ctx, goal, state.GoalPayload{Action: state.GoalWrapUp, Detail: reason, By: by})
	return nil
}

// goalDrop drops a card from its goal. A card the goal created stays in
// the goal, stamped dropped; a card that was attached goes back to the
// open board with its work kept, moved back onto main when it has a
// branch.
func (e *Engine) goalDrop(ctx context.Context, goal, card domain.Feature, reason, by string) error {
	e.Drop(card.ID)
	if err := e.cfg.Store.SetGoalDropped(ctx, card.ID, e.now()); err != nil {
		return err
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDropped, Card: card.ID, Detail: reason, By: by})
	// The card's own thread, as well as the goal's log. The goal knew
	// exactly why it dropped the card — stuck and not recoverable, the
	// goal is wrapping up, a dependency was dropped — and the card never
	// learned it: its row wore one faint word and its page, which is
	// where someone reading the card actually is, said nothing at all.
	e.cardNote(ctx, card.ID, card.Stage, "dropped by "+goalName(goal.ID)+" — "+reason)
	if !card.GoalAttached {
		return e.goalCloseDropped(ctx, card)
	}
	// the goal is finished with it: better on the board with a branch to
	// rebase than held inside a goal that has ended
	return e.goalDetach(ctx, goal, card, false)
}

// goalCloseDropped ends a card the goal created and then dropped. It
// exists only for the goal, so nothing is left to do with it: it leaves
// the board's work in flight for done, its branch kept with whatever it
// had written, the way a hand-off keeps one. Left where it stood it
// would sit under a finished goal forever, counted as work in progress.
func (e *Engine) goalCloseDropped(ctx context.Context, card domain.Feature) error {
	if wt, err := e.mgr(ctx, &card); err == nil {
		if ok, _ := wt.Exists(ctx, &card); ok {
			// best effort: the branch keeps what the card had written
			_, _ = wt.CommitAll(ctx, &card, string(card.ID)+": dropped by its goal")
		}
	}
	return e.cfg.Store.CloseGoalDropped(ctx, card.ID, ActorGoal, e.now())
}

// goalDetach returns an attached card to the open board.
func (e *Engine) goalDetach(ctx context.Context, goal, card domain.Feature, mustMove bool) error {
	gm, gerr := e.pool.ManagerFor(ctx, &card)
	fork := ""
	if gerr == nil {
		fork, _ = gm.ForkPoint(ctx, &card)
	}
	// The branch moves home BEFORE the card leaves the goal, and with
	// mustMove a move that fails leaves the card where it was.
	//
	// A card in a goal forks from the goal branch, so its recorded fork
	// point is a commit that main has never seen. Clearing the goal first
	// and then letting the move fail produced a card on the open board
	// still anchored there — which every later drift check reads as main
	// having been rewritten under it, and which the board then refuses to
	// drive. The adoption looked like it had worked. Cards whose worktree
	// path is the same under either manager (both resolve it from the
	// workspace root) make the order free to choose.
	home := card
	home.GoalID, home.GoalAttached, home.GoalDroppedAt = "", false, time.Time{}
	detail := "back on the board with its work kept"
	moveErr := gerr
	if main, err := e.pool.ManagerForName(ctx, card.Repo); err == nil {
		moveErr = main.RebaseOnto(ctx, &home, fork)
	} else if moveErr == nil {
		moveErr = err
	}
	if moveErr != nil {
		if mustMove {
			return fmt.Errorf("%s stays in %s: its branch cannot move off the goal branch (%w) — land %s first, then take it back",
				card.ID, goal.ID, moveErr, goal.ID)
		}
		detail = "back on the board; moving its branch onto main failed (" + moveErr.Error() + ") — rebase it before working on it"
	}
	if err := e.cfg.Store.ClearGoal(ctx, card.ID); err != nil {
		return err
	}
	card = home
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDetached, Card: card.ID, Detail: detail, By: ActorGoal})
	// An attached card that is released reappears on the board mid-stage
	// with no session and nothing saying where it has been — and if the
	// rebase above failed, that too was recorded only in the goal's log,
	// which the card's reader has no reason to open. Say it on the card.
	e.cardNote(ctx, card.ID, card.Stage, "released by "+goalName(goal.ID)+" — "+detail)
	return nil
}

// goalRaise raises a card's envelope from the goal's not-yet-given pool,
// refusing anything past the goal budget.
func (e *Engine) goalRaise(ctx context.Context, goal domain.Feature, gc GoalCard, to int, reason, by string) error {
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return err
	}
	delta := float64(to - gc.Feature.Budget.Envelope)
	if delta <= 0 {
		return fmt.Errorf("%s: %d credits is not a raise over %d", gc.Feature.ID, to, gc.Feature.Budget.Envelope)
	}
	if delta > view.Ledger.Available {
		return fmt.Errorf("%s: raising to %d needs %.0f credits; the goal has %.0f left to give", gc.Feature.ID, to, delta, max(0, view.Ledger.Available))
	}
	if err := e.RaiseEnvelope(ctx, gc.Feature.ID, to); err != nil {
		return err
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalRaised, Card: gc.Feature.ID, From: gc.Feature.Budget.Envelope, To: to, Detail: reason, By: by})
	return nil
}

// goalShrink lowers a card's envelope and returns the difference to the
// goal's pool. Refused on a card that has spent anything: the ledger only
// ever over-counts a card that has not started, and a running card's
// envelope is a promise its session is already spending against.
func (e *Engine) goalShrink(ctx context.Context, goal domain.Feature, gc GoalCard, to int, reason, by string) error {
	from := gc.Feature.Budget.Envelope
	if to >= from {
		return fmt.Errorf("%s: %d credits is not a reduction from %d", gc.Feature.ID, to, from)
	}
	if gc.Feature.Spend.Credits > 0 {
		return fmt.Errorf("%s has already spent %.0f credits; its envelope is not the goal's to reclaim",
			gc.Feature.ID, gc.Feature.Spend.Credits)
	}
	if err := e.RaiseEnvelope(ctx, gc.Feature.ID, to); err != nil {
		return err
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalRaised, Card: gc.Feature.ID,
		From: from, To: to, Detail: reason, By: by})
	return nil
}

// goalBounce sends a card back to implement with a note and returns the
// start the driving loop owes it.
func (e *Engine) goalBounce(ctx context.Context, goal, card domain.Feature, note, by string) (GoalStart, error) {
	e.Drop(card.ID)
	if card.Stage == domain.StageVerify {
		if _, err := e.cfg.Store.Transition(ctx, card.ID, domain.StageImplement, by); err != nil {
			return GoalStart{}, err
		}
	}
	_ = e.cfg.Store.ClearVerifiedAt(ctx, card.ID)
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalBounced, Card: card.ID, Detail: note, By: by})
	return GoalStart{ID: card.ID, Note: note}, nil
}

// goalLand lands a verified card on the goal branch as one commit. The
// goal branch catches up with main first; a card that forked from an
// older goal-branch commit is rebased and its checks run again, so what
// lands is what was checked. A conflict, or a check that stops passing,
// sends the card back to implement with the reason.
func (e *Engine) goalLand(ctx context.Context, goal, card domain.Feature) ([]GoalStart, error) {
	if card.Kind == domain.KindResearch {
		adv, err := e.Advance(ctx, card.ID, ActorGoal)
		if err != nil {
			return nil, err
		}
		if adv.Status == StatusAdvanced && adv.To == domain.StageDone {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLanded, Card: card.ID, Detail: "research document accepted", By: ActorGoal})
			return nil, nil
		}
		return nil, e.goalParkCard(ctx, card, "its research document could not be accepted: "+describeAdvance(adv))
	}

	// The goal tree is what this card lands on, and a tracked file left
	// modified there refuses the merge. Put it back first: the landing is
	// the point where its state stops being nobody's business.
	if t, terr := e.pool.GoalTree(&goal, card.Repo); terr == nil && t.Exists() {
		e.tidyGoalTree(ctx, goal, t.Dir)
	}
	e.goalCatchUp(ctx, goal)

	gm, err := e.pool.ManagerFor(ctx, &card)
	if err != nil {
		return nil, err
	}
	if _, err := gm.CommitAll(ctx, &card, "final checkpoint"); err != nil && !errors.Is(err, worktree.ErrNoWorktree) {
		return nil, err
	}
	rebased, err := gm.RebasedOnBase(ctx, &card)
	if err != nil {
		return nil, err
	}
	if !rebased {
		head, _ := gm.MainHead(ctx)
		// A conflict here is the goal's own doing — another of its cards
		// landed while this one was being verified — so it resolves it
		// with the same bounded session its catch-up uses, and falls back
		// to the bounce only when that cannot. The card is verified
		// already; sending it back through a fresh implement, its
		// critique and its verify to replay a rebase was the expensive
		// answer to the frequent case.
		resolve := func(rctx context.Context, dir string, files []string) error {
			return e.resolveGoalRebase(rctx, goal, card, dir, files)
		}
		if err := gm.RebaseOnMainResolving(ctx, &card, resolve); err != nil {
			var rc *worktree.RebaseConflictError
			if errors.As(err, &rc) {
				st, berr := e.goalBounce(ctx, goal, card, fmt.Sprintf(
					"The goal branch moved while this card was being verified. Rebase this branch onto the goal branch (`git rebase %s`), resolve the conflicts in %s, and make sure the checks still pass.",
					head, strings.Join(rc.Files, ", ")), ActorGoal)
				return []GoalStart{st}, berr
			}
			return nil, err
		}
		if err := gm.ReanchorOnMain(ctx, &card); err != nil {
			return nil, err
		}
		rv, err := e.Reverify(ctx, card.ID, ActorGoal)
		if err != nil {
			return nil, err
		}
		if rv.Status == ReverifyFailed {
			st, berr := e.goalBounce(ctx, goal, card, fmt.Sprintf(
				"After rebasing onto the goal branch, these checks fail: %s. Fix them on this branch.", strings.Join(rv.Failed, ", ")), ActorGoal)
			return []GoalStart{st}, berr
		}
	}

	adv, err := e.Advance(ctx, card.ID, ActorGoal)
	if err != nil {
		return nil, err
	}
	switch adv.Status {
	case StatusNeedsMerge:
	case StatusAdvanced:
		// nothing of its own to land (no commits past the goal branch)
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLanded, Card: card.ID, Detail: "nothing to land — already on the goal branch", By: ActorGoal})
		return nil, nil
	default:
		return nil, e.goalParkCard(ctx, card, "it could not cross its landing gate: "+describeAdvance(adv))
	}

	msg := e.goalCardMessage(ctx, card)
	cur, _ := e.cfg.Store.GetFeature(ctx, card.ID)
	sha, err := gm.SquashMerge(ctx, &cur, msg)
	if err != nil {
		var mc *worktree.MergeConflictError
		if errors.As(err, &mc) {
			st, berr := e.goalBounce(ctx, goal, cur, fmt.Sprintf(
				"This card conflicts with the goal branch in %s. Rebase onto the goal branch, resolve them, and make sure the checks still pass.",
				strings.Join(mc.Files, ", ")), ActorGoal)
			return []GoalStart{st}, berr
		}
		return nil, err
	}
	if _, err := e.cfg.Store.Transition(ctx, card.ID, domain.StageDone, ActorGoal); err != nil {
		return nil, err
	}
	e.Drop(card.ID)
	subject, _, _ := strings.Cut(msg, "\n")
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLanded, Card: card.ID, Detail: shortSHA(sha) + " " + subject, Ref: sha, By: ActorGoal})
	return nil, nil
}

// logLeadFailure records a failed lead turn, marking the ones the lead
// is not responsible for. A backend that cannot serve the turn — a
// provider quota, a rate limit, an overload — says so in its own words,
// and those words mean "wait", not "this lead is broken".
func (e *Engine) logLeadFailure(ctx context.Context, goal domain.FeatureID, card domain.FeatureID, err error) {
	p := state.GoalPayload{Action: state.GoalLeadFailed, Card: card, Detail: err.Error(), By: ActorGoal}
	if words, out := agent.Unavailable(err); out {
		p.Outage, p.Detail = true, words
	}
	e.goalLog(ctx, goal, p)
}

// NoteBackendOutage records a turn of the goal that the backend could not
// serve, and reports whether that is what happened. A quota, a rate limit
// or an overload means "wait", and it means exactly the same thing whether
// the turn was the lead's or one of the goal's cards': an outage is not a
// verdict, and waiting is not abandoning (§17.4a).
//
// Without this a card's drive failing on a provider quota is recorded as
// the card being stuck, which costs it two lead turns and then the card
// itself — the goal drops work for something no retry of the WORK could
// have fixed, while the identical failure one turn over stalls the goal
// and keeps everything.
func (e *Engine) NoteBackendOutage(ctx context.Context, goal, card domain.FeatureID, err error) bool {
	if goal == "" || err == nil {
		return false
	}
	if _, out := agent.Unavailable(err); !out {
		return false
	}
	e.logLeadFailure(ctx, goal, card, err)
	return true
}

// goalStall records that the goal stopped because its agent backend
// could not serve it. Unlike a wrap-up it drops nothing: every card keeps
// its branch, its spend and its place, and the next tick after the
// backend returns carries on.
func (e *Engine) goalStall(ctx context.Context, goal, card domain.FeatureID, experimentName, reason string) {
	log, err := e.cfg.Store.GoalLog(ctx, goal)
	if err == nil {
		for i := len(log) - 1; i >= 0; i-- {
			switch log[i].Action {
			case state.GoalStalled:
				if log[i].Detail == reason {
					return // already said, and nothing has happened since
				}
			case state.GoalLeadTurn, state.GoalLanded, state.GoalStarted:
				i = 0 // something worked since the last stall: say it again
			}
		}
	}
	p := state.GoalPayload{Action: state.GoalStalled, Card: card, Detail: reason, By: ActorGoal}
	if experimentName != "" {
		p.Ref = "experiment:" + experimentName
	}
	e.goalLog(ctx, goal, p)
}

// GoalResumed records that a person picked a stopped goal back up. It is
// what tells the conductor a card waiting on its environment is worth
// another verify: retrying costs a session, so the goal never does it on
// a timer, only when someone has been there since the card stopped. A goal
// that was not waiting on an environment records nothing.
func (e *Engine) GoalResumed(ctx context.Context, goalID domain.FeatureID) error {
	log, err := e.cfg.Store.GoalLog(ctx, goalID)
	if err != nil {
		return err
	}
	for i := len(log) - 1; i >= 0; i-- {
		switch log[i].Action {
		case state.GoalResumed:
			return nil
		case state.GoalStalled:
			switch {
			case log[i].Card != "":
				e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalResumed, By: "user",
					Detail: "picked back up after waiting on " + string(log[i].Card) + "'s environment"})
			case log[i].Ref != "":
				e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalResumed, By: "user",
					Detail: "picked back up after " + strings.TrimPrefix(log[i].Ref, "experiment:") + " could not be believed"})
			}
			return nil
		}
	}
	return nil
}

// tidyGoalTree puts a goal tree's tracked files back the way its branch
// has them, and records it when there was anything to put back.
//
// gummi runs the repository's own commands in this tree — the baseline at
// plan approval, the goal's verify — and a suite that regenerates a
// tracked file (a golden, a generated doc, a snapshot of the machine it
// ran on) leaves that file modified. Nothing there is anyone's work, and
// a modified tracked file in the tree a card lands on refuses every
// landing after it, so the tree is left as the checks found it.
func (e *Engine) tidyGoalTree(ctx context.Context, goal domain.Feature, dir string) {
	restored, err := worktree.RestoreTracked(ctx, dir)
	switch {
	case err != nil && len(restored) == 0:
		return // an operation in progress, or a tree that cannot be read: leave it
	case err != nil:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalTidied, By: ActorGoal,
			Detail: "could not put the goal tree back after running the checks: " + err.Error()})
	case len(restored) > 0:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalTidied, By: ActorGoal,
			Detail: "running the checks changed " + strings.Join(restored, ", ") + " in the goal tree; restored"})
	}
}

// goalParkCard records that a goal card stopped at something the
// conductor could not do for it; the next tick reads it as stuck.
func (e *Engine) goalParkCard(ctx context.Context, card domain.Feature, detail string) error {
	return e.cfg.Store.AppendPark(ctx, card.ID, card.Stage, state.ParkReasonNeedsYou, detail, "", e.now())
}

func describeAdvance(adv AdvanceResult) string {
	switch adv.Status {
	case StatusBlockedQuestions:
		return fmt.Sprintf("%d open comment thread(s)", adv.Blockers)
	case StatusBlockedDiff:
		return fmt.Sprintf("%d open diff comment(s)", adv.Blockers)
	case StatusBlockedDependency:
		var ids []string
		for _, d := range adv.BlockingDeps {
			ids = append(ids, d.String())
		}
		return "unmet dependencies " + strings.Join(ids, ", ")
	case StatusBlockedUndrafted:
		return "undrafted " + strings.Join(adv.Undrafted, ", ")
	case StatusBlockedOmission:
		return adv.Reason
	case StatusBlockedDocument:
		return "the document floor failed"
	case StatusBlockedGoalPlan:
		return adv.Reason
	}
	return "status " + fmt.Sprint(adv.Status)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// goalCardMessage is the commit message a card lands on its goal branch
// with: the scribe's draft when it validates, a plain one built from the
// card otherwise. Nobody is at a form to fix a bad draft here, so a draft
// that fails validation is never used.
func (e *Engine) goalCardMessage(ctx context.Context, card domain.Feature) string {
	if msg, err := e.DraftCommitMessage(ctx, card); err == nil && ValidateCommitMessage(msg) == nil {
		return msg
	}
	typ := "feat"
	if card.Kind == domain.KindBug {
		typ = "fix"
	}
	title := strings.TrimSpace(card.Title)
	if title != "" {
		title = strings.ToLower(title[:1]) + title[1:]
	}
	return fmt.Sprintf("%s: %s\n\n%s", typ, title, strings.TrimSpace(card.OneLiner))
}

// goalCatchUp merges main into each of the goal's branches whose repo has
// moved — one repository at a time, since git has no merge that spans
// them. A conflict is handed to the resolver (resolveGoalCatchUp) in that
// repository's goal tree; when that cannot finish the merge, the catch-up
// is aborted and logged, and the goal carries on — landing still works on
// the older base, and the goal catches up again before it lands.
func (e *Engine) goalCatchUp(ctx context.Context, goal domain.Feature) {
	trees, err := e.goalTrees(ctx, goal)
	if err != nil {
		return
	}
	for _, t := range trees {
		e.goalCatchUpTree(ctx, goal, t)
	}
}

func (e *Engine) goalCatchUpTree(ctx context.Context, goal domain.Feature, t worktree.GoalTree) {
	if !t.Exists() {
		return
	}
	behind, err := t.Behind(ctx)
	if err != nil || !behind {
		return
	}
	merged, err := t.CatchUp(ctx, true)
	var cc *worktree.CatchUpConflictError
	switch {
	case err == nil && merged:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into " + t.Label(), By: ActorGoal})
	case errors.As(err, &cc):
		if rerr := e.resolveGoalCatchUp(ctx, goal, t, cc.Files); rerr == nil {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into " + t.Label() + ", resolving " + strings.Join(cc.Files, ", "), By: ActorGoal})
			return
		} else {
			_, _ = t.AbortMerge(ctx)
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCatchUpFail, Detail: fmt.Sprintf("%s: conflicts in %s: %v", t.Label(), strings.Join(cc.Files, ", "), rerr), By: ActorGoal})
		}
	case err != nil:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCatchUpFail, Detail: t.Label() + ": " + err.Error(), By: ActorGoal})
	}
}

// goalFinish settles the goal's work and starts its review of the combined
// branch — the implement stage's critique pass, which from here runs
// through the driving loop like any card's.
func (e *Engine) goalFinish(ctx context.Context, goal domain.Feature, reason string) error {
	// One read for both halves of the same fact: which items lost every
	// card serving them makes the goal partial, AND is what the review
	// must be told before it judges the branch.
	view, verr := e.goalView(ctx, goal)
	if !goal.Goal.WrappingUp() {
		// dropped cards make a goal partial only when an agreed item lost
		// every card that served it: a card the lead replaced left nothing
		// of the goal unmet
		if verr == nil {
			reason = goalPartialReason(view)
		} else {
			reason = ""
		}
	}
	if err := e.cfg.Store.SetGoalPartial(ctx, goal.ID, reason); err != nil {
		return err
	}
	detail := "all cards settled; reviewing the combined branch"
	if reason != "" {
		detail += " (partial: " + reason + ")"
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalFinished, Detail: detail, By: ActorGoal})
	cur, err := e.cfg.Store.GetFeature(ctx, goal.ID)
	if err != nil {
		return err
	}
	scope := ""
	if verr == nil {
		scope = goalReviewScope(view)
	}
	return e.RunCritique(cur, scope)
}

// goalReviewScope is what the goal's review is told before it judges the
// combined branch: which done-when items the goal's own decisions put out
// of reach, and which cards were dropped.
//
// Without it the review is asked a question whose answer the goal already
// knows. Its contract says to name every done-when item the diff does not
// meet and to make each one a blocking finding — so a goal that dropped a
// card (which is what running out of budget MEANS) is guaranteed a
// `changes` verdict naming work nothing can do. The rework goes to a
// conductor that has finished, the diff comes back unrevised, and the
// critique re-finds the same item until the round cap. Naming the settled
// scope up front costs one paragraph and removes the whole loop: the
// review then judges the work that was attempted, and `changes` goes back
// to meaning something the lead can act on.
func goalReviewScope(v GoalView) string {
	lost := goalLostItems(v)
	var dropped []GoalCard
	for _, c := range v.Cards {
		if c.State == goalpolicy.Dropped {
			dropped = append(dropped, c)
		}
	}
	if len(lost) == 0 && len(dropped) == 0 {
		return ""
	}
	says := map[string]string{}
	for _, d := range v.DoneWhen {
		says[d.ID] = d.Says
	}
	var b strings.Builder
	b.WriteString("What this goal settled before the review. These are decisions already taken, not findings for you to make.\n")
	if len(lost) > 0 {
		b.WriteString("\nOut of scope — every card serving these done-when items was dropped, so the combined branch cannot meet them:\n")
		for _, id := range lost {
			b.WriteString("  " + id)
			if s := says[id]; s != "" {
				b.WriteString(" — " + s)
			}
			b.WriteString("\n")
		}
		b.WriteString("Record each as not met with that reason. They are NOT blocking findings and must not set your verdict to changes: no card remains that could make the change.\n")
	}
	if len(dropped) > 0 {
		b.WriteString("\nDropped cards, whose work is not on the branch:\n")
		for _, c := range dropped {
			line := "  " + string(c.Feature.ID) + " — " + c.Feature.Title
			if c.Reason != "" {
				line += " (" + c.Reason + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\nJudge the work that was attempted: whether it meets the items still in scope, whether the cards fit together, and whether the Limits were respected.")
	return b.String()
}

// goalLostItems names the done-when items whose every serving card was
// dropped — the items the goal's own decisions put out of reach. It is
// the one derivation behind both what makes a goal partial and what its
// review is told is out of scope, so the report and the reviewer can
// never disagree about which items were given up on.
func goalLostItems(v GoalView) []string {
	alive := map[string]bool{}
	served := map[string]bool{}
	for _, c := range v.Cards {
		for _, s := range c.Serves {
			served[s] = true
			if c.State != goalpolicy.Dropped {
				alive[s] = true
			}
		}
	}
	var lost []string
	for _, d := range v.DoneWhen {
		if served[d.ID] && !alive[d.ID] {
			lost = append(lost, d.ID)
		}
	}
	return lost
}

// goalPartialReason is goalLostItems as the report's one-line reason, ""
// when each item still has a card that landed or is running.
func goalPartialReason(v GoalView) string {
	lost := goalLostItems(v)
	if len(lost) == 0 {
		return ""
	}
	return strings.Join(lost, ", ") + " lost every card serving it"
}

// --- plan gate and start ---------------------------------------------------

// goalLandOrderProblem asks a goal whose cards are in more than one
// repository to say which lands first.
//
// Git has no merge that spans repositories, so such a goal lands once in
// each and a landing can stop part way (§17.12). Without an agreed order
// the order is the home repository's, which is settled from where most of
// the cards are — a fact about card counts, not about what depends on
// what. On the drive that prompted this the home was the repository whose
// cards CALLED the other's new endpoints, so the default order would have
// put the caller on a trunk without the callee.
//
// The gate cannot know the dependency; the plan can, and this is the one
// place anybody agrees it.
func (e *Engine) goalLandOrderProblem(ctx context.Context, goal domain.Feature, doc string, rows []domain.GoalCardRow) string {
	_, order, err := spec.ParseGoalProgramme(doc)
	if err != nil || len(order) > 0 {
		return "" // a malformed block is goalProgrammeProblem's to report
	}
	repos := map[string]bool{}
	for _, r := range rows {
		if r.IsTBD() {
			continue
		}
		repo := r.Repo
		if r.ID != "" {
			c, cerr := e.cfg.Store.GetFeature(ctx, r.ID)
			if cerr != nil {
				continue
			}
			repo = c.Repo
		}
		if repo == "" {
			repo = goal.Repo
		}
		repos[repo] = true
	}
	if len(repos) < 2 {
		return ""
	}
	names := make([]string, 0, len(repos))
	for r := range repos {
		names = append(names, repoName(r))
	}
	sort.Strings(names)
	return fmt.Sprintf("this goal's cards are in %s and the plan does not say which lands first — "+
		"git has no merge that spans repositories, so it lands once in each and can stop part way. "+
		"Give the gummi-goal block `land_order: [%s]`, with whatever a repository's changes depend on ahead of them",
		strings.Join(names, " and "), strings.Join(names, ", "))
}

// goalPlanProblems is the goal plan gate: the goal doc must carry a
// done-when list gummi can check, a card list whose every row serves an
// item and every item is served, attachable ids, and a card list the goal
// budget can fund. It returns the first problem, "" when the plan is fit
// to start.
func (e *Engine) goalPlanProblems(ctx context.Context, goal domain.Feature) string {
	path := e.artifactFile(&goal)
	if path == "" {
		return "the goal doc is missing"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "the goal doc cannot be read: " + err.Error()
	}
	doc := string(raw)
	items, found, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return err.Error()
	}
	if !found || len(items) == 0 {
		return "the goal has no done-when items — agree at least one checkable statement"
	}
	for _, it := range items {
		// A check is a command and a command needs somewhere to run. The
		// item may name that repository; what it may not do is name one
		// this workspace does not manage.
		if it.Check != "" && it.Repo != "" {
			if problem := e.goalRepoProblem(it.Repo); problem != "" {
				return fmt.Sprintf("%s's check %s", it.ID, problem)
			}
		}
		// A substrate the workspace does not configure is one the check
		// could never be given, and the lease is what keeps two jobs off
		// the same machines: an unknown name would take nothing and run
		// anyway, which is the failure it exists to prevent.
		if name := strings.TrimSpace(it.Substrate); name != "" {
			m, serr := e.Substrates()
			switch {
			case serr != nil:
				return fmt.Sprintf("%s names the substrate %q and the config cannot be read: %v", it.ID, name, serr)
			case !m.Has(name):
				known := m.Names()
				if len(known) == 0 {
					return fmt.Sprintf("%s's check names the substrate %q and this workspace configures none — an operator adds it under `substrates:` in .gummi/config.yaml", it.ID, name)
				}
				return fmt.Sprintf("%s's check names the substrate %q, which is not configured — use one of %s", it.ID, name, strings.Join(known, ", "))
			}
		}
	}
	if problem := e.goalExperimentProblem(items); problem != "" {
		return problem
	}
	if problem := e.goalProgrammeProblem(ctx, goal, doc); problem != "" {
		return problem
	}
	budget, _, err := spec.ParseGoalSubstrate(doc)
	if err != nil {
		return err.Error()
	}
	if names := experimentNames(items); len(names) > 0 && !budget.Agreed() {
		return fmt.Sprintf("the goal is proved by %s and agrees no substrate budget — give the gummi-goal block `runs:` and/or `minutes:`, "+
			"what the goal may spend of the substrate as its envelope is what it may spend of credits", strings.Join(names, ", "))
	}
	rows, found, err := spec.ParseGoalCards(doc, items)
	if err != nil {
		return err.Error()
	}
	if !found || len(rows) == 0 {
		return "the goal has no cards — list the cards that will meet the done-when items"
	}
	served := map[string]bool{}
	for _, r := range rows {
		for _, s := range r.Serves {
			served[s] = true
		}
	}
	for _, it := range items {
		if !served[it.ID] {
			return fmt.Sprintf("%s is served by no card", it.ID)
		}
	}
	// A live row is a promise that this card is proven on the substrate
	// before it lands, and the only thing that can prove it is an
	// experiment one of its items names. A row that promises it with
	// nothing behind it is read as an ordinary card and nothing says so,
	// which is the one way a plan can be wrong about proof without
	// anybody finding out.
	byItem := map[string]string{}
	for _, it := range items {
		byItem[it.ID] = strings.TrimSpace(it.Experiment)
	}
	for _, r := range rows {
		if !r.Live {
			continue
		}
		proven := false
		for _, s := range r.Serves {
			if byItem[s] != "" {
				proven = true
			}
		}
		if !proven {
			return fmt.Sprintf("card %q is marked live, but no item it serves (%s) is proved by an experiment — "+
				"a live card is one proved on the substrate before it lands, so give one of those items an `experiment:`, or drop `live:`",
				r.Title, strings.Join(r.Serves, ", "))
		}
	}
	if _, err := spec.ParseGoalLanes(doc); err != nil {
		return err.Error()
	}
	var want []int
	tranches := 0
	for _, r := range rows {
		if r.IsTBD() {
			tranches += r.Envelope
			continue
		}
		if r.ID == "" {
			// A goal is not in a repository: each row says where its card
			// goes, and the only thing the gate asks is that the name is
			// one this workspace manages. A row that names none is fine
			// wherever there is a default repo to mean.
			if problem := e.goalRepoProblem(r.Repo); problem != "" {
				return fmt.Sprintf("card %q %s", r.Title, problem)
			}
			want = append(want, r.Envelope)
			continue
		}
		c, err := e.cfg.Store.GetFeature(ctx, r.ID)
		if err != nil {
			return fmt.Sprintf("card %s does not exist", r.ID)
		}
		switch {
		case c.IsGoal():
			return fmt.Sprintf("%s is a goal; goals do not nest", r.ID)
		case c.GoalID != "" && c.GoalID != goal.ID:
			return fmt.Sprintf("%s already belongs to %s", r.ID, c.GoalID)
		case c.Stage == domain.StageDone && c.GoalID != goal.ID:
			return fmt.Sprintf("%s is already done", r.ID)
		}
		// An attached card keeps its own repository, and the goal grows a
		// branch there rather than the card moving; a repository gummi
		// cannot resolve is still a tree it could not cut.
		if problem := e.goalRepoProblem(c.Repo); problem != "" {
			return fmt.Sprintf("%s %s", r.ID, problem)
		}
	}
	if problem := e.goalLandOrderProblem(ctx, goal, doc, rows); problem != "" {
		return problem
	}
	if goal.Budget.Envelope <= 0 {
		return "the goal has no budget"
	}
	pool := goal.GoalMintPool(goal.Spend.CreditEquivalent(), len(rows)) - float64(tranches)
	if tranches > 0 {
		startable := 0
		for _, r := range rows {
			if !r.IsTBD() {
				startable++
			}
		}
		if startable == 0 {
			return "every row is tbd — a goal needs at least one card that can start, and what a tbd row waits for is one of them"
		}
		if pool < 0 {
			return fmt.Sprintf("the tbd rows hold %d credits, more than the goal budget leaves for cards at all", tranches)
		}
	}
	sizeUnestimatedRows(want, e.typicalCardCredits(ctx))
	if _, err := goalpolicy.StartEnvelopes(want, pool); err != nil && len(want) > 0 {
		if tranches > 0 {
			return fmt.Sprintf("%v — after the %d credits its tbd rows hold", err, tranches)
		}
		return err.Error()
	}
	return ""
}

// startGoal is the goal's plan→implement crossing: it records the lanes,
// puts every done-when command into the goal doc's gummi-checks, mints the
// rows that are new and attaches the rows that name existing cards, adds
// their dependencies, writes the ids back into the doc, and hands the goal
// to autopilot — from here it runs itself until it is ready for you.
// Rows already carrying an id this goal owns are skipped, so a crossing
// that failed half-way is resumed rather than repeated.
func (e *Engine) startGoal(ctx context.Context, goal *domain.Feature) error {
	path := filepath.Join(e.pool.Root(), goal.ArtifactPath())
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc := string(raw)
	items, _, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return err
	}
	rows, _, err := spec.ParseGoalCards(doc, items)
	if err != nil {
		return err
	}
	lanes, err := spec.ParseGoalLanes(doc)
	if err != nil {
		return err
	}
	if lanes > 0 {
		if err := e.cfg.Store.SetGoalLanes(ctx, goal.ID, lanes); err != nil {
			return err
		}
	}
	// A goal that continues another starts from what that one came to know.
	// Usually this happened when the goal was created (`--after`), so that
	// its plan was agreed against it; a plan that only names the goal in its
	// doc gets it here, before the reference is pinned.
	if after := e.goalAfter(ctx, *goal, doc); after != "" {
		if err := e.ContinueGoal(ctx, goal.ID, after); err != nil {
			return err
		}
	}
	// The reference is the owner's from here on: what is in it now is what
	// the goal was agreed against, and a document that changes later is
	// reported as changed wherever it is listed.
	if err := e.goalNotebook(goal.ID).Pin(); err != nil {
		return err
	}
	if budget, _, berr := spec.ParseGoalSubstrate(doc); berr != nil {
		return berr
	} else if budget.Agreed() && !substrateBudgetFrom(mustGoalLog(ctx, e, goal.ID), nil, e.now()).Agreed() {
		// once: a crossing resumed half-way must not reset a budget a
		// person has raised since
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalSubstrateBudget, To: budget.Runs, Minutes: budget.Minutes, By: "user",
			Detail: "agreed with the plan"})
	}
	// Where the goal itself belongs is settled here, from the plan, before
	// a single card exists: the goal was minted into a provisional repo
	// (nobody was asked for one) and the plan is the first thing that
	// knows where its work actually is. Nothing has landed on the goal
	// branch yet, so moving it is dropping an empty branch.
	if err := e.settleGoalHome(ctx, goal, rows); err != nil {
		return err
	}
	// The home tree first — the goal's own branch, where its lead works,
	// its checks run and its review reads — then one for every repository
	// the plan names, since a card forks from its repository's goal
	// branch and cannot be minted before there is one.
	if _, err := e.goalTreeIn(ctx, *goal, goal.Repo); err != nil {
		return err
	}
	for _, r := range rows {
		if r.IsTBD() {
			continue // not a card yet: the cards it becomes cut their own trees
		}
		repo := r.Repo
		if r.ID != "" {
			c, cerr := e.cfg.Store.GetFeature(ctx, r.ID)
			if cerr != nil {
				return cerr
			}
			repo = c.Repo
			if c.Kind == domain.KindResearch {
				continue // research never cuts a branch
			}
		} else if ct, ok := r.EffectiveType(); ok && ct.Kind == domain.KindResearch {
			continue
		}
		if _, err := e.goalTreeIn(ctx, *goal, repo); err != nil {
			return err
		}
	}

	// done-when commands join the goal's checks, never baselined: each is
	// the goal's promise, so failing on the fresh branch is the point
	checks, _, _ := spec.ParseChecks(doc)
	have := map[string]bool{}
	for _, c := range checks {
		have[c.Name] = true
	}
	no := false
	for _, it := range items {
		if it.Check != "" && !have[it.CheckName()] {
			checks = append(checks, domain.Check{Name: it.CheckName(), Cmd: it.Check, Baseline: &no})
		}
	}
	if len(checks) > 0 {
		if doc, err = spec.UpsertChecks(doc, checks); err != nil {
			return err
		}
	}

	itemText := map[string]string{}
	for _, it := range items {
		itemText[it.ID] = it.Says
	}
	var want []int
	tranches := 0
	opened := map[string]bool{}
	for _, en := range mustGoalLog(ctx, e, goal.ID) {
		if en.Action == state.GoalTranche {
			opened[en.Ref] = true
		}
	}
	for _, r := range rows {
		switch {
		case r.IsTBD():
			tranches += r.Envelope
			if !opened[r.Title] { // a crossing resumed half-way opens it once
				e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalTranche, Ref: r.Title, To: r.Envelope, By: ActorGoal,
					Detail: fmt.Sprintf("held for cards nobody can name until %s has settled", strings.Join(r.DependsOn, ", "))})
			}
		case r.ID == "":
			want = append(want, r.Envelope)
		}
	}
	pool := goal.GoalMintPool(goal.Spend.CreditEquivalent(), len(rows)) - float64(tranches)
	sizeUnestimatedRows(want, e.typicalCardCredits(ctx))
	envs, err := goalpolicy.StartEnvelopes(want, pool)
	if err != nil && len(want) > 0 {
		return err
	}
	next := 0
	byTitle := map[string]domain.FeatureID{}
	for i := range rows {
		r := &rows[i]
		if r.IsTBD() {
			continue
		}
		if r.ID == "" {
			// Validate has already refused every row whose kind does not
			// resolve, so the discard here can only be the feature default.
			ct, _ := r.EffectiveType()
			f, merr := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
				Kind:         ct.Kind,
				Mode:         ct.Mode,
				Description:  goalCardDescription(*goal, *r, itemText),
				Profile:      goal.Profile,
				Envelope:     envs[next],
				Repo:         r.Repo,
				RequireRepo:  e.RequireRepo,
				GateApproval: domain.GateAutopilot,
				Goal:         goal.ID,
			})
			next++
			if merr != nil {
				return fmt.Errorf("minting %q: %w", r.Title, merr)
			}
			r.ID = f.ID
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalMinted, Card: f.ID, Detail: f.Title, To: f.Budget.Envelope, By: ActorGoal})
		} else if err := e.goalAttach(ctx, *goal, r.ID, r.Envelope); err != nil {
			return err
		}
		byTitle[strings.ToLower(r.Title)] = r.ID
		byTitle[strings.ToLower(string(r.ID))] = r.ID
		// write ids back as we go: a failure further down resumes here
		if doc, err = spec.SetGoalCards(doc, rows); err != nil {
			return err
		}
		if err := atomicfile.Write(path, []byte(doc), 0o600); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if r.IsTBD() {
			continue // what a tbd row waits for is read from the plan, not the dependency table
		}
		for _, d := range r.DependsOn {
			dep := byTitle[strings.ToLower(strings.TrimSpace(d))]
			if dep == "" || dep == r.ID {
				continue
			}
			if err := e.cfg.Store.AddDependency(ctx, r.ID, dep); err != nil && !errors.Is(err, state.ErrLateAttachment) {
				return fmt.Errorf("%s depends on %s: %w", r.ID, dep, err)
			}
		}
	}
	if err := atomicfile.Write(path, []byte(doc), 0o600); err != nil {
		return err
	}
	if err := e.cfg.Store.SetGateApproval(ctx, goal.ID, domain.GateAutopilot); err != nil {
		return err
	}
	goal.GateApproval = domain.GateAutopilot
	e.Repool(goal.ID, domain.GateAutopilot)
	return nil
}

// goalCardDescription is what a minted goal card is created from: its
// title, its one-liner, and which of the goal's done-when items it serves,
// so its own plan starts from why it exists.
func goalCardDescription(goal domain.Feature, r domain.GoalCardRow, items map[string]string) string {
	var b strings.Builder
	b.WriteString(r.Title)
	b.WriteString("\n\n")
	if r.OneLiner != "" {
		b.WriteString(r.OneLiner + "\n\n")
	}
	fmt.Fprintf(&b, "This card is part of goal %s (%s). It serves:\n", goal.ID, goal.Title)
	for _, s := range r.Serves {
		fmt.Fprintf(&b, "- %s: %s\n", s, items[s])
	}
	return b.String()
}

// goalAttach hands an existing card to the goal: it joins the goal,
// runs on autopilot, and — when it already has a branch — moves its own
// commits onto the goal branch. What it spent before counts for nothing
// against the goal; envelope, when set, raises its envelope to that much
// of goal budget on top.
func (e *Engine) goalAttach(ctx context.Context, goal domain.Feature, id domain.FeatureID, envelope int) error {
	c, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if c.GoalID == goal.ID {
		return nil // attached on an earlier, half-finished crossing
	}
	before := int(c.Spend.CreditEquivalent())
	main, err := e.pool.ManagerFor(ctx, &c)
	if err != nil {
		return err
	}
	// An attached card keeps its repository and the goal grows a branch
	// there, rather than the card moving to the goal's — a card's work is
	// in the checkout it was written in, and no rebase moves it between
	// repositories.
	if c.Kind != domain.KindResearch {
		if _, terr := e.goalTreeIn(ctx, goal, c.Repo); terr != nil {
			return terr
		}
	}
	fork, _ := main.ForkPoint(ctx, &c)
	hasTree, _ := main.Exists(ctx, &c)
	if err := e.cfg.Store.SetGoal(ctx, c.ID, goal.ID, true); err != nil {
		return err
	}
	c.GoalID, c.GoalAttached = goal.ID, true
	if hasTree {
		e.Drop(c.ID)
		gm, err := e.pool.ManagerFor(ctx, &c)
		if err != nil {
			return err
		}
		if err := gm.RebaseOnto(ctx, &c, fork); err != nil {
			_ = e.cfg.Store.ClearGoal(ctx, c.ID)
			return fmt.Errorf("moving %s onto the goal branch: %w", c.ID, err)
		}
	}
	if err := e.cfg.Store.SetGateApproval(ctx, c.ID, domain.GateAutopilot); err != nil {
		return err
	}
	e.Repool(c.ID, domain.GateAutopilot)
	if envelope > 0 {
		c.Budget.Envelope = before + envelope
		if err := e.cfg.Store.UpdateFeature(ctx, &c); err != nil {
			return err
		}
	} else if c.Budget.Envelope-before < domain.MinEnvelope {
		c.Budget.Envelope = before + domain.MinEnvelope
		if err := e.cfg.Store.UpdateFeature(ctx, &c); err != nil {
			return err
		}
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalAttached, Card: c.ID, Detail: c.Title, From: before, By: ActorGoal})
	return nil
}

// --- your side of a running goal ------------------------------------------

// GoalNote adds one of your notes to a goal: into the goal doc's Notes and
// the goal's log, where the lead's next turn reads it. The goal stays
// silent.
func (e *Engine) GoalNote(ctx context.Context, goalID domain.FeatureID, note string) error {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return err
	}
	if !goal.IsGoal() {
		return fmt.Errorf("%s is not a goal", goalID)
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return errors.New("an empty note says nothing")
	}
	if path := e.artifactFile(&goal); path != "" {
		if raw, rerr := os.ReadFile(path); rerr == nil {
			if doc, nerr := spec.AppendGoalNote(string(raw), note, e.now().Local().Format("2006-01-02 15:04")); nerr == nil {
				_ = atomicfile.Write(path, []byte(doc), 0o600)
			}
		}
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalNote, Detail: note, By: "user"})
	e.send(Event{Feature: goalID, Stage: goal.Stage, Kind: EventGoal})
	return nil
}

// StopGoal tells a running goal to finish now: nothing new starts,
// verified work lands, the rest is dropped, and it comes back partial.
func (e *Engine) StopGoal(ctx context.Context, goalID domain.FeatureID) error {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return err
	}
	if !goal.IsGoal() {
		return fmt.Errorf("%s is not a goal", goalID)
	}
	if goal.Stage != domain.StageImplement {
		return fmt.Errorf("%s is at %s; only a goal whose cards are running can be stopped", goalID, goal.Stage)
	}
	if err := e.goalWrapUp(ctx, goalID, "you stopped the goal", "user"); err != nil {
		return err
	}
	// A finished session of the goal's own — its review, left registered
	// so a reader can still read it — is what goalView reports as
	// Reviewing, and Decide returns no actions at all while that is true.
	// Stamping a wrap-up behind one recorded an intention nothing would
	// carry out: no verified card landed, nothing was dropped, and the
	// goal never came back partial, while the board said "wrapping up".
	// The transcript is persisted, so dropping the finished session costs
	// the reader nothing and gives the wrap-up a conductor to run it.
	if s := e.Get(goalID); s != nil && !s.Live() {
		e.Drop(goalID)
	}
	e.send(Event{Feature: goalID, Stage: goal.Stage, Kind: EventGoal})
	return nil
}

// SendBackGoal returns a goal that is ready for you to its cards, with
// your notes: the lead reads them and works on the same branch with what
// is left of the budget. A wrap-up is lifted, so work can start again.
func (e *Engine) SendBackGoal(ctx context.Context, goalID domain.FeatureID, notes, by string) error {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return err
	}
	if !goal.IsGoal() {
		return fmt.Errorf("%s is not a goal", goalID)
	}
	if goal.Stage != domain.StageVerify && goal.Stage != domain.StageImplement {
		return fmt.Errorf("%s is at %s; only a goal at its review or ready for you can be sent back", goalID, goal.Stage)
	}
	notes = strings.TrimSpace(notes)
	e.Drop(goalID)
	if goal.Stage == domain.StageVerify {
		if _, err := e.cfg.Store.Transition(ctx, goalID, domain.StageImplement, by); err != nil {
			return err
		}
	}
	_ = e.cfg.Store.ClearVerifiedAt(ctx, goalID)
	if err := e.cfg.Store.ClearGoalWrapUp(ctx, goalID); err != nil {
		return err
	}
	if notes != "" {
		if path := e.artifactFile(&goal); path != "" {
			if raw, rerr := os.ReadFile(path); rerr == nil {
				if doc, nerr := spec.AppendGoalNote(string(raw), notes, e.now().Local().Format("2006-01-02 15:04")); nerr == nil {
					_ = atomicfile.Write(path, []byte(doc), 0o600)
				}
			}
		}
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalRework, Detail: firstNonEmpty(notes, "sent back"), By: by})
	e.send(Event{Feature: goalID, Stage: domain.StageImplement, Kind: EventGoal})
	return nil
}

// ReverseGoalDecision reverses a decision for review ("D-3"): the goal
// goes back to its cards and the lead redoes what the decision touched.
func (e *Engine) ReverseGoalDecision(ctx context.Context, goalID domain.FeatureID, ref, why string) error {
	log, err := e.cfg.Store.GoalLog(ctx, goalID)
	if err != nil {
		return err
	}
	var dec *state.GoalEntry
	for i := range log {
		if log[i].Action == state.GoalDecision && strings.EqualFold(log[i].DecisionRef(), strings.TrimSpace(ref)) {
			dec = &log[i]
		}
	}
	if dec == nil {
		return fmt.Errorf("%s has no decision %s", goalID, ref)
	}
	detail := fmt.Sprintf("take the other way: %s (instead of: %s)", firstNonEmpty(dec.Alternative, "the alternative"), dec.Detail)
	if why = strings.TrimSpace(why); why != "" {
		detail += " — " + why
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalReversed, Ref: dec.DecisionRef(), Card: dec.Card, Detail: detail, By: "user"})
	return e.SendBackGoal(ctx, goalID, "", "user")
}

// RaiseGoalBudget raises a goal's budget — the one move of the ceiling,
// and only a person makes it.
func (e *Engine) RaiseGoalBudget(ctx context.Context, goalID domain.FeatureID, to int) error {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return err
	}
	if !goal.IsGoal() {
		return fmt.Errorf("%s is not a goal", goalID)
	}
	if to <= goal.Budget.Envelope {
		return fmt.Errorf("%s: %d credits is not a raise over %d", goalID, to, goal.Budget.Envelope)
	}
	from := goal.Budget.Envelope
	goal.Budget.Envelope = to
	if err := e.cfg.Store.UpdateFeature(ctx, &goal); err != nil {
		return err
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalRaised, From: from, To: to, Detail: "you raised the goal budget", By: "user"})
	// a wrap-up the goal forced on itself for lack of budget is lifted by
	// more budget; one you or its lead asked for stands
	if goal.Stage == domain.StageImplement && goal.Goal.WrappingUp() {
		if log, lerr := e.cfg.Store.GoalLog(ctx, goalID); lerr == nil {
			for i := len(log) - 1; i >= 0; i-- {
				if log[i].Action == state.GoalWrapUp {
					if log[i].By == ActorGoal && strings.Contains(log[i].Detail, "budget") {
						_ = e.cfg.Store.ClearGoalWrapUp(ctx, goalID)
					}
					break
				}
			}
		}
	}
	e.send(Event{Feature: goalID, Stage: goal.Stage, Kind: EventGoal})
	return nil
}

// ErrGoalSentBack reports a landing that did not happen because catching
// the goal up with main broke its checks; the goal went back to its cards.
var ErrGoalSentBack = errors.New("the goal went back to its cards")

// ErrGoalPartlyLanded reports a landing that stopped part way through a
// goal that spans repositories: some repos have the goal, the rest do not.
var ErrGoalPartlyLanded = errors.New("the goal landed in some of its repositories")

// LandGoal lands a verified goal: it catches each of the goal's branches
// up with its own repository's main first and, when that brought anything
// in, runs the goal's checks again — a failure sends the goal back to its
// cards rather than landing what was never checked. Then each repository
// gets one merge commit joining the cards' commits there, home repo first,
// and the goal is done.
//
// Git has no merge that spans repositories, so a goal in several of them
// lands several times and can stop half-way: a repo that fails leaves the
// ones before it landed and the goal short of done, with
// ErrGoalPartlyLanded naming both halves. Retrying is safe — a repository
// whose goal branch is already in its main is skipped — and that is the
// honest shape of the thing. Pretending it is one landing would be the
// lie.
//
// It returns the merge commit in the goal's home repository, which is the
// goal card's landed commit.
func (e *Engine) LandGoal(ctx context.Context, goalID domain.FeatureID, message, by string) (string, error) {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return "", err
	}
	if !goal.IsGoal() {
		return "", fmt.Errorf("%s is not a goal", goalID)
	}
	trees, err := e.goalTrees(ctx, goal)
	if err != nil {
		return "", err
	}
	// The order the plan agreed, where it agreed one: a change that means
	// nothing until another repository's is in lands after it, and a
	// landing that stops part way stops with the dependency in and the
	// dependent out rather than the other way round.
	trees = orderGoalTrees(trees, e.goalLandOrder(goal))
	// no final checkpoint: nothing in a goal worktree that is not
	// committed belongs on the goal branch (see Engine.checkpoint)
	anyMerged := false
	for _, t := range trees {
		if !t.Exists() {
			continue
		}
		merged, cerr := t.CatchUp(ctx, false)
		if cerr != nil {
			var cc *worktree.CatchUpConflictError
			if errors.As(cerr, &cc) {
				note := "Landing needs " + t.Label() + " caught up with main, and that conflicts in " + strings.Join(cc.Files, ", ") + "."
				if serr := e.SendBackGoal(ctx, goalID, note, ActorGoal); serr != nil {
					return "", serr
				}
				return "", fmt.Errorf("%w: %s", ErrGoalSentBack, note)
			}
			return "", cerr
		}
		if merged {
			anyMerged = true
			e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into " + t.Label() + " before landing", By: ActorGoal})
		}
	}
	if anyMerged {
		// Evidence is about the heads it ran on, and the catch-up just moved
		// them. A command can be run again here and now; an experiment is an
		// hour on a substrate, so the goal goes back to its conductor, which
		// makes the run on what would actually land and brings it back.
		if names := e.goalExperimentNames(goal); len(names) > 0 {
			note := "Catching up with main moved the goal's branch, and the evidence for " + strings.Join(names, ", ") +
				" is about the branch before it did. The goal makes the run again on what it would land."
			if serr := e.SendBackGoal(ctx, goalID, note, ActorGoal); serr != nil {
				return "", serr
			}
			return "", fmt.Errorf("%w: %s", ErrGoalSentBack, note)
		}
		rv, err := e.Reverify(ctx, goalID, by)
		if err != nil {
			return "", err
		}
		if rv.Status == ReverifyFailed {
			note := "After catching up with main, these checks fail: " + strings.Join(rv.Failed, ", ") + "."
			if serr := e.SendBackGoal(ctx, goalID, note, ActorGoal); serr != nil {
				return "", serr
			}
			return "", fmt.Errorf("%w: %s", ErrGoalSentBack, note)
		}
	}
	if strings.TrimSpace(message) == "" {
		message = e.GoalMergeMessage(ctx, goal)
	}
	home := ""
	var landed []string
	for _, t := range trees {
		done, lerr := t.Landed(ctx)
		if lerr != nil {
			return "", lerr
		}
		if done {
			// an earlier attempt already merged this one
			continue
		}
		sha, merr := t.Merge(ctx, message)
		if merr != nil {
			if len(landed) == 0 {
				return "", merr
			}
			return "", fmt.Errorf("%w: landed in %s, but %s did not: %w",
				ErrGoalPartlyLanded, strings.Join(landed, ", "), repoName(t.Repo), merr)
		}
		landed = append(landed, repoName(t.Repo))
		e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalLanded, Detail: "landed on " + repoMain(t.Repo) + " as " + shortSHA(sha), Ref: sha, By: by})
		if t.Home {
			home = sha
		}
	}
	if home == "" {
		// every repo was already landed by an earlier attempt that failed
		// further down the list: the goal's commit is the one it recorded
		home, _ = e.cfg.Store.LandedSHA(ctx, goalID)
	}
	if goal.HandedOff() {
		_ = e.cfg.Store.ClearHandedOffAt(ctx, goalID)
	}
	if _, err := e.cfg.Store.Transition(ctx, goalID, domain.StageDone, by); err != nil {
		return home, err
	}
	e.Drop(goalID)
	return home, nil
}

// GoalMergeMessage is the merge commit a goal lands with: the goal on the
// subject line, one line per landed card below.
func (e *Engine) GoalMergeMessage(ctx context.Context, goal domain.Feature) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge %s: %s\n", goal.ID, goal.Title)
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return b.String()
	}
	var lines []string
	for _, c := range view.Cards {
		if c.State == goalpolicy.Landed {
			lines = append(lines, fmt.Sprintf("- %s %s", c.Feature.ID, c.Feature.Title))
		}
	}
	if len(lines) > 0 {
		b.WriteString("\n" + strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}

// AbandonGoal closes a goal without landing anything: its unfinished cards
// are dropped and the goal is handed off, so its branch stays until you
// clean it up.
func (e *Engine) AbandonGoal(ctx context.Context, goalID domain.FeatureID, by string) (AdvanceResult, error) {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return AdvanceResult{}, err
	}
	if !goal.IsGoal() {
		return AdvanceResult{}, fmt.Errorf("%s is not a goal", goalID)
	}
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return AdvanceResult{}, err
	}
	for _, c := range view.Cards {
		if c.State != goalpolicy.Landed && c.State != goalpolicy.Dropped {
			if err := e.goalDrop(ctx, goal, c.Feature, "the goal was abandoned", by); err != nil {
				return AdvanceResult{}, err
			}
		}
	}
	e.Drop(goalID)
	if goal.Stage != domain.StageVerify {
		// a hand-off crosses the verify gate; a goal abandoned earlier is
		// walked there first, the way a card with nothing to verify is
		for goal.Stage != domain.StageVerify && goal.Stage != domain.StageDone {
			next := e.nextStage(goal)
			if goal, err = e.cfg.Store.Transition(ctx, goalID, next, by); err != nil {
				return AdvanceResult{}, err
			}
		}
	}
	_ = e.cfg.Store.SetGoalPartial(ctx, goalID, "abandoned")
	if err := e.cfg.Store.SetHandedOffAt(ctx, goalID, e.now()); err != nil {
		return AdvanceResult{}, err
	}
	res := AdvanceResult{Feature: goal, From: goal.Stage, To: domain.StageDone}
	if goal.Stage != domain.StageDone {
		t, err := e.cfg.Store.Transition(ctx, goalID, domain.StageDone, by)
		if err != nil {
			return res, err
		}
		res.Feature, res.Status = t, StatusAdvanced
	}
	return res, nil
}

// --- the goal's own checks ------------------------------------------------

// recordGoalChecks keeps the results of a goal's verify-stage check run in
// its log, where the hand-over report reads which done-when items passed.
func (e *Engine) recordGoalChecks(f domain.Feature, results []goalCheckResult) {
	if !f.IsGoal() || len(results) == 0 {
		return
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return
	}
	e.goalLog(context.Background(), f.ID, state.GoalPayload{Action: state.GoalChecks, Detail: string(raw), By: ActorGoal})
}

// checkFailureNote is the little a hand-over keeps of what a failing
// command said. An exit code alone cannot tell a done-when item that is
// not met yet from one whose command never ran at all — a directory that
// is not there, a binary that is not on the path — and the person reading
// the hand-over is the only one who can act on the difference.
func checkFailureNote(r verify.Result) string {
	if r.OK {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(r.Output, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return clip(strings.Join(lines, " / "), 300)
}

// goalCheckResult is one check outcome as a goal records it.
type goalCheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	// Evidence is what an experiment item's result rests on — the run, what
	// held, and where its bundle is. Empty for a command's.
	Evidence string `json:"evidence,omitempty"`
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// sortedIDs returns ids sorted, for stable log lines.
func sortedIDs(ids []domain.FeatureID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	sort.Strings(out)
	return out
}
