package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui/theme"
)

func sampleCommands() []command {
	return []command{
		{id: "new-feature", label: "new feature", key: "n", available: true},
		{id: "new-bug", label: "new bug", key: "B", available: true},
		{id: "inbox", label: "open inbox", key: "i", available: false},
		{id: "import-bugs", label: "import bugs", key: "G", available: true},
	}
}

// TestCommandMenuOpensFilterFocused proves the menu opens ready to type.
func TestCommandMenuOpensFilterFocused(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	if !cm.filter.Focused() {
		t.Error("filter input should be focused on open")
	}
	if len(cm.visible()) != 4 {
		t.Fatalf("unfiltered visible = %d, want 4", len(cm.visible()))
	}
}

// TestCommandMenuFilterNarrowsVisible proves typing filters live over both
// label and id.
func TestCommandMenuFilterNarrowsVisible(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.filter.SetValue("bug")
	vis := cm.visible()
	if len(vis) != 2 {
		t.Fatalf("filter 'bug' visible = %d, want 2", len(vis))
	}
	if cm.cmds[vis[0]].id != "new-bug" || cm.cmds[vis[1]].id != "import-bugs" {
		t.Errorf("filtered visible = %+v", vis)
	}

	// a filter matching nothing hides everything.
	cm.filter.SetValue("zzz-nope")
	if len(cm.visible()) != 0 {
		t.Errorf("non-matching filter: visible=%d, want 0", len(cm.visible()))
	}
}

// TestCommandMenuCursorReclampsWhenFilterShrinks proves a cursor left past
// the end of a shrunk filtered set is pulled back into range rather than
// pointing at nothing (or, worse, the wrong row).
func TestCommandMenuCursorReclampsWhenFilterShrinks(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.cursor = 3 // last row of the unfiltered list
	cm.filter.SetValue("inbox")
	i := cm.selected()
	if i < 0 {
		t.Fatal("expected a selection after filtering to one match")
	}
	if cm.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after the filtered set shrank to one row", cm.cursor)
	}
	if cm.cmds[i].id != "inbox" {
		t.Errorf("selected id = %q, want inbox", cm.cmds[i].id)
	}
}

// TestCommandMenuUpDownMoveOverFilteredSet proves ↑/↓ move the cursor over
// the filtered list, not the whole command set.
func TestCommandMenuUpDownMoveOverFilteredSet(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.filter.SetValue("bug") // narrows to new-bug, import-bugs
	cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := cm.cmds[cm.selected()].id; got != "import-bugs" {
		t.Errorf("after down, selected = %q, want import-bugs", got)
	}
	cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := cm.cmds[cm.selected()].id; got != "import-bugs" {
		t.Errorf("down past the last row should not move further, got %q", got)
	}
	cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := cm.cmds[cm.selected()].id; got != "new-bug" {
		t.Errorf("after up, selected = %q, want new-bug", got)
	}
}

// TestCommandMenuEnterRunsSelected proves enter runs the command under the
// cursor and closes the menu.
func TestCommandMenuEnterRunsSelected(t *testing.T) {
	var ran string
	cm := newCommandMenu(sampleCommands(), func(id string) tea.Cmd { ran = id; return nil })
	cm.filter.SetValue("bug")

	done, _ := cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("enter on an available command should close the menu")
	}
	if ran != "new-bug" {
		t.Errorf("onRun called with %q, want new-bug", ran)
	}
}

// TestCommandMenuEnterOnUnavailableDoesNothing proves an unavailable
// selection neither runs nor closes — it sets a hint instead.
func TestCommandMenuEnterOnUnavailableDoesNothing(t *testing.T) {
	var ran string
	cm := newCommandMenu(sampleCommands(), func(id string) tea.Cmd { ran = id; return nil })
	cm.filter.SetValue("inbox") // only the unavailable "open inbox" matches

	done, cmd := cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Error("enter on an unavailable command should not close the menu")
	}
	if cmd != nil {
		t.Error("enter on an unavailable command should not carry a cmd")
	}
	if ran != "" {
		t.Errorf("onRun should not be called for an unavailable command, got %q", ran)
	}
	if cm.hint == "" {
		t.Error("expected a hint explaining why the command didn't run")
	}
}

// TestCommandMenuEscClosesWithoutRunning proves esc closes the menu without
// invoking onRun.
func TestCommandMenuEscClosesWithoutRunning(t *testing.T) {
	var ran bool
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { ran = true; return nil })
	done, cmd := cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("esc should close the menu")
	}
	if cmd != nil {
		t.Error("esc should not carry a cmd")
	}
	if ran {
		t.Error("esc should not run anything")
	}
}

// TestCommandMenuTypingFiltersAndReclamps proves ordinary keys fall through
// to the filter input (no separate focus step), and the cursor reclamps as
// a result.
func TestCommandMenuTypingFiltersAndReclamps(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.cursor = 3
	for _, r := range "bug" {
		cm.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := cm.filter.Value(); got != "bug" {
		t.Fatalf("filter value = %q, want %q", got, "bug")
	}
	if len(cm.visible()) != 2 {
		t.Fatalf("visible after typing = %d, want 2", len(cm.visible()))
	}
	if cm.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (reclamped from 3 into a 2-row set)", cm.cursor)
	}
}

// TestCommandMenuHandlePasteFiltersLikeTyping proves a bracketed paste goes
// into the filter, same as typed text.
func TestCommandMenuHandlePasteFiltersLikeTyping(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.HandlePaste(tea.PasteMsg{Content: "bug"})
	if got := cm.filter.Value(); got != "bug" {
		t.Fatalf("filter value after paste = %q, want %q", got, "bug")
	}
	if len(cm.visible()) != 2 {
		t.Errorf("visible after paste = %d, want 2", len(cm.visible()))
	}
}

// TestCommandMenuEmptyFilterRendersMessage proves a filter matching nothing
// renders an explicit message rather than an empty list.
func TestCommandMenuEmptyFilterRendersMessage(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	cm.filter.SetValue("zzz-nope")
	view := cm.View(theme.New(theme.GummiDark()), 60, 20)
	if !strings.Contains(view, "no commands match") {
		t.Errorf("expected the empty-state message in the view, got:\n%s", view)
	}
}

// TestCommandMenuUnavailableStillRenders proves an unavailable command is
// still shown (dimmed), not hidden — the menu should answer "can I do this
// here?" rather than pretend the action doesn't exist.
func TestCommandMenuUnavailableStillRenders(t *testing.T) {
	cm := newCommandMenu(sampleCommands(), func(string) tea.Cmd { return nil })
	view := cm.View(theme.New(theme.GummiDark()), 60, 20)
	if !strings.Contains(view, "open inbox") {
		t.Error("unavailable command should still render in the list")
	}
}

// TestCommandMenuViewCapsRowsAndNotesOverflow proves the row list is capped
// to what fits h, with a truncation line noting how many more there are.
func TestCommandMenuViewCapsRowsAndNotesOverflow(t *testing.T) {
	cmds := make([]command, 0, 20)
	for i := 0; i < 20; i++ {
		cmds = append(cmds, command{id: "cmd-" + string(rune('a'+i)), label: "command " + string(rune('a'+i)), available: true})
	}
	cm := newCommandMenu(cmds, func(string) tea.Cmd { return nil })
	view := cm.View(theme.New(theme.GummiDark()), 60, 12)
	if !strings.Contains(view, "more — type to filter") {
		t.Errorf("expected a truncation hint when rows overflow h, got:\n%s", view)
	}
}
