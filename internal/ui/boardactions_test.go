package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/config"
)

// testCardCommandProfile is a minimal declared profile, enough to make
// cardProfileActions/CardProfiles offer something to pick from.
func testCardCommandProfile() config.Profiles {
	return config.Profiles{
		Default:  "thrifty",
		Profiles: map[string]config.Profile{"thrifty": {"implementer": {Model: "m"}}},
	}
}

// TestRunCommandProfileOpensPicker: the "profile" command (the row
// cardCommands appends to the space menu) opens the card-scoped value
// tier — a commandMenu, the same overlay type the board's own /profile
// value tier uses, not a bespoke dialog.
func TestRunCommandProfileOpensPicker(t *testing.T) {
	m := populatedShell(100, 30)
	m.engine = testProfileEngine(t, testCardCommandProfile())

	m.runCommand("profile")

	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf(`runCommand("profile") did not open the value tier, got %T`, m.Overlay.Top())
	}
}

// TestRunCardActionProfileOpensPicker: the action list's keyless
// "profile" row reaches the identical picker, since it has no key to
// route through boardVerb.
func TestRunCardActionProfileOpensPicker(t *testing.T) {
	m := populatedShell(100, 30)
	m.engine = testProfileEngine(t, testCardCommandProfile())

	m.runCardAction(cardAction{id: "profile"})

	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf("runCardAction(profile) did not open the value tier, got %T", m.Overlay.Top())
	}
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

// TestActionFocusResetsOnSilentSelectionChange: a board reload can land
// the selection on a different card with no keypress (a card deleted, or
// the sort reordered). The cursor must not survive that — it could be
// sitting on "delete" and would then belong to the wrong card.
func TestActionFocusResetsOnSilentSelectionChange(t *testing.T) {
	m := populatedShell(100, 30)
	m.syncActionFocus() // adopt the current card
	m.actionFocused = true
	m.actionCursor = 3

	// drop the selected card, the way a reload can: the id it was on is
	// gone, so restoreSel falls to the top of the list and the cursor is
	// on a different card than it was.
	was := m.selectedID()
	m.rows = append(m.rows[:m.sel], m.rows[m.sel+1:]...)
	m.restoreSel(was)
	m.syncActionFocus()

	if m.actionFocused || m.actionCursor != 0 {
		t.Fatalf("focus=%v cursor=%d after the selection silently moved, want false/0",
			m.actionFocused, m.actionCursor)
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

// TestQuitConfirmsWithUnsavedProposals: an ingest pass is not an engine
// session, so liveSessions never saw it — and ctrl+c, hoisted above the
// overlay, was one keystroke from discarding a paid architect pass that
// esc already confirms before discarding.
func TestQuitConfirmsWithUnsavedProposals(t *testing.T) {
	m := populatedShell(100, 30)
	m.ingest = &ingestView{source: "prd.md", props: []ingestProposal{{}, {}}}
	if cmd := m.quitCmd(); cmd != nil {
		t.Fatal("quit returned a command instead of asking first")
	}
	if !m.Overlay.Contains("confirm-quit") {
		t.Fatal("quit did not confirm with unsaved proposals on screen")
	}
}

// TestQuitConfirmsDuringDecompose: same for a pass still running.
func TestQuitConfirmsDuringDecompose(t *testing.T) {
	m := populatedShell(100, 30)
	m.ingestRun = newIngestRunView("prd.md")
	if cmd := m.quitCmd(); cmd != nil {
		t.Fatal("quit returned a command instead of asking first")
	}
	if !m.Overlay.Contains("confirm-quit") {
		t.Fatal("quit did not confirm while a decompose was running")
	}
}

// TestQuitIdleStillOneKeypress: the confirm must not become the default.
func TestQuitIdleStillOneKeypress(t *testing.T) {
	m := populatedShell(100, 30)
	if cmd := m.quitCmd(); cmd == nil {
		t.Fatal("idle quit asked for confirmation")
	}
	if m.Overlay.HasDialogs() {
		t.Fatal("idle quit opened a dialog")
	}
}

// TestActionFocusResetsOnAttentionJump: jumping to a card from the
// inbox tab moves the selection to another card; a cursor left on merge
// would otherwise merge that one.
func TestActionFocusResetsOnAttentionJump(t *testing.T) {
	m := populatedShell(100, 30)
	m.syncActionFocus()
	m.actionFocused = true
	m.actionCursor = 3
	for _, r := range m.rows {
		m.inbox.add(r.F.ID, attnGate, "gate")
	}
	m.setTab(TabInbox)
	m.inboxSel = 0
	m.inboxKey("enter")
	if m.actionFocused || m.actionCursor != 0 {
		t.Fatalf("focus=%v cursor=%d after the inbox jump, want false/0",
			m.actionFocused, m.actionCursor)
	}
}

// TestCardActionEnterDispatchesOnce guards the recursion that
// backlogKey's enter branch documents in a comment: the run action's own
// accelerator IS enter, so dispatching it back through boardKey would
// reselect this same row and recurse until the stack blew. The original
// guard was deleted along with the split layout during the tab rework,
// but a focused action list still exists on the card page — the hazard
// moved rather than went away, so the guard moves with it.
func TestCardActionEnterDispatchesOnce(t *testing.T) {
	m := populatedShell(100, 30)
	m.openCard()

	idx := -1
	for i, a := range m.cardActions().rows() {
		if a.key == "enter" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no enter-keyed action on the opened card; this guard needs one to mean anything")
	}
	m.actionCursor = idx

	// an unguarded re-entry blows the stack and takes the test binary
	// with it, so returning from this call at all is the assertion.
	m.boardKey("enter")
}
