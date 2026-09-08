package engine

import (
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// attendedFeature builds a feature whose gate-approval mode is
// domain.GateAttended — the mode lanePoolFor reads as attended.
func attendedFeature(num int, title string, stage domain.Stage) domain.Feature {
	f := feature(num, title, stage)
	f.GateApproval = domain.GateAttended
	return f
}

// autopilotFeature builds a feature in the autopilot pool. The mode has to
// be set explicitly: domain.GateAutopilot is the ONLY mode that pools as
// autopilot, and the empty default feature() builds is attended (it reads
// as domain.GateAttended — see domain.(*Feature).GateMode). These lane
// tests used to rely on that empty default landing in the autopilot pool,
// which is precisely the misclassification lanePoolFor no longer makes.
func autopilotFeature(num int, title string) domain.Feature {
	f := feature(num, title, domain.StageImplement)
	f.GateApproval = domain.GateAutopilot
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

	auto1 := autopilotFeature(1, "auto one")
	auto2 := autopilotFeature(2, "auto two")
	auto3 := autopilotFeature(3, "auto three")
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

// TestLanePoolForEmptyGateIsAttended is the regression for the pooling
// half of the empty-gate defect: domain.Feature.GateApproval is storable
// empty and documented as reading like domain.GateAttended, so lanePoolFor
// must resolve it (GateMode) rather than compare the raw string. It used
// to compare, which put every card `bugs new` and the GitHub import ever
// minted into the autopilot pool — competing for autopilot lanes, and
// stopped by StopForQuit as autopilot work — while its own card page read
// "autopilot: off".
func TestLanePoolForEmptyGateIsAttended(t *testing.T) {
	for _, tc := range []struct {
		gate string
		want lanePool
	}{
		{"", poolAttended}, // the stored value every unset card carries
		{domain.GateAttended, poolAttended},
		{domain.GateAutopilot, poolAutopilot},
	} {
		f := feature(1, "gate", domain.StageImplement)
		f.GateApproval = tc.gate
		if got := lanePoolFor(f); got != tc.want {
			t.Errorf("lanePoolFor(GateApproval=%q) = %v, want %v", tc.gate, got, tc.want)
		}
	}
}
