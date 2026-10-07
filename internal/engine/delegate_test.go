package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestCardToolsAreOptIn: a freeform session is offered the card tools,
// and told about them, only once its person has set a delegation budget.
func TestCardToolsAreOptIn(t *testing.T) {
	for _, budget := range []int{0, 500} {
		var mu sync.Mutex
		var offered, hints []string
		ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
			mu.Lock()
			for _, td := range opts.Tools {
				offered = append(offered, td.Name)
			}
			hints = opts.SystemHints
			mu.Unlock()
			return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
		}}
		ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
		ws, store, wt := newRepo(t)
		e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
		f := freeformCard(1, "delegate maybe")
		f.Delegate.Budget = budget
		createFeature(t, store, f)
		ff, err := e.OpenFreeform(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if err := ff.Send(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		waitFreeformIdle(t, ff)
		mu.Lock()
		tools, hint := strings.Join(offered, ","), strings.Join(hints, "\n")
		mu.Unlock()
		e.Close()
		has := strings.Contains(tools, cardCreateToolName)
		if has != (budget > 0) || strings.Contains(hint, "card_create") != (budget > 0) {
			t.Errorf("budget %d: tools %s, hint mentions card_create: %v", budget, tools, strings.Contains(hint, "card_create"))
		}
	}
}

// TestCardCreateAsksThePersonAndHoldsTheBudget: each card_create is put
// to the person with three answers; "all that follow" mints the card and
// stops the asking; the budget caps the session after that answer too.
func TestCardCreateAsksThePersonAndHoldsTheBudget(t *testing.T) {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(3, "split the work")
	f.Delegate.Budget = 500
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	sess := ff.Session()

	e.handleClientTool(sess, &agent.ToolCall{
		ID: "c1", Name: cardCreateToolName,
		Args: json.RawMessage(`{"kind":"FD","description":"Add a flag\nmore words","envelope":300}`),
	})
	ask := sess.Snapshot().PendingAsk
	if ask == nil || ask.onAnswer == nil || len(ask.Options) != 3 {
		t.Fatalf("card_create did not put the card to the person: %+v", ask)
	}
	if got := ask.onAnswer(delegateDeny); !strings.Contains(got, "did not create") {
		t.Errorf("no answered %q", got)
	}
	if cards, _ := store.DelegatedCards(ctx, f.ID); len(cards) != 0 {
		t.Fatalf("a refused card was minted: %v", cards)
	}
	got := ask.onAnswer(delegateAll)
	cards, _ := store.DelegatedCards(ctx, f.ID)
	if len(cards) != 1 || !strings.HasPrefix(got, "created "+string(cards[0].ID)) {
		t.Fatalf("all-that-follow answered %q, cards %v", got, cards)
	}
	child := cards[0]
	if child.Kind != domain.KindFeature || child.Budget.Envelope != 300 || child.GateApproval != domain.GateAutopilot || child.Title != "Add a flag" {
		t.Errorf("child = %+v", child)
	}
	if !child.Offers(domain.LandSquash) || child.Offers(domain.LandMerge) {
		t.Error("a delegated card must land as a squash only")
	}
	parent, _ := store.GetFeature(ctx, f.ID)
	if !parent.Delegate.ConfirmAll {
		t.Error("all-that-follow did not stop the asking")
	}

	// 200 left: a 300-credit card is refused without a question...
	sess.takePendingAsk()
	if got := driveMemoryTool(t, e, sess, cardCreateToolName, `{"kind":"BG","description":"too big","envelope":300}`); !strings.Contains(got, "200 credits") {
		t.Errorf("over-budget card_create resolved %q", got)
	}
	// ...and one that fits is minted at once, no question asked
	if got := driveMemoryTool(t, e, sess, cardCreateToolName, `{"kind":"BG","description":"fits","envelope":150}`); !strings.HasPrefix(got, "created ") {
		t.Errorf("confirmed-all card_create resolved %q", got)
	}
	if got := driveMemoryTool(t, e, sess, cardListToolName, `{}`); !strings.Contains(got, "50 left to give") {
		t.Errorf("card_list = %q", got)
	}
	// research has no branch to land on
	if got := driveMemoryTool(t, e, sess, cardCreateToolName, `{"kind":"RS","description":"look"}`); !strings.Contains(got, "research") {
		t.Errorf("research card_create resolved %q", got)
	}
	// withdrawing the delegation stops the idle backend, so the next turn
	// respawns it without the card tools
	if err := e.SetDelegation(ctx, f.ID, domain.Delegation{}); err != nil {
		t.Fatal(err)
	}
	if parent, _ := store.GetFeature(ctx, f.ID); parent.Delegate.Enabled() {
		t.Error("the delegation was not withdrawn")
	}
	if sess.Live() {
		t.Error("the backend still runs with the card tools it was given")
	}
	if err := e.SetDelegation(ctx, child.ID, domain.Delegation{Budget: 10}); err == nil {
		t.Error("a feature card was given a delegation")
	}
}

// TestDelegationRules: what a delegated card holds of its parent's budget,
// and who may delegate or be delegated.
func TestDelegationRules(t *testing.T) {
	parent := domain.Feature{Kind: domain.KindFreeform, Delegate: domain.Delegation{Budget: 1000}}
	running := domain.Feature{Stage: domain.StageImplement, Budget: domain.Budget{Envelope: 300}, Spend: domain.Spend{Credits: 100}}
	over := domain.Feature{Stage: domain.StagePlan, Budget: domain.Budget{Envelope: 150}, Spend: domain.Spend{Credits: 200}}
	landed := domain.Feature{Stage: domain.StageDone, Budget: domain.Budget{Envelope: 300}, Spend: domain.Spend{Credits: 120}}
	if got := domain.DelegateAvailable(parent, []domain.Feature{running, over, landed}); got != 1000-300-200-120 {
		t.Errorf("available = %v", got)
	}
	if open := domain.UnlandedDelegates([]domain.Feature{{ID: "FD-001", Stage: domain.StageVerify}, {ID: "FD-002", Stage: domain.StageDone}}); len(open) != 1 || open[0] != "FD-001" {
		t.Errorf("unlanded = %v", open)
	}
	base := freeformCard(9, "x")
	child := base
	child.ID, child.Kind, child.Stage, child.ParentID = "FD-009", domain.KindFeature, domain.StagePlan, base.ID
	if err := child.Validate(); err != nil {
		t.Errorf("a feature under a freeform card: %v", err)
	}
	child.GoalID = "GL-001"
	if err := child.Validate(); err == nil {
		t.Error("a delegated card in a goal validated")
	}
	child.GoalID, child.ParentID = "", "FD-001"
	if err := child.Validate(); err == nil {
		t.Error("a card delegated by a non-freeform card validated")
	}
	child.ParentID, child.Delegate.Budget = "", 10
	if err := child.Validate(); err == nil {
		t.Error("a feature with a delegation budget validated")
	}
}
