package statusbar

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/ui/theme"
)

func testParts() ([]Pill, []Hint) {
	pills := []Pill{
		{Text: "gummi", Kind: KindMode},
		{Text: "⬤ 1 active · ⏸ 2", Kind: KindNeutral},
		{Text: "✉ 2 need you", Kind: KindAlert},
	}
	hints := []Hint{
		{Key: "enter", Label: "attach"},
		{Key: "n", Label: "new"},
		{Key: "?", Label: "help"},
	}
	return pills, hints
}

func TestRender80(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills, hints := testParts()
	out := Render(s, 80, pills, hints)
	if got := lipgloss.Width(out); got != 80 {
		t.Errorf("bar width = %d, want exactly 80", got)
	}
	golden.RequireEqual(t, []byte(out))
}

func TestRender120(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills, hints := testParts()
	out := Render(s, 120, pills, hints)
	if got := lipgloss.Width(out); got != 120 {
		t.Errorf("bar width = %d, want exactly 120", got)
	}
	golden.RequireEqual(t, []byte(out))
}

func TestRenderTightDropsHints(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills, hints := testParts()
	out := Render(s, 40, pills, hints)
	if got := lipgloss.Width(out); got > 40 {
		t.Errorf("bar width = %d, want <= 40", got)
	}
	golden.RequireEqual(t, []byte(out))
}

func TestRenderZeroAndEmpty(t *testing.T) {
	s := theme.New(theme.GummiDark())
	if Render(s, 0, nil, nil) != "" {
		t.Error("zero width should render empty")
	}
	out := Render(s, 20, nil, nil)
	if got := lipgloss.Width(out); got > 20 {
		t.Errorf("empty bar width = %d", got)
	}
}
