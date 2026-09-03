package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestBG086RestoreRecoversTheVerdictFloor is BG-086's engine half: the
// floor has to travel out to the store and back into the rehydrated
// session, or persisting the column buys nothing.
//
// The floor is gummi's own deterministic judgement on a stage, and it
// only ever downgrades — verdict.SessionVerdict turns a raw Pass into
// Blocked while it stands. Before this it lived on the session alone, so
// the next process read the agent's "VERDICT: pass" out of the restored
// transcript with nothing left to overrule it, and the reason it had
// been overruled was gone entirely.
func TestBG086RestoreRecoversTheVerdictFloor(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "checks that fail", domain.StageVerify)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	const reason = "check go-test failed"
	if err := store.SaveSession(ctx, state.SessionSnapshot{
		Feature: f.ID, Stage: domain.StageVerify, Role: string(agent.RoleReviewer),
		Flavor: "stage", State: "done",
		Verdict:            "pass",
		VerdictFloor:       "blocked",
		VerdictFloorReason: reason,
		Transcript: []state.SessionMessage{
			{Author: "assistant", Content: "all good\nVERDICT: pass"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	e := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	t.Cleanup(func() { e.Close() })
	if err := e.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e.Get(f.ID)
	if s == nil {
		t.Fatal("session not restored")
	}
	snap := s.Snapshot()
	if snap.VerdictFloor != "blocked" {
		t.Errorf("restored VerdictFloor = %q, want %q — the agent's pass stands unchallenged",
			snap.VerdictFloor, "blocked")
	}
	if snap.VerdictFloorReason != reason {
		t.Errorf("restored VerdictFloorReason = %q, want %q — the card cannot say what to fix",
			snap.VerdictFloorReason, reason)
	}
}
