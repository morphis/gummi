package statusbar

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/ui/theme"
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

// decisionHints is threadInputBindings' bar subset for a card page with a
// pinned decision (internal/ui/threadinput.go), condensed to the fields
// Render cares about: ↑↓ choose, enter <option label> (Sticky — F15),
// pgup/pgdn scroll, alt+o outputs, esc backlog.
func decisionHints(enterLabel string) []Hint {
	return []Hint{
		{Key: "↑↓", Label: "choose"},
		{Key: "enter", Label: enterLabel, Sticky: true},
		{Key: "pgup/pgdn", Label: "scroll"},
		{Key: "alt+o", Label: "outputs"},
		{Key: "esc", Label: "backlog"},
	}
}

// TestRenderStickyEnterSurvivesTightDecisionBar is F15's repro and fix: a
// card page with a pinned decision, the board's ordinary pills on the
// left, and a long first-option label ("start the architect", the real
// label a four-option diagnose-stage gate carries) used to collapse all
// the way to "↑↓ choose · esc backlog" at 120 columns — losing the one
// row saying enter does anything at all, on a surface where enter can
// spend credits. Sticky (statusbar.Hint.Sticky, set by keymap.go's
// binding.sticky) makes pgup/pgdn and outputs shed first instead.
func TestRenderStickyEnterSurvivesTightDecisionBar(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills, _ := testParts() // the standard board pills: mode, counts, needs-you
	hints := decisionHints("start the architect")

	out := Render(s, 120, pills, hints)
	if got := lipgloss.Width(out); got != 120 {
		t.Fatalf("bar width = %d, want exactly 120", got)
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "enter") || !strings.Contains(plain, "start the architect") {
		t.Fatalf("the enter row did not survive at 120 columns:\n%s", plain)
	}
	// esc is the surface's own escape hatch — still expected to survive
	// too; sticky only changed WHICH row is shed first, not that contract.
	if !strings.Contains(plain, "backlog") {
		t.Fatalf("esc's own row should still be present:\n%s", plain)
	}
	// alt+o's row is the one that gives way to make room — confirming
	// shedding actually happened rather than everything fitting by
	// coincidence (pgup/pgdn survives too: only one hint had to go).
	if strings.Contains(plain, "outputs") {
		t.Fatalf("expected alt+o to have been shed to make room for the sticky enter row, still present:\n%s", plain)
	}
	golden.RequireEqual(t, []byte(out))
}

// TestRenderStickyHintTruncatesRatherThanVanishing is the deep edge
// Sticky's own doc comment describes: when even the sticky hint alone
// (plus the escape hatch) doesn't fit, Render truncates instead of
// blanking the row outright — the failure mode Sticky exists to prevent.
func TestRenderStickyHintTruncatesRatherThanVanishing(t *testing.T) {
	s := theme.New(theme.GummiDark())
	hints := decisionHints("start the architect, a very long option label that cannot possibly fit")

	out := Render(s, 40, nil, hints)
	if got := lipgloss.Width(out); got > 40 {
		t.Fatalf("bar width = %d, want <= 40", got)
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "enter") {
		t.Fatalf("the sticky hint should have been truncated, not erased:\n%q", plain)
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
