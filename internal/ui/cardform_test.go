package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestCardFormHintNamesEachStop: four of the seven focus stops (envelope,
// profile, severity, and the collapsed runs line) used to fall through to
// one shared "tab rows · ←/→ choose · type a number · alt+o collapse ·
// enter create · esc cancel" line — a hint that named no row, so tabbing
// through the expanded options gave no way to tell which one had focus.
// Every stop now gets its own hint, and (kind/repo/buttons already did
// this) every one of them names the row it is on.
func TestCardFormHintNamesEachStop(t *testing.T) {
	d := newCardForm(domain.CardType{Kind: domain.KindBug}, nil, nil, true, "", nil, 2400, nil)
	d.expanded = true

	cases := []struct {
		stop int
		name string
	}{
		{cardStopKind, "kind"},
		{cardStopText, ""}, // text's own hint names itself via alt+g wording; not a bare row name
		{cardStopEnvelope, "credits"},
		{cardStopProfile, "profile"},
		{cardStopSeverity, "severity"},
		{cardStopAfter, "filter"},
		{cardStopButtons, "buttons"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		d.focus = c.stop
		hint := d.hint()
		if c.name != "" && !strings.Contains(hint, c.name) {
			t.Errorf("stop %d hint %q does not name its row (%q)", c.stop, hint, c.name)
		}
		if seen[hint] {
			t.Errorf("stop %d reuses another stop's hint: %q", c.stop, hint)
		}
		seen[hint] = true
	}
}

// TestCardFormOptionLabelMarksFocusWithoutColor: optionLabel used to
// signal focus with a band fill alone — the same color-only cue
// buttonRow's ▸ marker exists to back up (buttons.go). Every option row
// (envelope, profile, severity, after) also draws a "▸" beside whichever
// value is currently picked, on every row, focus or not, so that marker
// alone can't say which row tab actually landed on. The label cell's own
// margin must carry a mark that appears only when the row is focused.
func TestCardFormOptionLabelMarksFocusWithoutColor(t *testing.T) {
	s := theme.New(theme.GummiDark())
	focused := ansi.Strip(optionLabel(s, true, "envelope"))
	unfocused := ansi.Strip(optionLabel(s, false, "envelope"))
	if focused == unfocused {
		t.Fatalf("focused and unfocused option labels render identically once color is stripped: %q", focused)
	}
	if !strings.Contains(focused, "▸") {
		t.Errorf("focused option label carries no plain-text marker: %q", focused)
	}
	if strings.Contains(unfocused, "▸") {
		t.Errorf("unfocused option label carries the focus marker: %q", unfocused)
	}
	if ansi.StringWidth(focused) != ansi.StringWidth(unfocused) {
		t.Errorf("marker changed the row's width: focused=%d unfocused=%d", ansi.StringWidth(focused), ansi.StringWidth(unfocused))
	}
}

// "alt+o edit" opens a panel whose first field is a text input, and a
// text input draws its "> " prompt focused or not. Leaving focus in the
// description made that prompt a lie — the next keystrokes edited the
// card's text, below the panel that had just opened.
func TestOptionsOpenWhereTheTypingGoes(t *testing.T) {
	d := newCardForm(domain.CardType{Kind: domain.KindGoal}, nil, nil, true, "", nil, 2000, nil)
	d.setFocus(cardStopText)
	d.HandleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	if !d.expanded {
		t.Fatal("alt+o did not open the options")
	}
	if d.focus != cardStopEnvelope {
		t.Fatalf("focus = %d after alt+o, want the budget field (%d)", d.focus, cardStopEnvelope)
	}
	before := d.text.Value()
	d.HandleKey(tea.KeyPressMsg{Code: '5', Text: "5"})
	if d.text.Value() != before {
		t.Errorf("typing after alt+o edited the description: %q", d.text.Value())
	}
	if !strings.Contains(d.env.Value(), "5") {
		t.Errorf("budget field = %q, want the keystroke", d.env.Value())
	}
	// collapsing puts the person back where they were writing
	d.HandleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	if d.focus != cardStopText {
		t.Errorf("focus = %d after collapsing, want the description", d.focus)
	}
}
