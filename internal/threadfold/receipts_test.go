package threadfold

import (
	"slices"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

func (l *cardLog) pause(stage domain.Stage, by string) *cardLog {
	return l.add(stage, state.EventPause, "", state.PausePayload{By: by}, "")
}

func (l *cardLog) rebase(stage domain.Stage, p state.RebasePayload) *cardLog {
	return l.add(stage, state.EventRebase, "", p, "")
}

func receipts(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.T == ItemReceipt {
			out = append(out, it)
		}
	}
	return out
}

// A run stopped by hand and a branch rebased are receipts naming who did
// it — the terminal's "you", a person a paired device named, or the agent
// a conflicted rebase was handed to.
func TestPauseAndRebaseAreReceiptsNamingWhoDidIt(t *testing.T) {
	l := newLog().enter(domain.StageImplement, "implementer", "stage").
		say(domain.StageImplement, string(engine.AuthorAssistant), "working").
		pause(domain.StageImplement, state.ActorUser).
		pause(domain.StageImplement, state.PersonActor("Yuki")).
		rebase(domain.StageImplement, state.RebasePayload{Onto: "main", By: state.PersonActor("Simon")}).
		rebase(domain.StageImplement, state.RebasePayload{Onto: "trunk", Agent: true})
	got := receipts(Items(l.evs, Options{Live: true}))
	want := []Receipt{
		{Kind: "park", Text: "you parked it", By: "you"},
		{Kind: "park", Text: "Yuki parked it", By: "Yuki"},
		{Kind: "rebase", OK: true, Text: "Simon rebased it onto main", By: "Simon"},
		{Kind: "rebase", OK: true, Text: "the agent rebased it onto trunk", By: "the agent"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d receipts, want %d: %+v", len(got), len(want), got)
	}
	for i, it := range got {
		if *it.Receipt != want[i] {
			t.Errorf("receipt %d = %+v, want %+v", i, *it.Receipt, want[i])
		}
	}
}

// A pause inside a period autopilot ran ends it, taken back, and the rule
// says who — the pause is not said a second time beside it.
func TestAPauseClosesAnAutopilotPeriod(t *testing.T) {
	l := newLog().enter(domain.StageImplement, "implementer", "stage").
		autopilot(domain.StageImplement, state.AutopilotTookOver, domain.GateAutopilot, "").
		say(domain.StageImplement, string(engine.AuthorAssistant), "working").
		pause(domain.StageImplement, state.PersonActor("Yuki"))
	sts := Stretches(l.evs)
	if len(sts) != 1 || sts[0].Closed != StretchTakenBack || sts[0].Reason != "Yuki parked it" {
		t.Fatalf("stretches = %+v, want one taken back by Yuki", sts)
	}
	items := Items(l.evs, Options{Live: true})
	if r := receipts(items); len(r) != 0 {
		t.Errorf("the pause is said twice: %+v", r)
	}
	last := items[len(items)-1]
	if last.T != ItemStretch || last.Stretch.Edge != "close" || last.Stretch.Reason != "Yuki parked it" {
		t.Errorf("last item = %s, want the closing rule naming Yuki", brief(last))
	}
}

// A headless run's --until stop and the board's own gate decision can
// both sit at one stage; the crossing carries one id and passes both.
// Neither renders as the stop still waiting, and the crossing's receipt
// names both decisions as the items it takes the place of, so a page
// that drew one while it waited drops it.
func TestACrossingPassesEveryStopAtItsStage(t *testing.T) {
	l := newLog().enter(domain.StagePlan, "architect", "stage").
		say(domain.StagePlan, string(engine.AuthorAssistant), "converged").
		park(domain.StagePlan, state.ParkReasonNeedsYou, "stopped early at plan").
		decision(domain.StagePlan, "gate:driver", state.DecisionKindGate, "stopped early at plan").
		decision(domain.StagePlan, "gate:board", state.DecisionKindGate, "plan is ready").
		gate(domain.StagePlan, domain.StagePlan, domain.StageImplement, state.PersonActor("Simon"), "gate:board")
	items := Items(l.evs, Options{Live: true})
	for _, it := range items {
		if it.T == ItemDecision {
			t.Errorf("a passed stop still reads as waiting: %s", brief(it))
		}
		if it.Receipt != nil && it.Receipt.Kind == "decision" {
			t.Errorf("a passed stop reads as superseded unanswered: %s", brief(it))
		}
	}
	gate := items[len(items)-1]
	if gate.Receipt == nil || gate.Receipt.Kind != "gate" {
		t.Fatalf("last item = %s, want the crossing", brief(gate))
	}
	if want := []string{"ev:4", "ev:5"}; !slices.Equal(gate.Supersedes, want) {
		t.Errorf("crossing supersedes %q, want %q", gate.Supersedes, want)
	}
	if ans := AnsweredDecisions(l.evs); !ans["gate:driver"] || !ans["gate:board"] {
		t.Errorf("answered = %v, want both stops", ans)
	}
}

// The stop drawn as the card's current decision, once a later stop
// replaces it, comes back with a newer Seq: a page paging by Seq would
// otherwise go on drawing it as the decision waiting.
func TestASupersededStopIsDeliveredAgain(t *testing.T) {
	l := newLog().enter(domain.StagePlan, "architect", "stage").
		decision(domain.StagePlan, "budget:1", state.DecisionKindBudget, "Out of credits")
	before := Items(l.evs, Options{Live: true})
	last := before[len(before)-1]
	if last.T != ItemDecision {
		t.Fatalf("last item = %s, want the open decision", brief(last))
	}
	seen := l.evs[len(l.evs)-1].Seq
	l.decision(domain.StagePlan, "gate:1", state.DecisionKindGate, "plan is ready")
	var again *Item
	for _, it := range Since(Items(l.evs, Options{Live: true}), seen) {
		if it.Key == last.Key {
			again = &it
		}
	}
	if again == nil || again.T != ItemReceipt || again.Receipt.Kind != "decision" {
		t.Fatalf("the superseded stop is not delivered again as a receipt: %+v", again)
	}
}
