package engine

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// enterStage appends the stage_enter a session generation writes on its
// first persist (engine's mirrorEvents), deduped on the generation the
// way the real one is.
func enterStage(t *testing.T, store *state.Store, id domain.FeatureID, stage domain.Stage, generation string) {
	t.Helper()
	if err := store.AppendEvent(context.Background(), state.CardEvent{
		Feature: id, Stage: stage, Kind: state.EventStageEnter,
		At: time.Now(), Payload: `{"role":"implementer","model":"m"}`,
		Dedupe: generation + ":stage_enter",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetDecisionClosesWhenTheStageRunsAgain: a budget stop is the one
// decision with no answer event of its own — the answer kinds are gate
// and ask, and minting one of those for a top-up would fork their
// meaning. What resolves it is the stage running again on a raised
// envelope, and every generation writes a stage_enter, so a later one is
// that fact. Until then the record stays open: raising the envelope and
// resuming a run are two decisions, and the card is still parked after
// the first.
func TestBudgetDecisionClosesWhenTheStageRunsAgain(t *testing.T) {
	_, store, _ := newRepo(t)
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageImplement)
	putFeature(t, store, f)

	// the run that exhausted its envelope opened the stage first
	enterStage(t, store, f.ID, domain.StageImplement, "gen-1")
	if err := store.OpenDecision(ctx, f.ID, domain.StageImplement, state.DecisionPayload{
		ID: "budget:FD-001:implement:1", Kind: state.DecisionKindBudget,
		Question: "implement hit its budget — top up or park.",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	opens, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 1 {
		t.Fatalf("the budget stop should read open until the stage runs again: %+v", opens)
	}

	// the resume: a fresh generation on the same stage
	enterStage(t, store, f.ID, domain.StageImplement, "gen-2")

	opens, err = store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 0 {
		t.Fatalf("the re-run did not close the budget stop: %+v", opens["FD-001"])
	}
}

// TestOpenAskSurvivesItsStageRunningAgain guards the scope of the rule
// above. An open ask must survive a restore so the restored session can
// re-arm it (DESIGN §6.3's reopen path) — and that session writes a
// stage_enter of its own. Closing every kind on a later stage_enter
// would strand the question the reopen path exists to rescue, so the
// rule is budget's alone.
func TestOpenAskSurvivesItsStageRunningAgain(t *testing.T) {
	_, store, _ := newRepo(t)
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	putFeature(t, store, f)

	enterStage(t, store, f.ID, domain.StageBrainstorm, "gen-1")
	if err := store.OpenDecision(ctx, f.ID, domain.StageBrainstorm, state.DecisionPayload{
		ID: "call-1", Kind: state.DecisionKindAsk,
		Question: "Persist where?", FreeForm: true,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// the restore: a fresh session on the same stage, which is exactly
	// where the re-armed question has to still be waiting
	enterStage(t, store, f.ID, domain.StageBrainstorm, "gen-2")

	opens, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 1 {
		t.Fatalf("the open ask did not survive its stage running again: %+v", opens)
	}
	if got := opens["FD-001"][0].Kind; got != state.DecisionKindAsk {
		t.Errorf("surviving decision kind = %q, want ask", got)
	}
}

// enterBorrowedStage appends the stage_enter a session that borrows a
// stage writes — a plan critique or a rebase resolution. Both open a
// session on the stage they borrow without being a run of it.
func enterBorrowedStage(t *testing.T, store *state.Store, id domain.FeatureID, stage domain.Stage, generation, flavor string) {
	t.Helper()
	if err := store.AppendEvent(context.Background(), state.CardEvent{
		Feature: id, Stage: stage, Kind: state.EventStageEnter,
		At:      time.Now(),
		Payload: `{"role":"implementer","model":"m","flavor":"` + flavor + `"}`,
		Dedupe:  generation + ":stage_enter",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetDecisionSurvivesABorrowedStageRun: a rebase resolution and a
// plan critique both open a session on the stage they borrow, but
// neither is a run of that stage and neither raises anybody's envelope.
// Reading one as a top-up would close a budget stop nobody answered and
// take the card's top-up affordance out of the queue with it.
func TestBudgetDecisionSurvivesABorrowedStageRun(t *testing.T) {
	for _, flavor := range []string{"rebase", "critique"} {
		t.Run(flavor, func(t *testing.T) {
			_, store, _ := newRepo(t)
			ctx := context.Background()

			f := feature(1, "Dark mode", domain.StageVerify)
			putFeature(t, store, f)

			enterStage(t, store, f.ID, domain.StageVerify, "gen-1")
			if err := store.OpenDecision(ctx, f.ID, domain.StageVerify, state.DecisionPayload{
				ID: "budget:FD-001:verify:1", Kind: state.DecisionKindBudget,
				Question: "verify hit its budget — top up or park.",
			}, time.Now()); err != nil {
				t.Fatal(err)
			}

			enterBorrowedStage(t, store, f.ID, domain.StageVerify, "gen-2", flavor)

			opens, err := store.OpenDecisions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(opens["FD-001"]) != 1 {
				t.Fatalf("a %s generation closed the budget stop nobody topped up: %+v", flavor, opens["FD-001"])
			}

			// the real thing still closes it
			enterStage(t, store, f.ID, domain.StageVerify, "gen-3")
			opens, err = store.OpenDecisions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(opens["FD-001"]) != 0 {
				t.Fatalf("a genuine re-run did not close the budget stop: %+v", opens["FD-001"])
			}
		})
	}
}

// TestAnswerResumesTheWorkingFlag: a question drops the session's working
// flag while it waits on a person, and answering it hands the turn
// straight back to the agent — through the resolver, not through
// deliverTurn, which is where every other dispatch raises the flag again.
// Left down, the thread showed no spinner and no running label from the
// moment you answered, so a live turn read as a stopped one and the only
// way to make anything move again looked like sending a message.
func TestAnswerResumesTheWorkingFlag(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	s := &Session{
		Feature: f, Role: agent.RoleArchitect, Interactive: true,
		state: StateInteractive, done: make(chan struct{}),
	}
	if !e.replace(f.ID, s) {
		t.Fatal("replace")
	}
	defer s.stop()

	const callID = "mcp-1"
	ch := s.registerResolver(callID)
	s.markResolverWaiting(callID)
	e.handleClientTool(s, &agent.ToolCall{
		ID: callID, Name: "ask_user",
		Args: []byte(`{"question":"pick","options":[{"label":"a"},{"label":"b"}]}`),
	})
	if s.Snapshot().PendingAsk == nil {
		t.Fatal("ask never registered")
	}
	if s.Snapshot().Busy {
		t.Error("a card waiting on a person must not read as working")
	}

	delivered := make(chan string, 1)
	go func() { delivered <- <-ch }() // the agent's blocked call, resumed

	if err := e.Answer(context.Background(), f.ID, "a"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	select {
	case got := <-delivered:
		if got != "a" {
			t.Errorf("the agent's blocked call got %q, want the answer", got)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("the answer never reached the agent's blocked call")
	}
	if !s.Snapshot().Busy {
		t.Error("the answer resumed the agent but the session still reads idle")
	}
}
