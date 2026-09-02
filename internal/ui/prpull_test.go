package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
)

// linkFixture links FD-001 (from diffWorkspace: StageReview, a worktree
// with an uncommitted "+second line" README.md change) to a fake PR and
// reloads the rows so the board's own snapshot carries the link.
func linkFixture(t *testing.T) *Shell {
	t.Helper()
	m, _ := diffWorkspace(t)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: strings.Repeat("a", 40)}
	if err := m.store.SetPullRequest(context.Background(), "FD-001", ref); err != nil {
		t.Fatal(err)
	}
	return pump(t, m, m.loadRows)
}

func runPRPull(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.sel = 0
	cmd := m.runCardAction(cardAction{id: "prpull"})
	if cmd == nil {
		t.Fatalf("prpull produced no command (notice %q)", m.notice.text)
	}
	return pump(t, m, cmd)
}

// TestPRPullIngestsThreadsAndOpensDiff exercises the thin tracer-bullet
// slice: a stubbed fetch returns one thread anchoring cleanly onto the
// worktree diff and one that cannot be anchored, the notice reports both
// counts, the annotations land in the store, and the diff view opens on
// the card since at least one annotation is new.
func TestPRPullIngestsThreadsAndOpensDiff(t *testing.T) {
	m := linkFixture(t)
	hit := pr.ReviewThread{
		Id: "PRRT_1", Path: "README.md", DiffHunk: "@@ -1,1 +1,2 @@\n+second line",
		Comments: []pr.ThreadComment{{Id: "c1", AuthorLogin: "reviewer", Body: "nice addition"}},
	}
	miss := pr.ReviewThread{
		Id: "PRRT_2", Path: "MISSING.md", DiffHunk: "@@ -1,1 +1,2 @@\n+not present anywhere",
		Comments: []pr.ThreadComment{{Id: "c2", AuthorLogin: "reviewer", Body: "orphan me"}},
	}
	m.fetchPRReviewThreads = func(context.Context, domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return []pr.ReviewThread{hit, miss}, nil, "", nil
	}

	m = runPRPull(t, m)

	if !strings.Contains(m.notice.text, "2 new review thread(s)") {
		t.Errorf("notice = %q, want 2 new threads", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "0 already ingested") {
		t.Errorf("notice = %q, want 0 already ingested", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "1 could not be anchored") {
		t.Errorf("notice = %q, want 1 orphaned", m.notice.text)
	}
	if m.notice.isErr {
		t.Errorf("notice reported as an error: %q", m.notice.text)
	}

	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 2 {
		t.Fatalf("stored %d annotations, want 2", len(anns))
	}

	if m.diff == nil || m.diff.f.ID != "FD-001" {
		t.Fatal("pulling new review comments should open the diff view on the card")
	}
}

// TestPRPullReRunReportsNoNewAnnotations proves a repeated pull is safe
// to press twice: the store's (Feature, SourceRef) uniqueness makes the
// second run's threads all "already ingested", and with nothing new
// written it does not force the diff view open a second time.
func TestPRPullReRunReportsNoNewAnnotations(t *testing.T) {
	m := linkFixture(t)
	hit := pr.ReviewThread{
		Id: "PRRT_1", Path: "README.md", DiffHunk: "@@ -1,1 +1,2 @@\n+second line",
		Comments: []pr.ThreadComment{{Id: "c1", AuthorLogin: "reviewer", Body: "nice addition"}},
	}
	m.fetchPRReviewThreads = func(context.Context, domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return []pr.ReviewThread{hit}, nil, "", nil
	}
	m = runPRPull(t, m)
	if !strings.Contains(m.notice.text, "1 new review thread(s)") {
		t.Fatalf("first pull notice = %q, want 1 new thread", m.notice.text)
	}

	// close the diff view the first pull opened, then pull again — a
	// second open here would be the bug this test guards against.
	m.diff = nil
	m = runPRPull(t, m)
	if !strings.Contains(m.notice.text, "0 new review thread(s)") {
		t.Errorf("second pull notice = %q, want 0 new threads", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "1 already ingested") {
		t.Errorf("second pull notice = %q, want 1 already ingested", m.notice.text)
	}
	if m.diff != nil {
		t.Error("a run with nothing newly written should not open the diff view")
	}
}

// TestPRPullRefusesOnUnlinkRaceUnderLock proves the stale-row guard
// re-reads the store from inside the card lock rather than trusting the
// captured domain.Feature the board dispatched with: an unlink that lands
// after the snapshot was taken (but before the lock is held) must still be
// caught, or a pull can go ahead and fetch/ingest against a PR the card no
// longer claims to be linked to.
func TestPRPullRefusesOnUnlinkRaceUnderLock(t *testing.T) {
	m := linkFixture(t)
	stale := m.rows[0].F
	if stale.PullRequest.Empty() {
		t.Fatal("fixture row should carry the linked PR")
	}

	if err := m.store.SetPullRequest(context.Background(), "FD-001", domain.PullRequestRef{}); err != nil {
		t.Fatal(err)
	}

	m.fetchPRReviewThreads = func(context.Context, domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		t.Fatal("pull should refuse before reaching the fetch backend")
		return nil, nil, "", nil
	}

	cmd := m.pullPRReview(stale)
	if cmd == nil {
		t.Fatal("pullPRReview(stale) produced no command")
	}
	m = pump(t, m, cmd)

	if !m.notice.isErr || !strings.Contains(m.notice.text, "no linked PR") {
		t.Errorf("notice = %q, want a no-linked-PR refusal", m.notice.text)
	}
	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 0 {
		t.Errorf("stored %d annotations, want 0 — the race should have been caught before ingest", len(anns))
	}
}

// TestPRPullRefusesWithoutALink guards the stale-row re-check: a card
// whose selected-row snapshot carries no PullRequest refuses rather than
// calling the (nil, in this scaffold) fetch backend.
func TestPRPullRefusesWithoutALink(t *testing.T) {
	m, _ := diffWorkspace(t)
	m.sel = 0
	cmd := m.runCardAction(cardAction{id: "prpull"})
	if cmd != nil {
		t.Fatal("prpull on an unlinked card should not dispatch a command")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "no linked PR") {
		t.Errorf("notice = %q, want a no-linked-PR refusal", m.notice.text)
	}
}
