package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
)

// prPullDoneMsg carries a pull-PR-review run's outcome: the notice naming
// what changed, and whether it wrote at least one new annotation — the
// signal that earns the diff view auto-opening on the card the comments
// just landed on, rather than merely counting them.
type prPullDoneMsg struct {
	f            domain.Feature
	notice       noticeMsg
	newlyWritten bool
}

// pullPRReview fetches f's linked PR's unresolved review threads and
// anchors them onto the card's current worktree diff, writing one
// DiffAnnotation per thread via pr.Ingest — the same loop `gummi pr
// comments --ingest` runs. It runs under the card's lock since it writes
// annotations. The store's (Feature, SourceRef) uniqueness already makes a
// repeated pull idempotent, which is what makes the action safe to run
// twice: pr.Ingest adds no new de-duplication of its own.
func (m *Shell) pullPRReview(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx := context.Background()
		// stale-row safety: the board snapshot may predate an unlink that
		// landed while this action waited for the card lock, so re-fetch
		// before trusting PullRequest rather than testing the captured f.
		cur, err := m.store.GetFeature(ctx, f.ID)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		f = cur
		if f.PullRequest.Empty() {
			return noticeMsg{text: string(f.ID) + " has no linked PR", isErr: true}
		}
		if m.fetchPRReviewThreads == nil {
			return noticeMsg{text: "pull PR review is unavailable — no PR backend wired", isErr: true}
		}
		threads, _, headSHA, err := m.fetchPRReviewThreads(ctx, f.PullRequest)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": fetching review threads: " + err.Error()), isErr: true}
		}
		if headSHA != "" && headSHA != f.PullRequest.HeadSHA {
			refreshed := f.PullRequest
			refreshed.HeadSHA = headSHA
			_ = m.store.SetPullRequest(ctx, f.ID, refreshed) // side effect only; the ingest below runs off the fetched threads either way
		}
		diff, diffErr := m.wt.Diff(ctx, &f)
		var worktreeLines []string
		if diffErr == nil {
			worktreeLines = strings.Split(strings.TrimRight(diff, "\n"), "\n")
		}
		res, err := pr.Ingest(ctx, m.store, f.ID, worktreeLines, threads)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		text := fmt.Sprintf("%s: %d new review thread(s) → diff (%d already ingested, %d could not be anchored)",
			f.ID, res.Written, res.AlreadyExisting, res.Orphaned)
		return prPullDoneMsg{f: f, notice: noticeMsg{text: text}, newlyWritten: res.Written > 0}
	})
}
