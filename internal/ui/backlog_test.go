package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
)

// backlogShell is populatedShell switched into the backlog layout, sized
// after the switch so the layout is the one under test. Detached, like
// populatedShell: enough for anything that only renders.
func backlogShell(w, h int) *Shell {
	m := populatedShell(w, h)
	m.toggleLayout()
	m.notice = noticeMsg{}
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return model.(*Shell)
}

// attachedBoard is the same fixture board on a real workspace — the
// board's keys are refused while detached, so key routing needs one.
func attachedBoard(t *testing.T, w, h int) *Shell {
	t.Helper()
	m := populatedShell(w, h)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)
	return m
}

// attachedBacklog is attachedBoard already in the backlog layout.
func attachedBacklog(t *testing.T, w, h int) *Shell {
	t.Helper()
	m := attachedBoard(t, w, h)
	return press(t, m, key('L'))
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestBacklogView80(t *testing.T) {
	golden.RequireEqual(t, []byte(backlogShell(80, 24).View().Content))
}

func TestBacklogView120(t *testing.T) {
	golden.RequireEqual(t, []byte(backlogShell(120, 34).View().Content))
}

func TestBacklogCardPage120(t *testing.T) {
	m := backlogShell(120, 34)
	m.openCard()
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestBacklogTakesTheFullWidth: the point of the mode is the space, so
// the kanban column must actually be gone — not merely unpainted.
func TestBacklogTakesTheFullWidth(t *testing.T) {
	m := backlogShell(120, 34)
	if m.layout.KanbanVisible {
		t.Fatal("backlog layout should not carve out a kanban column")
	}
	if m.layout.Main.Dx() != 120 || m.layout.Main.Min.X != 0 {
		t.Fatalf("main pane should span the terminal: %+v", m.layout.Main)
	}
	if strings.Contains(m.View().Content, "│") {
		t.Error("backlog layout still paints the column separator")
	}
}

// TestLayoutToggleRoundTrips: L is a toggle, and coming back restores the
// split board's geometry rather than leaving a half-switched shell.
func TestLayoutToggleRoundTrips(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	if m.viewMode != ModeSplit {
		t.Fatal("a fresh shell should start on the split board")
	}
	m = press(t, m, key('L'))
	if m.viewMode != ModeBacklog || m.layout.KanbanVisible {
		t.Fatalf("L should switch to the backlog: mode=%v kanban=%v", m.viewMode, m.layout.KanbanVisible)
	}
	m = press(t, m, key('L'))
	if m.viewMode != ModeSplit || !m.layout.KanbanVisible {
		t.Fatalf("L should switch back to the split board: mode=%v kanban=%v", m.viewMode, m.layout.KanbanVisible)
	}
}

// TestLayoutToggleClosesTheCardPage: the card page exists only in the
// backlog layout, so switching away has to leave it.
func TestLayoutToggleClosesTheCardPage(t *testing.T) {
	m := attachedBacklog(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter should open the card page")
	}
	m = press(t, m, key('L'))
	if m.cardOpen {
		t.Fatal("switching layout should close the card page")
	}
}

// TestBacklogEnterOpensAndEscCloses is the mode's whole navigation
// contract: one keystroke in, one keystroke out.
func TestBacklogEnterOpensAndEscCloses(t *testing.T) {
	m := attachedBacklog(t, 120, 34)
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

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc on the card page should return to the backlog")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "BACKLOG") {
		t.Error("esc did not land back on the backlog list")
	}
}

// TestCardPageArrowsDriveTheActionList: the page has one list, so ↑↓ move
// it without anything first having to be focused with →.
func TestCardPageArrowsDriveTheActionList(t *testing.T) {
	m := attachedBacklog(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m := attachedBacklog(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m := attachedBacklog(t, 120, 34)
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

// TestBacklogKeepsTheCardVerbs: the mode changes where cards are shown,
// not what the board can do to them — every verb still answers from the
// list, so muscle memory survives the switch.
func TestBacklogKeepsTheCardVerbs(t *testing.T) {
	m := attachedBacklog(t, 120, 34)
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
	m := backlogShell(120, 20)
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
	m := backlogShell(120, 34)

	name, bs := m.activeSurface()
	if name != "backlog" {
		t.Fatalf("active surface = %q, want backlog", name)
	}
	assertNoKey(t, bs, "→")
	assertLabel(t, bs, "enter", "open card")

	m.openCard()
	name, bs = m.activeSurface()
	if name != "card" {
		t.Fatalf("active surface = %q, want card", name)
	}
	assertNoKey(t, bs, "→")
	assertLabel(t, bs, "enter", "run action")
	assertLabel(t, bs, "esc", "backlog")
	assertLabel(t, bs, "J/K", "prev/next")
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
