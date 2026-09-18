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
	// the full ask is not affordable, but something is: raise to what is
	// there rather than all-or-nothing. 1700 − 500 − 500 − 600 reserve =
	// 100 available, and the ask of 630 needs 130.
	in.LeadAvailable = false
	in.Envelope = 1700
	if got := acts(in); !strings.HasPrefix(got, "raise FD-002 → 600") {
		t.Fatalf("a partial raise beats stopping the goal: %q", got)
	}

	// nothing left worth giving: the goal asks a person, and does not
	// choose work to abandon on its own
	in.Envelope = 1620 // 20 available, under one turn's worth
	got := acts(in)
	if !strings.Contains(got, "need-budget FD-002 → 630") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "has spent 500 of 500 and needs about 630") {
		t.Fatalf("it says what it needs and why: %q", got)
	}
	if strings.Contains(got, "drop") || strings.Contains(got, "wrap-up") {
		t.Fatalf("running out of budget drops nothing: %q", got)
	}
	if strings.Contains(got, "start") {
		t.Fatalf("nothing new starts while it waits on you: %q", got)
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

// TestStartEnvelopes: a card starts at the lesser of its estimate and an
// even share, and what is left over is not handed out at all — it stays in
// the pool for the raises the conductor makes on evidence.
func TestStartEnvelopes(t *testing.T) {
	// under its share keeps its estimate; the rest start at the share,
	// and 2000 − (600 + 666 + 666) stays unallocated
	got, err := StartEnvelopes([]int{600, 0, 0}, 2000)
	if err != nil || got[0] != 600 || got[1] != 666 || got[2] != 666 {
		t.Fatalf("got %v, %v", got, err)
	}
	// the case that used to hand one card the budget: a big estimate
	// starts at its share, not at what it guessed
	got, err = StartEnvelopes([]int{390, 440, 1120}, 1470)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 390 || got[1] != 440 || got[2] != 490 {
		t.Fatalf("a big estimate starts at its share: %v", got)
	}
	if total := got[0] + got[1] + got[2]; float64(total) >= 1470 {
		t.Fatalf("the rest stays in the pool to raise from, allocated %d of 1470", total)
	}
	// every card over its share: an even start, nothing committed beyond it
	got, err = StartEnvelopes([]int{1500, 1500}, 2000)
	if err != nil || got[0] != 1000 || got[1] != 1000 {
		t.Fatalf("even shares: %v, %v", got, err)
	}
	if _, err := StartEnvelopes([]int{0, 0, 0}, 300); err == nil {
		t.Fatalf("a pool that cannot fund MinEnvelope per card is refused")
	}
	if got, err := StartEnvelopes(nil, 0); err != nil || len(got) != 0 {
		t.Fatalf("no cards is fine: %v %v", got, err)
	}
}

// TestShrinkBeforeDroppingACardTheGoalCanAfford is the drive's case E in
// one table. The ledger counts what a card HOLDS, so a card that has not
// started holds its whole allocation — and when that allocation is what
// tips the ledger past the reserve, the card the goal drops is the one
// whose unspent credits made the number negative. Case E dropped its
// one-paragraph doc card with 330 of 1,400 credits never spent, then
// spent 213 more failing its own review for that card's missing work.
func TestShrinkBeforeDroppingACardTheGoalCanAfford(t *testing.T) {
	in := Input{
		Stage:    domain.StageImplement,
		Envelope: 1400,
		Reserve:  140,
		OwnSpent: 470,
		Cards: []Card{
			{ID: "BG-002", State: Landed, Envelope: 484, Spent: 490},
			{ID: "BG-003", State: Waiting, Envelope: 416},
		},
	}
	if got := ComputeLedger(in).Available; got >= 0 {
		t.Fatalf("fixture is not the failing one: Available = %.0f, want negative", got)
	}

	acts := Decide(in)
	var shrink *Action
	for i := range acts {
		switch acts[i].Kind {
		case Shrink:
			shrink = &acts[i]
		case Drop:
			t.Errorf("dropped %s while the goal still had credits to give: %v", acts[i].Card, acts)
		case WrapUp:
			t.Errorf("wrapped up while the goal still had credits to give: %v", acts)
		}
	}
	if shrink == nil {
		t.Fatalf("no shrink: %v", acts)
	}
	if shrink.Card != "BG-003" {
		t.Errorf("shrank %s, want the card that never started", shrink.Card)
	}
	if shrink.To >= 416 || shrink.To < MinCardEnvelope {
		t.Errorf("shrank to %d, want between %d and 416", shrink.To, MinCardEnvelope)
	}
	// and the shrink has to actually balance the ledger
	in.Cards[1].Envelope = shrink.To
	if got := ComputeLedger(in).Available; got < 0 {
		t.Errorf("after the shrink Available = %.0f, still negative", got)
	}
}

// When even the floor cannot cover the shortfall, wrapping up is right:
// a card funded below MinCardEnvelope cannot finish its design stage, so
// funding it buys a guaranteed exhaustion rather than a chance at the work.
func TestShrinkGivesUpWhenThereIsNoRoom(t *testing.T) {
	in := Input{
		Stage:    domain.StageImplement,
		Envelope: 600,
		Reserve:  100,
		OwnSpent: 480,
		Cards:    []Card{{ID: "BG-003", State: Waiting, Envelope: 150}},
	}
	acts := Decide(in)
	var wrapped, dropped bool
	for _, a := range acts {
		switch a.Kind {
		case WrapUp:
			wrapped = true
		case Drop:
			dropped = true
		case Shrink:
			t.Errorf("shrank below the floor: %v", a)
		}
	}
	if !wrapped || !dropped {
		t.Errorf("a goal that genuinely cannot fund its card must wrap up and drop it: %v", acts)
	}
}

// The measured failure. Three ticks in two seconds each got the same
// "You've hit your session limit · resets 3:10pm (UTC)" back from the
// backend, which spent MaxLeadFailures and wrapped the goal up — dropping
// two cards that had working branches and 124 credits of plan work
// between them. A backend that cannot serve a turn says nothing about the
// work, and waiting is not abandoning.
func TestAnOutageStopsTheGoalWithoutDroppingItsWork(t *testing.T) {
	in := Input{
		Stage: domain.StageImplement, Envelope: 3000, Lanes: 2,
		LeadAvailable: true, LeadOutage: "You've hit your session limit · resets 3:10pm (UTC)",
		Cards: []Card{
			{ID: "BG-002", State: Running, Envelope: 1048, Spent: 55},
			{ID: "BG-003", State: Waiting, Envelope: 1048},
		},
	}
	acts := Decide(in)
	if len(acts) != 1 || acts[0].Kind != Stall {
		t.Fatalf("an outage must stop the goal and do nothing else: %v", acts)
	}
	if acts[0].Reason != in.LeadOutage {
		t.Errorf("reason = %q, want the backend's own words", acts[0].Reason)
	}
	for _, a := range acts {
		if a.Kind == Drop || a.Kind == WrapUp || a.Kind == Start || a.Kind == Finish {
			t.Errorf("an outage decided %s — the work is not at fault", a)
		}
	}

	// the same snapshot with the lead genuinely failing still wraps up:
	// MaxLeadFailures is for a lead that cannot do its job.
	broken := in
	broken.LeadOutage, broken.LeadFailures = "", MaxLeadFailures
	var wrapped bool
	for _, a := range Decide(broken) {
		if a.Kind == WrapUp {
			wrapped = true
		}
	}
	if !wrapped {
		t.Error("a lead that keeps failing still wraps the goal up")
	}
}

// A verify that said the environment cannot run the plan is no opinion on
// the work. Read as stuck, the card cost two lead turns and then its
// branch.
func TestABlockedCardIsNeverDroppedForIt(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.Cards = []Card{
		{ID: "FD-002", State: Blocked, Envelope: 500, Spent: 300, Reason: "no docker here"},
		{ID: "FD-003", State: Running, Envelope: 500},
	}
	if got, want := acts(in), "lead [FD-002 cannot be verified in this environment: no docker here]"; got != want {
		t.Fatalf("the lead gets one look: got %q, want %q", got, want)
	}
	in.Cards[0].LeadTries = 1
	if got := acts(in); got != "" {
		t.Fatalf("a blocked card waits while other work runs, got %q", got)
	}
	in.Cards[0].LeadTries = 5
	if got := acts(in); strings.Contains(got, "drop") {
		t.Fatalf("no number of lead turns drops a blocked card, got %q", got)
	}
}

func TestAGoalWithOnlyBlockedWorkStallsAndKeepsIt(t *testing.T) {
	in := base()
	in.LeadAvailable = true
	in.Cards = []Card{
		{ID: "FD-002", State: Landed, Envelope: 500, Spent: 300},
		{ID: "FD-003", State: Blocked, Envelope: 500, Spent: 300, LeadTries: 1, Reason: "no docker here"},
		{ID: "FD-004", State: Waiting, Envelope: 500, DependsOn: []domain.FeatureID{"FD-003"}},
	}
	got := Decide(in)
	if len(got) != 1 || got[0].Kind != Stall || got[0].Card != "FD-003" {
		t.Fatalf("got %v, want one stall naming FD-003", got)
	}
	if !strings.Contains(got[0].Reason, "no docker here") {
		t.Fatalf("the stall says what it waits on, got %q", got[0].Reason)
	}
	// something in flight: not stalled yet
	in.Cards[2] = Card{ID: "FD-004", State: Running, Envelope: 500}
	if got := acts(in); got != "" {
		t.Fatalf("a goal with a card running is not stalled, got %q", got)
	}
}

func TestABlockedCardIsRetriedOnlyWhenThereIsAReason(t *testing.T) {
	in := base()
	in.Cards = []Card{{ID: "FD-002", State: Blocked, Envelope: 500, LeadTries: 1, Reason: "no docker here", Retry: true}}
	if got, want := acts(in), "start FD-002"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	in.Cards[0].TakenOver = true
	if got := acts(in); strings.Contains(got, "start") {
		t.Fatalf("a taken-over card is never started by the goal, got %q", got)
	}
}

func TestWrappingUpDropsABlockedCardLikeAnyOther(t *testing.T) {
	in := base()
	in.WrapUp, in.WrapReason = true, "you stopped it"
	in.Cards = []Card{{ID: "FD-002", State: Blocked, Envelope: 500, Reason: "no docker here"}}
	if got := acts(in); !strings.Contains(got, "drop FD-002") || !strings.Contains(got, "finish") {
		t.Fatalf("got %q", got)
	}
}
