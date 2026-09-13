package goalpolicy

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func base() Input {
	return Input{Stage: domain.StageImplement, Envelope: 4000, Reserve: 600, Lanes: 2}
}

func acts(in Input) string {
	var s []string
	for _, a := range Decide(in) {
		s = append(s, a.String())
	}
	return strings.Join(s, " | ")
}

func TestOnlyImplementIsConducted(t *testing.T) {
	in := base()
	in.Cards = []Card{{ID: "FD-002", State: Waiting, Envelope: 500}}
	in.Stage = domain.StagePlan
	if got := Decide(in); got != nil {
		t.Fatalf("plan stage conducts nothing, got %v", got)
	}
	in.Stage = domain.StageImplement
	in.Reviewing = true
	if got := Decide(in); got != nil {
		t.Fatalf("nothing is conducted while the goal's review runs, got %v", got)
	}
}

func TestStartsReadyCardsUpToLanes(t *testing.T) {
	in := base()
	in.Cards = []Card{
		{ID: "FD-002", State: Waiting, Envelope: 500},
		{ID: "FD-003", State: Waiting, Envelope: 500, DependsOn: []domain.FeatureID{"FD-002"}},
		{ID: "FD-004", State: Waiting, Envelope: 500},
		{ID: "FD-005", State: Waiting, Envelope: 500},
	}
	if got, want := acts(in), "start FD-002 | start FD-004"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	in.Cards[0].State = Landed
	in.Cards[2].State = Running
	if got, want := acts(in), "start FD-003"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	in.Cards[1].TakenOver = true
	if got, want := acts(in), "start FD-005"; got != want {
		t.Fatalf("a taken-over card is never started by the goal: got %q, want %q", got, want)
	}
}

func TestLandsOneVerifiedCardPerTick(t *testing.T) {
	in := base()
	in.Cards = []Card{
		{ID: "FD-003", State: Verified, Envelope: 500},
		{ID: "FD-002", State: Verified, Envelope: 500, TakenOver: true},
	}
	if got, want := acts(in), "land FD-002"; got != want {
		t.Fatalf("got %q, want %q (lowest id first, taken over or not)", got, want)
	}
}

func TestFindingsGoToTheLeadOnceBeforeLanding(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.Cards = []Card{{ID: "FD-002", State: Verified, Envelope: 500, Findings: 2}}
	if got := acts(in); !strings.HasPrefix(got, "lead [FD-002 verified with 2 open reviewer finding(s)") {
		t.Fatalf("got %q", got)
	}
	in.Cards[0].LeadTries = 1
	if got, want := acts(in), "land FD-002"; got != want {
		t.Fatalf("after one lead look the card lands: got %q", got)
	}
	in.Cards[0].LeadTries = 0
	in.LeadAvailable = false
	if got, want := acts(in), "land FD-002"; got != want {
		t.Fatalf("with no lead the card lands: got %q", got)
	}
}

func TestExhaustedCardRaiseOrWrapUp(t *testing.T) {
	in := base()
	in.Cards = []Card{
		{ID: "FD-002", State: Exhausted, Envelope: 500, Spent: 500},
		{ID: "FD-003", State: Waiting, Envelope: 500},
	}
	// no lead: raise to RaisedEnvelope(500) = 630 when affordable
	if got := acts(in); !strings.HasPrefix(got, "raise FD-002 → 630") {
		t.Fatalf("got %q", got)
	}
	// lead available and not yet consulted: the lead decides
	in.LeadAvailable = true
	if got := acts(in); !strings.Contains(got, "lead [FD-002 ran out of budget") {
		t.Fatalf("got %q", got)
	}
	// not affordable: wrap up and drop what is unfinished
	in.LeadAvailable = false
	in.Envelope = 1700 // 1700 − 500 − 500 − 600 = 100 available, raise needs 130
	got := acts(in)
	if !strings.HasPrefix(got, "wrap-up: FD-002 needs more budget") || !strings.Contains(got, "drop FD-002") || !strings.Contains(got, "drop FD-003") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "start") {
		t.Fatalf("nothing starts once wrapping up: %q", got)
	}
}

func TestStuckCardLeadThenDrop(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.Cards = []Card{{ID: "FD-002", State: Stuck, Envelope: 500, Reason: "review cap"}}
	if got := acts(in); got != "lead [FD-002 is stuck: review cap]" {
		t.Fatalf("got %q", got)
	}
	in.Cards[0].LeadTries = MaxLeadTriesStuck
	if got := acts(in); got != "drop FD-002: stuck and not recoverable: review cap | finish: 1 card(s) dropped" {
		t.Fatalf("got %q", got)
	}
}

func TestDroppedDependencyStrandsDependents(t *testing.T) {
	in := base()
	in.Cards = []Card{
		{ID: "FD-002", State: Dropped, Envelope: 500},
		{ID: "FD-003", State: Waiting, Envelope: 500, DependsOn: []domain.FeatureID{"FD-002"}},
	}
	if got, want := acts(in), "drop FD-003: depends on FD-002, which was dropped | finish: 2 card(s) dropped"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBudgetPastReserveWrapsUp(t *testing.T) {
	in := base()
	in.OwnSpent = 3000
	in.Cards = []Card{
		{ID: "FD-002", State: Verified, Envelope: 500, Spent: 400},
		{ID: "FD-003", State: Running, Envelope: 500, Spent: 100},
	}
	got := acts(in)
	want := "wrap-up: the budget reached the reserve | drop FD-003: the goal is wrapping up: the budget reached the reserve | land FD-002"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// once landed and dropped, it finishes partial with the wrap reason
	in.WrapUp, in.WrapReason = true, "the budget reached the reserve"
	in.Cards[0].State, in.Cards[1].State = Landed, Dropped
	if got := acts(in); got != "finish: the budget reached the reserve" {
		t.Fatalf("got %q", got)
	}
}

func TestLeadFailuresWrapUp(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.LeadFailures = MaxLeadFailures
	in.LeadPending = []string{"kickoff"}
	in.Cards = []Card{{ID: "FD-002", State: Running, Envelope: 500}}
	got := acts(in)
	if !strings.HasPrefix(got, "wrap-up: the lead kept failing | drop FD-002") || strings.Contains(got, "lead [") {
		t.Fatalf("got %q", got)
	}
}

func TestPendingLeadReasonsAndFinish(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.LeadPending = []string{"kickoff"}
	if got := acts(in); got != "lead [kickoff]" {
		t.Fatalf("a pending kickoff runs the lead before finishing, got %q", got)
	}
	in.LeadPending = nil
	if got := acts(in); got != "finish" {
		t.Fatalf("a goal with nothing left finishes whole, got %q", got)
	}
	in.Cards = []Card{{ID: "FD-002", State: Landed, Envelope: 500, Spent: 300}}
	if got := acts(in); got != "finish" {
		t.Fatalf("got %q", got)
	}
	in.Cards = append(in.Cards, Card{ID: "FD-003", State: Running, TakenOver: true, Envelope: 300})
	if got := acts(in); got != "" {
		t.Fatalf("a card you are driving holds the finish, got %q", got)
	}
}

func TestLedger(t *testing.T) {
	in := base()
	in.OwnSpent = 180
	in.Cards = []Card{
		{ID: "FD-101", State: Landed, Envelope: 600, Spent: 540},
		{ID: "BG-103", State: Landed, Envelope: 150, Spent: 90},
		{ID: "FD-104", State: Running, Envelope: 900, Spent: 820},
		{ID: "FD-105", State: Running, Envelope: 300, Spent: 350},
	}
	l := ComputeLedger(in)
	if l.Given != 540+90+900+350 {
		t.Fatalf("given = %v", l.Given)
	}
	if l.Available != 4000-180-1880-600 {
		t.Fatalf("available = %v", l.Available)
	}
	if l.OwnBudget() != 4000-180-1880 {
		t.Fatalf("own budget = %v (reserve belongs to the goal's own stages)", l.OwnBudget())
	}
}

func TestSplitEnvelopes(t *testing.T) {
	got, err := SplitEnvelopes([]int{600, 0, 0}, 2000)
	if err != nil || got[0] != 600 || got[1] != 700 || got[2] != 700 {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = SplitEnvelopes([]int{1500, 1500}, 2000)
	if err != nil || got[0] != 1000 || got[1] != 1000 {
		t.Fatalf("overflow scales down: %v, %v", got, err)
	}
	if _, err := SplitEnvelopes([]int{0, 0, 0}, 300); err == nil {
		t.Fatalf("a pool that cannot fund MinEnvelope per card is refused")
	}
	if got, err := SplitEnvelopes(nil, 0); err != nil || len(got) != 0 {
		t.Fatalf("no cards is fine: %v %v", got, err)
	}
}
