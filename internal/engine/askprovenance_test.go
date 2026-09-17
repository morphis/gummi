package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestCapturedAnswerNamesItsAnswerer locks the provenance of the one
// record that outlives the run. The spec on the branch is what a reviewer
// reads and what the next stage re-reads as settled, and captureAnswer
// filed every answer — including one autopilot took by itself, unattended,
// with nobody at the keyboard — as `%% @user`. On the lxd autopilot drive
// the architect then read its own marker back and wrote "confirmed by the
// user" into Chosen approach about a question no human ever saw.
//
// The event log and the card page have always carried the answerer; only
// the artifact was hardcoded.
func TestCapturedAnswerNamesItsAnswerer(t *testing.T) {
	for _, c := range []struct {
		by   string
		want string
	}{
		{state.ActorAutopilot, "%% @autopilot"},
		{state.ActorUser, "%% @user"},
		{"", "%% @user"}, // an undeclared answerer is a person, as before
	} {
		args := askArgs(t, Ask{
			ChangesSection: "Chosen approach",
			Question:       "Persist where?",
			SpecAnchor:     "dark problem.",
			Options:        []AskOption{{Label: "per-device"}, {Label: "synced"}},
		})
		ag := clientToolFake(args)
		ws, store, wt := newRepo(t)
		e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt,
			Workspace: ws, Model: "fake-model", MaxActive: 1})

		f := feature(1, "Dark mode", domain.StagePlan)
		putFeature(t, store, f)
		seedDraft(t, e, f)
		s, err := e.Attach(context.Background(), f)
		if err != nil {
			e.Close()
			t.Fatalf("by %q: Attach: %v", c.by, err)
		}
		waitFor(t, e, EventQuestion)

		if err := e.AnswerAs(context.Background(), f.ID, "per-device", c.by); err != nil {
			e.Close()
			t.Fatalf("by %q: AnswerAs: %v", c.by, err)
		}
		raw, err := os.ReadFile(s.SpecPath())
		e.Close()
		if err != nil {
			t.Fatalf("by %q: read spec: %v", c.by, err)
		}
		got := string(raw)
		if !strings.Contains(got, c.want+"(") {
			t.Errorf("by %q: answer filed without %s — the artifact does not say who decided\n%s",
				c.by, c.want, got)
		}
		if c.by == state.ActorAutopilot && strings.Contains(got, "%% @user(") {
			t.Errorf("an autopilot-taken answer is still filed as the user's:\n%s", got)
		}
	}
}
