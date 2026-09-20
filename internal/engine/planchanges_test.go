package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// The submitted plan from the trial that found this: three cards, one of
// them asking to prove itself live before it lands.
const submittedPlan = "## Done when\n\n" +
	"```gummi-done-when\n" +
	"- id: DW-1\n  says: \"tier one exists\"\n  experiment: matrix\n  assertions: [a]\n" +
	"- id: DW-2\n  says: \"it builds\"\n  check: \"go build ./...\"\n" +
	"```\n\n## Budget\n\n" +
	"```gummi-goal\nlanes: 2\n```\n\n## Cards\n\n" +
	"```gummi-cards\n" +
	"- title: \"Provider routers\"\n  one_liner: x\n  serves: [DW-1]\n" +
	"- title: \"Unnumbered BGP\"\n  one_liner: y\n  serves: [DW-1]\n" +
	"- title: \"ECMP\"\n  one_liner: z\n  serves: [DW-1]\n  live: true\n" +
	"```\n"

// GL-001: the architect merged three cards into two and the live flag
// went with them. Nothing in the plan diff, the gate output or the goal's
// status said a proof mechanism had been dropped.
func TestPlanChangesReportsDroppedLiveProof(t *testing.T) {
	items, _, err := spec.ParseDoneWhen(submittedPlan)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := spec.ParseGoalCards(submittedPlan, items)
	if err != nil {
		t.Fatal(err)
	}
	// Approved: the same items, two cards, neither live, one lane.
	merged := rows[:2]
	got := strings.Join(planChanges(submittedPlan, items, merged, 1), "; ")

	for _, want := range []string{"dropped live proof", "3 cards became 2", "lanes 2 became 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("planChanges did not report %q; got: %s", want, got)
		}
	}
}

// An unchanged plan has nothing to say. A gate that narrates every
// approval teaches people to stop reading it.
func TestPlanChangesIsSilentWhenNothingChanged(t *testing.T) {
	items, _, err := spec.ParseDoneWhen(submittedPlan)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := spec.ParseGoalCards(submittedPlan, items)
	if err != nil {
		t.Fatal(err)
	}
	if got := planChanges(submittedPlan, items, rows, 2); len(got) != 0 {
		t.Errorf("expected silence, got %v", got)
	}
}

func TestPlanChangesReportsDoneWhenAddedAndDropped(t *testing.T) {
	items, _, err := spec.ParseDoneWhen(submittedPlan)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := spec.ParseGoalCards(submittedPlan, items)
	if err != nil {
		t.Fatal(err)
	}
	// The gate added DW-3 (as GL-001's really did) and dropped DW-2.
	changed := append([]domain.DoneWhen{}, items[0])
	extra := items[1]
	extra.ID = "DW-3"
	changed = append(changed, extra)

	got := strings.Join(planChanges(submittedPlan, changed, rows, 2), "; ")
	if !strings.Contains(got, "added done-when DW-3") {
		t.Errorf("did not report the added item; got: %s", got)
	}
	if !strings.Contains(got, "dropped done-when DW-2") {
		t.Errorf("did not report the dropped item; got: %s", got)
	}
}
