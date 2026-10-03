package engine

// Stacks in the engine. A stack is a chain of cards whose branches fork
// from one another; when one changes, the cards above it are sitting on
// commits that have moved and have to be replayed. StackTick is what does
// that replaying, and it is the counterpart of GoalTick: read, ask the
// pure policy (internal/stack), execute one step.
//
// The board and the headless driver both tick; neither decides. That is
// the same arrangement goalpolicy has, and for the same reason — two
// drivers that each carried their own rules would drift, and a stack that
// restacked differently depending on which process noticed would be worse
// than one that did not restack at all.
//
// Nothing here gates a card. A stack orders landing (git orders it: a
// card's branch contains the commits of every card below it) and nothing
// else, so no path in this file can stop a card from running.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/stack"
	"github.com/morphis/gummi/internal/worktree"
)

// stackLock serializes everything done for one stack: two ticks must
// never replay two members at once, nor read a snapshot the other is
// changing. The shape is goalLock's, deliberately.
func (e *Engine) stackLock(id domain.StackID) *sync.Mutex {
	e.stackLocksMu.Lock()
	defer e.stackLocksMu.Unlock()
	if e.stackLocks == nil {
		e.stackLocks = map[domain.StackID]*sync.Mutex{}
	}
	m := e.stackLocks[id]
	if m == nil {
		m = &sync.Mutex{}
		e.stackLocks[id] = m
	}
	return m
}

// StackView is a snapshot of one stack plus the card records the
// executor needs, so a tick reads the store and git once.
type StackView struct {
	Snapshot stack.View
	byID     map[domain.FeatureID]domain.Feature
}

// Card returns the member's full record.
func (v StackView) Card(id domain.FeatureID) (domain.Feature, bool) {
	f, ok := v.byID[id]
	return f, ok
}

// StackTickResult reports what one tick did.
type StackTickResult struct {
	Stack domain.StackID
	// Actions is what the policy asked for — at most one restack, since a
	// replay changes what every card above it forks from.
	Actions []stack.Action
	// Restacked names the card actually replayed, empty when none was.
	Restacked domain.FeatureID
	// Conflict is set when the replay stopped on conflicts. The card's
	// branch is left untouched (RebaseOnto aborts before returning), and
	// the caller decides whether to offer the agent — the TUI has that
	// flow already, in internal/ui/rebase.go.
	Conflict *worktree.RebaseConflictError
	// Again reports that more remains to do: tick once more when this
	// one's work has settled.
	Again bool
	// Push is the `git push --force-with-lease` the replayed card's branch
	// now needs, set beside Restacked on a clean replay.
	Push string
	// Settled is the replay walk this tick found finished: set once, on
	// the tick that finds nothing left to replay after one or more ticks
	// that did. Whoever ran that tick says what moved and what to push —
	// a board tick as much as a forced restack, because gummi prints the
	// push a replayed branch needs and never runs it (§18.5).
	Settled *StackReplay
}

// StackReplay is one walk of replays over a stack: the cards whose
// branches moved, in the order they did, the push each now needs (Push[i]
// is Cards[i]'s, empty only for a card with no branch name on record), and
// when the last of them happened. A walk is the run of ticks from the
// first replay to the tick that finds the stack settled; a replay after
// that starts a new one.
type StackReplay struct {
	Cards []domain.FeatureID
	Push  []string
	At    time.Time
}

// stackWalk is a stack's latest walk and whether it has been handed back.
type stackWalk struct {
	replay  StackReplay
	settled bool
}

// LastReplay is the stack's latest replay walk, finished or still going,
// and whether it has one. It lives as long as this engine does: the
// branches' own state is the durable record, and the push lines are a
// reminder of it, not a second copy anyone reads to decide anything.
func (e *Engine) LastReplay(id domain.StackID) (StackReplay, bool) {
	e.stackLocksMu.Lock()
	defer e.stackLocksMu.Unlock()
	w := e.stackReplays[id]
	if w == nil {
		return StackReplay{}, false
	}
	r := w.replay
	r.Cards = slices.Clone(r.Cards)
	r.Push = slices.Clone(r.Push)
	return r, true
}

// noteReplay adds one clean replay to the stack's walk, starting a new
// walk when the last one was already handed back.
func (e *Engine) noteReplay(id domain.StackID, card domain.FeatureID, push string) {
	e.stackLocksMu.Lock()
	defer e.stackLocksMu.Unlock()
	if e.stackReplays == nil {
		e.stackReplays = map[domain.StackID]*stackWalk{}
	}
	w := e.stackReplays[id]
	if w == nil || w.settled {
		w = &stackWalk{}
		e.stackReplays[id] = w
	}
	w.replay.At = time.Now()
	if slices.Contains(w.replay.Cards, card) {
		return
	}
	w.replay.Cards = append(w.replay.Cards, card)
	w.replay.Push = append(w.replay.Push, push)
}

// settleReplay hands back the stack's walk if one is open, marking it
// handed back; nil when there is nothing new to say.
func (e *Engine) settleReplay(id domain.StackID) *StackReplay {
	e.stackLocksMu.Lock()
	defer e.stackLocksMu.Unlock()
	w := e.stackReplays[id]
	if w == nil || w.settled {
		return nil
	}
	w.settled = true
	r := w.replay
	r.Cards = slices.Clone(r.Cards)
	r.Push = slices.Clone(r.Push)
	return &r
}

// StackTick conducts one stack a single step. Safe to call often and
// from any event; a settled stack does nothing.
func (e *Engine) StackTick(ctx context.Context, id domain.StackID) (StackTickResult, error) {
	mu := e.stackLock(id)
	mu.Lock()
	defer mu.Unlock()

	res := StackTickResult{Stack: id}
	view, err := e.StackSnapshot(ctx, id)
	if err != nil {
		return res, err
	}
	acts := stack.Decide(view.Snapshot)
	res.Actions = acts
	for _, a := range acts {
		switch a.Kind {
		case stack.ActionWait:
			// Something must settle first. Ask to be ticked again rather
			// than skipping over it: replaying a card above one that is
			// about to change would just make it stale again.
			res.Again = true
			return res, nil
		case stack.ActionRestack:
			conflict, err := e.stackRestackOne(ctx, view, a)
			if err != nil {
				return res, err
			}
			res.Restacked = a.Card
			if conflict != nil {
				// The walk stops here. Everything below this card is
				// already correct and everything above stays as it was,
				// which is what makes a half-restacked stack legible.
				// The replays before it stay in the open walk, said once
				// the stack settles after the conflict is resolved.
				res.Conflict = conflict
				return res, nil
			}
			if f, ok := view.Card(a.Card); ok {
				res.Push = e.replayPushCommand(ctx, &f)
			}
			e.noteReplay(id, a.Card, res.Push)
			// A replay rewrites what the cards above fork from, so their
			// staleness is only knowable now. Come back round.
			res.Again = true
			return res, nil
		}
	}
	// nothing left to replay: a walk that was going has finished
	res.Settled = e.settleReplay(id)
	return res, nil
}

// stackRestackOne replays one card onto its current base.
//
// The recipe is the goal's, verbatim (goalAttach / goalDetach): read the
// fork point BEFORE anything moves, then RebaseOnto with it as oldBase so
// `--onto` replays only this card's own commits and never carries the
// commits of the card below it. Getting that order wrong is how a card
// ends up owning work it did not write.
func (e *Engine) stackRestackOne(ctx context.Context, view StackView, a stack.Action) (*worktree.RebaseConflictError, error) {
	f, ok := view.Card(a.Card)
	if !ok {
		return nil, fmt.Errorf("restacking %s: no such card in %s", a.Card, view.Snapshot.ID)
	}
	mgr, err := e.pool.ManagerFor(ctx, &f)
	if err != nil {
		return nil, err
	}
	fork, err := mgr.ForkPoint(ctx, &f)
	if err != nil {
		return nil, fmt.Errorf("restacking %s: reading its fork point: %w", f.ID, err)
	}
	// Hold the card's own lock for the replay, so a session cannot start
	// on it while its branch is being rewritten. The policy already
	// declined to move a card with a live session; this closes the gap
	// between that decision and this write.
	if e.cfg.CardLocks != nil && !e.cfg.CardLocks.Holds(f.ID) {
		unlock, lerr := e.cfg.CardLocks.Acquire(f.ID)
		if lerr != nil {
			// Another gummi process is driving this card. Not an error
			// here: the next tick finds it settled or still held.
			return nil, nil
		}
		defer unlock()
	}
	if err := mgr.RebaseOnto(ctx, &f, fork); err != nil {
		var ce *worktree.RebaseConflictError
		if errors.As(err, &ce) {
			e.cardNote(ctx, f.ID, f.Stage,
				"replaying onto "+a.Base+" hit conflicts — its branch is untouched, resolve them to carry on")
			return ce, nil
		}
		return nil, fmt.Errorf("restacking %s onto %s: %w", f.ID, a.Base, err)
	}
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventUpdated})
	// The card's base just moved, so what its baseline excused was
	// measured on a commit it no longer forks from (rebaseline.go). The
	// re-measure runs the repo's checks, so it goes off the tick.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if note := e.rebaselineNote(context.Background(), f); note != "" {
			e.cardNote(context.Background(), f.ID, f.Stage, note)
			e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventUpdated})
		}
	}()
	return nil, nil
}

// sessionHoldsTree reports whether a card's session may still touch its
// worktree, so a replay must wait for it: one working or waiting for a
// slot, or a conversation with an agent attached. A session that finished,
// was paused, or was restored from a process that is gone stays in the
// engine's map for its thread and holds nothing — reading it as running
// kept every card that had ever run in this process out of every replay.
func sessionHoldsTree(s *Session) bool {
	if s == nil {
		return false
	}
	switch s.State() {
	case StateRunning:
		return true
	}
	return s.Live()
}

// freeformHoldsTree is sessionHoldsTree for a freeform card, whose
// conversation lives in e.freeform rather than in the stage sessions'
// map. The same rule applies to its current backend: a turn in flight,
// or an agent still attached between turns, may write to the worktree,
// and this process holding the card's lock for it is exactly why
// stackRestackOne's own lock would not keep the replay out.
func (e *Engine) freeformHoldsTree(id domain.FeatureID) bool {
	ff := e.Freeform(id)
	if ff == nil {
		return false
	}
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	return sessionHoldsTree(sess) || (sess != nil && sess.Busy())
}

// forksFromMember reports whether f's base is the branch of another card
// in its stack rather than the stack's own base.
func (e *Engine) forksFromMember(ctx context.Context, f *domain.Feature, members []domain.Feature) bool {
	base, err := e.StackBaseFor(ctx, f)
	if err != nil || base == "" {
		return false
	}
	for _, o := range members {
		if o.ID != f.ID && o.BranchName() == base {
			return true
		}
	}
	return false
}

// StackSnapshot builds the policy's view of one stack: the members in
// order, and for each the git facts the policy needs.
func (e *Engine) StackSnapshot(ctx context.Context, id domain.StackID) (StackView, error) {
	st, err := e.cfg.Store.GetStack(ctx, id)
	if err != nil {
		return StackView{}, err
	}
	members, err := e.cfg.Store.ListStackCards(ctx, id)
	if err != nil {
		return StackView{}, err
	}
	view := StackView{byID: make(map[domain.FeatureID]domain.Feature, len(members))}
	snap := stack.View{ID: st.ID, Name: st.Name, Repo: st.Repo}
	live := e.Sessions()
	for _, f := range members {
		view.byID[f.ID] = f
		if f.StackPos == 0 {
			// The stack sits on the bottom card's own chosen base.
			snap.Base = f.Base
		}
		m := stack.Member{
			ID:      f.ID,
			Pos:     f.StackPos,
			Branch:  f.BranchName(),
			Running: sessionHoldsTree(live[f.ID]) || e.freeformHoldsTree(f.ID) || e.oneShotBusy(f.ID),
		}
		mgr, merr := e.pool.ManagerFor(ctx, &f)
		if merr == nil {
			if ok, eerr := mgr.Exists(ctx, &f); eerr == nil {
				m.HasTree = ok
			}
			if m.HasTree {
				// Landed is asked of the card first (a gummi squash or a
				// merged PR both show up here) and of the record second,
				// so a card whose PR merged upstream reads as landed as
				// soon as main carries it.
				//
				// Only when the card's base is the trunk, though: asked
				// against another card's branch, "is my branch in my
				// base" is answered yes by a card that a reorder just put
				// beneath the card it used to sit on, and reading that as
				// a landing would drop it from the replay.
				if !e.forksFromMember(ctx, &f, members) {
					if landed, lerr := mgr.Landed(ctx, &f); lerr == nil {
						m.Landed = landed
					}
				}
				if !m.Landed && f.LandedSHA != "" {
					m.Landed = true
				}
				if rebased, rerr := mgr.RebasedOnBase(ctx, &f); rerr == nil {
					// a landed card's branch is never replayed, so it is
					// never "stale" — its commits are in the base already
					m.Stale = !rebased && !m.Landed
				}
				if dirty, derr := mgr.TrackedDirty(ctx, &f); derr == nil {
					m.Dirty = dirty
				}
			}
		}
		if f.Stage == domain.StageDone {
			// A done card is finished either way: landed, handed off, or
			// dropped. Nothing above it should fork from it any more, and
			// nothing will replay it: it cannot be stale.
			m.Landed, m.Stale = true, false
		}
		snap.Members = append(snap.Members, m)
	}
	view.Snapshot = snap
	return view, nil
}

// StackBaseFor resolves the revision a card forks from — the value the
// worktree package asks for through its BaseLookup seam.
//
// A card with no stack gets its own chosen base (empty meaning the
// checkout's HEAD). A stacked card gets the branch of the nearest card
// below it that has not landed, which is what chains a stack and what
// makes the chain collapse a rung when the bottom lands.
//
// It deliberately does NOT use StackSnapshot. This function is what the
// worktree manager calls to answer "what is this card's base?", and
// StackSnapshot asks git questions (Landed, RebasedOnBase) that each
// resolve a base — so going through it here would recurse without end.
// So this reads the store and the filesystem only, and takes its
// "landed" answer from the card's own record rather than from git.
func (e *Engine) StackBaseFor(ctx context.Context, f *domain.Feature) (string, error) {
	if f == nil {
		return "", nil
	}
	if f.StackID == "" {
		return f.Base, nil
	}
	view, err := e.stackBaseView(ctx, f.StackID)
	if err != nil {
		// A card pointing at a stack that cannot be read still has its
		// own base, and behaving as an unstacked card is better than
		// failing an operation git would accept.
		return f.Base, nil //nolint:nilerr // deliberate degrade, see comment
	}
	return stack.BaseFor(view, f.ID), nil
}

// stackBaseView builds the minimum view BaseFor needs — order, branch
// names, whether each member has a branch at all, and whether it has
// finished — using only the store and os.Stat.
//
// "Landed" here is the record's answer (a stamped squash commit, or a
// card that has reached done by any route) and not git's. That is a
// slightly coarser read than StackSnapshot's, and it is the right one for
// this caller: it cannot ask git without recursing, and a card whose PR
// merged but whose record has not caught up simply keeps its successor
// forked from its branch for one more tick — which is correct, because
// that branch still exists and still carries the work.
func (e *Engine) stackBaseView(ctx context.Context, id domain.StackID) (stack.View, error) {
	st, err := e.cfg.Store.GetStack(ctx, id)
	if err != nil {
		return stack.View{}, err
	}
	members, err := e.cfg.Store.ListStackCards(ctx, id)
	if err != nil {
		return stack.View{}, err
	}
	v := stack.View{ID: st.ID, Name: st.Name, Repo: st.Repo}
	for _, f := range members {
		if f.StackPos == 0 {
			v.Base = f.Base
		}
		m := stack.Member{
			ID:     f.ID,
			Pos:    f.StackPos,
			Branch: f.BranchName(),
			Landed: f.LandedSHA != "" || f.Stage == domain.StageDone,
		}
		if mgr, merr := e.pool.ManagerFor(ctx, &f); merr == nil {
			// Exists is a stat, not a git call — no base to resolve, so
			// no recursion.
			if ok, eerr := mgr.Exists(ctx, &f); eerr == nil {
				m.HasTree = ok
			}
		}
		v.Members = append(v.Members, m)
	}
	return v, nil
}

// StackLandBlocker names the card that must land before f may, if any.
// It is the one ordering a stack imposes — see stack.LandBlocker.
func (e *Engine) StackLandBlocker(ctx context.Context, f *domain.Feature) (domain.FeatureID, bool) {
	if f == nil || f.StackID == "" {
		return "", false
	}
	// The light view again: LandBlocker only needs order and whether
	// each member has finished, and this is called from the landing
	// guards, which are themselves inside git operations.
	view, err := e.stackBaseView(ctx, f.StackID)
	if err != nil {
		return "", false
	}
	return stack.LandBlocker(view, f.ID)
}
