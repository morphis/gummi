package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// helpDialog is the ? overlay: the active surface's full key table,
// built by helpOverlay (keymap.go) from the same bindings the status
// bar hints render from. It scrolls: the board's table outgrew a single
// screen, and the overflow used to be silent — the last rows fell off
// the bottom of the frame, which is a poor failure for the one surface
// whose job is to say what the keys are.
type helpDialog struct {
	title  string
	rows   [][2]string
	scroll int // first visible row
}

func (*helpDialog) ID() string { return "help" }

func (d *helpDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "?", "q", "enter":
		return true, nil
	case "down", "j":
		d.scroll++
	case "up", "k":
		d.scroll--
	case "pgdown":
		d.scroll += 10
	case "pgup":
		d.scroll -= 10
	}
	return false, nil
}

func (d *helpDialog) View(s *theme.Styles, w, h int) string {
	keyW := 0
	for _, r := range d.rows {
		keyW = max(keyW, ansi.StringWidth(r[0]))
	}
	// The frame is a rounded border plus Padding(1, 2) — 6 columns of
	// chrome — and the key gutter takes keyW+2 more. What is left is what
	// a help line may use.
	//
	// w used to be ignored entirely: rows were written at their natural
	// width and the frame grew past the pane, so the `keys · card` table
	// rendered on a 120-column terminal with no right border at all and
	// its first row cut mid-word (round 3 §2.4). The board's table fits by
	// luck; the card's longest row is around 150 columns and does not. A
	// help line that does not fit now wraps under the text column, which
	// is where a reader looks for its continuation.
	//
	// Wrapping is applied BEFORE the vertical window, and the window
	// counts display lines rather than table rows: a row that takes two
	// lines used to be able to push the last row of a scrolled table off
	// the bottom of the pane, which is the same clipping one axis over.
	textW := max(w-6-keyW-2, 20)
	var lines []string
	for _, r := range d.rows {
		gutter := s.KeyHint.Render(padRight(r[0], keyW)) + "  "
		wrapped := strings.Split(wrapText(r[1], textW), "\n")
		lines = append(lines, gutter+s.Subtle.Render(wrapped[0]))
		for _, cont := range wrapped[1:] {
			lines = append(lines, strings.Repeat(" ", keyW+2)+s.Subtle.Render(cont))
		}
	}

	// chrome: title, blank, blank, hint, plus the frame's border and
	// padding — what is left is what the lines may use.
	budget := max(h-6, 1)
	clipped := len(lines) > budget
	if clipped {
		d.scroll = min(max(d.scroll, 0), len(lines)-budget)
	} else {
		d.scroll = 0
	}

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render(d.title) + "\n\n")
	for _, l := range lines[d.scroll:min(d.scroll+budget, len(lines))] {
		b.WriteString(l + "\n")
	}
	hint := "esc close"
	if clipped {
		hint = fmt.Sprintf("↑↓ scroll · %d–%d of %d · esc close",
			d.scroll+1, min(d.scroll+budget, len(lines)), len(lines))
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}

func padRight(str string, n int) string {
	if w := ansi.StringWidth(str); w < n {
		return str + strings.Repeat(" ", n-w)
	}
	return str
}

// confirmDialog asks a yes/no question before a destructive action. Its
// last (only) tab stop is a buttonRow — Cancel focused by default so a
// stray enter never fires the destructive side — named by the verb it
// performs rather than a bare "yes", per DESIGN.md's confirm convention.
// callers construct it as a struct literal (no constructor exists, so
// existing Push sites across the package don't need to change); buttons
// is built lazily on first use from confirmLabel/cancelLabel, which
// default to "Confirm"/"Cancel" when left unset.
type confirmDialog struct {
	id        string
	question  string
	detail    string
	onConfirm func() tea.Cmd

	// confirmLabel/cancelLabel override the button row's legends; a caller
	// naming the verb ("Delete", "Quit") gets that instead of a bare
	// "Confirm".
	confirmLabel string
	cancelLabel  string

	buttons *buttonRow
}

func (d *confirmDialog) ID() string { return d.id }

// ensureButtons lazily builds the row on first use, since confirmDialog
// has no constructor callers go through.
func (d *confirmDialog) ensureButtons() *buttonRow {
	if d.buttons == nil {
		confirm := d.confirmLabel
		if confirm == "" {
			confirm = "Confirm"
		}
		cancel := d.cancelLabel
		if cancel == "" {
			cancel = "Cancel"
		}
		d.buttons = newButtonRow(button{label: cancel}, button{label: confirm, danger: true})
	}
	return d.buttons
}

func (d *confirmDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	row := d.ensureButtons()
	switch key.String() {
	// y/n stay as accelerators: existing callers and tests rely on them,
	// and the muscle memory is exactly what an accelerator is for.
	case "y", "Y":
		return true, d.onConfirm()
	case "n", "N", "esc":
		return true, nil
	case "left", "h", "shift+tab":
		row.Move(-1)
		return false, nil
	case "right", "l", "tab":
		row.Move(1)
		return false, nil
	case "enter":
		if row.Selected().danger {
			return true, d.onConfirm()
		}
		return true, nil
	}
	return false, nil
}

func (d *confirmDialog) View(s *theme.Styles, w, h int) string {
	row := d.ensureButtons()
	var b strings.Builder
	b.WriteString(s.Destructive.Bold(true).Render(d.question) + "\n")
	if d.detail != "" {
		b.WriteString(s.Subtle.Render(d.detail) + "\n")
	}
	b.WriteString("\n" + row.View(s, true) + "\n")
	// "y / n", not "y/n accelerators". Round 2 §5 listed "accelerators" as
	// a word the reader will not know and this is the last dialog carrying
	// it — on the confirm boxes, which is where a first-time user meets it.
	b.WriteString("\n" + s.Faint.Render("enter select · ←/→ move · y / n · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
