package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"strings"

	"github.com/morphis/gummi/internal/ui/statusbar"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestEveryTableEndsWithItsWayOut generalizes BG-071 to the four
// surfaces that were left alone at the time.
//
// statusbar.Render sheds hints from the second-to-last backwards and
// never drops the last one, so a table's order IS its shedding order:
// whichever row a surface puts last is the row that survives. inboxview,
// ingestview, bugingestview and deppicker all ended with `? help`, which
// put their way out — `esc discard`, `esc back`, `tab next tab` — in the
// sheddable pool ahead of the help key. Rounds N and N+1 declined to
// touch them because no loss reproduced on a real board; the ordering
// was still one width change away from hiding the exit, and BG-084 has
// since changed the width arithmetic underneath them.
//
// The assertion is the rule, not a reproduction: a table that ends with
// its escape hatch cannot lose it, at any width, whatever the pills do.
func TestEveryTableEndsWithItsWayOut(t *testing.T) {
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	// key: the surface, value: the key that is its way out. The inbox is
	// a tab rather than a modal, so cycling away is its exit; the rest
	// hold the keyboard and hand it back on esc.
	surfaces := []struct {
		name     string
		bindings []binding
		wayOut   string
	}{
		{"inbox", m.inboxBindings(), "tab"},
		{"ingest", (&ingestView{}).bindings(), "esc"},
		{"bug ingest", (&bugIngestView{}).bindings(), "esc"},
		{"dep picker", (&depPicker{}).bindings(), "esc"},
		// the two BG-071 already fixed, kept here so one test owns the rule
		{"spec", (&specView{}).bindings(), "esc"},
		{"diff", (&diffView{}).bindings(), "esc"},
	}

	for _, s := range surfaces {
		hints := barHints(s.bindings)
		if len(hints) == 0 {
			t.Errorf("%s: no bar hints at all", s.name)
			continue
		}
		if last := hints[len(hints)-1]; last.Key != s.wayOut {
			t.Errorf("%s: the protected last bar hint is %q %q, not the surface's way out (%q)",
				s.name, last.Key, last.Label, s.wayOut)
		}

		// and it actually survives a bar tight enough to shed: every other
		// hint may go, this one may not
		bar := ansi.Strip(statusbar.Render(theme.New(theme.GummiDark()), 60,
			[]statusbar.Pill{{Text: "gummi", Kind: statusbar.KindMode}}, hints))
		if !strings.Contains(bar, s.wayOut) {
			t.Errorf("%s at 60 cols: the way out (%q) shed from the bar\n%s", s.name, s.wayOut, bar)
		}
	}
}
