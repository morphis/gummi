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
	got := judgeChecks(cfg, ws)
	if len(got) != 1 || got[0].Status != statusWarn {
		t.Fatalf("want one warning, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "experiment matrix's run") ||
		!strings.Contains(got[0].Detail, "is in lxd") {
		t.Errorf("the warning must name the command and the repository; got %q", got[0].Detail)
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
	got := judgeChecks(cfg, ws)
	if len(got) != 1 || got[0].Status != statusOK {
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
	got := judgeChecks(cfg, ws)
	if len(got) != 1 || got[0].Status != statusWarn {
		t.Fatalf("want one warning, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "substrate rig's reset") {
		t.Errorf("got %q", got[0].Detail)
	}
}
