package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// mouseShell is a shell parked on the agent tab with a live child, in
// the given lock state, laid out so m.layout.Main is real.
func mouseShell(t *testing.T, locked bool) *Shell {
	t.Helper()
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	if m.agent == nil {
		t.Fatal("the agent tab did not spawn a child")
	}
	m.locked = locked
	return m
}

// TestMouseGoesToTheChildOnlyWhenLocked: taking the mouse suppresses the
// terminal's own click-drag selection, and selecting agent output to copy
// is far more common than clicking inside a CLI. So capture follows the
// lock, not the tab — one gesture hands over the whole input.
func TestMouseGoesToTheChildOnlyWhenLocked(t *testing.T) {
	area := mouseShell(t, true).layout.Main
	click := tea.MouseClickMsg{X: area.Min.X + 4, Y: area.Min.Y + 2, Button: tea.MouseLeft}

	if _, ok := mouseShell(t, false).paneMouse(click); ok {
		t.Error("unlocked, the mouse must be left to the terminal")
	}
	if _, ok := mouseShell(t, true).paneMouse(click); !ok {
		t.Error("locked, the child must get the mouse")
	}
}

// TestMouseIsTranslatedIntoPaneCoordinates: the child has no idea the tab
// bar exists, so an untranslated click lands rows away from where the
// user aimed — a class of bug no compiler catches.
func TestMouseIsTranslatedIntoPaneCoordinates(t *testing.T) {
	m := mouseShell(t, true)
	area := m.layout.Main
	ev, ok := m.paneMouse(tea.MouseClickMsg{X: area.Min.X + 9, Y: area.Min.Y + 3, Button: tea.MouseLeft})
	if !ok {
		t.Fatal("a click inside the pane should reach the child")
	}
	if got := ev.Mouse(); got.X != 9 || got.Y != 3 {
		t.Errorf("pane coordinates = (%d,%d), want (9,3)", got.X, got.Y)
	}
}

// TestMouseOverGummiChromeIsDropped: the tab bar and the status bar are
// gummi's own rows. Forwarding a click there would arrive at some
// unrelated cell of the child's screen.
func TestMouseOverGummiChromeIsDropped(t *testing.T) {
	m := mouseShell(t, true)
	area := m.layout.Main
	for name, pt := range map[string][2]int{
		"tab bar":    {area.Min.X, area.Min.Y - 1},
		"status bar": {area.Min.X, area.Max.Y},
		"left of":    {area.Min.X - 1, area.Min.Y},
		"right of":   {area.Max.X, area.Min.Y},
	} {
		if _, ok := m.paneMouse(tea.MouseClickMsg{X: pt[0], Y: pt[1], Button: tea.MouseLeft}); ok {
			t.Errorf("a click on the %s reached the child", name)
		}
	}
}

// TestEveryMouseKindKeepsItsType: x/vt reads the event's dynamic type to
// decide whether it encodes a press, a release or motion, so collapsing
// them into one type would send the child the wrong thing entirely.
func TestEveryMouseKindKeepsItsType(t *testing.T) {
	m := mouseShell(t, true)
	area := m.layout.Main
	x, y := area.Min.X+1, area.Min.Y+1
	for _, tc := range []struct {
		msg  tea.MouseMsg
		want uv.MouseEvent
	}{
		{tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}, uv.MouseClickEvent{}},
		{tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}, uv.MouseReleaseEvent{}},
		{tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp}, uv.MouseWheelEvent{}},
		{tea.MouseMotionMsg{X: x, Y: y}, uv.MouseMotionEvent{}},
	} {
		ev, ok := m.paneMouse(tc.msg)
		if !ok {
			t.Fatalf("%T was dropped", tc.msg)
		}
		if got, want := mouseKind(ev), mouseKind(tc.want); got != want {
			t.Errorf("%T translated to %s, want %s", tc.msg, got, want)
		}
	}
}

// mouseKind names an event's dynamic type, which is the thing under test
// — the fields are identical across all four, so only the type differs.
func mouseKind(v uv.MouseEvent) string {
	switch v.(type) {
	case uv.MouseClickEvent:
		return "click"
	case uv.MouseReleaseEvent:
		return "release"
	case uv.MouseWheelEvent:
		return "wheel"
	case uv.MouseMotionEvent:
		return "motion"
	}
	return "unknown"
}
