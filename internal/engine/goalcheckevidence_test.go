package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/verify"
)

// TestAFailingCheckSaysWhatItSaid: an exit code cannot tell a done-when
// item that is not met yet from one whose command never ran — a directory
// that is not there, a binary that is not on the path. The hand-over is
// the only place the person who can act on that difference sees it.
func TestAFailingCheckSaysWhatItSaid(t *testing.T) {
	note := checkFailureNote(verify.Result{
		Name: "done-when DW-4", OK: false, ExitCode: 1,
		Output: "\n+ cd rig\nbash: line 1: cd: rig: No such file or directory\n",
	})
	if !strings.Contains(note, "cd: rig: No such file or directory") {
		t.Fatalf("a failing check kept nothing of what it said: %q", note)
	}
	if strings.Contains(note, "\n") {
		t.Errorf("the note is one line in a hand-over: %q", note)
	}
	if got := checkFailureNote(verify.Result{Name: "x", OK: true, Output: "all good"}); got != "" {
		t.Errorf("a passing check needs no excuse: %q", got)
	}
	long := strings.Repeat("a very long line of check output ", 40)
	if n := checkFailureNote(verify.Result{Name: "x", Output: long}); len(n) > 320 {
		t.Errorf("the note is bounded, got %d characters", len(n))
	}

	// and it reaches the page the owner actually reads
	body := RenderGoalReport(GoalReport{
		ID: "GL-001", Title: "a goal", Ready: true,
		DoneWhen: []DoneWhenStatus{{
			ID: "DW-4", Says: "the tests pass", How: "check: cd rig && go test ./...",
			Status: DoneWhenNotMet, Evidence: "check FAIL (exit 1) — " + note,
		}},
	})
	if !strings.Contains(body, "cd: rig: No such file or directory") {
		t.Fatalf("the hand-over does not say what the check said:\n%s", body)
	}
}
