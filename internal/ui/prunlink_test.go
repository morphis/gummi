package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// TestPRUnlinkClearsRefAndUnblocksMerge covers step 4's own test: unlink
// clears PullRequestRef, and m merge — run immediately after, on the same
// card — no longer refuses it (merge.go's prepareMerge guard).
func TestPRUnlinkClearsRefAndUnblocksMerge(t *testing.T) {
	m, _, _ := mergeFixture(t)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://github.com/o/r/pull/7"}
	if err := m.store.SetPullRequest(context.Background(), "FD-001", ref); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m.sel = 0

	// linked: merge refuses, naming the PR.
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "linked to o/r#7") {
		t.Fatalf("merge on a linked card: notice = %q, want the linked-PR refusal", m.notice.text)
	}

	if cmd := m.runCardAction(cardAction{id: "prunlink"}); cmd != nil {
		t.Fatal("prunlink should raise a confirm, not dispatch directly")
	}
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok {
		t.Fatalf("prunlink did not open a confirm dialog (notice %q)", m.notice.text)
	}
	if !strings.Contains(d.question, "o/r#7") {
		t.Errorf("confirm question = %q, want the linked PR named", d.question)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.Overlay.Top() != nil {
		t.Fatal("confirm dialog should have closed")
	}
	if !strings.Contains(m.notice.text, "unlinked from o/r#7") {
		t.Errorf("notice = %q, want an unlinked confirmation", m.notice.text)
	}

	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if !f.PullRequest.Empty() {
		t.Errorf("PullRequest = %+v, want cleared", f.PullRequest)
	}

	// unlinked: merge stops refusing, immediately, on the very same card.
	m = pump(t, m, m.loadRows)
	m = pressMerge(t, m)
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("merge should no longer refuse after unlink (notice %q)", m.notice.text)
	}
}

// TestPRUnlinkRefusesWithoutALink guards the run-time re-check (the store
// read inside unlinkPR, not the stale row cardActionsFor already gates on)
// against a card that carries no link at all.
func TestPRUnlinkRefusesWithoutALink(t *testing.T) {
	m, _, _ := mergeFixture(t)
	m.sel = 0
	if cmd := m.runCardAction(cardAction{id: "prunlink"}); cmd != nil {
		t.Fatal("prunlink on an unlinked card should not dispatch a command")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "no linked PR") {
		t.Errorf("notice = %q, want a no-linked-PR refusal", m.notice.text)
	}
}
