package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// command is one entry in the command menu (DESIGN §space-key palette): an
// action that belongs to no particular card — creating work, importing it,
// opening the inbox — rather than a card-scoped verb the board already
// binds to a letter.
type command struct {
	id        string // stable identifier the Shell switches on to run it
	label     string // imperative, user-facing; the interface
	key       string // accelerator shown right-aligned; "" when there is none
	available bool   // false renders dimmed and cannot be run — visible but not offered
}

// commandMenu is the space-key menu: a filter line over a list of
// commands, opened for both a newcomer who reads the list and an expert
// who types three characters and presses enter. Unlike bugIngestView's
// filter/list toggle, this surface has only one mode — the filter is
// always focused, and ↑/↓ move the cursor without leaving it — so there
// is no tab step and no j/k list-navigation to shadow the query.
type commandMenu struct {
	cmds   []command
	cursor int // index into the filtered (visible) list
	filter textinput.Model
	onRun  func(id string) tea.Cmd
	hint   string // set instead of running when the selection is unavailable
}

// newCommandMenu opens with the filter input focused: typing narrows the
// list live, and the caller (the Shell) supplies both the command set and
// what running one does — this file owns only the picking.
func newCommandMenu(cmds []command, onRun func(id string) tea.Cmd) *commandMenu {
	filter := textinput.New()
	filter.Placeholder = "type to filter…"
	filter.CharLimit = 60
	filter.SetWidth(40)
	filter.Focus()
	return &commandMenu{cmds: cmds, filter: filter, onRun: onRun}
}

// ID implements overlay.Dialog.
func (m *commandMenu) ID() string { return "command-menu" }

// commandMatches reports whether a command matches the (already
// lowercased) filter query — an empty query matches everything. Matches on
// both the label (what the user reads) and the id (what they might recall
// typing before) so either can narrow the set.
func commandMatches(c command, q string) bool {
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(c.label), q) || strings.Contains(strings.ToLower(c.id), q)
}

// visible returns the indices (into cmds) of commands matching the current
// filter, in list order.
func (m *commandMenu) visible() []int {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	var out []int
	for i, c := range m.cmds {
		if commandMatches(c, q) {
			out = append(out, i)
		}
	}
	return out
}

// selected returns the cmds index under the cursor, or -1 when the
// filtered view is empty.
func (m *commandMenu) selected() int {
	vis := m.visible()
	if len(vis) == 0 {
		return -1
	}
	m.cursor = min(max(m.cursor, 0), len(vis)-1)
	return vis[m.cursor]
}

// setCursor moves the cursor and reclamps it to the current filtered set —
// typing can shrink that set out from under a cursor left at the bottom.
func (m *commandMenu) setCursor(n int) {
	if vis := len(m.visible()); vis > 0 {
		m.cursor = min(max(n, 0), vis-1)
	} else {
		m.cursor = 0
	}
}

// HandleKey implements overlay.Dialog. Every key not claimed below falls
// through to the filter input, so ordinary typing narrows the list without
// a separate focus step.
func (m *commandMenu) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "up":
		m.hint = ""
		m.setCursor(m.cursor - 1)
		return false, nil
	case "down":
		m.hint = ""
		m.setCursor(m.cursor + 1)
		return false, nil
	case "enter":
		i := m.selected()
		if i < 0 {
			return false, nil
		}
		c := m.cmds[i]
		if !c.available {
			// visible but not offered: say why instead of silently
			// swallowing the key or, worse, running it anyway.
			m.hint = c.label + " — not available here"
			return false, nil
		}
		return true, m.onRun(c.id)
	}
	m.hint = ""
	m.filter, _ = m.filter.Update(key)
	m.setCursor(m.cursor) // reclamp: the visible set may have shrunk
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the filter,
// same as typed text.
func (m *commandMenu) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	m.hint = ""
	m.filter, _ = m.filter.Update(msg)
	m.setCursor(m.cursor)
	return nil
}

// View implements overlay.Dialog.
func (m *commandMenu) View(s *theme.Styles, w, h int) string {
	// the rows, the rule and the filter share one width, and it comes from
	// the widest command rather than from the dialog's outer bound: at the
	// old fixed 60 the accelerator for "New bug" sat 50 columns from its
	// label, and the rule ran 20 columns past the last one.
	labels := make([]string, len(m.cmds))
	keys := make([]string, len(m.cmds))
	for i, c := range m.cmds {
		labels[i], keys[i] = c.label, c.key
	}
	width := max(min(keyColumn(w-8, labels, keys), 60), 24)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("commands") + "\n\n")
	m.filter.SetWidth(width)
	b.WriteString(m.filter.View() + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", width)) + "\n")

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString(s.Faint.Render("no commands match") + "\n")
	}

	footer := "\n" + s.Faint.Render("↑↓ move · enter run · esc close")
	if m.hint != "" {
		footer = "\n" + s.Warning.Render(m.hint)
	}
	headerLines := strings.Count(b.String(), "\n")
	footerLines := strings.Count(footer, "\n") + 1

	rows := make([]string, len(vis))
	for pos, i := range vis {
		rows[pos] = m.renderRow(s, i, pos == m.cursor, width)
	}

	budget := max(h-headerLines-footerLines, 1)
	shown := rows
	more := 0
	if len(rows) > budget {
		listBudget := max(budget-1, 1)
		shown = windowLines(rows, m.cursor, listBudget)
		more = len(rows) - len(shown)
	}
	for _, line := range shown {
		b.WriteString(line + "\n")
	}
	if more > 0 {
		b.WriteString(s.Faint.Render(fmt.Sprintf("…%d more — type to filter", more)) + "\n")
	}
	b.WriteString(footer)
	return s.DialogFrame.Render(b.String())
}

// renderRow paints one command line: the cursor marker, the label left,
// and the accelerator key right-aligned within width.
func (m *commandMenu) renderRow(s *theme.Styles, i int, sel bool, width int) string {
	c := m.cmds[i]
	marker := "  "
	label := s.Base
	if !c.available {
		label = s.Faint
	}
	if sel {
		marker = s.Cursor.Render("▸ ")
		if c.available {
			label = s.Subtle
		}
	}
	labelText := label.Render(c.label)
	keyText := ""
	if c.key != "" {
		keyText = s.Faint.Render(c.key)
	}
	pad := width - ansi.StringWidth(marker) - ansi.StringWidth(labelText) - ansi.StringWidth(keyText)
	return marker + labelText + strings.Repeat(" ", max(pad, 1)) + keyText
}
