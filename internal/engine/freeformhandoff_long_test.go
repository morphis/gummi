package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

// TestALongLiveBriefIsHandedBackWhole: the session's own brief is what the
// person edits before anything mints, so a reply longer than the prompt's
// size hint must reach the dialog whole — on the first open and on a
// reopen that hands the recorded brief back.
func TestALongLiveBriefIsHandedBackWhole(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(7, "the retry test flakes on CI")
	createFeature(t, store, f)

	long := "asked:\n- fix the flake\n\ndecided:\n" + strings.Repeat("- a decision the architect needs to read\n", 200) +
		"\ndone:\n- the retry loop\n\nremaining:\n- THE LAST LINE OF THE BRIEF"
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			return []agent.Event{{Kind: agent.EventMessage, Text: long}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "looking at it"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "why does the retry test flake on CI?"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	for _, open := range []string{"first open", "reopen"} {
		brief, source, err := awaitBrief(e, f.ID)
		if err != nil {
			t.Fatalf("%s: %v", open, err)
		}
		if source != BriefLive {
			t.Errorf("%s: source = %q, want live", open, source)
		}
		if strings.Contains(brief, "trimmed to fit") || !strings.Contains(brief, "THE LAST LINE OF THE BRIEF") {
			t.Errorf("%s: the session's %d-character brief came back cut to %d:\n…%s", open, len(long), len(brief), brief[max(len(brief)-120, 0):])
		}
	}
}
