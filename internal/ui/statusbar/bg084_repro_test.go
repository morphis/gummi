package statusbar

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG084NoticeSurvivesATightBar is BG-084's regression test.
//
// The notice is the last pill on the bar: the answer to the key just
// pressed, on screen for a moment and nowhere else afterwards. When the
// hints had shed to the bone and the row was still over, Render made
// room by truncating the pill row from its right edge — which is the
// notice, cut from its tail, where the sentence says what happened. The
// user pressed a key and read "RS-006: no diff — …" for "no diff —
// research cards carry no branch", while the ambient counts beside it
// survived in full.
//
// The card page reaches this constantly, because a card showing an open
// decision marks its enter row Sticky, so shedding stops early and the
// truncation branch is where a tight bar lands.
func TestBG084NoticeSurvivesATightBar(t *testing.T) {
	s := theme.New(theme.GummiDark())
	const notice = "RS-006: no diff — research cards carry no branch"
	pills := []Pill{
		{Text: "gummi", Kind: KindMode},
		{Text: "1 todo · 2 active · 1 research · 2 in review · attended 0/1 · unattended 0/2", Kind: KindNeutral},
		{Text: "✉ 2 need you", Kind: KindAlert},
		{Text: notice, Kind: KindNeutral},
	}
	hints := decisionHints("run review")

	out := Render(s, 140, pills, hints)
	if got := lipgloss.Width(out); got != 140 {
		t.Fatalf("bar width = %d, want exactly 140", got)
	}
	plain := ansi.Strip(out)

	if !strings.Contains(plain, notice) {
		t.Errorf("the notice answering the keystroke was cut at 140 columns:\n%s", plain)
	}
	// the sticky enter row and the escape hatch still outrank everything,
	// which is the contract the pill shedding had to slot in underneath
	// rather than replace
	if !strings.Contains(plain, "run review") {
		t.Errorf("the sticky enter row should still survive:\n%s", plain)
	}
	if !strings.Contains(plain, "backlog") {
		t.Errorf("the escape hatch should still survive:\n%s", plain)
	}
	// and something actually had to give for that to be true, so the test
	// is not passing because everything happened to fit
	if strings.Contains(plain, "attended 0/1") {
		t.Errorf("nothing was shed — the case no longer reproduces the tight bar:\n%s", plain)
	}
}

// TestBG084ModePillIsNeverShed guards the other end of the pill row. The
// mode pill is where a locked workspace announces itself ("⬤ locked ·
// ctrl+g"), so the shedding scan holds index 0 back the way it holds the
// last pill back.
func TestBG084ModePillIsNeverShed(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills := []Pill{
		{Text: "⬤ locked · ctrl+g", Kind: KindAlert},
		{Text: "1 todo · 2 active · 1 research · 2 in review · attended 0/1 · unattended 0/2", Kind: KindNeutral},
		{Text: "✉ 2 need you", Kind: KindAlert},
		{Text: "RS-006: document verify — clean", Kind: KindNeutral},
	}
	out := Render(s, 100, pills, decisionHints("run review"))
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "locked") {
		t.Errorf("the mode pill was shed, so a locked workspace stopped saying so:\n%s", plain)
	}
}

// TestBG084ShortPillRowStillTruncates: with only the two protected ends
// present there is nothing to shed, and Render falls through to the
// truncation it always did rather than rendering over its width.
func TestBG084ShortPillRowStillTruncates(t *testing.T) {
	s := theme.New(theme.GummiDark())
	pills := []Pill{
		{Text: "gummi", Kind: KindMode},
		{Text: "a notice far too long to fit on a bar this narrow, by a wide margin indeed", Kind: KindNeutral},
	}
	out := Render(s, 40, pills, decisionHints("run review"))
	if got := lipgloss.Width(out); got > 40 {
		t.Errorf("bar width = %d, want <= 40", got)
	}
}
