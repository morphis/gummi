package main

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/state"
)

// §17.8 makes experiments operator config so that a goal which changes
// the harness it is tested on does not thereby change what counts as
// passing. The config file is outside every worktree; the scripts it
// names need not be. A command inside a managed repository is a judge a
// card can rewrite on its own branch.
func TestDoctorNoticesAJudgeTheWorkCanEdit(t *testing.T) {
	ws := state.Workspace{Root: "/ws"}
	cfg := config.Config{
		Repos: map[string]string{"lxd": "git/lxd"},
		Experiments: map[string]config.Experiment{
			"matrix": {Run: "git/lxd/test/rig.sh --all"},
		},
	}
	got := named(t, judgeChecks(cfg, ws), "judge")
	if got.Status != statusWarn {
		t.Fatalf("want a warning, got %+v", got)
	}
	if !strings.Contains(got.Detail, "experiment matrix's run") ||
		!strings.Contains(got.Detail, "is in lxd") {
		t.Errorf("the warning must name the command and the repository; got %q", got.Detail)
	}
}

func TestDoctorIsQuietWhenTheJudgeIsOutOfReach(t *testing.T) {
	ws := state.Workspace{Root: "/ws"}
	cfg := config.Config{
		Repos: map[string]string{"lxd": "git/lxd"},
		Experiments: map[string]config.Experiment{
			"matrix": {Run: "sim/simctl test", Control: "sim/simctl control"},
		},
		Substrates: map[string]config.Substrate{
			"rig": {Probe: "sim/simctl probe"},
		},
	}
	if got := named(t, judgeChecks(cfg, ws), "judge"); got.Status != statusOK {
		t.Fatalf("a harness outside every managed repo is fine; got %+v", got)
	}
}

// A substrate's commands are judged the same way: a reset the work can
// rewrite is a reset that can hide what it did.
func TestDoctorNoticesASubstrateCommandInARepo(t *testing.T) {
	ws := state.Workspace{Root: "/ws"}
	cfg := config.Config{
		Repo:       "git/lxd",
		Substrates: map[string]config.Substrate{"rig": {Reset: "git/lxd/tools/reset.sh"}},
	}
	got := named(t, judgeChecks(cfg, ws), "judge")
	if got.Status != statusWarn {
		t.Fatalf("want a warning, got %+v", got)
	}
	if !strings.Contains(got.Detail, "substrate rig's reset") {
		t.Errorf("got %q", got.Detail)
	}
}

// A rig that cannot prove itself judges everything anyway: a missing
// control is skipped silently and the run proceeds. Seven of eight
// harness defects in the trials made the rig look healthier than it was,
// and the control is the only mechanism that catches that.
func TestDoctorNoticesAnExperimentWithNoControl(t *testing.T) {
	ws := state.Workspace{Root: "/ws"}
	cfg := config.Config{
		Experiments: map[string]config.Experiment{
			"proved":   {Run: "sim/simctl test", Control: "sim/simctl control"},
			"unproved": {Run: "sim/simctl test"},
		},
	}
	control := named(t, judgeChecks(cfg, ws), "control")
	if control.Status != statusWarn {
		t.Fatalf("want a control warning, got %+v", control)
	}
	if !strings.Contains(control.Detail, "unproved") {
		t.Errorf("it must name the experiment; got %q", control.Detail)
	}
	if strings.Contains(control.Detail, "proved,") || strings.Contains(control.Detail, ", proved") {
		t.Errorf("and not the one that has a control; got %q", control.Detail)
	}
}

// named picks one check out of the set by name, so a test asserting
// about the judge does not break when a control check joins it.
func named(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check among %+v", name, checks)
	return doctorCheck{}
}
