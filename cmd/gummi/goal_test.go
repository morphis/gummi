package main

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/driver"
)

// A goal's envelope is its whole budget: missing, it fails before any
// workspace work, the same way a run does.
func TestGoalRequiresEnvelope(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "")
	err := runGoal([]string{"export works offline"})
	if err == nil || !strings.Contains(err.Error(), "envelope is required") {
		t.Fatalf("err = %v, want an envelope-required failure", err)
	}
}

func TestGoalValidatesItsArguments(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "1000")
	if err := runGoal(nil); err == nil || !strings.Contains(err.Error(), "exactly one objective") {
		t.Fatalf("err = %v", err)
	}
	if err := runGoal([]string{"--until", "implement", "x"}); err == nil || !strings.Contains(err.Error(), "not a valid stop") {
		t.Fatalf("err = %v", err)
	}
	if err := runGoal([]string{"--plan-file", "/nonexistent/goal.md", "x"}); err == nil || !strings.Contains(err.Error(), "--plan-file") {
		t.Fatalf("a missing plan file names the flag: %v", err)
	}
}

func TestGoalResumeInput(t *testing.T) {
	in, err := goalResumeInput(driver.ResumeInput{}, "also Windows", "", false, true, false)
	if err != nil || in.Note == nil || *in.Note != "also Windows" {
		t.Fatalf("--goal-note: %+v %v", in, err)
	}
	why := "tables read better"
	in, err = goalResumeInput(driver.ResumeInput{RequestChanges: &why}, "", "D-2", false, false, true)
	if err != nil || in.Reverse == nil || *in.Reverse != "D-2" || in.RequestChanges == nil {
		t.Fatalf("--reverse with a reason: %+v %v", in, err)
	}
	if in, err = goalResumeInput(driver.ResumeInput{}, "", "", true, false, false); err != nil || !in.WrapUp {
		t.Fatalf("--wrap-up: %+v %v", in, err)
	}
	if _, err := goalResumeInput(driver.ResumeInput{}, "n", "D-1", false, true, true); err == nil {
		t.Fatal("two goal flags at once must be refused")
	}
	if _, err := goalResumeInput(driver.ResumeInput{Approve: true}, "", "", true, false, false); err == nil {
		t.Fatal("a goal flag with another decision must be refused")
	}
	if in, err := goalResumeInput(driver.ResumeInput{Approve: true}, "", "", false, false, false); err != nil || !in.Approve {
		t.Fatalf("no goal flag passes the input through: %+v %v", in, err)
	}
}

func TestGoalCmdRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"goal"})
	if err != nil || cmd != goalCmd {
		t.Fatalf("goal is not registered: %v %v", cmd, err)
	}
}
