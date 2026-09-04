package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/statusbar"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG071ReviewSurfacesKeepTheWayOutAndTheGate is BG-071's regression
// test. statusbar.Render sheds hints from the second-to-last backwards
// and never drops the last one, so a table's order IS its shedding
// order and whichever row sits last is the one that survives. The spec
// and diff surfaces both ended their tables with `? help`, which put
// `esc back` — and, one drop earlier, `g approve` and `R request
// changes` — in the sheddable pool ahead of it. On an ordinary board the
// pills are wide enough that all three went, leaving a reader on the
// document with comment/resolve/markers and no visible way to approve
// it, send it back, or leave.
func TestBG071ReviewSurfacesKeepTheWayOutAndTheGate(t *testing.T) {
	// a real card at a stage it can still cross: the gate row this test
	// is about only exists on one (BG-091), and a zero-value feature is
	// at no stage of no kind.
	f := domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageSpec}
	surfaces := map[string][]binding{
		"spec": (&specView{f: f}).bindings(),
		"diff": (&diffView{f: f}).bindings(),
	}
	for name, bs := range surfaces {
		hints := barHints(bs)
		if len(hints) == 0 {
			t.Fatalf("%s: no bar hints at all", name)
		}
		// the escape hatch is the one row Render protects unconditionally
		if last := hints[len(hints)-1]; last.Key != "esc" {
			t.Errorf("%s: the protected last bar hint is %q %q, not the surface's way out",
				name, last.Key, last.Label)
		}

		// and at a width a real terminal hits — 140 columns, with the
		// pill row an ordinary board carries — the gate rows outlive the
		// line-level annotation keys instead of the other way round.
		pills := []statusbar.Pill{
			{Text: "1 todo · 1 active · 2 in review · attended 0/1 · unattended 0/2"},
			{Text: "✉ 2 need you"},
		}
		bar := ansi.Strip(statusbar.Render(theme.New(theme.GummiDark()), 140, pills, hints))
		for _, want := range []string{"approve", "back"} {
			if !strings.Contains(bar, want) {
				t.Errorf("%s at 140 cols: %q shed from the bar\n%s", name, want, bar)
			}
		}
	}
}
