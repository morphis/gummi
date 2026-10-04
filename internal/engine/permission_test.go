package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// permissionFake stages a backend whose turn raises a guarded tool-call
// approval and ends: the ruling is taken by the caller, and the turn's
// idle is pushed when a test wants the turn to have ended around it.
func permissionFake() *agent.Fake {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{{
			Kind:   agent.EventPermission,
			Tool:   "bash",
			Detail: "make test",
			CallID: "perm-9",
		}}
	}
	return ag
}

// permissionAnswers reads the rulings a session's fake delivered through
// ResolvePermission.
type permissionAnswers interface {
	PermissionAnswers() map[string]bool
}

// TestPermissionBecomesCardDecision: a guarded board's held tool call
// becomes the card's open decision on the same machinery an ask_user
// question rides; answering it delivers the ruling to the session by
// request id and the turn continues.
func TestPermissionBecomesCardDecision(t *testing.T) {
	ag := permissionFake()
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m",
		Permission: agent.PermissionGuarded,
	})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := feature(1, "Dark mode", domain.StagePlan)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	snap := s.Snapshot()
	if snap.PendingAsk == nil {
		t.Fatal("no open decision for the held tool call")
	}
	ask := snap.PendingAsk
	if !ask.Permission || ask.CallID != "perm-9" {
		t.Errorf("ask = %+v, want a permission decision on perm-9", ask)
	}
	if ask.Question != "Allow bash — make test?" {
		t.Errorf("question = %q", ask.Question)
	}
	if len(ask.Options) != 2 || ask.Options[0].Label != "Approve" || ask.Options[1].Label != "Deny" {
		t.Errorf("options = %+v, want approve/deny in that order", ask.Options)
	}
	if snap.Busy {
		t.Error("the session is busy while its tool call is held on a human")
	}
	// the durable row went down in the same breath
	opens, err := e.cfg.Store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := opens["FD-001"]
	if len(rows) != 1 || rows[0].Kind != state.DecisionKindAsk || !strings.Contains(rows[0].Question, "Allow bash") {
		t.Fatalf("open decisions = %+v, want one ask row for the held call", rows)
	}

	if err := e.Answer(ctx, "FD-001", "Approve"); err != nil {
		t.Fatal(err)
	}
	// the ruling landed: the held call went through, the turn runs on to
	// its idle, and the decision is closed by the time it does
	if p, ok := s.agent().(interface{ Push(events ...agent.Event) }); !ok {
		t.Fatal("the fake session cannot push a late event")
	} else {
		p.Push(agent.Event{Kind: agent.EventIdle})
	}
	waitFor(t, e, EventIdle)
	r, ok := s.agent().(permissionAnswers)
	if !ok {
		t.Fatal("the fake session does not resolve permissions")
	}
	if approve, ok := r.PermissionAnswers()["perm-9"]; !ok || !approve {
		t.Errorf("permission answers = %v, want perm-9 approved", r.PermissionAnswers())
	}
	if s.Snapshot().PendingAsk != nil {
		t.Error("the decision stayed open after the ruling")
	}
	found := false
	for _, a := range s.Snapshot().Activity {
		if strings.Contains(a, "approved") && strings.Contains(a, "make test") {
			found = true
		}
	}
	if !found {
		t.Errorf("activity never recorded the ruling: %+v", s.Snapshot().Activity)
	}
	// the ruling was not echoed into the transcript as a user turn: the
	// turn continues server-side, so the record is the decision's, not a
	// reply the model would read twice
	for _, m := range s.Snapshot().Transcript {
		if m.Author == AuthorUser && m.Content == "Approve" {
			t.Error("the ruling was echoed into the transcript")
		}
	}
}

// A ruling's only two readings are the options' own: free-form words on a
// held tool call deny, so the call takes a ruling even when the answer
// came typed.
func TestPermissionFreeFormAnswerDenies(t *testing.T) {
	ag := permissionFake()
	e := newEngine(t, ag)
	e.cfg.Permission = agent.PermissionGuarded
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StagePlan))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	if err := e.Answer(ctx, "FD-001", "only if it stays in the worktree"); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.agent().(interface{ Push(events ...agent.Event) }); !ok {
		t.Fatal("the fake session cannot push a late event")
	} else {
		p.Push(agent.Event{Kind: agent.EventIdle})
	}
	waitFor(t, e, EventIdle)
	r := s.agent().(permissionAnswers)
	if approve, ok := r.PermissionAnswers()["perm-9"]; !ok || approve {
		t.Errorf("permission answers = %v, want perm-9 denied by free-form words", r.PermissionAnswers())
	}
}

// TestPermissionAuxiliaryLoopsStayAllowAll: a loop with no decision
// surface spawns its sessions allow-all on a guarded board too, so
// guarded asks would never hang one of them behind a decision nobody can
// see.
func TestPermissionAuxiliaryLoopsStayAllowAll(t *testing.T) {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	var got []agent.SessionOpts
	ag.OnNewSession = func(opts agent.SessionOpts) { got = append(got, opts) }
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m",
		Permission: agent.PermissionGuarded,
	})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.CodeVsPlan(ctx, f); err != nil {
		t.Fatalf("one-shot pass on a guarded board: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("one-shot sessions spawned = %d, want one", len(got))
	}
	if got[0].Permission != agent.PermissionAllowAll {
		t.Errorf("one-shot permission = %q, want allow-all on a guarded board", got[0].Permission)
	}
}

// A guarded decision still open when the turn ends is cut loose from its
// held call: the ask stays on the card (its answer rides the next turn)
// and the activity says the agent stopped waiting. This is the expiry the
// session close rides too — a close mid-turn emits the same idle first.
func TestPermissionOutlivesItsTurn(t *testing.T) {
	ag := permissionFake()
	e := newEngine(t, ag)
	e.cfg.Permission = agent.PermissionGuarded
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StagePlan))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	// the turn ends while the ruling is still open, the way a close
	// mid-turn would land it: an idle behind the still-open decision
	if p, ok := s.agent().(interface {
		Push(events ...agent.Event)
	}); !ok {
		t.Fatal("the fake session cannot push a late event")
	} else {
		p.Push(agent.Event{Kind: agent.EventIdle})
	}
	waitFor(t, e, EventUpdated)

	snap := s.Snapshot()
	if snap.PendingAsk == nil || !snap.PendingAsk.Outlived || snap.PendingAsk.CallID != "" {
		t.Fatalf("pending ask = %+v, want the ruling cut loose from its held call", snap.PendingAsk)
	}
	if !strings.Contains(strings.Join(snap.Activity, "\n"), "stopped waiting") {
		t.Errorf("activity = %+v, want the outlived note", snap.Activity)
	}
}

// The per-spawn-site ruling table is pinned where the sites live; the two
// tests above cover the routed and the allow-all halves. What remains of
// the plan's expiry rule — a decision pending when the SESSION ends — is
// answerable after the fact on the machinery that already restores asks:
// the answer rides a turn (AnswerAs's rerun branch), never a dead call,
// so nothing is left unanswerable in the attention lane.
func TestPermissionExpiredRowStaysAnswerable(t *testing.T) {
	ag := permissionFake()
	e := newEngine(t, ag)
	e.cfg.Permission = agent.PermissionGuarded
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StagePlan))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	// end the session's stream without a terminal turn (a pause does)
	if err := e.Pause(ctx, "FD-001"); err != nil {
		t.Fatal(err)
	}

	// the durable row is still there, and answering it does not look for
	// a live call: the answer rides a turn on the rerun path
	if err := e.Answer(ctx, "FD-001", "Deny"); err != nil {
		t.Fatalf("answering a decision whose session ended: %v", err)
	}
	// the answer never reached the stopped session's resolver
	if r, ok := s.agent().(permissionAnswers); ok {
		if answers := r.PermissionAnswers(); len(answers) != 0 {
			t.Errorf("permission answers = %v, want none — the session was gone", answers)
		}
	}
	// the durable row is closed by the answer
	opens, err := e.cfg.Store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows := opens["FD-001"]; len(rows) != 0 {
		t.Errorf("open decisions = %+v, want the answered row closed", rows)
	}
}
