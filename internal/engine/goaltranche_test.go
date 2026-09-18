package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/state"
)

const tbdGoalDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: the cache file exists\n  check: test -f cache.txt\n" +
	"- id: DW-2\n  says: the docs mention the cache\n  judge: true\n```\n\n" +
	"## Limits\n\nNone.\n\n" +
	"## Budget\n\nAbout 4000 credits.\n\n```gummi-goal\nlanes: 1\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: how the exporter caches\n  kind: research\n  serves: [DW-1]\n  envelope: 400\n" +
	"- title: the cache work\n  kind: tbd\n  serves: [DW-1, DW-2]\n  depends_on: [how the exporter caches]\n  envelope: 1500\n```\n\n" +
	"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n## Verification plan\n\nRun the checks.\n\n## Report\n\n\n"

// A plan that begins with discovery cannot honestly name all its cards. A
// tbd row says so: it serves its items, holds part of the budget, and
// becomes cards once what it waits for has settled.
func TestAPlanMayHoldBudgetForCardsItCannotNameYet(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()

	g := goalAtPlan(t, store, wt, tbdGoalDoc, 6000)
	for name, doc := range map[string]string{
		"unbounded":        strings.Replace(tbdGoalDoc, "  depends_on: [how the exporter caches]\n  envelope: 1500\n", "  depends_on: [how the exporter caches]\n", 1),
		"waits on nothing": strings.Replace(tbdGoalDoc, "  depends_on: [how the exporter caches]\n", "", 1),
	} {
		writeArtifact(t, wt.Root(), g, doc)
		if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusBlockedGoalPlan {
			t.Fatalf("%s: %v %v %q", name, res.Status, err, res.Reason)
		}
	}
	writeArtifact(t, wt.Root(), g, tbdGoalDoc)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	cards := goalCards(t, store, g.ID)
	if len(cards) != 1 {
		t.Fatalf("the tbd row is not a card: %d minted", len(cards))
	}
	view, _ := e.GoalView(ctx, g.ID)
	if len(view.Tranches) != 1 || view.Tranches[0].Envelope != 1500 || view.Tranches[0].Ready || view.Tranches[0].Closed {
		t.Fatalf("%+v", view.Tranches)
	}
	if held := view.Ledger.Given; held < float64(1500+cards[0].Budget.Envelope) {
		t.Fatalf("the tranche is held against the ledger as a waiting card's envelope is: given %.0f", held)
	}

	lt := &leadTurn{e: e, view: view}
	create := `{"title":"write the cache","serves":["DW-1"],"envelope":600,"from":"the cache work"}`
	if _, err := lt.dispatch(ctx, "card_create", []byte(create)); err == nil || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("creating its cards before the research lands is guessing at what it will find: %v", err)
	}

	// the research settles (dropped counts: there is nothing left to wait for)
	if err := e.goalDrop(ctx, view.Goal, cards[0], "test", ActorGoal); err != nil {
		t.Fatal(err)
	}
	view, _ = e.GoalView(ctx, g.ID)
	if !view.Tranches[0].Ready {
		t.Fatalf("%+v", view.Tranches[0])
	}
	lt.view = view
	if _, err := lt.dispatch(ctx, "card_create", []byte(`{"title":"unrelated","serves":["DW-9"],"from":"the cache work"}`)); err == nil {
		t.Fatal("a card out of a tranche serves what the tranche was held for")
	}
	if _, err := lt.dispatch(ctx, "card_create", []byte(`{"title":"too big","serves":["DW-1"],"envelope":1600,"from":"the cache work"}`)); err == nil {
		t.Fatal("the tranche bounds what comes out of it")
	}
	out, err := lt.dispatch(ctx, "card_create", []byte(create))
	if err != nil {
		t.Fatalf("%v", err)
	}
	view, _ = e.GoalView(ctx, g.ID)
	if tr := view.Tranches[0]; tr.Given != 600 || len(tr.Cards) != 1 || !strings.Contains(out, string(tr.Cards[0])) {
		t.Fatalf("%+v %q", tr, out)
	}

	// no lead resolves in this engine, so the look it would get is skipped
	// and the tranche closes: what it did not use returns to the goal
	before := view.Ledger.Available
	res := tick(t, e, g.ID)
	closed := false
	for _, a := range res.Actions {
		closed = closed || a.Kind == goalpolicy.CloseTranche
	}
	view, _ = e.GoalView(ctx, g.ID)
	if !closed || !view.Tranches[0].Closed || view.Ledger.Available < before+800 {
		t.Fatalf("closed %v, available %.0f → %.0f", closed, before, view.Ledger.Available)
	}
	lt.view = view
	if _, err := lt.dispatch(ctx, "card_create", []byte(create)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("%v", err)
	}
	rep, _ := e.GoalReport(ctx, g.ID)
	if len(rep.Tranches) != 1 || !rep.Tranches[0].Closed || !strings.Contains(RenderGoalReport(rep), `held for "the cache work"`) {
		t.Fatalf("the hand-over says what became of it: %+v", rep.Tranches)
	}
}

// A finding that would change what "done" means is the owner's. The goal
// freezes what it is about, runs everything else on, and stops to ask when
// nothing else can move — and a note is the answer.
func TestAFindingThatChangesDoneIsTheOwners(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	cards := goalCards(t, store, g.ID) // [0] serves DW-1, [1] serves DW-2
	view, _ := e.GoalView(ctx, g.ID)
	lt := &leadTurn{e: e, view: view}
	if _, err := lt.dispatch(ctx, "owner_ask", []byte(`{"item":"DW-7","question":"q","proposal":"p"}`)); err == nil {
		t.Fatal("the question is about an agreed item")
	}
	if _, err := lt.dispatch(ctx, "owner_ask", []byte(`{"item":"DW-1","question":"the exporter has no cache file; it caches in memory"}`)); err == nil {
		t.Fatal("the owner needs what the lead would change, to be able to simply accept it")
	}
	out, err := lt.dispatch(ctx, "owner_ask", []byte(`{"item":"dw-1","question":"the exporter has no cache file; it caches in memory","proposal":"DW-1: a second export makes no network call"}`))
	if err != nil || !strings.Contains(out, string(cards[0].ID)) || strings.Contains(out, string(cards[1].ID)) {
		t.Fatalf("it freezes the cards that serve only that item: %q %v", out, err)
	}
	lt.view, _ = e.GoalView(ctx, g.ID)
	if _, err := lt.dispatch(ctx, "owner_ask", []byte(`{"item":"DW-2","question":"q","proposal":"p"}`)); err == nil {
		t.Fatal("one question at a time")
	}

	res := tick(t, e, g.ID)
	if !res.NeedsOwner.Waiting() || res.OwnerStop {
		t.Fatalf("the board hears at once; the goal has not stopped, there is other work: %+v", res)
	}
	if len(res.Start) != 1 || res.Start[0].ID != cards[1].ID {
		t.Fatalf("everything that does not depend on the answer runs on: %+v", res.Start)
	}
	verifyCard(t, e, store, wt.Root(), cards[1].ID, "docs.txt")
	for i := 0; i < 3 && !res.OwnerStop; i++ {
		res = tick(t, e, g.ID)
	}
	if !res.OwnerStop || res.Finished {
		t.Fatalf("nothing else can move: it stops and asks: %+v", res)
	}
	if got, _ := store.GetFeature(ctx, cards[0].ID); got.GoalDropped() {
		t.Fatal("the frozen card keeps everything it has")
	}
	rep, _ := e.GoalReport(ctx, g.ID)
	if body := RenderGoalReport(rep); !rep.NeedsOwner.Waiting() || !strings.Contains(body, "a second export makes no network call") || !strings.Contains(body, "--goal-note") {
		t.Fatalf("the hand-over carries the question, the proposal and how to answer:\n%s", body)
	}

	if err := e.GoalNote(ctx, g.ID, "accepted — I have amended DW-1 in the doc"); err != nil {
		t.Fatal(err)
	}
	res = tick(t, e, g.ID)
	if res.NeedsOwner.Waiting() || len(res.Start) != 1 || res.Start[0].ID != cards[0].ID {
		t.Fatalf("answered, the frozen card starts: %+v", res)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	asked := 0
	for _, en := range log {
		if en.Action == state.GoalNeedOwner {
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
}
