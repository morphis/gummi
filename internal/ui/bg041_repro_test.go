package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/state"
)

// BG-041 repro: a multi-line message must not lose trailing lines when
// stageEventLine truncates against a width budget spent on the sum of
// the lines.
func TestBG041ReproMultiLineMessageDropped(t *testing.T) {
	s := m0Styles()
	answered := map[string]bool{}
	payload, _ := json.Marshal(messagePayload{
		Author:  "reviewer",
		Content: "Repo checks clean; verification plan satisfied.\n\nVERDICT: pass",
	})
	ev := state.CardEvent{Kind: state.EventMessage, Payload: string(payload)}

	for _, w := range []int{60, 56, 52, 48, 44, 40} {
		got := ansi.Strip(stageEventLine(s, ev, w, answered))
		t.Logf("w=%d got=%q", w, got)
		if !strings.Contains(got, "VERDICT: pass") {
			t.Errorf("w=%d: VERDICT: pass missing from rendered history line: %q", w, got)
		}
	}
}
