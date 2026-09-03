package statusbar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// TestRenderDropsStickyHintWhenPillsAloneOverflow is BG-064's repro: when the
// pill row alone is already wider than the bar, Render used to bail out and
// return the truncated pills with no hints at all — sticky or not. A pinned
// decision's "enter <option>" row is exactly the kind of hint Sticky exists
// to keep visible under width pressure, so it must survive here too.
func TestRenderDropsStickyHintWhenPillsAloneOverflow(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills := []Pill{
		{Text: "gummi", Kind: KindMode},
		{Text: "3 todo · 4 active · 2 in review · 1 done", Kind: KindNeutral},
		{Text: "⬤ 2 running · ◔ 1 queued", Kind: KindNeutral},
		{Text: "✉ 3 need you", Kind: KindAlert},
		{Text: "spec generated", Kind: KindNeutral},
	}
	hints := decisionHints("start the architect") // helper already in statusbar_test.go

	out := Render(s, 100, pills, hints)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "start the architect") {
		t.Fatalf("sticky enter hint vanished when pills alone overflowed the bar:\n%q", plain)
	}
}
