package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
)

// attachedBoard is the same fixture board on a real workspace — the
// board's keys are refused while detached, so key routing needs one.
func attachedBoard(t *testing.T, w, h int) *Shell {
	t.Helper()
	m := populatedShell(w, h)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)
	return m
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestBacklogCardPage120(t *testing.T) {
	m := populatedShell(120, 34)
	m.openCard()
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestSwitchingTabKeepsTheCardPage: the card page is a board surface
// like the spec view and the diff, and the rule for all of them is that
// leaving the tab hides them and coming back restores them — never
// discards (DESIGN §6). It used to be the one exception, which made a
// glance at the inbox cost you your place: the card you were reading was
// closed behind you, draft and all, and had to be found again.
func TestSwitchingTabKeepsTheCardPage(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter should open the card page")
	}
	open := m.rows[m.sel].F.ID
	m = typeString(t, m, "a draft I have not sent")

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.tab != TabInbox {
		t.Fatalf("tab should move to the inbox tab, got %v", m.tab)
	}
	// hidden, not closed: the board's own surfaces are not drawn or
	// listening while another tab is up, which boardSurfacesLive answers
	// for all of them at once.
	if m.boardSurfacesLive() {
		t.Error("the board's surfaces should not be live on the inbox tab")
	}

	// all the way round and back
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.tab != TabBoard {
		t.Fatalf("cycling should return to the board tab, got %v", m.tab)
	}
	if !m.cardOpen {
		t.Fatal("coming back to the board dropped the open card page")
	}
	if got := m.rows[m.sel].F.ID; got != open {
		t.Errorf("came back to %s, want the card that was open (%s)", got, open)
	}
	if got := m.threadInput.Value(); got != "a draft I have not sent" {
		t.Errorf("draft = %q, want the unsent line still there", got)
	}
}

// TestThreadDraftScopedPerCard is F5: a line typed on one card must not
// show up on another's, whichever way the two cards were reached —
// stepCard (alt+j/alt+k), closeCard+openCard (esc, then re-selecting a
// different row), and the tab round-trip TestSwitchingTabKeepsTheCardPage
// already covers for a single card. Each card keeps its own unsent line;
// leaving hides it (like the page itself), never discards it.
func TestThreadDraftScopedPerCard(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	a := m.rows[m.sel].F.ID

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open card A
	m = typeString(t, m, "kickoff note for A")

	m = press(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt}) // step to B
	b := m.rows[m.sel].F.ID
	if b == a {
		t.Fatal("precondition: alt+j should have moved to a different card")
	}
	if got := m.threadInput.Value(); got != "" {
		t.Fatalf("card B opened with A's draft still in the box: %q", got)
	}
	m = typeString(t, m, "a different note for B")

	m = press(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModAlt}) // step back to A
	if m.rows[m.sel].F.ID != a {
		t.Fatalf("alt+k landed on %s, want %s", m.rows[m.sel].F.ID, a)
	}
	if got := m.threadInput.Value(); got != "kickoff note for A" {
		t.Fatalf("A's draft was not restored by stepping back: %q", got)
	}

	// leave the page entirely (closeCard) and come back through the
	// backlog list rather than stepCard, landing on B this time
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc should have closed the card page")
	}
	selectRow(t, m, b)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // openCard on B
	if got := m.threadInput.Value(); got != "a different note for B" {
		t.Fatalf("openCard on B did not restore B's draft: %q", got)
	}

	// and A's own draft is still there, untouched, when reopened the
	// same way
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	selectRow(t, m, a)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.threadInput.Value(); got != "kickoff note for A" {
		t.Fatalf("A's draft did not survive the round trip through B: %q", got)
	}
}

// TestBacklogEnterOpensAndEscCloses is the board tab's whole navigation
// contract: one keystroke in, one keystroke out.
func TestBacklogEnterOpensAndEscCloses(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	sel := m.rows[m.sel].F.ID

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter on the backlog should open the card page")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, string(sel)) {
		t.Errorf("card page does not show the selected card %s:\n%s", sel, view)
	}
	if !strings.Contains(view, "esc backlog") {
		t.Errorf("card page has no way back in its breadcrumb:\n%s", view)
	}

	// the composer owns the keyboard on arrival and keeps it, so leaving
	// is one press: esc no longer blurs into an accelerator layer that
	// has nothing on screen to show for itself
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc on the card page should return to the backlog in one press")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "BACKLOG") {
		t.Error("esc did not land back on the backlog list")
	}
}

// TestCardPageArrowsDriveTheActionList: the page has one list, so ↑↓ move
// it without anything first having to be focused with →.
func TestCardPageArrowsDriveTheActionList(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = toKeys(t, m)
	if !m.actionsOwnArrows() {
		t.Fatal("the card page's action list should own the arrow keys on arrival")
	}
	if m.actionCursor != 0 {
		t.Fatalf("action cursor should start at the recommendation, got %d", m.actionCursor)
	}
	m = press(t, m, key('j'))
	if m.actionCursor != 1 {
		t.Fatalf("j should move the action cursor, got %d", m.actionCursor)
	}
	m = press(t, m, key('k'))
	if m.actionCursor != 0 {
		t.Fatalf("k should move it back, got %d", m.actionCursor)
	}
	if m.sel != 1 {
		t.Fatalf("j/k must not move the card selection on the card page, sel=%d", m.sel)
	}
}

// TestCardPageJKStepsCards: scanning several cards should not mean a trip
// back to the list for each one.
func TestCardPageJKStepsCards(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = toKeys(t, m)
	first := m.rows[m.sel].F.ID

	m = press(t, m, key('J'))
	if !m.cardOpen {
		t.Fatal("J should stay on the card page")
	}
	next := m.rows[m.sel].F.ID
	if next == first {
		t.Fatal("J did not move to another card")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), string(next)) {
		t.Errorf("the page still shows %s after stepping to %s", first, next)
	}

	m = press(t, m, key('K'))
	if m.rows[m.sel].F.ID != first {
		t.Fatalf("K should step back to %s, got %s", first, m.rows[m.sel].F.ID)
	}
}

// TestBacklogListMovesSelection: j/k belong to the list at list level —
// the same keys that drive the action list one level in.
func TestBacklogListMovesSelection(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	start := m.sel
	m = press(t, m, key('j'))
	if m.sel == start {
		t.Fatal("j should move the backlog selection")
	}
	if m.cardOpen {
		t.Fatal("j should not open a card")
	}
	m = press(t, m, key('k'))
	if m.sel != start {
		t.Fatalf("k should move back to %d, got %d", start, m.sel)
	}
}

// TestBacklogKeepsTheCardVerbs: every verb still answers from the list,
// so muscle memory survives whichever tab it's read from.
func TestBacklogKeepsTheCardVerbs(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, key('S'))
	if m.sortMode != SortSeverity {
		t.Error("S should still toggle the sort from the backlog list")
	}
	m = press(t, m, key('n'))
	if m.Overlay.Top() == nil {
		t.Error("n should still raise the new-feature form from the backlog list")
	}
}

// TestBacklogScrollsToTheSelection: the column never scrolled (it could
// not outgrow a third of the screen without the board being unusable
// anyway); a full-width backlog is meant to hold more cards than fit.
func TestBacklogScrollsToTheSelection(t *testing.T) {
	m := populatedShell(120, 20)
	m.rows = nil
	for i := 1; i <= 40; i++ {
		m.rows = append(m.rows, row(i, "card "+itoa(i), domain.StageTodo, "thrifty", false))
	}
	m.sel = 39
	m.syncActionFocus()

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "FD-040") {
		t.Errorf("the selected card scrolled off the backlog:\n%s", view)
	}
	if !strings.Contains(view, "more") {
		t.Errorf("a clipped backlog should say how much is off-screen:\n%s", view)
	}
	// the window is bounded by the pane: a 20-row terminal cannot be
	// showing the first card and the fortieth at once.
	if strings.Contains(view, "FD-001 ") {
		t.Errorf("the backlog rendered past the bottom of the pane:\n%s", view)
	}
}

// TestBacklogBindingsMatchTheLevel: the status bar and ? overlay read
// from these tables, so the level's keys must be what they list — no →
// into a region that doesn't exist here, and enter renamed at each level.
func TestBacklogBindingsMatchTheLevel(t *testing.T) {
	m := populatedShell(120, 34)

	name, bs := m.activeSurface()
	if name != "backlog" {
		t.Fatalf("active surface = %q, want backlog", name)
	}
	assertNoKey(t, bs, "→")
	assertLabel(t, bs, "enter", "open card")

	// a card opens with its composer holding the keyboard, so the bar
	// teaches the line's keys first. FD-042 (implement, nothing running)
	// has an open decision, so enter names the highlighted answer rather
	// than plain send — the bar states what enter is about to do before
	// it does it (DESIGN §6.3).
	m.openCard()
	name, bs = m.activeSurface()
	if name != "card" {
		t.Fatalf("active surface = %q, want card", name)
	}
	assertNoKey(t, bs, "→")
	assertLabel(t, bs, "↑↓", "choose")
	assertLabel(t, bs, "enter", "run implement")
	// esc leaves the page from the line — there is no accelerator layer
	// between the composer and the backlog any more
	assertLabel(t, bs, "esc", "backlog")
	assertLabel(t, bs, "alt+j/k", "prev/next")

	// that layer still exists for a card another process drives, which
	// withholds the composer; blurring is how such a card arrives
	m.blurThreadInput()
	_, bs = m.activeSurface()
	assertNoKey(t, bs, "→")
	assertLabel(t, bs, "enter", "run action")
	assertLabel(t, bs, "esc", "backlog")
	assertLabel(t, bs, "J/K", "prev/next")

	// with no decision open (a done card), enter is back to send: a bare
	// composer means exactly one thing — nothing is waiting on a person
	m.threadInput.Focus()
	m.sel = 5 // FD-039, done
	_, bs = m.activeSurface()
	assertLabel(t, bs, "enter", "send")
	assertLabel(t, bs, "↑", "actions")
}

func assertNoKey(t *testing.T, bs []binding, key string) {
	t.Helper()
	for _, b := range bs {
		if b.key == key {
			t.Errorf("key %q should not be offered here (%q)", key, b.help)
		}
	}
}

func assertLabel(t *testing.T, bs []binding, key, label string) {
	t.Helper()
	for _, b := range bs {
		if b.key == key {
			if b.label != label {
				t.Errorf("key %q label = %q, want %q", key, b.label, label)
			}
			return
		}
	}
	t.Errorf("key %q is missing from the table", key)
}
