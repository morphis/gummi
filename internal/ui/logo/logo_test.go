package logo

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/ui/theme"
)

func TestWordmarkShape(t *testing.T) {
	wm := Wordmark()
	lines := strings.Split(wm, "\n")
	if len(lines) != wordmarkRows {
		t.Fatalf("wordmark has %d rows, want %d", len(lines), wordmarkRows)
	}
	w := lipgloss.Width(wm)
	if w < 20 || w > 40 {
		t.Errorf("wordmark width %d looks wrong", w)
	}
	golden.RequireEqual(t, []byte(wm))
}

func TestRenderGradient(t *testing.T) {
	s := theme.New(theme.GummiDark())
	golden.RequireEqual(t, []byte(Render(s, 80)))
}

func TestSplash80(t *testing.T) {
	s := theme.New(theme.GummiDark())
	golden.RequireEqual(t, []byte(Splash(s, "v0.1.0-test", 80, 24)))
}

func TestSplash120(t *testing.T) {
	s := theme.New(theme.GummiDark())
	golden.RequireEqual(t, []byte(Splash(s, "v0.1.0-test", 120, 34)))
}
