package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/substrate"
	"github.com/morphis/gummi/internal/verify"
)

// substrateCheckDoc is a goal whose done-when item is proved by a command
// that drives a cluster, and says so.
const substrateCheckDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: the deployed build serves the cache\n  check: touch ran\n  substrate: rig\n" +
	"- id: DW-2\n  says: the docs mention it\n  judge: true\n```\n\n" +
	"## Limits\n\nNone.\n\n" +
	"## Budget\n\nAbout 1500 credits.\n\n```gummi-goal\nlanes: 1\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: local cache for export\n  serves: [DW-1, DW-2]\n  envelope: 600\n```\n\n" +
	"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n## Verification plan\n\nRun it.\n\n## Report\n\n\n"

// TestACheckThatNeedsASubstrateTakesItsLease: a done-when check is a
// command in a checkout, and a command that drives a cluster is as scarce,
// slow, stateful and shared as any run on the same machines. Until an item
// could say which substrate it needs, nothing but internal/experiment ever
// took a lease, and a goal's own verify drove the cluster beside a run
// that believed it had the machines to itself.
func TestACheckThatNeedsASubstrateTakesItsLease(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	e, _, store, wt := advanceEngine(t)
	rig := t.TempDir()
	cfg := "substrates:\n  rig:\n    probe: test -f " + rig + "/up\n    provision: touch " + rig + "/up\n"
	if err := os.WriteFile(filepath.Join(wt.Root(), ".gummi", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rig, "up"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, substrateCheckDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("the plan gate refused a check that names a configured substrate: %v %q", err, res.Reason)
	}
	goal, err := store.GetFeature(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	checks := []domain.Check{{Name: "done-when DW-1", Cmd: "touch ran"}}

	// nobody holds it: the check runs
	out, err := e.runGoalChecks(ctx, goal, substrateCheckDoc, checks)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].OK {
		t.Fatalf("the check did not run on a free substrate: %+v", out)
	}

	// somebody else holds it: the check is NOT RUN, not failed — a check
	// that cannot run is no opinion rather than a verdict
	mgr, err := e.Substrates()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := mgr.Acquire("rig", substrate.Holder{Who: "FD-009"})
	if err != nil {
		t.Fatal(err)
	}
	out, err = e.runGoalChecks(ctx, goal, substrateCheckDoc, checks)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != verify.StatusNotRun {
		t.Fatalf("a check whose substrate somebody else holds = %+v, want not-run", out)
	}
	if !strings.Contains(out[0].Output, "FD-009") {
		t.Errorf("the result does not say who has it: %q", out[0].Output)
	}
}

// TestThePlanGateRefusesACheckOnASubstrateNobodyConfigured: an unknown
// name would take no lease and run anyway, which is the failure the lease
// exists to prevent.
func TestThePlanGateRefusesACheckOnASubstrateNobodyConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	e, _, store, wt := advanceEngine(t)
	g := goalAtPlan(t, store, wt, strings.Replace(substrateCheckDoc, "substrate: rig", "substrate: nowhere", 1), 4000)
	res, err := e.Advance(context.Background(), g.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, `substrate "nowhere"`) {
		t.Fatalf("status %v reason %q", res.Status, res.Reason)
	}
}
