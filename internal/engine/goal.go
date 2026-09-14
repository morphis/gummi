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

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// ActorGoal is the transition actor for moves the goal's conductor makes
// on its own cards (a landing, a send-back).
const ActorGoal = "goal"

// goalIdleGrace is how long a started goal card may sit with no session,
// no park and no progress before the conductor restarts it. It covers the
// moment between one step of a driving loop and the next, so a card that
// is merely between steps is never restarted under the loop's feet.
const goalIdleGrace = 90 * time.Second

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
	LeadTries int
	TakenOver bool
	Serves    []string // done-when ids, from the goal doc's card row
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
	v.DocPath = e.artifactFile(&goal)
	if v.DocPath != "" {
		if raw, rerr := os.ReadFile(v.DocPath); rerr == nil {
			v.DoneWhen, _, _ = spec.ParseDoneWhen(string(raw))
			v.Rows, _, _ = spec.ParseGoalCards(string(raw), v.DoneWhen)
		}
	}
	serves := map[domain.FeatureID][]string{}
	for _, r := range v.Rows {
		if r.ID != "" {
			serves[r.ID] = r.Serves
		}
	}
	open, err := e.cfg.Store.OpenDecisions(ctx)
	if err != nil {
		return v, err
	}
	members := map[domain.FeatureID]bool{}
	for _, c := range cards {
		members[c.ID] = true
	}

	// the last log entry that touched each card, and the pre-goal spend of
	// attached cards (it does not count against the goal)
	lastTouch := map[domain.FeatureID]int64{}
	lastTouchAt := map[domain.FeatureID]time.Time{}
	attachSpent := map[domain.FeatureID]int{}
	dropReason := map[domain.FeatureID]string{}
	leadSeen := map[domain.FeatureID][]int64{}
	var lastLeadOK, lastLeadAny int64
	failures := 0
	for _, en := range v.Log {
		switch en.Action {
		case state.GoalStarted, state.GoalBounced, state.GoalRaised, state.GoalAnswered,
			state.GoalPlanApproved, state.GoalAttached, state.GoalMinted:
			lastTouch[en.Card] = en.Seq
			if en.Action != state.GoalMinted && en.Action != state.GoalAttached {
				lastTouchAt[en.Card] = en.At
			}
		case state.GoalDropped:
			dropReason[en.Card] = en.Detail
		case state.GoalLeadTurn:
			// only a wake turn names the cards it was woken over in Ref;
			// a turn answering a card's question or checking its plan
			// carries the card in Card and is not a try at unsticking it
			lastLeadOK, lastLeadAny = en.Seq, en.Seq
			failures = 0
			for _, id := range strings.Split(en.Ref, ",") {
				if id = strings.TrimSpace(id); id != "" {
					leadSeen[domain.FeatureID(id)] = append(leadSeen[domain.FeatureID(id)], en.Seq)
				}
			}
		case state.GoalLeadFailed:
			lastLeadAny = en.Seq
			failures++
		}
		if en.Action == state.GoalAttached {
			attachSpent[en.Card] = en.From
		}
	}

	now := e.now()
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
		if merr != nil {
			return v, merr
		}
		gc.State, gc.Reason = e.goalCardState(ctx, c, marks, open[c.ID], lastTouch[c.ID], lastTouchAt[c.ID], now)
		if gc.State == goalpolicy.Dropped && gc.Reason == "" {
			gc.Reason = dropReason[c.ID]
		}
		if gc.State == goalpolicy.Verified {
			gc.Findings = e.openReviewerFindings(&c)
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

	in := goalpolicy.Input{
		Stage:        goal.Stage,
		Envelope:     goal.Budget.Envelope,
		OwnSpent:     goal.Spend.CreditEquivalent(),
		Reserve:      goal.ReserveCredits(),
		Lanes:        goal.Goal.LaneCount(),
		WrapUp:       goal.Goal.WrappingUp(),
		WrapReason:   e.goalWrapReason(v.Log),
		LeadFailures: failures,
		Reviewing:    e.Get(goal.ID) != nil,
	}
	for _, gc := range v.Cards {
		in.Cards = append(in.Cards, goalpolicy.Card{
			ID: gc.Feature.ID, State: gc.State, Envelope: gc.Envelope, Spent: gc.Spent,
			DependsOn: gc.DependsOn, TakenOver: gc.TakenOver, Findings: gc.Findings,
			LeadTries: gc.LeadTries, Reason: gc.Reason,
		})
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
		case StateRunning, StateQueued, StateInteractive:
			return goalpolicy.Running, ""
		}
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
}

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
	res.Actions = acts
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
	case goalpolicy.Lead:
		starts, err := e.runLeadTurn(ctx, view, a.Reasons)
		res.Start = append(res.Start, starts...)
		res.Again = true
		if err != nil {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadFailed, Detail: err.Error(), By: ActorGoal})
		}
		return nil
	case goalpolicy.Start:
		gc, _ := view.Card(a.Card)
		note := ""
		if gc.State == goalpolicy.Stuck {
			note = "The goal restarted this card after it stopped: " + gc.Reason
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalStarted, Card: a.Card, By: ActorGoal})
		res.Start = append(res.Start, GoalStart{ID: a.Card, Note: note})
		return nil
	case goalpolicy.Finish:
		res.Finished = true
		return e.goalFinish(ctx, goal, a.Reason)
	}
	return nil
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
	if !card.GoalAttached {
		return e.goalCloseDropped(ctx, card)
	}
	return e.goalDetach(ctx, goal, card)
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
func (e *Engine) goalDetach(ctx context.Context, goal, card domain.Feature) error {
	gm, gerr := e.pool.ManagerFor(ctx, &card)
	fork := ""
	if gerr == nil {
		fork, _ = gm.ForkPoint(ctx, &card)
	}
	if err := e.cfg.Store.ClearGoal(ctx, card.ID); err != nil {
		return err
	}
	card.GoalID, card.GoalAttached, card.GoalDroppedAt = "", false, time.Time{}
	detail := "back on the board with its work kept"
	if main, err := e.pool.ManagerFor(ctx, &card); err == nil {
		if merr := main.RebaseOnto(ctx, &card, fork); merr != nil {
			detail = "back on the board; moving its branch onto main failed (" + merr.Error() + ") — rebase it before working on it"
		}
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDetached, Card: card.ID, Detail: detail, By: ActorGoal})
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

	e.goalCatchUp(ctx, goal)

	gm, err := e.pool.ManagerFor(ctx, &card)
	if err != nil {
		return nil, err
	}
	if _, err := gm.CommitAll(ctx, &card, "final checkpoint"); err != nil && !errors.Is(err, worktree.ErrNoWorktree) {
		return nil, err
	}
	rebased, err := gm.RebasedOnMain(ctx, &card)
	if err != nil {
		return nil, err
	}
	if !rebased {
		head, _ := gm.MainHead(ctx)
		if err := gm.RebaseOnMain(ctx, &card); err != nil {
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

// goalCatchUp merges main into the goal branch when main has moved. A
// conflict is handed to the resolver (resolveGoalCatchUp); when that
// cannot finish the merge, the catch-up is aborted and logged, and the
// goal carries on — landing still works on the older base, and the goal
// catches up again before it lands on main.
func (e *Engine) goalCatchUp(ctx context.Context, goal domain.Feature) {
	main, err := e.mgr(ctx, &goal)
	if err != nil {
		return
	}
	behind, err := main.GoalBehindMain(ctx, &goal)
	if err != nil || !behind {
		return
	}
	merged, err := main.CatchUpGoal(ctx, &goal, true)
	var cc *worktree.CatchUpConflictError
	switch {
	case err == nil && merged:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into the goal branch", By: ActorGoal})
	case errors.As(err, &cc):
		if rerr := e.resolveGoalCatchUp(ctx, goal, cc.Files); rerr == nil {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into the goal branch, resolving " + strings.Join(cc.Files, ", "), By: ActorGoal})
			return
		} else {
			_, _ = main.AbortGoalMerge(ctx, &goal)
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCatchUpFail, Detail: fmt.Sprintf("conflicts in %s: %v", strings.Join(cc.Files, ", "), rerr), By: ActorGoal})
		}
	case err != nil:
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCatchUpFail, Detail: err.Error(), By: ActorGoal})
	}
}

// goalFinish settles the goal's work and starts its review of the combined
// branch — the implement stage's critique pass, which from here runs
// through the driving loop like any card's.
func (e *Engine) goalFinish(ctx context.Context, goal domain.Feature, reason string) error {
	if !goal.Goal.WrappingUp() {
		// dropped cards make a goal partial only when an agreed item lost
		// every card that served it: a card the lead replaced left nothing
		// of the goal unmet
		reason = e.goalPartialReason(ctx, goal)
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
	return e.RunCritique(cur, "")
}

// goalPartialReason names the done-when items whose every serving card was
// dropped, "" when each item still has a card that landed or is running.
func (e *Engine) goalPartialReason(ctx context.Context, goal domain.Feature) string {
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return ""
	}
	alive := map[string]bool{}
	served := map[string]bool{}
	for _, c := range view.Cards {
		for _, s := range c.Serves {
			served[s] = true
			if c.State != goalpolicy.Dropped {
				alive[s] = true
			}
		}
	}
	var lost []string
	for _, d := range view.DoneWhen {
		if served[d.ID] && !alive[d.ID] {
			lost = append(lost, d.ID)
		}
	}
	if len(lost) == 0 {
		return ""
	}
	return strings.Join(lost, ", ") + " lost every card serving it"
}

// --- plan gate and start ---------------------------------------------------

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
	if _, err := spec.ParseGoalLanes(doc); err != nil {
		return err.Error()
	}
	var want []int
	for _, r := range rows {
		if r.ID == "" {
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
		case c.Repo != goal.Repo:
			return fmt.Sprintf("%s is in a different repository than the goal", r.ID)
		}
	}
	if goal.Budget.Envelope <= 0 {
		return "the goal has no budget"
	}
	pool := goal.GoalMintPool(goal.Spend.CreditEquivalent(), len(rows))
	if _, err := goalpolicy.SplitEnvelopes(want, pool); err != nil && len(want) > 0 {
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
	for _, r := range rows {
		if r.ID == "" {
			want = append(want, r.Envelope)
		}
	}
	pool := goal.GoalMintPool(goal.Spend.CreditEquivalent(), len(rows))
	envs, err := goalpolicy.SplitEnvelopes(want, pool)
	if err != nil && len(want) > 0 {
		return err
	}
	next := 0
	byTitle := map[string]domain.FeatureID{}
	for i := range rows {
		r := &rows[i]
		if r.ID == "" {
			f, merr := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
				Kind:         r.EffectiveKind(),
				Description:  goalCardDescription(*goal, *r, itemText),
				Profile:      goal.Profile,
				Envelope:     envs[next],
				Repo:         goal.Repo,
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

// LandGoal lands a verified goal on main: it catches the goal branch up
// with main first and, when that brought anything in, runs the goal's
// checks again — a failure sends the goal back to its cards rather than
// landing what was never checked. Then one merge commit joins the cards'
// commits on main, and the goal is done.
func (e *Engine) LandGoal(ctx context.Context, goalID domain.FeatureID, message, by string) (string, error) {
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return "", err
	}
	if !goal.IsGoal() {
		return "", fmt.Errorf("%s is not a goal", goalID)
	}
	main, err := e.mgr(ctx, &goal)
	if err != nil {
		return "", err
	}
	if _, err := main.CommitAll(ctx, &goal, "final checkpoint"); err != nil && !errors.Is(err, worktree.ErrNoWorktree) {
		return "", err
	}
	merged, err := main.CatchUpGoal(ctx, &goal, false)
	if err != nil {
		var cc *worktree.CatchUpConflictError
		if errors.As(err, &cc) {
			note := "Landing needs the goal branch caught up with main, and that conflicts in " + strings.Join(cc.Files, ", ") + "."
			if serr := e.SendBackGoal(ctx, goalID, note, ActorGoal); serr != nil {
				return "", serr
			}
			return "", fmt.Errorf("%w: %s", ErrGoalSentBack, note)
		}
		return "", err
	}
	if merged {
		e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalCaughtUp, Detail: "merged main into the goal branch before landing", By: ActorGoal})
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
	sha, err := main.MergeGoal(ctx, &goal, message)
	if err != nil {
		return "", err
	}
	if goal.HandedOff() {
		_ = e.cfg.Store.ClearHandedOffAt(ctx, goalID)
	}
	if _, err := e.cfg.Store.Transition(ctx, goalID, domain.StageDone, by); err != nil {
		return sha, err
	}
	e.Drop(goalID)
	return sha, nil
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

// goalCheckResult is one check outcome as a goal records it.
type goalCheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
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
