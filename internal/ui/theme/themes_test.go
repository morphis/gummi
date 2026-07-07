package theme

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
)

// renderSwatch draws one line per derived style, so a theme's rendered
// output is reviewable as a golden.
func renderSwatch(s *Styles) string {
	var b strings.Builder
	line := func(name, styled string) { b.WriteString(name + ": " + styled + "\n") }
	line("base", s.Base.Render("The quick brown fox"))
	line("muted", s.Muted.Render("The quick brown fox"))
	line("faint", s.Faint.Render("The quick brown fox"))
	line("title", s.Title.Render("Feature detail"))
	line("selection", s.Selection.Render(" FD-042 dark mode "))
	line("pill-alert", s.PillAlert.Render("✉ 2 need you"))
	line("error", s.Error.Render("✗ build failed"))
	line("warning", s.Warning.Render("⚠ budget 80%"))
	line("success", s.Success.Render("✓ tests green"))
	line("info", s.Info.Render("ℹ session resumed"))
	for _, st := range domain.Stages {
		line("stage-"+string(st), s.Stage(st).Render("●")+" "+s.StagePill(st).Render(string(st)))
	}
	return b.String()
}

func TestLightSwatch(t *testing.T) {
	golden.RequireEqual(t, []byte(renderSwatch(New(GummiLight()))))
}

func TestNeonSwatch(t *testing.T) {
	golden.RequireEqual(t, []byte(renderSwatch(New(GummiNeon()))))
}

// TestAllThemesCompleteAndBuild checks every registered theme covers all
// workflow stages, fills every semantic slot, and derives a usable style
// set — so a new theme can't ship a nil color that would panic at render.
func TestAllThemesCompleteAndBuild(t *testing.T) {
	for _, name := range Names() {
		th, ok := ByName(name)
		if !ok {
			t.Errorf("Names() lists %q but ByName doesn't know it", name)
			continue
		}
		for _, st := range domain.Stages {
			if _, ok := th.StageAccents[st]; !ok {
				t.Errorf("theme %s: stage %s has no accent", name, st)
			}
		}
		slots := map[string]interface{}{
			"Primary": th.Primary, "Secondary": th.Secondary, "Accent": th.Accent,
			"FgBase": th.FgBase, "FgSubtle": th.FgSubtle, "FgMuted": th.FgMuted, "FgFaint": th.FgFaint,
			"BgBase": th.BgBase, "BgSubtle": th.BgSubtle, "BgSurface": th.BgSurface, "BgRaised": th.BgRaised,
			"OnAccent": th.OnAccent, "Separator": th.Separator,
			"Error": th.Error, "Warning": th.Warning, "Success": th.Success, "Info": th.Info, "Destructive": th.Destructive,
		}
		for slot, c := range slots {
			if c == nil {
				t.Errorf("theme %s: slot %s is nil", name, slot)
			}
		}
		if s := New(th); s == nil {
			t.Errorf("theme %s: New returned nil styles", name)
		}
	}
}

func TestByNameFallsBackToDark(t *testing.T) {
	th, ok := ByName("does-not-exist")
	if ok {
		t.Error("unknown theme reported as known")
	}
	if th.Name != "gummi-dark" {
		t.Errorf("fallback = %s, want gummi-dark", th.Name)
	}
}

// TestFilledPillsAreReadable guards every theme's filled pills against
// the white-on-Zest class of bug (contrast 1.1): ink picked for one
// fill silently reused on a much brighter one. 3.0 is the WCAG AA
// ratio for large/bold text — the right floor for pill labels; some
// mid-tone fills (Squid, Chili) can't reach 4.5 with any ink.
func TestFilledPillsAreReadable(t *testing.T) {
	const minRatio = 3.0
	for _, name := range Names() {
		th, _ := ByName(name)
		check := func(what string, fill, ink color.Color) {
			if r := contrast(fill, ink); r < minRatio {
				t.Errorf("theme %s: %s ink/fill contrast %.2f < %.1f", name, what, r, minRatio)
			}
		}
		check("mode pill", th.Accent, th.OnAccent)
		check("alert pill", th.Warning, th.OnFill(th.Warning))
		for _, st := range domain.Stages {
			a := th.StageAccent(st)
			check("stage pill "+string(st), a, th.OnFill(a))
		}
	}
}
