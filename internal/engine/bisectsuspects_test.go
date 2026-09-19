package engine

import (
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
)

// TestOnlyALandingTheRunCouldHaveSeenIsASuspect: the bisect asks which
// landing broke what was green, and its candidates are the landings
// between the last green run and the failing one. A landing in a
// repository the experiment does not deploy cannot have changed what the
// run saw — and leaving it in is worse than noise, because headsAfter
// leaves the head tuple unmoved across it, so it shares a tuple with the
// landing before it and one run answers for both.
func TestOnlyALandingTheRunCouldHaveSeenIsASuspect(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	green := experiment.Result{
		ID: "green", State: experiment.StateDone, Outcome: experiment.Pass,
		Started: t0, Heads: map[string]string{"netd": "n0", "fabricd": "f0"},
		Assertions: []experiment.Assertion{{ID: "ing-a", OK: true}},
	}
	red := experiment.Result{
		ID: "red", State: experiment.StateDone, Outcome: experiment.Fail,
		Started: t0.Add(30 * time.Minute), Ended: t0.Add(35 * time.Minute),
		Heads:      map[string]string{"netd": "n1", "fabricd": "f0"},
		Assertions: []experiment.Assertion{{ID: "ing-a", OK: false, Detail: "gone"}},
	}
	x := GoalExperiment{
		Name:  "matrix",
		Heads: map[string]string{"netd": "n1", "fabricd": "f0"}, // the experiment's inputs
		Runs:  []experiment.Result{green, red}, Evidence: &red,
	}
	x.readFrontier([]goalLanding{
		{Card: "FD-001", Repo: "netd", SHA: "n1", At: t0.Add(10 * time.Minute)},
		{Card: "RS-002", Repo: "docs", SHA: "d1", At: t0.Add(20 * time.Minute)},
	})

	if len(x.Regressed) != 1 || x.Regressed[0] != "ing-a" {
		t.Fatalf("regressed = %v, want the assertion that stopped holding", x.Regressed)
	}
	for _, s := range x.Suspects {
		if s.Repo == "docs" {
			t.Fatalf("a landing in a repository the experiment never deploys is a suspect: %+v", x.Suspects)
		}
	}
	if len(x.Suspects) != 1 || x.Suspects[0].Card != domain.FeatureID("FD-001") {
		t.Fatalf("suspects = %+v, want only the landing the run could have seen", x.Suspects)
	}
}
