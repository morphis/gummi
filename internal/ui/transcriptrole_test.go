package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestTranscriptLabelsSeededTurnsWithTheirOwnRole: Engine.OpenConsult
// seeds a consult session with the card's stage transcript, and those
// turns keep the role that produced them (engine.Message.Role). The
// renderer must use it.
//
// Labelling every assistant turn with the session's current role is what
// made the plan stage's kickoff and the reviewer's own "VERDICT: pass"
// render once under `reviewer` in the stage segment and again, fifteen
// lines down the same card page, under `consult` — inside a block
// captioned with the architect's model.
func TestTranscriptLabelsSeededTurnsWithTheirOwnRole(t *testing.T) {
	s := theme.New(theme.GummiDark())
	snap := engine.Snapshot{
		Role: agent.RoleConsult,
		Transcript: []engine.Message{
			// seeded from the stage that produced them
			{Author: engine.AuthorAssistant, Content: "plan-turn", Role: agent.RoleArchitect},
			{Author: engine.AuthorAssistant, Content: "VERDICT: pass", Role: agent.RoleReviewer},
			// this session's own turn: unstamped, the ordinary case
			{Author: engine.AuthorAssistant, Content: "consult-turn"},
		},
	}
	lines := stripANSI(strings.Join(transcriptLines(s, snap, 80, false), "\n"))

	for _, want := range []struct{ role, body string }{
		{"architect", "plan-turn"},
		{"reviewer", "VERDICT: pass"},
		{"consult", "consult-turn"},
	} {
		if !labelPrecedes(lines, want.role, want.body) {
			t.Errorf("%q is not labelled %q:\n%s", want.body, want.role, lines)
		}
	}
	// exactly one label line reads "consult": the session's role is a
	// fallback for its own turn, never a relabelling of the seeded ones
	labels := 0
	for _, l := range strings.Split(lines, "\n") {
		if strings.TrimSpace(l) == "consult" {
			labels++
		}
	}
	if labels != 1 {
		t.Errorf("%d turns labelled consult, want 1:\n%s", labels, lines)
	}
}

// TestTranscriptFallsBackToTheSessionRole: an unstamped message is the
// normal case and means "whatever this session is", so a transcript with
// no roles at all renders exactly as it always did.
func TestTranscriptFallsBackToTheSessionRole(t *testing.T) {
	s := theme.New(theme.GummiDark())
	snap := engine.Snapshot{
		Role: agent.RoleArchitect,
		Transcript: []engine.Message{
			{Author: engine.AuthorAssistant, Content: "first"},
			{Author: engine.AuthorAssistant, Content: "second"},
		},
	}
	lines := stripANSI(strings.Join(transcriptLines(s, snap, 80, false), "\n"))
	if strings.Count(lines, "architect") != 2 {
		t.Errorf("unstamped turns lost the session's own role:\n%s", lines)
	}
}

// labelPrecedes reports whether body appears on a line after a line that
// is exactly the label — transcriptLines writes the author label on its
// own line with the wrapped body indented beneath it.
func labelPrecedes(rendered, label, body string) bool {
	lines := strings.Split(rendered, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != label {
			continue
		}
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				break
			}
			if strings.Contains(next, body) {
				return true
			}
		}
	}
	return false
}
