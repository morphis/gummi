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
	// chrome: title, blank, blank, hint, plus the frame's border and
	// padding — what is left is what the rows may use.
	budget := max(h-6, 1)
	clipped := len(d.rows) > budget
	if clipped {
		d.scroll = min(max(d.scroll, 0), len(d.rows)-budget)
	} else {
		d.scroll = 0
	}

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render(d.title) + "\n\n")
	for _, r := range d.rows[d.scroll:min(d.scroll+budget, len(d.rows))] {
		b.WriteString(s.KeyHint.Render(padRight(r[0], keyW)) + "  " + s.Subtle.Render(r[1]) + "\n")
	}
	hint := "esc close"
	if clipped {
		hint = fmt.Sprintf("↑↓ scroll · %d–%d of %d · esc close",
			d.scroll+1, min(d.scroll+budget, len(d.rows)), len(d.rows))
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
	b.WriteString("\n" + s.Faint.Render("enter select · ←/→ move · y/n accelerators · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
