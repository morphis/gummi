package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
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

func TestEstimateStreamedReplyNotDoubled(t *testing.T) {
	// streaming adapters emit deltas and then the completed message they
	// were streaming; the reply is collected once (message replaces its
	// deltas) and reasoning stays out of the parse — a number mentioned
	// while thinking must not become the estimate.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventReasoningDelta, Text: "hmm, maybe ESTIMATE: 999?"},
			{Kind: agent.EventTextDelta, Text: "Small change.\nESTIM"},
			{Kind: agent.EventTextDelta, Text: "ATE: 40"},
			{Kind: agent.EventMessage, Text: "Small change.\nESTIMATE: 40"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.Estimate(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got != 40 {
		t.Errorf("Estimate = %v, want 40", got)
	}

	// a reply with no estimate stays no-estimate even when the thinking
	// mentioned a number.
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventReasoningDelta, Text: "could be ESTIMATE: 999"},
			{Kind: agent.EventMessage, Text: "I cannot estimate this."},
			{Kind: agent.EventIdle},
		}
	}
	got, err = e.Estimate(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("Estimate = %v, want 0 (thinking must not be parsed)", got)
	}
}

// claudeNamed presents a fake backend under the claude backend's name,
// so the estimate multiplier keys off it.
type claudeNamed struct{ *agent.Fake }

func (claudeNamed) Name() string { return "claude" }

func TestEstimateAppliesBackendCostFactor(t *testing.T) {
	// the scribe's raw guess is scaled by the backend's cost factor —
	// claude sessions burn a multiple of the mid-tier price the scribe
	// assumes, so the raw number would gate almost immediately.
	ag := claudeNamed{&agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Medium.\nESTIMATE: 100"},
			{Kind: agent.EventIdle},
		}
	}}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.Estimate(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got != 250 {
		t.Errorf("Estimate = %v, want 250 (100 × 2.5 claude factor)", got)
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
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

func TestEstimateCarriesArtifactPath(t *testing.T) {
	for _, f := range []domain.Feature{
		feature(1, "impl", domain.StageImplement),
		bugFeature("flaky"),
	} {
		t.Run(string(f.ID), func(t *testing.T) {
			ws, store, wt := newRepo(t)
			rec := recordingAgent()
			e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
			t.Cleanup(func() { e.Close() })
			withWorktree(t, wt, f)
			if _, err := e.Estimate(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(wt.Root(), f.ArtifactPath())
			if rec.opts().ArtifactPath != want {
				t.Errorf("ArtifactPath = %s, want %s", rec.opts().ArtifactPath, want)
			}
		})
	}
}

func TestEstimatePassesSpecPathAsExtraRead(t *testing.T) {
	for _, f := range []domain.Feature{
		feature(1, "impl", domain.StageImplement),
		bugFeature("flaky"),
	} {
		t.Run(string(f.ID), func(t *testing.T) {
			ws, store, wt := newRepo(t)
			rec := recordingAgent()
			e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
			t.Cleanup(func() { e.Close() })
			withWorktree(t, wt, f)
			if _, err := e.Estimate(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(wt.Root(), f.ArtifactPath())
			if got := rec.opts().ExtraReadAllows; !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("ExtraReadAllows = %v, want [%s]", got, want)
			}
		})
	}
}
