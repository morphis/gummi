package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// draftTimeout bounds the scribe pass so a hung agent can't wedge the
// merge flow; on expiry the editor opens with the plain template.
const draftTimeout = 90 * time.Second

// commitDraftMsg carries the drafted (or fallback) commit message for a
// pending squash merge, or the guard error that stops the merge.
// thenDone marks a merge launched from the verify→done gate: landing it
// also moves the feature to Done.
type commitDraftMsg struct {
	f        domain.Feature
	draft    string
	thenDone bool
	err      error
}

// startMergeDraft checks the merge preconditions off the render loop and
// drafts the squash-commit message: a scribe pass over the branch diff
// when an engine is wired, a plain "<ID>: <title>" template otherwise —
// drafting is a convenience and never blocks the merge. Anything still
// uncommitted in the worktree (including untracked files — new source
// files are agent work like any other) is committed as a final
// checkpoint first: gummi owns the branch's commits, and only committed
// work merges.
func (m *Shell) startMergeDraft(f domain.Feature, thenDone bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if _, err := m.wt.CommitAll(ctx, &f, string(f.ID)+": final checkpoint"); err != nil {
			return commitDraftMsg{err: err}
		}
		if dirty, err := m.wt.MainTrackedDirty(ctx); err != nil {
			return commitDraftMsg{err: err}
		} else if dirty {
			return commitDraftMsg{err: errors.New("main checkout has uncommitted changes — commit or stash them before merging")}
		}
		// stale-row safety: the board flag may predate an outside merge
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return commitDraftMsg{err: err}
		} else if landed {
			return commitDraftMsg{err: errors.New(string(f.ID) + " already landed on main — press c to clean up")}
		}
		var draft string
		if m.engine != nil {
			dctx, cancel := context.WithTimeout(ctx, draftTimeout)
			defer cancel()
			if d, err := m.engine.DraftCommitMessage(dctx, f); err == nil {
				draft = d
			}
		}
		if draft == "" {
			draft = string(f.ID) + ": " + f.Title
		}
		return commitDraftMsg{f: f, draft: draft, thenDone: thenDone}
	}
}

// squashMergeFeature lands the branch on main as one commit carrying the
// user-approved message. Landed is re-checked at run time so a stale
// board row (or a dialog left open across an outside merge) can't land
// the work twice. With thenDone set (the verify→done gate) a landed
// merge also moves the feature to Done — the user's "this is done"
// decision and the landing are one action.
func (m *Shell) squashMergeFeature(f domain.Feature, message string, thenDone bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if landed {
			return noticeMsg{text: string(f.ID) + " already landed on main — press c to clean up", isErr: true}
		}
		if err := m.wt.SquashMerge(ctx, &f, message); err != nil {
			var ce *worktree.MergeConflictError
			if errors.As(err, &ce) {
				// ce carries git-derived file names; sanitize like every
				// other notice before it reaches the terminal.
				return noticeMsg{text: sanitize(string(f.ID) + ": " + ce.Error() + " — rebase (r) to resolve, then retry"), isErr: true}
			}
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if thenDone {
			if _, err := m.store.Transition(ctx, f.ID, domain.StageDone, "user"); err != nil {
				return noticeMsg{text: sanitize(string(f.ID) + " squash-merged into main, but moving to done failed: " + err.Error()), isErr: true}
			}
			m.dropSession(f.ID)
			return noticeMsg{text: string(f.ID) + " squash-merged into main → done — press c to clean up"}
		}
		return noticeMsg{text: string(f.ID) + " squash-merged into main — press c to clean up"}
	}
}

// mergeThenDoneMsg asks the shell to run the merge flow as the
// verify→done gate: draft the commit message, confirm it with the user,
// squash-merge, and only then move the feature to Done.
type mergeThenDoneMsg struct {
	f domain.Feature
}
