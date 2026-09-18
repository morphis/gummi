package experiment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/substrate"
)

// job builds a run against a substrate made of files under root: "up" is
// the provisioned cluster, "dirty" says a job used it since its last reset.
func job(t *testing.T, def config.Experiment, sub config.Substrate) Job {
	t.Helper()
	root := t.TempDir()
	if sub.Probe == "" {
		sub.Probe = "test -f up"
	}
	if sub.Provision == "" {
		sub.Provision = "touch up"
	}
	if sub.Reset == "" {
		sub.Reset = "rm -f dirty; echo reset >> resets"
	}
	def.Substrate = "rig"
	j := Job{
		ID: NewID(time.Now()), Experiment: "matrix", Owner: "GL-001", Purpose: "integration",
		Heads: map[string]string{"": "abc123", "lxd": "def456"},
		Trees: map[string]string{"": filepath.Join(root, "home"), "lxd": filepath.Join(root, "lxd")},
		Def:   def, Substrate: sub, Root: root, StateDir: filepath.Join(root, ".gummi", "state"),
	}
	j.Dir = filepath.Join(root, ".gummi", "evidence", j.Owner, j.ID)
	if err := Prepare(j); err != nil {
		t.Fatal(err)
	}
	return j
}

func count(t *testing.T, root, name string) int {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(root, name))
	return strings.Count(string(raw), "\n")
}

func TestAPassLeavesARecordAndTheEvidence(t *testing.T) {
	j := job(t, config.Experiment{
		Deploy:  `test -n "$GUMMI_TREE_LXD" && test "$GUMMI_HEAD_HOME" = abc123 && touch dirty`,
		Settle:  "true",
		Run:     `printf '{"id":"tor1-ingress","ok":true}\n{"id":"egress","ok":true,"detail":"4/4"}\n' > "$GUMMI_EVIDENCE/results.ndjson"`,
		Collect: `echo "router dump" > "$GUMMI_EVIDENCE/nb.txt"`,
	}, config.Substrate{})
	res := Execute(context.Background(), j)
	if res.Outcome != Pass || res.State != StateDone || res.Reason != "2 of 2 assertions held" {
		t.Fatalf("%+v", res)
	}
	if len(res.Ops) == 0 || !strings.HasPrefix(res.Ops[len(res.Ops)-1], "reset ok") {
		t.Fatalf("it was provisioned and then reset before the attempt: %v", res.Ops)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, "evidence", "nb.txt")); err != nil {
		t.Fatal("collect's output is part of the run")
	}
	back, err := Load(j.Dir)
	if err != nil || back.Outcome != Pass || !back.About(map[string]string{"": "abc123", "lxd": "def456", "other": "x"}) {
		t.Fatalf("read back: %+v %v", back, err)
	}
	if back.About(map[string]string{"": "abc123", "lxd": "moved"}) {
		t.Fatal("evidence is about the heads it ran on, and stale once one moves")
	}
	// the substrate is let go
	m := substrate.New(j.StateDir, j.Root, map[string]config.Substrate{"rig": j.Substrate})
	if st, _ := m.Status(context.Background(), "rig"); st.State != substrate.Ready {
		t.Fatalf("got %s", st.State)
	}
}

// A failure is a verdict only when it happens again on a reset substrate.
func TestAFailureIsBelievedWhenItReproduces(t *testing.T) {
	j := job(t, config.Experiment{
		Run:     `echo attempt >> attempts; printf '{"id":"a","ok":true}\n{"id":"b","ok":false,"detail":"no route"}\n' > "$GUMMI_EVIDENCE/results.ndjson"; exit 1`,
		Collect: "echo collected >> collects",
	}, config.Substrate{})
	res := Execute(context.Background(), j)
	if res.Outcome != Fail || res.FailedPhase != "run" || res.Flaky {
		t.Fatalf("%+v", res)
	}
	// three resets: one while bringing the substrate up, one before each attempt
	if count(t, j.Root, "attempts") != 2 || count(t, j.Root, "resets") != 3 || count(t, j.Root, "collects") != 2 {
		t.Fatal("two attempts, each on a reset substrate, each collected")
	}
	if held, ok := res.Holds([]string{"a"}); !held || !ok {
		t.Fatal("an item about the assertions that held is met by a run that failed elsewhere")
	}
	if held, ok := res.Holds([]string{"a", "b"}); held || !ok {
		t.Fatal("and one about an assertion that did not is not")
	}
	if held, ok := res.Holds(nil); held || !ok {
		t.Fatal("an item about the whole run follows its outcome")
	}
}

func TestAFailureThatDoesNotReproduceJudgesNothing(t *testing.T) {
	j := job(t, config.Experiment{Run: "echo x >> attempts; test $(wc -l < attempts) -ge 2"}, config.Substrate{})
	res := Execute(context.Background(), j)
	if res.Outcome != Inconclusive || !res.Flaky || res.FailedPhase != "run" {
		t.Fatalf("%+v", res)
	}
	if _, ok := res.Holds(nil); ok {
		t.Fatal("a flaky run has no opinion")
	}
}

func TestTheEnvironmentsFailuresAreNotTheWorks(t *testing.T) {
	ctx := context.Background()
	t.Run("a phase says it could not be judged", func(t *testing.T) {
		j := job(t, config.Experiment{Settle: "echo 'bgp never converged'; exit 75", Run: "echo ran >> ran"}, config.Substrate{})
		res := Execute(ctx, j)
		if res.Outcome != Inconclusive || !strings.Contains(res.Reason, "bgp never converged") || count(t, j.Root, "ran") != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the substrate cannot be made ready", func(t *testing.T) {
		j := job(t, config.Experiment{Run: "true"}, config.Substrate{Provision: "false"})
		if res := Execute(ctx, j); res.Outcome != Inconclusive || len(res.Phases) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the rig fails its own control", func(t *testing.T) {
		j := job(t, config.Experiment{Control: "echo 'reference topology: 19/27'; false", Run: "echo ran >> ran"}, config.Substrate{})
		j.Control = true
		res := Execute(ctx, j)
		if res.Outcome != Inconclusive || !res.ControlFailed || count(t, j.Root, "ran") != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("someone else has it", func(t *testing.T) {
		j := job(t, config.Experiment{Run: "true"}, config.Substrate{})
		m := substrate.New(j.StateDir, j.Root, map[string]config.Substrate{"rig": j.Substrate})
		lease, err := m.Acquire("rig", substrate.Holder{Who: "FD-009"})
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		if res := Execute(ctx, j); res.Outcome != NotRun || !strings.Contains(res.Reason, "FD-009") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("it would expire mid-run and cannot be renewed", func(t *testing.T) {
		j := job(t, config.Experiment{Run: "true", Timeout: "1h"}, config.Substrate{TTL: "90m"})
		// provisioned by the run itself, then too short-lived for two
		// hour-long phases; renewing it is possible, so it is renewed
		if res := Execute(ctx, j); res.Outcome != Pass || len(res.Ops) < 2 {
			t.Fatalf("%+v", res)
		}
	})
}

// A run nobody finished judged nothing, and says so rather than reading
// as running for ever.
func TestARunWhoseRunnerDiedIsInconclusive(t *testing.T) {
	j := job(t, config.Experiment{Run: "true"}, config.Substrate{})
	r := &runner{job: j, now: time.Now}
	r.res = Result{ID: j.ID, State: StateRunning, PID: 1 << 30, Started: time.Now(), Heads: j.Heads}
	r.save()
	got, err := Load(j.Dir)
	if err != nil || got.State != StateDone || got.Outcome != Inconclusive {
		t.Fatalf("%+v %v", got, err)
	}
	if runs := List(filepath.Dir(j.Dir)); len(runs) != 1 || runs[0].ID != j.ID {
		t.Fatalf("%+v", runs)
	}
}
