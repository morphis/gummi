package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

func TestButtonRowMoveWraps(t *testing.T) {
	r := newButtonRow(button{label: "Cancel"}, button{label: "Confirm"}, button{label: "Later"})

	r.Move(-1)
	if got := r.Cursor(); got != 2 {
		t.Fatalf("Move(-1) from 0 = %d, want 2 (wrap backward)", got)
	}
	r.Move(1)
	if got := r.Cursor(); got != 0 {
		t.Fatalf("Move(1) from 2 = %d, want 0 (wrap forward)", got)
	}
	r.Move(1)
	r.Move(1)
	if got := r.Cursor(); got != 2 {
		t.Fatalf("Move(1) x2 from 0 = %d, want 2", got)
	}
}

func TestButtonRowMoveWrapsTwoItems(t *testing.T) {
	r := newButtonRow(button{label: "Cancel"}, button{label: "Confirm"})

	r.Move(1)
	if got := r.Cursor(); got != 1 {
		t.Fatalf("Move(1) from 0 = %d, want 1", got)
	}
	r.Move(1)
	if got := r.Cursor(); got != 0 {
		t.Fatalf("Move(1) from 1 = %d, want 0 (a two-item row cycles)", got)
	}
	r.Move(-1)
	if got := r.Cursor(); got != 1 {
		t.Fatalf("Move(-1) from 0 = %d, want 1 (wrap backward on a two-item row)", got)
	}
}

func TestButtonRowSetCursorClamps(t *testing.T) {
	r := newButtonRow(button{label: "Cancel"}, button{label: "Confirm"})

	r.SetCursor(5)
	if got := r.Cursor(); got != 1 {
		t.Fatalf("SetCursor(5) = %d, want clamped to 1", got)
	}
	r.SetCursor(-5)
	if got := r.Cursor(); got != 0 {
		t.Fatalf("SetCursor(-5) = %d, want clamped to 0", got)
	}
}

func TestButtonRowSelected(t *testing.T) {
	r := newButtonRow(button{label: "Cancel"}, button{label: "Delete", danger: true})

	if got := r.Selected(); got.label != "Cancel" {
		t.Fatalf("Selected() at cursor 0 = %q, want Cancel", got.label)
	}
	r.Move(1)
	if got := r.Selected(); got.label != "Delete" || !got.danger {
		t.Fatalf("Selected() at cursor 1 = %+v, want Delete/danger", got)
	}
}

func TestButtonRowViewDangerRendersDistinctly(t *testing.T) {
	s := theme.New(theme.GummiDark())
	r := newButtonRow(button{label: "Cancel"}, button{label: "Delete", danger: true})
	r.Move(1) // focus the danger button

	view := r.View(s, true)
	if !strings.Contains(view, "Cancel") || !strings.Contains(view, "Delete") {
		t.Fatalf("view %q missing a button label", view)
	}

	// "Cancel" and "Delete" are equal width, so no padding is in play here:
	// the unfocused plain button is s.Faint, the focused danger button is
	// s.Error — visibly alarmed only now that it's about to fire.
	plain := s.Faint.Render("[ Cancel ]")
	dangerFocused := s.Error.Render("[ Delete ]")
	if !strings.Contains(view, plain) {
		t.Fatalf("unfocused Cancel not rendered s.Faint: %q", view)
	}
	if !strings.Contains(view, dangerFocused) {
		t.Fatalf("focused danger button not rendered s.Error: %q", view)
	}

	// unfocused, the same danger button stays visibly destructive rather
	// than blending into the row.
	unfocusedDanger := newButtonRow(button{label: "Cancel"}, button{label: "Delete", danger: true})
	dangerUnfocused := s.Destructive.Render("[ Delete ]")
	if !strings.Contains(unfocusedDanger.View(s, true), dangerUnfocused) {
		t.Fatalf("unfocused danger button not rendered s.Destructive: %q", unfocusedDanger.View(s, true))
	}
}

func TestButtonRowViewFocusedVsUnfocused(t *testing.T) {
	s := theme.New(theme.GummiDark())
	// equal-width labels so no padding complicates the comparison.
	r := newButtonRow(button{label: "Cancel"}, button{label: "Delete"})

	unfocusedRow := r.View(s, false)
	focusedRow := r.View(s, true)
	if unfocusedRow == focusedRow {
		t.Fatal("focused and unfocused row rendered identically")
	}

	// with focused=false, no button should be highlighted — every label
	// renders the same (faint) way regardless of cursor position.
	allFaint := s.Faint.Render("[ Cancel ]") + "  " + s.Faint.Render("[ Delete ]")
	if unfocusedRow != allFaint {
		t.Fatalf("unfocused row = %q, want every button faint: %q", unfocusedRow, allFaint)
	}

	// with focused=true, the cursor button is highlighted, the other stays
	// faint.
	wantFocused := s.Cursor.Render("[ ") + s.Subtle.Render("Cancel") + s.Cursor.Render(" ]") +
		"  " + s.Faint.Render("[ Delete ]")
	if focusedRow != wantFocused {
		t.Fatalf("focused row = %q, want %q", focusedRow, wantFocused)
	}

	r.Move(1)
	movedFocused := r.View(s, true)
	if movedFocused == focusedRow {
		t.Fatal("moving the cursor didn't change the focused rendering")
	}
}

func TestButtonRowViewPadsLabelsEvenly(t *testing.T) {
	s := theme.New(theme.GummiDark())
	r := newButtonRow(button{label: "No"}, button{label: "Delete forever", danger: true})

	view := ansi.Strip(r.View(s, true))
	want := "[ No" + strings.Repeat(" ", ansi.StringWidth("Delete forever")-ansi.StringWidth("No")) +
		" ]  [ Delete forever ]"
	if view != want {
		t.Fatalf("view = %q, want %q (short label padded to the long one's width)", view, want)
	}
}

// TestCommitMsgMergeReachableWithoutCtrl: zellij binds ctrl+s to search
// mode, so the merge must be reachable by tabbing to the button row —
// otherwise the most consequential action in gummi is unreachable inside
// a multiplexer.
func TestCommitMsgMergeReachableWithoutCtrl(t *testing.T) {
	var got string
	d := newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "x"},
		func(msg string) tea.Cmd { got = msg; return nil },
		nil,
	)
	d.input.SetValue("land the thing")
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}) // → buttons
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // Cancel → Redraft → Merge
	done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("enter on Merge did not close the dialog")
	}
	if got != "land the thing" {
		t.Fatalf("onSubmit got %q, want the message", got)
	}
}

// TestCommitMsgEnterStillTypesNewline: the textarea owns enter, which is
// why the merge needed a button in the first place.
func TestCommitMsgEnterStillTypesNewline(t *testing.T) {
	d := newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "x"},
		func(string) tea.Cmd { t.Fatal("enter in the textarea must not submit"); return nil },
		nil,
	)
	d.input.SetValue("line one")
	if done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("enter in the textarea closed the dialog")
	}
}
