package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestCheckpointFailureWarnsWithoutStopping: a checkpoint commit that
// fails mid-stage (main drifted past the feature's recorded fork point)
// must surface as one "checkpoint_failed" NDJSON line naming the card and
// stage, without the stage loop treating it as a decision boundary — the
// stream keeps going past it instead of ending there.
func TestCheckpointFailureWarnsWithoutStopping(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(h *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// The spec-to-implement gate advance also fires a one-shot
			// scribe pass (discoverAndBaselineChecks) against this same
			// worktree before the real implement turn runs; it shares this
			// stage's key in the script map, so only rewind on the
			// implementer's own turn — not the scribe's.
			if o.Role != agent.RoleImplementer {
				return msgIdle(o.Model, "")
			}
			// runs strictly after Driver.Run's spec-to-implement gate advance
			// has already called wt.Create and stamped the fork point
			// (internal/engine/advance.go:236-240), so the rewind is
			// guaranteed to postdate the recorded fork point.
			rewindMain(t, h.root)
			if err := os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	// The rewind is permanent (not just a one-turn glitch), so implement's
	// checkpoint failure is followed by the pre-existing, out-of-scope
	// drift refusal at the next gate advance (locate's own
	// AssertNoForkDrift precheck) — Run legitimately ends in that error.
	// What this test cares about is the checkpoint_failed line the failed
	// checkpoint itself produced, and that it wasn't treated as ending the
	// implement stage's own awaitStage loop.
	_, _ = h.driver(Options{}).Run(context.Background(), "add a json export")

	events := h.events()
	var idx int
	var found map[string]any
	for i, ev := range events {
		if ev["event"] == "checkpoint_failed" {
			idx = i
			found = ev
			break
		}
	}
	if found == nil {
		t.Fatalf("no checkpoint_failed event in stream; stream=%v", h.eventKinds())
	}
	if found["id"] != "FD-001" {
		t.Errorf("checkpoint_failed id = %v, want FD-001", found["id"])
	}
	if found["stage"] != "implement" {
		t.Errorf("checkpoint_failed stage = %v, want implement", found["stage"])
	}
	if s, _ := found["error"].(string); s == "" {
		t.Error("checkpoint_failed error is empty")
	}
	if idx == len(events)-1 {
		t.Fatal("checkpoint_failed was the last stream entry; awaitStage must keep reading past it")
	}
}
