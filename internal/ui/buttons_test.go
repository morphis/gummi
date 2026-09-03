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

	// "Cancel" and "Delete" are equal width, so no padding is in play
	// here: the unfocused plain button is an unfilled faint legend, the
	// focused danger button is filled with the destructive color.
	plain := s.Button.Render("[ Cancel ]")
	dangerFocused := s.ButtonDangerFocus.Render("[ Delete ]")
	if !strings.Contains(view, plain) {
		t.Fatalf("unfocused Cancel not rendered s.Button: %q", view)
	}
	if !strings.Contains(view, dangerFocused) {
		t.Fatalf("focused danger button not filled (s.ButtonDangerFocus): %q", view)
	}

	// unfocused, the same danger button stays visibly destructive rather
	// than blending into the row.
	unfocusedDanger := newButtonRow(button{label: "Cancel"}, button{label: "Delete", danger: true})
	dangerUnfocused := s.ButtonDanger.Render("[ Delete ]")
	if !strings.Contains(unfocusedDanger.View(s, true), dangerUnfocused) {
		t.Fatalf("unfocused danger button not rendered s.ButtonDanger: %q", unfocusedDanger.View(s, true))
	}
}

// TestButtonRowDangerFocusVisibleOnEveryTheme is the regression this
// whole treatment exists for: focus used to be a hue swap from
// Destructive to Error, and on the light theme those two slots are the
// same color — so the focused Merge/Delete button rendered byte for byte
// like the unfocused one, on the single most consequential control in
// the app. Focus is now a fill, which no palette can collapse.
func TestButtonRowDangerFocusVisibleOnEveryTheme(t *testing.T) {
	for _, name := range theme.Names() {
		th, ok := theme.ByName(name)
		if !ok {
			t.Fatalf("theme %q not in the registry", name)
		}
		t.Run(name, func(t *testing.T) {
			s := theme.New(th)
			focused := s.ButtonDangerFocus.Render("[ Delete ]")
			unfocused := s.ButtonDanger.Render("[ Delete ]")
			if focused == unfocused {
				t.Errorf("focused and unfocused danger buttons render identically: %q", focused)
			}
			// and the fill must not be the same as an ordinary button's
			// fill, or "about to delete" reads as "about to confirm".
			if focused == s.ButtonFocus.Render("[ Delete ]") {
				t.Errorf("focused danger button renders like an ordinary focused button: %q", focused)
			}
		})
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

	// with focused=false, no button is filled — every label renders the
	// same unfilled way regardless of cursor position.
	allPlain := s.Button.Render("[ Cancel ]") + "  " + s.Button.Render("[ Delete ]")
	if unfocusedRow != allPlain {
		t.Fatalf("unfocused row = %q, want every button unfilled: %q", unfocusedRow, allPlain)
	}

	// with focused=true, the cursor button is filled, the other stays a
	// plain legend.
	wantFocused := s.ButtonFocus.Render("[ Cancel ]") + "  " + s.Button.Render("[ Delete ]")
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
	// simulate the operator having typed this themselves (SetValue alone
	// is a test shortcut that bypasses the modified flag, which would
	// otherwise arm the merge as an unreviewed draft — see BG-054).
	d.modified = true
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

// TestCommitMsgMergeButtonArmsUnmodifiedDraft: the button row reaches the
// same merge() gate as ctrl+s, so an unreviewed scribe draft (non-empty,
// never modified) arms on the first Merge as well — see BG-054.
func TestCommitMsgMergeButtonArmsUnmodifiedDraft(t *testing.T) {
	var got string
	d := newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "x"},
		func(msg string) tea.Cmd { got = msg; return nil },
		nil,
	)
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "feat(ui): add dark mode"})

	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}) // → buttons
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // Cancel → Redraft → Merge
	if done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("first Merge against an unreviewed draft closed the dialog, want arm")
	}
	if got != "" {
		t.Fatalf("onSubmit fired on the first Merge: got %q", got)
	}

	if done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("second Merge against the still-unmodified draft did not submit")
	}
	if got != "feat(ui): add dark mode" {
		t.Fatalf("onSubmit got %q, want the draft", got)
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
