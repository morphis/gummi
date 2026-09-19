package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/notebook"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// A programme is a sequence of goals. One that continues another starts
// from what that one came to know, and does not start before it has ended.
func TestAGoalThatContinuesAnotherWaitsForItAndInheritsWhatItKnew(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	first := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	nb := e.GoalNotebook(first.ID)
	if _, err := nb.Set(notebook.Entry{Key: "cache-path", Value: "~/.cache/export", Decision: "D-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nb.Record(notebook.Finding{Claim: "the exporter opens its cache read-only", Evidence: "RS-002 §2"}); err != nil {
		t.Fatal(err)
	}

	id, _ := domain.NewID(domain.KindGoal, 9)
	second := domain.Feature{ID: id, Num: 9, Kind: domain.KindGoal, Title: "Export syncs", Slug: "export-syncs",
		Stage: domain.StagePlan, Budget: domain.Budget{Envelope: 4000}, CreatedAt: first.CreatedAt, UpdatedAt: first.CreatedAt}
	putFeature(t, store, second)
	withWorktree(t, wt, second)
	doc := strings.Replace(testGoalDoc, "lanes: 1\n", "lanes: 1\nafter: "+string(first.ID)+"\n", 1)
	writeArtifact(t, wt.Root(), second, doc)

	res, err := e.Advance(ctx, second.ID, "user")
	if err != nil || res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, "has not ended") {
		t.Fatalf("it can be planned while the first runs, and not started: %v %v %q", res.Status, err, res.Reason)
	}
	writeArtifact(t, wt.Root(), second, strings.Replace(doc, "after: "+string(first.ID), "after: GL-777", 1))
	if res, _ = e.Advance(ctx, second.ID, "user"); res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, "not a goal on this board") {
		t.Fatalf("%v %q", res.Status, res.Reason)
	}
	writeArtifact(t, wt.Root(), second, doc)

	// created to continue it: what the first knew is there for the plan
	if err := e.ContinueGoal(ctx, second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	hint := e.notebookHint(second)
	for _, want := range []string{"cache-path = ~/.cache/export", "F-1 (holds) the exporter opens its cache read-only", string(first.ID) + "-handover.md"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("the plan conversation is shown what came with it — missing %q:\n%s", want, hint)
		}
	}
	if fs := e.GoalNotebook(second.ID).Findings(); len(fs) != 1 || fs[0].From != string(first.ID) {
		t.Fatalf("a finding says which goal it came from: %+v", fs)
	}
	if err := e.ContinueGoal(ctx, second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if fs := e.GoalNotebook(second.ID).Findings(); len(fs) != 1 {
		t.Fatalf("twice is once: %d findings", len(fs))
	}

	// the first ends; now the second may start
	for _, st := range []domain.Stage{domain.StageImplement, domain.StageVerify, domain.StageDone} {
		if _, err := store.Transition(ctx, first.ID, st, "user"); err != nil {
			t.Fatal(err)
		}
	}
	// ended is not landed: a goal that reached done with nothing merged has
	// left this goal's cards nothing to fork from
	if res, err = e.Advance(ctx, second.ID, "user"); err != nil || res.Status != StatusBlockedGoalPlan ||
		!strings.Contains(res.Reason, "ended without landing anything") {
		t.Fatalf("a predecessor that landed nothing was accepted: %v %v %q", res.Status, err, res.Reason)
	}
	if err := store.SetLandedSHA(ctx, first.ID, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if res, err = e.Advance(ctx, second.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	rep, _ := e.GoalReport(ctx, second.ID)
	if rep.After != first.ID {
		t.Fatalf("the hand-over says what it continues: %q", rep.After)
	}
	log, _ := store.GoalLog(ctx, second.ID)
	n := 0
	for _, en := range log {
		if en.Action == state.GoalContinued {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("recorded once: %d", n)
	}
}

// Where the plan agreed an order, that is the order the repositories land
// in; what it did not name follows, home first, as it always did.
func TestRepositoriesLandInTheOrderThePlanAgreed(t *testing.T) {
	trees := []worktree.GoalTree{{Repo: "lxd", Home: true}, {Repo: "microovn"}, {Repo: "ovn"}, {Repo: "docs"}}
	names := func(ts []worktree.GoalTree) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Repo)
		}
		return strings.Join(out, " ")
	}
	if got := names(orderGoalTrees(trees, nil)); got != "lxd microovn ovn docs" {
		t.Fatalf("no order agreed: home first, then as they were: %q", got)
	}
	if got := names(orderGoalTrees(trees, []string{"ovn", "microovn"})); got != "ovn microovn lxd docs" {
		t.Fatalf("got %q", got)
	}
}
