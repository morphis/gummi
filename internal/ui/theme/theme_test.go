package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/domain"
)

func TestGummiDarkCoversAllStages(t *testing.T) {
	th := GummiDark()
	for _, st := range domain.Stages {
		if _, ok := th.StageAccents[st]; !ok {
			t.Errorf("stage %s has no accent", st)
		}
	}
	if th.StageAccent("bogus") != th.FgMuted {
		t.Error("unknown stage should fall back to FgMuted")
	}
}

// TestStyleSwatch golden-renders one line per style so any change to
// the derived styles is visible in review.
func TestStyleSwatch(t *testing.T) {
	s := New(GummiDark())
	var b strings.Builder
	line := func(name string, styled string) {
		b.WriteString(name + ": " + styled + "\n")
	}
	line("base", s.Base.Render("The quick brown fox"))
	line("subtle", s.Subtle.Render("The quick brown fox"))
	line("muted", s.Muted.Render("The quick brown fox"))
	line("faint", s.Faint.Render("The quick brown fox"))
	line("title", s.Title.Render("Feature detail"))
	line("subtitle", s.Subtitle.Render("Activity"))
	line("panetitle", s.PaneTitle.Render("IN PROGRESS"))
	line("keyhint", s.KeyHint.Render("enter")+" "+s.KeyLabel.Render("attach"))
	line("selection", s.Selection.Render(" FD-042 dark mode "))
	line("cardid", s.CardID.Render("FD-042")+" "+s.CardTitle.Render("Dark mode")+" "+s.ProfileTag.Render("[thrifty]"))
	line("pill-mode", s.PillMode.Render("gummi"))
	line("pill", s.Pill.Render("2 paused"))
	line("pill-alert", s.PillAlert.Render("✉ 2 need you"))
	line("destructive", s.Destructive.Render("✗ delete feature"))
	line("error", s.Error.Render("✗ build failed"))
	line("warning", s.Warning.Render("⚠ budget 80%"))
	line("success", s.Success.Render("✓ tests green"))
	line("info", s.Info.Render("ℹ session resumed"))
	for _, st := range domain.Stages {
		line("stage-"+string(st), s.Stage(st).Render("●")+" "+s.StagePill(st).Render(string(st)))
	}
	golden.RequireEqual(t, []byte(b.String()))
}

func TestGradStableAndCovering(t *testing.T) {
	s := New(GummiDark())
	out := Grad(s.Base, "gummi wordmark", s.Theme.Primary, s.Theme.Secondary)
	if got := ansi.Strip(out); got != "gummi wordmark" {
		t.Errorf("gradient mangled text: %q", got)
	}
	if Grad(s.Base, "", s.Theme.Primary, s.Theme.Secondary) != "" {
		t.Error("empty input should render empty")
	}
	golden.RequireEqual(t, []byte(out))
}
