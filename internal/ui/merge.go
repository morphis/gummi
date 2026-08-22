package ui

import (
	"context"
	"errors"
	"fmt"
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
		// a card lands either via its linked PR or locally, never both.
		if !f.PullRequest.Empty() {
			return mergeReadyMsg{err: fmt.Errorf("%s is linked to %s#%d (%s); land it via the PR, or run `gummi pr unlink %s` to land it locally instead",
				f.ID, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, f.ID)}
		}
		if _, err := m.wt.CommitAll(ctx, &f, string(f.ID)+": final checkpoint"); err != nil {
			return mergeReadyMsg{err: err}
		}
		if dirty, err := m.wt.MainTrackedDirty(ctx, &f); err != nil {
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
		if _, err := m.wt.SquashMerge(ctx, &f, message); err != nil {
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
				return noticeMsg{text: sanitize(string(f.ID) + " squash-merged into main, but moving to done failed: " + err.Error()), isErr: true, reload: true}
			}
			m.dropSession(f.ID)
			return noticeMsg{text: string(f.ID) + " squash-merged into main → done — press c to clean up", reload: true}
		}
		return noticeMsg{text: string(f.ID) + " squash-merged into main — press c to clean up", reload: true}
	}
}

// recordCommitDraftFail persists a squash-merge scribe pass's outcome on
// the feature (the failure reason, or "" cleared on a successful draft)
// and, once written, reflects it on the row's dashboard in place. It runs
// in a command because the store write can block on the single sqlite
// connection (SetMaxOpenConns(1)); the row reflection needs no board
// reload — CommitDraftFail is metadata for the feature's own already-open
// dashboard, with no git-state change for the board list to re-walk.
func (m *Shell) recordCommitDraftFail(id domain.FeatureID, reason string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.store.SetCommitDraftFail(ctx, id, reason); err != nil {
			return noticeMsg{text: sanitize("recording commit-draft outcome: " + err.Error()), isErr: true}
		}
		return commitDraftPersistedMsg{id: id, reason: reason}
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
	// reason is a non-empty explanation when the draft pass failed (empty
	// on success); guard marks a deliberate guard rejection so the dialog
	// renders it as a warning rather than a config fault.
	reason string
	guard  bool
}

// commitDraftPersistedMsg reports that a scribe pass's outcome was
// durably recorded on the feature (the failure reason, or "" cleared on a
// successful draft). The shell reflects it on the feature's own dashboard
// row in place — it is row metadata only, so no board reload is needed.
type commitDraftPersistedMsg struct {
	id     domain.FeatureID
	reason string
}

// mergeThenDoneMsg asks the shell to run the merge flow as the
// verify→done gate: collect the commit message from the user,
// squash-merge, and only then move the feature to Done.
type mergeThenDoneMsg struct {
	f domain.Feature
}
