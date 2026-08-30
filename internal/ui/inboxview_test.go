package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

func TestInboxTabGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.inbox.add("FD-042", attnGate, "implement finished — review & advance")
	m.inbox.add("FD-049", attnFailure, "provider rate-limited")
	m.setTab(TabInbox)
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestInboxTabEmptyGolden covers the empty-queue message, distinct
// enough from the populated render that a single golden can't stand in
// for both.
func TestInboxTabEmptyGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.setTab(TabInbox)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestInboxTabMoveSelection(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.inbox.add("FD-047", attnQuestion, "which approach?")
	m.setTab(TabInbox)
	if m.inboxSel != 0 {
		t.Fatalf("inboxSel = %d, want 0 on open", m.inboxSel)
	}
	m.inboxKey("j")
	if m.inboxSel != 1 {
		t.Fatalf("j should move the inbox cursor forward, got %d", m.inboxSel)
	}
	// clamped at the end
	m.inboxKey("j")
	if m.inboxSel != 1 {
		t.Fatalf("j should clamp at the last item, got %d", m.inboxSel)
	}
	m.inboxKey("k")
	if m.inboxSel != 0 {
		t.Fatalf("k should move the inbox cursor back, got %d", m.inboxSel)
	}
	m.inboxKey("k")
	if m.inboxSel != 0 {
		t.Fatalf("k should clamp at the first item, got %d", m.inboxSel)
	}
}

func TestInboxTabEnterJumpsToCard(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.setTab(TabInbox)
	m.inboxSel = 0
	m.inboxKey("enter")
	if m.tab != TabBoard {
		t.Fatalf("enter should switch back to the board tab, got %v", m.tab)
	}
	if m.rows[m.sel].F.ID != "FD-044" {
		t.Fatalf("enter should select the jumped-to card, got %s", m.rows[m.sel].F.ID)
	}
	if !m.cardOpen {
		t.Fatal("enter should open the card page — the decision is pinned there, not on the backlog row")
	}
	if m.inbox.len() != 0 {
		t.Fatal("enter should clear the item it jumped to")
	}
}

// TestInboxViewOldestFirst: the tab renders items oldest-first by At,
// independent of the order they were added in.
func TestInboxViewOldestFirst(t *testing.T) {
	m := populatedShell(120, 34)
	newer := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	older := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	// added newer-first, so a naive insertion-order render would get this
	// backwards
	m.inbox.seed(attnItem{Feature: "FD-042", Kind: attnGate, Text: "implement finished", At: newer})
	m.inbox.seed(attnItem{Feature: "FD-049", Kind: attnFailure, Text: "provider rate-limited", At: older})
	m.setTab(TabInbox)
	view := ansi.Strip(m.inboxView(120, 34))
	iOlder := strings.Index(view, "FD-049")
	iNewer := strings.Index(view, "FD-042")
	if iOlder == -1 || iNewer == -1 || iOlder > iNewer {
		t.Fatalf("expected the older item (FD-049) before the newer one (FD-042):\n%s", view)
	}
}

func TestInboxTabDismiss(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.inbox.add("FD-047", attnQuestion, "which approach?")
	m.setTab(TabInbox)
	m.inboxSel = 0
	m.inboxKey("x")
	if m.inbox.len() != 1 {
		t.Fatalf("x should dismiss the selected item, len = %d", m.inbox.len())
	}
	if m.tab != TabInbox {
		t.Fatal("dismissing should not leave the inbox tab")
	}
	if got := m.inbox.list()[0].Feature; got != "FD-047" {
		t.Fatalf("wrong item survived dismissal: %s", got)
	}
}

func TestInboxTabDismissClampsSelection(t *testing.T) {
	// dismissing the last row must not leave the cursor pointing past the
	// end of the shrunk queue.
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.inbox.add("FD-047", attnQuestion, "which approach?")
	m.setTab(TabInbox)
	m.inboxSel = 1
	m.inboxKey("x")
	if m.inboxSel != 0 {
		t.Fatalf("inboxSel = %d after dismissing the last row, want 0", m.inboxSel)
	}
}

func TestInboxTabTopUpOnlyBudgetItems(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnBudget, "verify hit its budget")
	m.inbox.add("FD-047", attnGate, "review ready")
	m.setTab(TabInbox)

	// u on the non-budget item (index 1) is a no-op.
	m.inboxSel = 1
	m.inboxKey("u")
	if m.inbox.len() != 2 {
		t.Fatalf("u on a non-budget item acted: len = %d, want 2", m.inbox.len())
	}

	// u on the budget item tops it up, which clears it (topUpBudget
	// removes it immediately rather than waiting on the engine round trip).
	m.inboxSel = 0
	m.inboxKey("u")
	if m.inbox.len() != 1 {
		t.Fatalf("u on the budget item did not clear it: len = %d", m.inbox.len())
	}
	if got := m.inbox.list()[0].Feature; got != "FD-047" {
		t.Fatalf("wrong item survived top-up: %s", got)
	}
}

func TestInboxTabBindingsAnswerHelp(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.setTab(TabInbox)
	name, bs := m.activeSurface()
	if name != "inbox" {
		t.Fatalf("active surface = %q, want inbox", name)
	}
	assertLabel(t, bs, "enter", "go")
	assertLabel(t, bs, "x", "dismiss")
	assertLabel(t, bs, "tab", "next tab")
}

// TestInboxTabViewShowsSuggestion pins the ↳ suggestion line to the
// selected row only — the same behaviour the old modal had, moved onto
// the tab (inboxview.go's inboxView).
func TestInboxTabViewShowsSuggestion(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-042", attnGate, "implement finished — review & advance")
	m.setTab(TabInbox)
	m.inboxSel = 0
	view := ansi.Strip(m.inboxView(100, 30))
	if !strings.Contains(view, "↳") {
		t.Errorf("selected item should show its suggested next action:\n%s", view)
	}
}
