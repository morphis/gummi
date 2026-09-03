package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG078PinnedArtifactHintFiresFromTheCardPage is BG-078's regression
// test. The thread's pinned artifact line ends in a key hint its own doc
// comment calls "the key that opens the full view", and that line is
// drawn on exactly one surface: the card page. But a card opens with the
// composer focused and never loses it, and the composer owns every
// printable key — so the plain s the line used to name was typed into
// the reader's draft rather than opening anything. The hint could not
// fire on the only surface that draws it.
//
// Both halves are asserted, because either one alone lets the defect
// back: the line must name a key the page answers, and pressing that key
// must not type.
func TestBG078PinnedArtifactHintFiresFromTheCardPage(t *testing.T) {
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 4, "file pull truncation", domain.StageSpec)
	r := featureRow{F: f}
	m.rows = []featureRow{r}
	m.sel = 0
	m.cardOpen = true
	m.focusThreadInput()
	if !m.threadInput.Focused() {
		t.Fatal("precondition: the card page is supposed to open with the composer focused")
	}

	line := ansi.Strip(pinnedSpecLine(m0Styles(), r, 100))
	if line == "" {
		t.Fatal("precondition: this stage draws no pinned artifact line")
	}

	// the key the line names has to be one the composer cannot swallow.
	// A bare printable letter is exactly what it can.
	hint := strings.TrimSpace(line[strings.LastIndex(line, " "):])
	if !strings.HasPrefix(hint, "alt+") {
		t.Errorf("the pinned line advertises %q, which the focused composer types instead of acting on", hint)
	}

	// and pressing it must open the artifact rather than reach the draft
	before := m.threadInput.Value()
	cmd := m.handleThreadInputKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt})
	if got := m.threadInput.Value(); got != before {
		t.Errorf("the advertised key typed into the composer: draft %q -> %q", before, got)
	}
	if cmd == nil {
		t.Error("the advertised key did nothing — the artifact never opened")
	}
}
