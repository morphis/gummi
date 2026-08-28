package ui

import "testing"

// TestActionEnterDispatchesOnce guards the recursion the action list
// invited: the recommended action's own accelerator is enter, and the
// focus layer also claims enter, so dispatching back through boardKey
// re-selected the same row until the stack blew.
func TestActionEnterDispatchesOnce(t *testing.T) {
	m := populatedShell(100, 30)
	m.actionFocused = true
	m.actionCursor = 0
	a, ok := m.cardActions().Selected()
	if !ok {
		t.Fatal("no action under the cursor on a populated board")
	}
	if a.key != "enter" {
		t.Skipf("first action key = %q; this repro needs the enter-keyed one", a.key)
	}
	m.boardKey("enter") // recursed to a stack overflow before boardVerb existed
}

// TestActionFocusResetsOnCardChange: the list belongs to the selected
// card, so moving off it must not carry a cursor onto an unrelated
// action.
func TestActionFocusResetsOnCardChange(t *testing.T) {
	m := populatedShell(100, 30)
	m.actionFocused = true
	m.actionCursor = 3
	m.moveSel(1)
	if m.actionFocused || m.actionCursor != 0 {
		t.Fatalf("focus=%v cursor=%d after moving card, want false/0", m.actionFocused, m.actionCursor)
	}
}

// TestRightFocusesActionsAndLeftReturns covers the board's two new focus
// keys end to end.
func TestRightFocusesActionsAndLeftReturns(t *testing.T) {
	m := populatedShell(100, 30)
	m.boardKey("right")
	if !m.actionFocused {
		t.Fatal("right did not focus the action list")
	}
	m.boardKey("left")
	if m.actionFocused {
		t.Fatal("left did not return focus to the cards")
	}
}

// TestSpaceOpensCommandMenu: the menu is the space key's whole job, and
// space must not be swallowed by the board's letter switch.
func TestSpaceOpensCommandMenu(t *testing.T) {
	m := populatedShell(100, 30)
	m.boardKey("space")
	if !m.Overlay.Contains("command-menu") {
		t.Fatal("space did not open the command menu")
	}
}

// TestRunCommandRoutesGlobalKeys: q and ? are answered above handleKey's
// attached check and never reach boardVerb, so the menu has to route
// them itself or those entries are silent no-ops.
func TestRunCommandRoutesGlobalKeys(t *testing.T) {
	m := populatedShell(100, 30)
	m.runCommand("?")
	if !m.Overlay.Contains("help") {
		t.Fatal("the menu's help entry did not open the help overlay")
	}
}
