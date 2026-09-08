package engine

import (
	"context"
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

// TestRepoolRunningCardFreesItsOldPool is the mid-run half of the
// dispatch-time-pool defect. A card's pool used to be decided once, from
// the feature run() was handed, and the `A` switch flipping it mid-stage
// changed nothing: the run kept the attended slot it took, so the next
// attended card queued behind unattended work — with the board's own bar
// reading "attended 1/1 · unattended 0/2" over a card badged autopilot.
func TestRepoolRunningCardFreesItsOldPool(t *testing.T) {
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

	handed := attendedFeature(1, "handed over mid-run", domain.StageImplement)
	attended := attendedFeature(2, "attended", domain.StageImplement)
	for _, f := range []domain.Feature{handed, attended} {
		withWorktree(t, wt, f)
	}
	if err := e.Run(handed); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)

	// the `A` switch: this card is unattended from here on.
	e.Repool(handed.ID, domain.GateAutopilot)

	lc := e.LaneCounts()
	if lc.AttendedRunning != 0 {
		t.Errorf("attended lane = %d/%d, want 0/1: the run moved out of it",
			lc.AttendedRunning, lc.AttendedMax)
	}
	if lc.AutopilotRunning != 1 {
		t.Errorf("autopilot lane = %d/%d, want 1/2: the run moved into it",
			lc.AutopilotRunning, lc.AutopilotMax)
	}

	// the attended pool is genuinely free now, so an attended card starts
	// at once rather than waiting on unattended work.
	if err := e.Run(attended); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateRunning)

	// and the slot the moved run holds comes back to the pool it moved
	// INTO when it ends, never to the one it was dispatched in.
	e.freeSlot(e.Get("FD-001"))
	lc = e.LaneCounts()
	if lc.AutopilotRunning != 0 {
		t.Errorf("autopilot lane = %d after the moved run freed its slot, want 0", lc.AutopilotRunning)
	}
	if lc.AttendedRunning != 1 {
		t.Errorf("attended lane = %d after the moved run freed its slot, want 1 (FD-002's own)",
			lc.AttendedRunning)
	}
}

// TestRepoolTakenBackCardEntersAttendedPool is the same defect the other
// way round: a card taken back from autopilot while it runs kept its
// unattended lane, so two of them ran at once under an attended cap of
// one and the bar reported "attended 0/1" over two attended runs.
func TestRepoolTakenBackCardEntersAttendedPool(t *testing.T) {
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

	auto := autopilotFeature(1, "auto")
	attended := attendedFeature(2, "attended", domain.StageImplement)
	for _, f := range []domain.Feature{auto, attended} {
		withWorktree(t, wt, f)
	}
	if err := e.Run(auto); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)

	e.Repool(auto.ID, domain.GateAttended)

	lc := e.LaneCounts()
	if lc.AttendedRunning != 1 || lc.AutopilotRunning != 0 {
		t.Fatalf("lanes = attended %d, unattended %d; want the run counted as attended",
			lc.AttendedRunning, lc.AutopilotRunning)
	}
	// the taken-back card now occupies the single attended lane, so a
	// second attended card waits — which is what MaxActive: 1 means.
	if err := e.Run(attended); err != nil {
		t.Fatal(err)
	}
	if s := e.Get("FD-002"); s == nil || s.State() != StateQueued {
		t.Fatalf("FD-002 should be queued behind the taken-back card, got %v", s)
	}
}

// TestRepoolQueuedCardChangesQueue covers the state that has the least
// excuse for a stale pool: a card that has not started at all. Queued in
// the full autopilot pool and then taken back, it must move to the
// attended queue — and start there, because that pool is free.
func TestRepoolQueuedCardChangesQueue(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, AutopilotLanes: 1,
	})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	running := autopilotFeature(1, "auto running")
	waiting := autopilotFeature(2, "auto waiting")
	for _, f := range []domain.Feature{running, waiting} {
		withWorktree(t, wt, f)
	}
	if err := e.Run(running); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(waiting); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateQueued)

	// taken back before it ever started: it belongs in the attended
	// queue, and the attended lane is free, so it starts immediately
	// instead of waiting out the autopilot card it no longer competes with.
	e.Repool(waiting.ID, domain.GateAttended)
	waitState(t, e, "FD-002", StateRunning)

	lc := e.LaneCounts()
	if lc.AttendedRunning != 1 || lc.AutopilotRunning != 1 {
		t.Errorf("lanes = attended %d/%d, unattended %d/%d; want one run in each",
			lc.AttendedRunning, lc.AttendedMax, lc.AutopilotRunning, lc.AutopilotMax)
	}
}

// TestRepoolIgnoresSessionsThatHoldNoSlot: an interactive session is not
// in either pool (you are the scarce resource, not a lane), and a mode
// write must not invent a running count for one.
func TestRepoolIgnoresSessionsThatHoldNoSlot(t *testing.T) {
	ag := &agent.Fake{}
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, AutopilotLanes: 2,
	})
	t.Cleanup(func() { e.Close() })

	f := attendedFeature(1, "chatting", domain.StagePlan)
	withWorktree(t, wt, f)
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	e.Repool(f.ID, domain.GateAutopilot)
	if lc := e.LaneCounts(); lc.AttendedRunning != 0 || lc.AutopilotRunning != 0 {
		t.Errorf("lanes = attended %d, unattended %d; an interactive session holds no slot",
			lc.AttendedRunning, lc.AutopilotRunning)
	}
	// a card with no session at all is a no-op, not a panic.
	e.Repool("FD-404", domain.GateAutopilot)
}
