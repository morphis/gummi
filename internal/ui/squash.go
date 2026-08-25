package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// squashReadyMsg carries a squash-in-place that passed its preconditions
// and awaits the user's commit message, or the guard/probe error that
// stops it. openThreads and prURL feed the linked-PR warn-and-confirm
// gate: a linked PR does not block squash, but open review threads must
// be acknowledged before collapsing.
type squashReadyMsg struct {
	f           domain.Feature
	openThreads int
	prURL       string
	err         error
}

// squashNoticeErr carries a fully-formed, ID-prefixed notice string that
// bypasses the generic "squash failed:" wrapper in the shell. It is used
// for the landed guard so its redirect text is emitted verbatim.
type squashNoticeErr struct {
	text string
}

func (e squashNoticeErr) Error() string { return e.text }

// squashOpenDialogMsg asks the shell to open the commit-message dialog
// for a squash in place. It is sent from the linked-PR warn-and-confirm
// dialog's onConfirm so the dialog stack is popped before the new dialog
// is pushed.
type squashOpenDialogMsg struct {
	f domain.Feature
}

// prepareSquash checks the squash preconditions off the render loop. On
// success it carries the open review-thread count (if any) back to the
// shell so the user can acknowledge the risk before the commit-message
// dialog opens. A linked PR does not block squash, unlike merge.
func (m *Shell) prepareSquash(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// stale-row safety: the board flag may predate an outside merge.
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return squashReadyMsg{f: f, err: err}
		} else if landed {
			return squashReadyMsg{f: f, err: squashNoticeErr{text: string(f.ID) + " already landed on main — press c to clean up"}}
		}
		openThreads, prURL, err := m.probeOpenReviewThreads(ctx, f)
		if err != nil {
			return squashReadyMsg{f: f, err: err}
		}
		return squashReadyMsg{f: f, openThreads: openThreads, prURL: prURL}
	}
}

// collapseFeature rewrites the feature branch to one commit on its fork
// point in place. It re-checks landed state at run time so a stale board
// row cannot rewrite history after the work has already reached main.
func (m *Shell) collapseFeature(f domain.Feature, message string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return noticeMsg{text: string(f.ID) + " squash failed: " + err.Error(), isErr: true}
		} else if landed {
			return noticeMsg{text: string(f.ID) + " already landed on main — press c to clean up", isErr: true}
		}
		mgr, err := m.wt.ManagerFor(ctx, &f)
		if err != nil {
			return noticeMsg{text: string(f.ID) + " squash failed: " + err.Error(), isErr: true}
		}
		base, err := worktree.ResolveCollapseBase(ctx, m.store, mgr, &f)
		if err != nil {
			return noticeMsg{text: string(f.ID) + " squash failed: " + err.Error(), isErr: true}
		}
		if _, err := m.wt.Head(ctx, &f); err != nil {
			return noticeMsg{text: string(f.ID) + " squash failed: " + err.Error(), isErr: true}
		}
		sha, err := m.wt.Collapse(ctx, &f, message, base)
		if err != nil {
			return noticeMsg{text: string(f.ID) + " squash failed: " + err.Error(), isErr: true}
		}
		if sha == "" {
			return noticeMsg{text: string(f.ID) + " already collapsed, nothing to do"}
		}
		return noticeMsg{text: string(f.ID) + " squashed to " + sha + "\n  git push --force-with-lease origin " + f.BranchName(), reload: true}
	}
}
