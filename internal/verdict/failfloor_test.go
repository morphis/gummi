package verdict

import (
	"testing"

	"github.com/morphis/gummi/internal/engine"
)

func snapWith(floor, tool string) engine.Snapshot {
	return engine.Snapshot{Verdict: tool, VerdictFloor: floor}
}

// TestFailFloorOutranksASelfReportedPass: the floor exists so gummi's own
// machine judgement cannot be overruled by what the agent concluded. A
// branch shipping a build artifact is not landable whatever the reviewer
// says about the code.
func TestFailFloorOutranksASelfReportedPass(t *testing.T) {
	if got := SessionVerdict(snapWith("fail", "pass")); got != Fail {
		t.Errorf("pass under a fail floor = %v, want Fail", got)
	}
	// blocked is an environment claim; the branch still ships the artifact
	if got := SessionVerdict(snapWith("fail", "blocked")); got != Fail {
		t.Errorf("blocked under a fail floor = %v, want Fail", got)
	}
	// a worse raw verdict is left alone — the floor only ever downgrades
	if got := SessionVerdict(snapWith("fail", "fail")); got != Fail {
		t.Errorf("fail under a fail floor = %v, want Fail", got)
	}
}

// The pre-existing blocked floor keeps its behaviour exactly.
func TestBlockedFloorUnchanged(t *testing.T) {
	if got := SessionVerdict(snapWith("blocked", "pass")); got != Blocked {
		t.Errorf("pass under a blocked floor = %v, want Blocked", got)
	}
	if got := SessionVerdict(snapWith("blocked", "fail")); got != Fail {
		t.Errorf("a blocked floor promoted a fail to %v", got)
	}
}

// With no floor the agent's verdict stands.
func TestNoFloorLeavesTheVerdictAlone(t *testing.T) {
	if got := SessionVerdict(snapWith("", "pass")); got != Pass {
		t.Errorf("unfloored pass = %v, want Pass", got)
	}
}
