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
type commitDraftMsg struct {
	f     domain.Feature
	draft string
	err   error
}

// startMergeDraft checks the merge preconditions off the render loop and
// drafts the squash-commit message: a scribe pass over the branch diff
// when an engine is wired, a plain "<ID>: <title>" template otherwise —
// drafting is a convenience and never blocks the merge. Untracked files
// in the worktree don't block either: only committed work merges, and
// they stay behind for the later cleanup.
func (m *Shell) startMergeDraft(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if dirty, err := m.wt.TrackedDirty(ctx, &f); err != nil {
			return commitDraftMsg{err: err}
		} else if dirty {
			return commitDraftMsg{err: errors.New(string(f.ID) + " has uncommitted changes on its branch — commit them before merging")}
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
		return commitDraftMsg{f: f, draft: draft}
	}
}

// squashMergeFeature lands the branch on main as one commit carrying the
// user-approved message. Landed is re-checked at run time so a stale
// board row (or a dialog left open across an outside merge) can't land
// the work twice.
func (m *Shell) squashMergeFeature(f domain.Feature, message string) tea.Cmd {
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
		return noticeMsg{text: string(f.ID) + " squash-merged into main — press c to clean up"}
	}
}
