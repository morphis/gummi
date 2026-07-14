package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
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
}

// rebaseSettledMsg carries the judged outcome of a finished
// rebase-resolve session: ok when the branch is rebased and the
// worktree clean; otherwise problem says what the git state shows.
type rebaseSettledMsg struct {
	f       domain.Feature
	ok      bool
	problem string
}

// offerAgentRebase pushes the hand-off confirm for a conflicted rebase.
// An agent session costs credits, so it never starts without a yes.
func (m *Shell) offerAgentRebase(msg rebaseConflictMsg) {
	f, files := msg.f, msg.files
	detail := "runs an agent session in the worktree"
	if f.Stage == domain.StageVerify {
		detail += "; verify re-runs after"
	}
	if len(files) > 0 {
		// git-derived file names; sanitize like every other notice
		detail = sanitize("conflicts: "+strings.Join(files, ", ")) + " — " + detail
	}
	m.Overlay.Push(&confirmDialog{
		id:        "agent-rebase",
		question:  "rebase " + string(f.ID) + " onto main hit conflicts — let the agent resolve them?",
		detail:    detail,
		onConfirm: func() tea.Cmd { return m.agentRebase(f, files) },
	})
}

// agentRebase dispatches the engine's rebase-resolve session.
func (m *Shell) agentRebase(f domain.Feature, files []string) tea.Cmd {
	return func() tea.Msg {
		if err := m.engine.RunRebase(context.Background(), f, files); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + ": agent dispatched to rebase onto main"}
	}
}

// judgeRebase reads the git state a finished rebase-resolve session
// left behind. The engine has already aborted anything mid-flight, so a
// clean worktree whose branch now carries main's HEAD is success — the
// agent's own claims are never consulted.
func (m *Shell) judgeRebase(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if dirty {
			return rebaseSettledMsg{f: f, problem: "the worktree was left dirty"}
		}
		if rebased, err := m.wt.RebasedOnMain(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if !rebased {
			return rebaseSettledMsg{f: f, problem: "the branch is still not rebased"}
		}
		return rebaseSettledMsg{f: f, ok: true}
	}
}

// rebaseSettled folds the judged outcome into the board: a failed agent
// rebase escalates to the human; a successful one at Verify re-runs the
// stage (the resolution is unreviewed agent work), and elsewhere the
// workflow's remaining stages already cover it.
func (m *Shell) rebaseSettled(msg rebaseSettledMsg) tea.Cmd {
	id := msg.f.ID
	if !msg.ok {
		m.raiseEscalation(id, "agent rebase failed — "+msg.problem+"; read the transcript (t), then resolve on the branch")
		m.notice = noticeMsg{text: string(id) + ": agent rebase failed — " + msg.problem, isErr: true}
		return m.loadRows
	}
	if msg.f.Stage != domain.StageVerify {
		m.notice = noticeMsg{text: string(id) + " rebased onto main"}
		return m.loadRows
	}
	f := msg.f
	return func() tea.Msg {
		m.dropSession(f.ID) // the finished rebase session is stale
		if err := m.engine.Run(f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + " rebased onto main → re-verifying"}
	}
}
