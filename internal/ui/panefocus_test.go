package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// bandBG is the SGR the focused selection band opens with — the thing a
// row is looked up by when a test asks "is this row banded?".
func bandBG(t *testing.T, s *theme.Styles, focused bool) string {
	t.Helper()
	// Band on an empty row is exactly the band's opening sequence plus a
	// reset, which is the cheapest honest way to get it without exporting
	// the color slot.
	return strings.TrimSuffix(s.Band("", 0, focused), ansi.ResetStyle)
}

// bandedRows returns the rendered lines carrying the given band.
func bandedRows(view, bg string) []string {
	var out []string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, bg) {
			out = append(out, l)
		}
	}
	return out
}

// TestSelectedCardWearsFocusedBand: the selection is a full-width bar, not
// a lone ▸ — the complaint that started this was that the marker alone is
// too small to track with the arrow keys.
func TestSelectedCardWearsFocusedBand(t *testing.T) {
	m := populatedShell(120, 34)
	s := m.styles
	view := m.boardView(36, true)

	rows := bandedRows(view, bandBG(t, s, true))
	if len(rows) != 1 {
		t.Fatalf("want exactly one banded row on the board, got %d", len(rows))
	}
	if !strings.Contains(ansi.Strip(rows[0]), "FD-042") {
		t.Fatalf("banded row is not the selected card: %q", ansi.Strip(rows[0]))
	}
	if w := ansi.StringWidth(rows[0]); w != 36 {
		t.Fatalf("banded row width = %d, want the full column (36)", w)
	}
	// the unselected cards carry no band at all.
	if got := len(bandedRows(view, bandBG(t, s, false))); got != 0 {
		t.Fatalf("focused board painted %d unfocused bands, want 0", got)
	}
}

// TestBoardBandDimsWhenFocusMovesRight: both panes keep a selection, so
// the band's strength — not its presence — is what says which one the
// arrow keys are driving.
func TestBoardBandDimsWhenFocusMovesRight(t *testing.T) {
	m := populatedShell(120, 34)
	s := m.styles

	unfocused := m.boardView(36, false)
	if got := len(bandedRows(unfocused, bandBG(t, s, true))); got != 0 {
		t.Errorf("unfocused board painted %d focused bands, want 0", got)
	}
	if got := len(bandedRows(unfocused, bandBG(t, s, false))); got != 1 {
		t.Errorf("unfocused board painted %d dim bands, want 1", got)
	}
	if m.boardView(36, true) == unfocused {
		t.Error("the board renders identically focused and unfocused")
	}
}

// TestBoardPaneFocusFollowsTheArrowKeys: → hands the arrow keys to the
// action list, ← takes them back, and boardPaneFocused — the single input
// to how both panes render — must agree with the handler both times.
func TestBoardPaneFocusFollowsTheArrowKeys(t *testing.T) {
	m := populatedShell(120, 34)
	if !m.boardPaneFocused() {
		t.Fatal("the board should own the arrow keys at rest")
	}
	m.boardKey("right")
	if !m.actionFocused {
		t.Fatal("→ did not move focus into the action list")
	}
	if m.boardPaneFocused() {
		t.Error("the board still claims focus while the action list has it")
	}
	m.boardKey("left")
	if m.boardPaneFocused() != true {
		t.Error("← did not hand focus back to the board")
	}
}

// TestMainPaneSurfaceTakesFocusFromTheBoard: opening the spec (or any
// other main-pane surface) routes the arrow keys away from the kanban,
// so the kanban must stop looking live.
func TestMainPaneSurfaceTakesFocusFromTheBoard(t *testing.T) {
	m := populatedShell(120, 34)
	m.spec = &specView{}
	if m.boardPaneFocused() {
		t.Error("the board claims focus while the spec surface owns the pane")
	}
	m.spec = nil
	if !m.boardPaneFocused() {
		t.Error("closing the spec did not hand focus back to the board")
	}
}

// TestActionListBandFollowsFocus: the right pane tells the same story the
// left one does — banded either way, bright only when it owns the keys.
func TestActionListBandFollowsFocus(t *testing.T) {
	m := populatedShell(120, 34)
	s := m.styles
	l := m.cardActions()
	if l.Len() == 0 {
		t.Fatal("fixture card offers no actions")
	}

	focused := l.View(s, 60, 0, true)
	if got := len(bandedRows(focused, bandBG(t, s, true))); got != 1 {
		t.Errorf("focused action list painted %d bright bands, want 1", got)
	}
	blurred := l.View(s, 60, 0, false)
	if got := len(bandedRows(blurred, bandBG(t, s, false))); got != 1 {
		t.Errorf("unfocused action list painted %d dim bands, want 1", got)
	}
	if got := len(bandedRows(blurred, bandBG(t, s, true))); got != 0 {
		t.Errorf("unfocused action list painted %d bright bands, want 0", got)
	}
}

// TestStatusBarNamesTheFocusedRegion: the bar is the one place that can
// say in words which region the arrows drive, so it has to change when
// focus does.
func TestStatusBarNamesTheFocusedRegion(t *testing.T) {
	m := populatedShell(120, 34)
	atRest := ansi.Strip(m.statusView(120))
	if !strings.Contains(atRest, "actions") {
		t.Fatalf("bar at rest should offer the way into the actions: %q", atRest)
	}

	m.boardKey("right")
	focused := ansi.Strip(m.statusView(120))
	if !strings.Contains(focused, "cards") {
		t.Errorf("bar with the action list focused should offer the way back to the cards: %q", focused)
	}
	if !strings.Contains(focused, "run action") {
		t.Errorf("bar with the action list focused should say what enter does: %q", focused)
	}
}

// TestFocusedFormFieldWearsTheBand: the creation dialogs move between
// stacked fields with the same arrow keys, and had the same problem.
func TestFocusedFormFieldWearsTheBand(t *testing.T) {
	s := theme.New(theme.GummiDark())
	bg := bandBG(t, s, true)
	if !strings.Contains(fieldRow(s, true, "full workflow"), bg) {
		t.Error("the focused field row is not banded")
	}
	if strings.Contains(fieldRow(s, false, "full workflow"), bg) {
		t.Error("an unfocused field row is banded")
	}
}

// TestDangerButtonFocusIsAFill covers the dialog half of the same
// complaint end to end: tabbing onto a red button has to change how it
// looks.
func TestDangerButtonFocusIsAFill(t *testing.T) {
	s := theme.New(theme.GummiLight())
	d := &confirmDialog{question: "Delete FD-042?", confirmLabel: "Delete"}
	before := d.View(s, 80, 24)
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // Cancel → Delete
	after := d.View(s, 80, 24)
	if before == after {
		t.Fatal("moving onto the destructive button changed nothing on screen")
	}
	if !strings.Contains(after, s.ButtonDangerFocus.Render("[ Delete ]")) {
		t.Errorf("focused destructive button is not filled: %q", after)
	}
}
