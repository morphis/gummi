package goalpolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// TestAGoalThatProducesNothingSaysSo: every rule of the conductor's has had
// its turn, nothing is running, no run is in flight, and none of them found
// work. That is not a state a goal reaches on its own — it is what any bug
// that makes one card unreadable looks like from the outside, and a goal in
// it will tick silently for ever. One such bug cost a drive 2h49m.
func TestAGoalThatProducesNothingSaysSo(t *testing.T) {
	// The card that froze a goal for 2h49m: "running" as far as the
	// snapshot knows, so no rule claims it and no stop counts it as idle.
	in := Input{
		Stage: domain.StageImplement, Envelope: 4000, Lanes: 2, LeadAvailable: true,
		Cards: []Card{{ID: "FD-001", State: Running, Envelope: 600}},
	}
	if acts := Decide(in); len(acts) != 0 {
		t.Fatalf("a goal with a running card has work to do: %+v", acts)
	}

	in.Quiet = MaxQuiet + time.Minute
	acts := Decide(in)
	if len(acts) != 1 || acts[0].Kind != Stall {
		t.Fatalf("a goal that has produced nothing for %s did not say so: %+v", in.Quiet, acts)
	}
	if !strings.Contains(acts[0].Reason, "nothing has happened") ||
		!strings.Contains(acts[0].Reason, "not what it reads as") {
		t.Errorf("the stall does not say what is wrong: %q", acts[0].Reason)
	}
}

// TestTheBackstopDoesNotPreemptWork: it is a backstop, not a timer. A goal
// with something to do does it, however long it has been quiet — otherwise
// a slow stretch would stop a goal that was about to land a card.
func TestTheBackstopDoesNotPreemptWork(t *testing.T) {
	in := Input{
		Stage: domain.StageImplement, Envelope: 4000, Lanes: 2, LeadAvailable: true,
		Quiet: MaxQuiet * 4,
		Cards: []Card{{ID: "FD-001", State: Verified, Envelope: 600}},
	}
	acts := Decide(in)
	if len(acts) == 0 || acts[0].Kind == Stall {
		t.Fatalf("a verified card was not landed because the goal had been quiet: %+v", acts)
	}
}

// The backstop stays clear of whatever a stage is allowed to be silent
// for. MaxQuiet is longer than the DEFAULT stage timeout on purpose — so
// a stuck stage is cut off by its driver, which is an event, before the
// goal gives up on it — but the timeout is a flag and MaxQuiet was a
// constant, so --stage-timeout 45m put a stage the operator had given 45
// quiet minutes inside a 30-minute backstop.
func TestTheQuietBackstopStaysClearOfTheStageTimeout(t *testing.T) {
	for _, tc := range []struct {
		timeout, want time.Duration
	}{
		{0, MaxQuiet},                // no driver cut-off: MaxQuiet is the only backstop
		{20 * time.Minute, MaxQuiet}, // the default: unchanged, 30m
		{5 * time.Minute, MaxQuiet},  // a short timeout never lowers it
		{45 * time.Minute, 67*time.Minute + 30*time.Second},
		{2 * time.Hour, 3 * time.Hour},
	} {
		if got := QuietCeilingFor(tc.timeout); got != tc.want {
			t.Errorf("QuietCeilingFor(%v) = %v, want %v", tc.timeout, got, tc.want)
		}
	}

	// and a goal quiet for 40 minutes under a 45-minute stage timeout is
	// not stopped, where it used to be
	in := Input{
		Stage: domain.StageImplement, Envelope: 2000, Reserve: 300, Lanes: 2,
		Cards: []Card{{ID: "FD-001", State: Running, Envelope: 500, Spent: 100}},
		Quiet: 40 * time.Minute, QuietCeiling: QuietCeilingFor(45 * time.Minute),
	}
	for _, a := range Decide(in) {
		if a.Kind == Stall {
			t.Errorf("stalled a goal whose stages have 45 minutes to be quiet in: %v", a)
		}
	}
	// past the raised ceiling it still stops
	in.Quiet = 70 * time.Minute
	var stalled bool
	for _, a := range Decide(in) {
		if a.Kind == Stall {
			stalled = true
		}
	}
	if !stalled {
		t.Error("a goal quiet past even the raised ceiling must still say so")
	}
}
