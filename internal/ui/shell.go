package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/ui/layout"
	"github.com/morphia/gummi/internal/ui/logo"
	"github.com/morphia/gummi/internal/ui/overlay"
	"github.com/morphia/gummi/internal/ui/statusbar"
	"github.com/morphia/gummi/internal/ui/theme"
	"github.com/morphia/gummi/internal/worktree"
)

// Shell is gummi's top-level Bubble Tea model. It owns the screen
// buffer, the rectangle layout, the style set, the dialog stack, and —
// once attached to a workspace — the kanban board state. Panes render
// to strings and are painted into their rects (the Crush hybrid
// pattern); all IO runs in commands, never in Update or View.
type Shell struct {
	styles  *theme.Styles
	version string

	width, height int
	layout        layout.Layout

	// Dialogs (gate prompts, forms) live on this stack.
	Overlay overlay.Stack

	// workspace wiring (nil store means detached: splash only)
	store *state.Store
	wt    *worktree.Manager
	ws    state.Workspace

	rows   []featureRow
	sel    int
	notice noticeMsg

	// now is injectable for deterministic tests.
	now func() time.Time
}

// NewShell builds a detached shell (splash + empty board).
func NewShell(t theme.Theme, version string) *Shell {
	return &Shell{styles: theme.New(t), version: version, now: time.Now}
}

// Attach wires the shell to a workspace: its store, worktree manager,
// and paths. Must be called before Run for board functionality.
func (m *Shell) Attach(store *state.Store, wt *worktree.Manager, ws state.Workspace) {
	m.store, m.wt, m.ws = store, wt, ws
}

// Styles exposes the derived style set to panes.
func (m *Shell) Styles() *theme.Styles { return m.styles }

// attached reports whether a workspace is wired in.
func (m *Shell) attached() bool { return m.store != nil }

// Init implements tea.Model.
func (m *Shell) Init() tea.Cmd {
	if m.attached() {
		return m.loadRows
	}
	return nil
}

// Update implements tea.Model.
func (m *Shell) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout = layout.Compute(m.width, m.height)
		return m, nil

	case rowsMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		m.rows = msg.rows
		m.clampSel()
		return m, nil

	case noticeMsg:
		m.notice = msg
		if m.attached() {
			return m, m.loadRows
		}
		return m, nil

	case tea.KeyPressMsg:
		if consumed, cmd := m.Overlay.HandleKey(msg); consumed {
			return m, cmd
		}
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Shell) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		return tea.Quit
	case "?":
		m.Overlay.Push(helpDialog{})
		return nil
	}
	if !m.attached() {
		return nil
	}
	switch key {
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "n":
		m.Overlay.Push(newFeatureForm(m.createFeature))
	case "g":
		if r, ok := m.selected(); ok {
			return m.advanceStage(r.F.ID)
		}
	case "b":
		if r, ok := m.selected(); ok {
			return m.bounceStage(r.F.ID)
		}
	case "x":
		if r, ok := m.selected(); ok {
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:        "confirm-delete",
				question:  "delete " + string(f.ID) + "?",
				detail:    f.Title + " — removes worktree, branch, and record",
				onConfirm: func() tea.Cmd { return m.deleteFeature(f.ID) },
			})
		}
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			m.jumpSel(int(key[0] - '0'))
		}
	}
	return nil
}

// selected returns the selected row, if any.
func (m *Shell) selected() (featureRow, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return featureRow{}, false
	}
	return m.rows[m.sel], true
}

// moveSel moves the selection through the board's display order.
func (m *Shell) moveSel(delta int) {
	order := m.displayOrder()
	if len(order) == 0 {
		return
	}
	pos := 0
	for i, idx := range order {
		if idx == m.sel {
			pos = i
			break
		}
	}
	pos = (pos + delta + len(order)) % len(order)
	m.sel = order[pos]
}

// jumpSel selects the nth visible card (1-based), matching the numbers
// shown on the board.
func (m *Shell) jumpSel(n int) {
	order := m.displayOrder()
	if n >= 1 && n <= len(order) {
		m.sel = order[n-1]
	}
}

// displayOrder lists row indices in board display order (grouped by
// super-state).
func (m *Shell) displayOrder() []int {
	var order []int
	for _, super := range domain.SuperStates {
		for i, r := range m.rows {
			if r.F.Stage.SuperState() == super {
				order = append(order, i)
			}
		}
	}
	return order
}

func (m *Shell) clampSel() {
	if len(m.rows) == 0 {
		m.sel = 0
		return
	}
	if m.sel >= len(m.rows) {
		m.sel = len(m.rows) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

// View implements tea.Model: compute the buffer, paint the panes, the
// status bar, then the dialog stack.
func (m *Shell) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.BackgroundColor = m.styles.Theme.BgBase
	v.WindowTitle = "gummi"

	if m.width <= 0 || m.height <= 0 {
		return v
	}
	canvas := uv.NewScreenBuffer(m.width, m.height)
	m.draw(&canvas)

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	v.Content = strings.Join(lines, "\n")
	return v
}

func (m *Shell) draw(scr uv.Screen) {
	s := m.styles
	l := m.layout

	if l.KanbanVisible {
		uv.NewStyledString(m.boardView(l.Kanban.Dx())).Draw(scr, l.Kanban)
		sep := strings.TrimSuffix(strings.Repeat(s.Separator.Render("│")+"\n", l.Main.Dy()), "\n")
		uv.NewStyledString(sep).Draw(scr, uv.Rect(l.Main.Min.X, 0, 1, l.Main.Dy()))
	}
	main := m.mainView(max(l.Main.Dx()-3, 0), l.Main.Dy())
	mainArea := uv.Rect(l.Main.Min.X+2, l.Main.Min.Y, max(l.Main.Dx()-2, 0), l.Main.Dy())
	uv.NewStyledString(main).Draw(scr, mainArea)

	uv.NewStyledString(m.statusView(l.Status.Dx())).Draw(scr, l.Status)

	m.Overlay.Draw(scr, l.Area, s)
}

func (m *Shell) mainView(w, h int) string {
	if len(m.rows) > 0 {
		return m.dashboardView(w, h)
	}
	return logo.Splash(m.styles, m.version, w, h)
}

func (m *Shell) statusView(w int) string {
	pills := []statusbar.Pill{
		{Text: "gummi", Kind: statusbar.KindMode},
		{Text: m.boardCounts(), Kind: statusbar.KindNeutral},
	}
	if m.notice.text != "" {
		kind := statusbar.KindNeutral
		if m.notice.isErr {
			kind = statusbar.KindAlert
		}
		pills = append(pills, statusbar.Pill{Text: m.notice.text, Kind: kind})
	}
	return statusbar.Render(m.styles, w, pills,
		[]statusbar.Hint{
			{Key: "n", Label: "new"},
			{Key: "g", Label: "advance"},
			{Key: "?", Label: "help"},
			{Key: "q", Label: "quit"},
		},
	)
}
