package engine

import (
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// attendedFeature builds a feature whose gate-approval mode is
// domain.GateOff — the only mode lanePoolFor reads as attended. Every
// other mode, including the empty default feature() builds, lands in
// the autopilot pool.
func attendedFeature(num int, title string, stage domain.Stage) domain.Feature {
	f := feature(num, title, stage)
	f.GateApproval = domain.GateOff
	return f
}

// TestAttendedNeverQueuesBehindAutopilot is the split-scheduler's
// acceptance test. With the pools at gummi's real defaults (one attended
// lane, two autopilot lanes): three autopilot cards compete for the two
// autopilot lanes, so the third must wait; a fourth, attended card must
// start immediately even though the autopilot pool is completely full
// and already has a card queued behind it — an attended card must never
// queue behind autopilot work.
func TestAttendedNeverQueuesBehindAutopilot(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, AutopilotLanes: 2,
	})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	auto1 := feature(1, "auto one", domain.StageImplement)
	auto2 := feature(2, "auto two", domain.StageImplement)
	auto3 := feature(3, "auto three", domain.StageImplement)
	attended := attendedFeature(4, "attended", domain.StageImplement)
	for _, f := range []domain.Feature{auto1, auto2, auto3, attended} {
		withWorktree(t, wt, f)
	}

	if err := e.Run(auto1); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(auto2); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateRunning)
	if err := e.Run(auto3); err != nil {
		t.Fatal(err)
	}
	// the autopilot pool's two lanes are both taken: the third autopilot
	// card waits rather than starting a third concurrent run.
	waitState(t, e, "FD-003", StateQueued)

	// the attended card must start immediately — it competes in its own
	// pool, so a full (and already-queued-behind) autopilot pool must not
	// make it wait at all.
	if err := e.Run(attended); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-004", StateRunning)

	lc := e.LaneCounts()
	if lc.AttendedRunning != 1 || lc.AttendedMax != 1 {
		t.Errorf("attended lane = %d/%d, want 1/1", lc.AttendedRunning, lc.AttendedMax)
	}
	if lc.AutopilotRunning != 2 || lc.AutopilotMax != 2 {
		t.Errorf("autopilot lane = %d/%d, want 2/2", lc.AutopilotRunning, lc.AutopilotMax)
	}

	// FD-003 is still waiting, unaffected by the attended card's run.
	if s := e.Get("FD-003"); s == nil || s.State() != StateQueued {
		t.Fatalf("FD-003 should still be queued, got %v", s)
	}
}
