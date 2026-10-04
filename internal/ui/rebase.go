package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// The agent-rebase flow: rebaseFeature's plain rebase stops on
// conflicts → rebaseConflictMsg offers the hand-off → agentRebase
// dispatches the engine's rebase-resolve session → on its idle,
// judgeRebase reads the resulting git state → rebaseSettled re-verifies
// or escalates. Conflict resolution is agent-authored change, so a
// successful rebase of a Verify-stage feature re-runs Verify instead of
// letting the resolution land unseen; earlier stages still have the
// quality floor ahead of them.

// rebaseConflictMsg reports a rebase that stopped on conflicts (and
// self-aborted) while an engine is wired — the hand-off offer.
type rebaseConflictMsg struct {
	f     domain.Feature
	files []string
	// reason is git's account of a stop that was not a plain conflict
	// (an untracked file in the way, a stop with nothing unmerged)
	reason string
	// dirty: the worktree carried uncommitted work into the rebase
	// (autostash), which the agent's rebase has to carry back out
	dirty bool
}

// rebaseSettledMsg carries the judged outcome of a finished
// rebase-resolve session: ok when the branch is rebased and the
// worktree clean; otherwise problem says what the git state shows.
// settled is the judged session's construction time, so the settlement
// can tell a dispatch that took the card over since from the session
// this judgment is about.
type rebaseSettledMsg struct {
	f       domain.Feature
	ok      bool
	problem string
	settled time.Time
}

// offerAgentRebase pushes the hand-off confirm for a conflicted rebase.
// An agent session costs credits, so it never starts without a yes.
func (m *Shell) offerAgentRebase(msg rebaseConflictMsg) {
	f, files := msg.f, msg.files
	detail := "runs an agent session in the worktree"
	if msg.dirty {
		detail += ", carrying the uncommitted work across"
	}
	if f.Stage == domain.StageVerify {
		detail += "; verify re-runs after"
	}
	question := "rebase " + string(f.ID) + " onto " + m.baseBranch(f) + " hit conflicts — let the agent resolve them?"
	switch {
	case msg.reason != "":
		// git-derived; sanitize like every other notice
		detail = sanitize("git stopped: "+msg.reason) + " — " + detail
		question = "rebase " + string(f.ID) + " onto " + m.baseBranch(f) + " stopped — let the agent sort it out?"
	case len(files) > 0:
		detail = sanitize("conflicts: "+strings.Join(files, ", ")) + " — " + detail
	}
	m.Overlay.Push(&confirmDialog{
		card:      f.ID,
		id:        "agent-rebase",
		question:  question,
		detail:    detail,
		onConfirm: func() tea.Cmd { return m.agentRebase(msg) },
	})
}

// agentRebase dispatches the engine's rebase-resolve session, holding the
// stop the card waits at so an unresolved rebase can put it back.
func (m *Shell) agentRebase(msg rebaseConflictMsg) tea.Cmd {
	f, files, reason := msg.f, msg.files, msg.reason
	if m.rebaseDirty == nil {
		m.rebaseDirty = map[domain.FeatureID]bool{}
	}
	// The stage session this hand-off interrupts is still writing: it can
	// dirty the worktree between the press that offered this confirm and
	// the confirm itself. Re-check here, so the judge's tolerance matches
	// what actually went in — the engine re-checks at dispatch and tells
	// the agent to autostash.
	dirty := msg.dirty
	if d, err := m.wt.Dirty(context.Background(), &f); err == nil && d {
		dirty = true
	}
	m.rebaseDirty[f.ID] = dirty
	if it, ok := m.inbox.get(f.ID); ok {
		if m.rebaseHeld == nil {
			m.rebaseHeld = map[domain.FeatureID]attnItem{}
		}
		m.rebaseHeld[f.ID] = it
	}
	return func() tea.Msg {
		if err := m.engine.RunRebaseStopped(context.Background(), f, files, reason); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + ": agent dispatched to rebase onto " + m.baseBranch(f)}
	}
}

// judgeRebase reads the git state a finished rebase-resolve session
// left behind. The engine has already aborted anything mid-flight, so a
// clean worktree whose branch now carries main's HEAD is success — the
// agent's own claims are never consulted.
//
// A worktree that went in carrying uncommitted work comes out carrying it
// again — the autostash puts it back — so dirty is only a failure for one
// that went in clean. Unmerged paths are a failure either way: that is a
// resolution left half done.
func (m *Shell) judgeRebase(id domain.FeatureID) tea.Cmd {
	wasDirty := m.rebaseDirty[id]
	// the judged session's construction time: the settlement re-runs an
	// interrupted stage only when this same session is still the live
	// one — a dispatch that took the card over since decides through
	// its own settlement instead.
	var settled time.Time
	if m.engine != nil {
		if s := m.engine.Get(id); s != nil {
			settled = s.Snapshot().StartedAt
		}
	}
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if left, err := m.wt.Unmerged(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if len(left) > 0 {
			return rebaseSettledMsg{f: f, problem: "unmerged paths were left behind (" + strings.Join(left, ", ") + ")", settled: settled}
		}
		if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if dirty && !wasDirty {
			return rebaseSettledMsg{f: f, problem: "the worktree was left dirty", settled: settled}
		}
		if rebased, err := m.wt.RebasedOnBase(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if !rebased {
			return rebaseSettledMsg{f: f, problem: "the branch is still not rebased", settled: settled}
		}
		return rebaseSettledMsg{f: f, ok: true, settled: settled}
	}
}

// rebaseSettled folds the judged outcome into the board: a failed agent
// rebase escalates to the human; a successful one at Verify re-runs the
// stage (the resolution is unreviewed agent work), one that interrupted a
// mid-turn stage session re-runs that stage where it stopped, and
// elsewhere the workflow's remaining stages already cover it.
func (m *Shell) rebaseSettled(msg rebaseSettledMsg) tea.Cmd {
	id := msg.f.ID
	held, wasHeld := m.rebaseHeld[id]
	delete(m.rebaseHeld, id)
	delete(m.rebaseDirty, id)
	if !msg.ok {
		// The resolve session took the stage session's place on the engine,
		// and it carries no verdict of its own: left standing, it made the
		// card read as a stage that ended with no clear verdict — at a
		// verify that had passed, "verification stopped here" with "land
		// anyway" on offer, while the store still said verified. Drop it,
		// so the card reads as its stage really ended, as it does after a
		// restart.
		m.dropSession(id)
		text := "agent rebase failed — " + msg.problem + ": the branch is still on its old base, so its conflicts with " +
			m.baseBranchOf(id) + " remain; read the transcript, then resolve them on the branch"
		if wasHeld && held.Kind == attnGate && !held.Escalated {
			// the card was already waiting on a person (a passed verify
			// whose landing hit the conflict): that decision still
			// stands, and the rebase not happening is the news, not a
			// new stop that replaces it
			m.inbox.put(held)
			m.logPark(id, state.ParkReasonNeedsYou, text)
			if msg.f.Stage == domain.StageVerify {
				// a landing from here would hit the same conflicts, so
				// the decision leads with the rebase again
				if m.landConflicts == nil {
					m.landConflicts = map[domain.FeatureID][]string{}
				}
				m.landConflicts[id] = []string{}
			}
		} else {
			m.raiseEscalation(id, text)
		}
		m.notice = noticeMsg{text: string(id) + ": " + text, isErr: true, id: id}
		return m.loadRows
	}
	f := msg.f
	drifted, _ := m.wt.Drift(context.Background(), &f)
	// The rebase resolved cleanly; re-anchor the recorded fork to main's
	// HEAD so a drifted feature is cleared in the same gesture and the
	// resolution does not go stale under the next rewrite of main.
	if err := m.wt.ReanchorOnMain(context.Background(), &f); err != nil {
		return func() tea.Msg {
			return noticeMsg{text: sanitize(fmt.Sprintf("%s: rebased but fork not re-anchored: %v", f.ID, err)), isErr: true}
		}
	}
	// The hand-off this settlement closes may have interrupted a stage
	// session that was mid-turn (the engine marks the rebase session it
	// swapped in when it does). Nothing else re-runs the stage that
	// interrupt stopped — no stage crossing opened an idle decision, the
	// finished rebase session's idle is judged here from git state, and
	// the interrupted session's stop reaches no loop — so the success
	// arm re-runs the stage itself, and skips the held-stop restore: any
	// item the interrupt held belonged to the interrupted run, and a
	// fresh run that still needs to stop will stop again. The settled
	// session must still be the live one; a dispatch that took the card
	// over since settles itself. Verify needs none of this — its arm
	// re-runs on any success.
	interrupted := false
	if m.engine != nil && f.Stage != domain.StageVerify {
		if s := m.engine.Get(id); s != nil {
			snap := s.Snapshot()
			interrupted = snap.Rebase && snap.ReplacedRunning &&
				!msg.settled.IsZero() && snap.StartedAt.Equal(msg.settled)
		}
	}
	if f.Stage != domain.StageVerify && wasHeld && !interrupted {
		// The stop the card was waiting at still stands — a failed stage
		// has still not run, a gate is still unanswered — and the resolve
		// session, which carries no verdict, must not read as the stage
		// having ended: the same reasoning as the failure arm above.
		m.dropSession(id)
		m.inbox.put(held)
	}
	if drifted != nil {
		m.driftCleared(id)
	}
	if f.Stage != domain.StageVerify {
		if interrupted {
			return func() tea.Msg {
				m.dropSession(f.ID) // the finished rebase session is stale
				if err := m.engine.Run(f); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: string(f.ID) + " rebased onto " + m.baseBranch(f) + " → re-running " + string(f.Stage), reload: true}
			}
		}
		m.notice = noticeMsg{text: string(id) + " rebased onto " + m.baseBranch(f)}
		// the base moved: re-measure what its baseline excused (at
		// verify, the re-run below does it as the stage starts)
		return tea.Batch(m.loadRows, m.rebaselineCmd(id))
	}
	return func() tea.Msg {
		m.dropSession(f.ID) // the finished rebase session is stale
		if err := m.engine.Run(f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + " rebased onto " + m.baseBranch(f) + " → re-verifying", reload: true}
	}
}

// driftCleared rewrites a failure the fork drift caused once a rebase has
// cleared it. The stop itself stands — the stage still has not run — but
// its sentence was the drift, and left in place it went on telling the
// reader to rebase a card that just was, above a "try again" that will
// now work.
func (m *Shell) driftCleared(id domain.FeatureID) {
	// the row too, in the same update: its answers must not go on
	// offering the rebase under a sentence that says it happened
	for i := range m.rows {
		if m.rows[i].F.ID == id {
			m.rows[i].Drift = nil
		}
	}
	it, ok := m.inbox.get(id)
	if !ok || it.Kind != attnFailure {
		return
	}
	stage := "the stage"
	if f, err := m.store.GetFeature(context.Background(), id); err == nil {
		stage = string(f.Stage)
	}
	it.Text = "rebased onto " + m.baseBranchOf(id) + " — the fork drift that stopped " + stage + " is cleared; try again to run it"
	m.inbox.put(it)
}

// inboxIDs is the set of cards with a stop in the inbox right now.
func (m *Shell) inboxIDs() map[domain.FeatureID]bool {
	ids := map[domain.FeatureID]bool{}
	for _, it := range m.inbox.list() {
		ids[it.Feature] = true
	}
	return ids
}
