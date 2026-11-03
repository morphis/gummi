package domain

import (
	"strings"
	"testing"
)

func TestWorkflowRoles(t *testing.T) {
	reads := map[WorkflowRole]bool{RoleScout: true, RoleReviewer: true}
	for _, r := range WorkflowRoles {
		if !r.Known() || r.Reads() != reads[r] || r.Writes() == reads[r] {
			t.Errorf("%s: known %v reads %v writes %v", r, r.Known(), r.Reads(), r.Writes())
		}
	}
	if WorkflowRole("wizard").Known() || WorkflowRole("wizard").Writes() || WorkflowRole("").Writes() {
		t.Error("an unknown role is known or writes")
	}
}

func TestEntryID(t *testing.T) {
	for _, tc := range []struct {
		e    Entry
		want string
	}{
		{Entry{Step: &Step{ID: "s"}}, "s"},
		{Entry{Parallel: &Parallel{ID: "p"}}, "p"},
		{Entry{Repeat: &Repeat{ID: "r"}}, "r"},
		{Entry{}, ""},
	} {
		if got := tc.e.ID(); got != tc.want {
			t.Errorf("ID = %q, want %q", got, tc.want)
		}
	}
}

func TestUntilKind(t *testing.T) {
	for _, tc := range []struct {
		u    Until
		want string
	}{
		{Until{Checks: true}, UntilChecks},
		{Until{Verdict: "OK", In: "r"}, UntilVerdict},
		{Until{In: "r"}, UntilVerdict},
		{Until{Contains: "x"}, UntilContains},
		{Until{}, ""},
		{Until{Checks: true, Contains: "x"}, UntilContains},
		{Until{Checks: true, Verdict: "OK", In: "r"}, UntilVerdict},
		{Until{Verdict: "OK", In: "r", Contains: "x"}, ""},
		{Until{Checks: true, Verdict: "OK", In: "r", Contains: "x"}, ""},
	} {
		if got := tc.u.Kind(); got != tc.want {
			t.Errorf("%+v: kind %q, want %q", tc.u, got, tc.want)
		}
		if and := tc.u.AndChecks(); and != (tc.u.Checks && tc.want != "" && tc.want != UntilChecks) {
			t.Errorf("%+v: AndChecks %v", tc.u, and)
		}
	}
}

func TestJoinsAndOnMax(t *testing.T) {
	for _, j := range []Join{JoinAll, JoinAllSettled, JoinAny} {
		if !j.Known() {
			t.Errorf("%s unknown", j)
		}
	}
	if Join("").Known() || Join("race").Known() {
		t.Error("a bad join is known")
	}
	if !OnMax("").Known() || OnMax("").Or() != OnMaxPause || OnMaxAbort.Or() != OnMaxAbort || OnMax("retry").Known() {
		t.Error("onMax")
	}
	if !(Step{}).Captures() {
		t.Error("a step captures its output by default")
	}
	no := false
	if (Step{CaptureOutput: &no}).Captures() {
		t.Error("captureOutput: false captured")
	}
}

func TestRunAndStepStates(t *testing.T) {
	for _, tc := range []struct {
		s           RunState
		open, ended bool
	}{
		{RunProposed, false, false},
		{RunQueued, false, false},
		{RunRunning, true, false},
		{RunPaused, true, false},
		{RunDone, false, true},
		{RunPartial, false, true},
		{RunStopped, false, true},
		{RunDenied, false, false},
	} {
		if tc.s.Open() != tc.open || tc.s.Ended() != tc.ended {
			t.Errorf("%s: open %v ended %v", tc.s, tc.s.Open(), tc.s.Ended())
		}
	}
	for _, tc := range []struct {
		s             StepState
		live, settled bool
	}{
		{StepWaiting, false, false},
		{StepRunning, true, false},
		{StepAsking, true, false},
		{StepDone, false, true},
		{StepFailed, false, true},
		{StepStopped, false, true},
	} {
		if tc.s.Live() != tc.live || tc.s.Settled() != tc.settled {
			t.Errorf("%s: live %v settled %v", tc.s, tc.s.Live(), tc.s.Settled())
		}
	}
}

func TestPaths(t *testing.T) {
	if got := StepPath("build", "2", "implement"); got != "build/2/implement" {
		t.Errorf("StepPath = %q", got)
	}
	if got := IterationPath("", "build", 2); got != "build/2" {
		t.Errorf("IterationPath = %q", got)
	}
	if got := IterationPath("outer/1", "inner", 3); got != "outer/1/inner/3" {
		t.Errorf("nested IterationPath = %q", got)
	}
}

func TestSpecStateOf(t *testing.T) {
	met := []ItemResult{{Item: "DW-1", Checked: true, Verdict: ItemMet}, {Item: "DW-2", Verdict: ItemMet}}
	unmet := []ItemResult{{Item: "DW-1", Checked: true, Verdict: ItemUnmet}}
	unjudged := []ItemResult{{Item: "DW-1", Checked: true, Verdict: ItemMet}, {Item: "DW-2", Checked: true, Verdict: ItemUnjudged}}
	for _, tc := range []struct {
		name string
		runs []Run
		want SpecState
	}{
		{"no runs", nil, SpecDraft},
		{"only proposed and denied", []Run{{N: 1, State: RunDenied}, {N: 2, State: RunProposed}, {N: 3, State: RunQueued}}, SpecDraft},
		{"one running", []Run{{N: 1, State: RunDone, Results: met}, {N: 2, State: RunRunning}}, SpecRunning},
		{"one paused", []Run{{N: 1, State: RunPaused}}, SpecRunning},
		{"last met", []Run{{N: 2, State: RunDone, Results: met}, {N: 1, State: RunPartial}}, SpecMet},
		{"last not met", []Run{{N: 1, State: RunDone, Results: met}, {N: 2, State: RunPartial, Results: unmet}}, SpecPartial},
		{"last unjudged", []Run{{N: 1, State: RunPartial, Results: unjudged}}, SpecPartial},
		{"nothing checked", []Run{{N: 1, State: RunDone, Results: []ItemResult{{Item: "DW-1", Verdict: ItemMet}}}}, SpecPartial},
		{"stopped", []Run{{N: 1, State: RunStopped, Results: met}}, SpecPartial},
	} {
		if got := SpecStateOf(tc.runs); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestQualifiedItem(t *testing.T) {
	if got := QualifiedItem(2, "DW-1"); got != "S2/DW-1" {
		t.Errorf("QualifiedItem = %q", got)
	}
	n, item, err := ParseQualifiedItem(" S12/DW-3 ")
	if err != nil || n != 12 || item != "DW-3" {
		t.Errorf("ParseQualifiedItem = %d %q %v", n, item, err)
	}
	for _, bad := range []string{"DW-1", "S0/DW-1", "S/DW-1", "2/DW-1", "S2/dw-1", "S2/DW-", "Sx/DW-1", "S2-DW-1"} {
		if _, _, err := ParseQualifiedItem(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestStepCardLink(t *testing.T) {
	for p, want := range map[string]bool{
		"design": true, "build/2/implement": true, "build/1/reviews/review-a": true,
		"": false, "build//x": false, "/design": false, "has space": false, "tab\there": false,
	} {
		if got := ValidStepPath(p); got != want {
			t.Errorf("ValidStepPath(%q) = %v, want %v", p, got, want)
		}
	}
	sess, _ := NewID(KindFreeform, 1)
	card, _ := NewID(KindFreeform, 2)
	f := Feature{ID: card, Num: 2, Kind: KindFreeform, Title: "x", Slug: "x", Stage: StageOpen, ParentID: sess}
	f.Child = StepLink{Session: sess, Run: 3, Path: "build/2/implement"}.Link()
	if err := f.Validate(); err != nil {
		t.Fatalf("a step's card does not validate: %v", err)
	}
	if l := f.Step(); l.Session != sess || l.Run != 3 || l.Path != "build/2/implement" || !f.IsChild() || f.BranchName() != "" {
		t.Errorf("step link = %+v", l)
	}
	f.Child.Node = "has space"
	if err := f.Validate(); err == nil {
		t.Error("a step path with a space validated")
	}
}

func TestSessionSpecPath(t *testing.T) {
	id, _ := NewID(KindFreeform, 7)
	if got := SessionSpecPath(id, 2, "Import CSV"); got != ".gummi/specs/FF-007/2-import-csv.md" {
		t.Errorf("path = %q", got)
	}
	if got := SessionSpecPath(id, 1, "!!!"); got != ".gummi/specs/FF-007/1-spec.md" {
		t.Errorf("path for a slugless title = %q", got)
	}
}

// TestRetryRefusal: only the session's newest run that started, ended
// partial or stopped with a failed or stopped step (or a loop a person
// stopped), and with no other run open or queued, is retried.
func TestRetryRefusal(t *testing.T) {
	partial := Run{N: 2, State: RunPartial, StartTree: "t"}
	failed := []RunStep{{Path: "a", State: StepDone}, {Path: "b", State: StepFailed}}
	for _, c := range []struct {
		name  string
		run   Run
		steps []RunStep
		runs  []Run
		want  string
	}{
		{"a partial run with a failed step", partial, failed, []Run{{N: 1, State: RunDone, StartTree: "s"}, partial}, ""},
		{"a run that ended done", Run{N: 2, State: RunDone, StartTree: "t"}, failed, nil, "only a run that ended partial or stopped"},
		{"a run that never started", Run{N: 2, State: RunStopped}, failed, nil, "never started"},
		{"another run open", partial, failed, []Run{partial, {N: 3, State: RunPaused, StartTree: "u"}}, "run 3 is paused"},
		{"a run queued", partial, failed, []Run{partial, {N: 3, State: RunQueued}}, "run 3 is queued"},
		{"a later run ran", partial, failed, []Run{partial, {N: 3, State: RunDone, StartTree: "u"}}, "ran after it"},
		{"a later run denied", partial, failed, []Run{partial, {N: 3, State: RunDenied}}, ""},
		{"nothing failed", partial, []RunStep{{Path: "a", State: StepDone}}, nil, "no failed or stopped step"},
		{
			"a loop a person stopped",
			Run{N: 2, State: RunStopped, StartTree: "t", OnMax: map[string]string{"fix/2": "stop"}},
			[]RunStep{{Path: "fix/2/a", State: StepDone}},
			nil, "",
		},
	} {
		got := RetryRefusal(c.run, c.steps, c.runs)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if RetriedPath("build/2/implement", 1) != "build/2/implement@1" {
		t.Error(RetriedPath("build/2/implement", 1))
	}
}
