package ui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// mergeReadyMsg carries a squash merge that passed its preconditions and
// awaits the user's commit message, or the guard error that stops it.
// thenDone marks a merge launched from the verify→done gate: landing it
// also moves the feature to Done.
type mergeReadyMsg struct {
	f        domain.Feature
	thenDone bool
	// warn carries a non-blocking pre-land caution (agent attribution
	// found in branch commit messages); the merge still proceeds.
	warn string
	err  error
}

// prepareMerge checks the merge preconditions off the render loop. On
// success it opens the commit-message dialog, which drafts a suggested
// landing message from the spec and the branch — the user still approves
// and can edit it; nothing lands without an explicit ctrl+s. Anything
// still uncommitted in the worktree (including untracked files — new
// source files are agent work like any other) is committed as a final
// checkpoint first: gummi owns the branch's commits, and only committed
// work merges.
func (m *Shell) prepareMerge(f domain.Feature, thenDone bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if _, err := m.wt.CommitAll(ctx, &f, string(f.ID)+": final checkpoint"); err != nil {
			return mergeReadyMsg{err: err}
		}
		if dirty, err := m.wt.MainTrackedDirty(ctx); err != nil {
			return mergeReadyMsg{err: err}
		} else if dirty {
			return mergeReadyMsg{err: errors.New("main checkout has uncommitted changes — commit or stash them before merging")}
		}
		// stale-row safety: the board flag may predate an outside merge
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return mergeReadyMsg{err: err}
		} else if landed {
			return mergeReadyMsg{err: errors.New(string(f.ID) + " already landed on main — press c to clean up")}
		}
		// pre-land provenance scan: warn (never block) when branch commits
		// carry agent attribution — the squash discards their messages, but
		// the user should know before landing, not after
		var warn string
		if leaks, err := m.wt.ProvenanceWarnings(ctx, &f); err == nil && len(leaks) > 0 {
			warn = string(f.ID) + ": branch commits carry agent attribution — " + strings.Join(leaks, ", ")
		}
		return mergeReadyMsg{f: f, thenDone: thenDone, warn: warn}
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

// commitDraftMsg carries a best-effort draft landing message for the open
// commit-message dialog. gen tags the pass that produced it: only the
// latest generation is applied, so a late reply from a re-draft (ctrl+r)
// or a dialog closed by esc is dropped rather than clobbering.
type commitDraftMsg struct {
	f     domain.FeatureID
	gen   int
	draft string
}

// mergeThenDoneMsg asks the shell to run the merge flow as the
// verify→done gate: collect the commit message from the user,
// squash-merge, and only then move the feature to Done.
type mergeThenDoneMsg struct {
	f domain.Feature
}
