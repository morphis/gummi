// Package overlay is the dialog-stack primitive: modal surfaces (gate
// prompts, forms, comment popovers) pushed over the main UI, drawn over
// a dimmed backdrop, top dialog receiving input first.
package overlay

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/ui/theme"
)

// Dialog is one modal surface. Dialogs are plain stateful structs (the
// Crush pattern): the shell routes input to the top dialog and paints
// the stack last.
type Dialog interface {
	// ID uniquely names the dialog within the stack.
	ID() string
	// HandleKey processes a key press. done=true asks the stack to pop
	// this dialog; cmd carries any side effects.
	HandleKey(key tea.KeyPressMsg) (done bool, cmd tea.Cmd)
	// View renders the dialog's framed content for the available area.
	View(s *theme.Styles, width, height int) string
}

// Stack manages the open dialogs, last element on top.
type Stack struct {
	dialogs []Dialog
}

// Len returns the number of open dialogs.
func (st *Stack) Len() int { return len(st.dialogs) }

// HasDialogs reports whether anything is open.
func (st *Stack) HasDialogs() bool { return len(st.dialogs) > 0 }

// Push opens a dialog on top of the stack.
func (st *Stack) Push(d Dialog) { st.dialogs = append(st.dialogs, d) }

// Pop closes the top dialog and returns it (nil when empty).
func (st *Stack) Pop() Dialog {
	if len(st.dialogs) == 0 {
		return nil
	}
	d := st.dialogs[len(st.dialogs)-1]
	st.dialogs = st.dialogs[:len(st.dialogs)-1]
	return d
}

// Top returns the active dialog (nil when empty).
func (st *Stack) Top() Dialog {
	if len(st.dialogs) == 0 {
		return nil
	}
	return st.dialogs[len(st.dialogs)-1]
}

// Contains reports whether a dialog with the given ID is open.
func (st *Stack) Contains(id string) bool {
	for _, d := range st.dialogs {
		if d.ID() == id {
			return true
		}
	}
	return false
}

// HandleKey routes a key press to the top dialog. It returns true when
// the key was consumed (a dialog was open).
func (st *Stack) HandleKey(key tea.KeyPressMsg) (consumed bool, cmd tea.Cmd) {
	top := st.Top()
	if top == nil {
		return false, nil
	}
	done, cmd := top.HandleKey(key)
	if done {
		st.Pop()
	}
	return true, cmd
}

// Paster is an optional Dialog extension: dialogs hosting a text input
// implement it to receive bracketed-paste text.
type Paster interface {
	HandlePaste(msg tea.PasteMsg) tea.Cmd
}

// HandlePaste routes pasted text to the top dialog. It returns true
// whenever a dialog is open — a paste never leaks past a modal, even
// one with nothing to paste into.
func (st *Stack) HandlePaste(msg tea.PasteMsg) (consumed bool, cmd tea.Cmd) {
	top := st.Top()
	if top == nil {
		return false, nil
	}
	if p, ok := top.(Paster); ok {
		return true, p.HandlePaste(msg)
	}
	return true, nil
}

// Draw dims the backdrop and paints every open dialog, bottom to top,
// centered in area.
func (st *Stack) Draw(scr uv.Screen, area uv.Rectangle, s *theme.Styles) {
	if len(st.dialogs) == 0 {
		return
	}
	Dim(scr, area, s)
	for _, d := range st.dialogs {
		view := d.View(s, area.Dx(), area.Dy())
		w, h := lipgloss.Width(view), lipgloss.Height(view)
		x := area.Min.X + max((area.Dx()-w)/2, 0)
		y := area.Min.Y + max((area.Dy()-h)/2, 0)
		uv.NewStyledString(view).Draw(scr, uv.Rect(x, y, min(w, area.Dx()), min(h, area.Dy())))
	}
}

// Dim repaints every cell's foreground in area with the theme's faint
// tier, visually receding the backdrop behind a modal.
func Dim(scr uv.Screen, area uv.Rectangle, s *theme.Styles) {
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			c := scr.CellAt(x, y)
			if c == nil {
				continue
			}
			cc := *c
			cc.Style.Fg = s.Theme.FgFaint
			cc.Style.Bg = s.Theme.BgBase
			cc.Style.Attrs = 0
			scr.SetCell(x, y, &cc)
		}
	}
}
