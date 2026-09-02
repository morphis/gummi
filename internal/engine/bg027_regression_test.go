package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestBG027AttendedLaneOutlivesBusy locks in that the footer's attended
// count never holds a slot for a card whose agent has already gone idle.
// A slow pre-commit hook stretches settle()'s checkpoint commit to a
// deterministic, observable width, so this test can catch the window
// between setBusy(false) and freeSlot — freeSlot now runs as soon as the
// successful-finish branch is entered, ahead of settle/stageReceipt/
// gateVerifyVerdict, so no such window remains.
func TestBG027AttendedLaneOutlivesBusy(t *testing.T) {
	ws, store, wt := newRepo(t)

	hookDir := filepath.Join(ws.RepoRoot, ".git", "hooks")
	if err := os.WriteFile(filepath.Join(hookDir, "pre-commit"), []byte("#!/bin/sh\nsleep 0.3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "x.txt"), []byte("x\n"), 0o600); err != nil {
			t.Error(err)
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", AutopilotLanes: 1})
	t.Cleanup(func() { e.Close() })

	f := attendedFeature(1, "attended", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)

	deadline := time.After(5 * time.Second)
	for e.Get("FD-001").Busy() {
		select {
		case <-deadline:
			t.Fatal("agent never went idle")
		case <-time.After(2 * time.Millisecond):
		}
	}
	// Busy() just flipped false; freeSlot must follow within the same
	// handful of milliseconds of engine-internal bookkeeping — nowhere
	// near the pre-commit hook's 300ms — so a short bounded poll here
	// distinguishes "still bounded by the checkpoint commit" (fails) from
	// "already free" (passes) without pinning to a single instant that a
	// stray git-status subprocess could occasionally still be running.
	freeDeadline := time.After(100 * time.Millisecond)
	for {
		if lc := e.LaneCounts(); lc.AttendedRunning == 0 {
			break
		}
		select {
		case <-freeDeadline:
			t.Fatalf("BG-027: attended lane still shows a card running 100ms after Busy() went false")
		case <-time.After(1 * time.Millisecond):
		}
	}
}

// TestBG027AutopilotPoolStaysWiderThanTheBadge pins the scheduling
// invariant that makes defect 2 (see internal/ui's
// TestBG027FooterLabelMatchesBadgedPopulation) a labeling problem rather
// than a pooling one: lanePoolFor deliberately pools every non-GateOff
// card — including the empty default every TUI-created card stores —
// into poolAutopilot, while the card line's badge (board.go) lights up
// only for the explicit GateGates value. That gap is correct and
// pinned by TestAttendedNeverQueuesBehindAutopilot; this test documents
// it stays open so a future change here doesn't silently make the UI
// test moot by narrowing the pool to match the badge.
func TestBG027AutopilotPoolStaysWiderThanTheBadge(t *testing.T) {
	f := feature(1, "default gate", domain.StageImplement)
	inAutopilotPool := lanePoolFor(f) == poolAutopilot
	badgedAsAutopilot := f.GateApproval == domain.GateGates // board.go's badge condition
	if inAutopilotPool == badgedAsAutopilot {
		t.Fatalf("BG-027: expected lanePoolFor's autopilot grouping (%v) to stay wider than the card-line autopilot badge (%v) for GateApproval=%q — if these now agree, the footer's \"unattended\" label may no longer be needed",
			inAutopilotPool, badgedAsAutopilot, f.GateApproval)
	}
}
