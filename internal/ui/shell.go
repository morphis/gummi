package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphia/gummi/internal/ui/layout"
	"github.com/morphia/gummi/internal/ui/logo"
	"github.com/morphia/gummi/internal/ui/overlay"
	"github.com/morphia/gummi/internal/ui/statusbar"
	"github.com/morphia/gummi/internal/ui/theme"
)

// Shell is gummi's top-level Bubble Tea model. It owns the screen
// buffer, the rectangle layout, the style set, and the dialog stack;
// panes render to strings and are painted into their rects (the Crush
// hybrid-rendering pattern). In M0 the main pane shows the splash until
// the kanban content lands (phase 4).
type Shell struct {
	styles  *theme.Styles
	version string

	width, height int
	layout        layout.Layout

	// Dialogs (gate prompts, forms) live on this stack.
	Overlay overlay.Stack
}

// NewShell builds the shell with the given theme and version string.
func NewShell(t theme.Theme, version string) *Shell {
	return &Shell{styles: theme.New(t), version: version}
}

// Styles exposes the derived style set to panes.
func (m *Shell) Styles() *theme.Styles { return m.styles }

// Init implements tea.Model.
func (m *Shell) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Shell) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout = layout.Compute(m.width, m.height)
		return m, nil
	case tea.KeyPressMsg:
		if consumed, cmd := m.Overlay.HandleKey(msg); consumed {
			return m, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
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
		kanban := m.kanbanView()
		uv.NewStyledString(kanban).Draw(scr, l.Kanban)
		// vertical separator on the kanban/main boundary
		sep := strings.TrimSuffix(strings.Repeat(s.Separator.Render("│")+"\n", l.Main.Dy()), "\n")
		uv.NewStyledString(sep).Draw(scr, uv.Rect(l.Main.Min.X, 0, 1, l.Main.Dy()))
	}
	main := m.mainView(l.Main.Dx()-2, l.Main.Dy())
	mainArea := uv.Rect(l.Main.Min.X+2, l.Main.Min.Y, max(l.Main.Dx()-2, 0), l.Main.Dy())
	uv.NewStyledString(main).Draw(scr, mainArea)

	bar := m.statusView(l.Status.Dx())
	uv.NewStyledString(bar).Draw(scr, l.Status)

	m.Overlay.Draw(scr, l.Area, s)
}

func (m *Shell) kanbanView() string {
	s := m.styles
	var b strings.Builder
	b.WriteString("\n " + s.PaneTitle.Render("BOARD") + "\n\n")
	b.WriteString(" " + s.Faint.Render("no features yet") + "\n")
	b.WriteString(" " + s.Muted.Render("press ") + s.KeyHint.Render("n") + s.Muted.Render(" to create one") + "\n")
	return b.String()
}

func (m *Shell) mainView(w, h int) string {
	return logo.Splash(m.styles, m.version, w, h)
}

func (m *Shell) statusView(w int) string {
	return statusbar.Render(m.styles, w,
		[]statusbar.Pill{
			{Text: "gummi", Kind: statusbar.KindMode},
			{Text: "0 features", Kind: statusbar.KindNeutral},
		},
		[]statusbar.Hint{
			{Key: "n", Label: "new"},
			{Key: "?", Label: "help"},
			{Key: "q", Label: "quit"},
		},
	)
}
