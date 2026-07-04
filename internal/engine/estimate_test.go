package engine

import (
	"context"
	"testing"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
)

func TestParseScribeEstimate(t *testing.T) {
	cases := map[string]float64{
		"ESTIMATE: 120":                         120,
		"blah\nESTIMATE: 85.5\nthanks":          85.5,
		"estimate:  $240":                       240,
		"ESTIMATE: 10\n...revised ESTIMATE: 30": 30, // last wins
	}
	for in, want := range cases {
		if got, ok := parseScribeEstimate(in); !ok || got != want {
			t.Errorf("parse(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"no number here", "ESTIMATE: zero", "ESTIMATE: 0", ""} {
		if got, ok := parseScribeEstimate(in); ok {
			t.Errorf("parse(%q) = %v, want no estimate", in, got)
		}
	}
}

func TestEstimateRunsScribe(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role != agent.RoleScribe {
			t.Errorf("estimate used role %q, want scribe", opts.Role)
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Looks medium-sized.\nESTIMATE: 175"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.Estimate(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got != 175 {
		t.Errorf("Estimate = %v, want 175", got)
	}
}
