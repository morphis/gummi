package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// leadFake is a tool-capable fake whose lead sessions run script: each
// turn's prompt goes to it and it returns the tool calls to make. Every
// other session gets a plain idle.
type leadFake struct {
	*agent.Fake
	mu      sync.Mutex
	prompts []string
	hints   []string
}

func newLeadFake(script func(prompt string) []agent.Event) *leadFake {
	lf := &leadFake{Fake: agent.NewFake("")}
	lf.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	lf.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role != agent.RoleLead {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		lf.mu.Lock()
		lf.prompts = append(lf.prompts, msg)
		lf.hints = append(lf.hints, strings.Join(opts.SystemHints, "\n"))
		lf.mu.Unlock()
		evs := script(msg)
		return append(evs,
			agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 12, Model: "lead-model"}},
			agent.Event{Kind: agent.EventMessage, Text: "did the lead things"},
			agent.Event{Kind: agent.EventIdle})
	}
	return lf
}

func toolCall(id, name string, args any) agent.Event {
	raw, _ := json.Marshal(args)
	return agent.Event{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: id, Name: name, Args: raw}}
}

// leadEngine is advanceEngine with a lead-capable agent.
func leadEngine(t *testing.T, ag agent.Agent) (*Engine, *state.Store, string, domain.Feature) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", Persist: true})
	t.Cleanup(func() { e.Close() })
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(context.Background(), g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	return e, store, wt.Root(), g
}

func TestLeadKickoffActsThroughGoalTools(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		if !strings.Contains(prompt, "kickoff") {
			return nil
		}
		return []agent.Event{
			toolCall("1", "reserve_set", map[string]any{"credits": 300, "reason": "two cards, one review"}),
			toolCall("2", "card_create", map[string]any{"title": "offline flag", "one_liner": "add --offline", "serves": []string{"DW-1"}, "envelope": 200}),
			toolCall("3", "decision_record", map[string]any{"decision": "the flag is --offline", "alternative": "--no-network"}),
			toolCall("4", "card_create", map[string]any{"title": "serves nothing", "serves": []string{}}),
			toolCall("5", "done_when_add", map[string]any{"says": "sneaky", "note": "none"}),
			toolCall("6", "backlog_file", map[string]any{"kind": "bug", "description": "export crashes on empty input"}),
		}
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()

	view, _ := e.GoalView(ctx, g.ID)
	if !view.Input.LeadAvailable || len(view.Input.LeadPending) != 1 {
		t.Fatalf("a started goal with a tool-capable lead owes a kickoff: %+v", view.Input)
	}
	r := tick(t, e, g.ID)
	var sawLead bool
	for _, a := range r.Actions {
		if a.Kind == goalpolicy.Lead {
			sawLead = true
		}
	}
	if !sawLead {
		t.Fatalf("actions = %v", r.Actions)
	}

	got, _ := store.GetFeature(ctx, g.ID)
	if got.Goal.Reserve != 300 {
		t.Fatalf("reserve = %d", got.Goal.Reserve)
	}
	if got.Spend.Credits < 12 {
		t.Fatalf("the lead's turn is booked to the goal, spend = %v", got.Spend.Credits)
	}
	cards := goalCards(t, store, g.ID)
	if len(cards) != 3 || cards[2].Title != "offline flag" || cards[2].Budget.Envelope != 200 || cards[2].GateMode() != domain.GateAutopilot {
		t.Fatalf("cards = %+v", cards)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	var decisions, found, turns int
	for _, en := range log {
		switch en.Action {
		case state.GoalDecision:
			decisions++
		case state.GoalFound:
			found++
		case state.GoalLeadTurn:
			turns++
		case state.GoalItemAdded:
			t.Fatal("the lead may not add a done-when item no note asked for")
		}
	}
	if decisions != 1 || found != 1 || turns != 1 {
		t.Fatalf("decisions %d found %d turns %d in %+v", decisions, found, turns, log)
	}
	all, _ := store.ListFeatures(ctx)
	var backlog *domain.Feature
	for i := range all {
		if all[i].FoundBy == g.ID {
			backlog = &all[i]
		}
	}
	if backlog == nil || backlog.InGoal() {
		t.Fatalf("a found-along-the-way card goes on the open board: %+v", backlog)
	}
	raw, _ := os.ReadFile(view.DocPath)
	if !strings.Contains(string(raw), "offline flag") {
		t.Fatal("a lead-created card is added to the goal doc's card list")
	}

	// the kickoff is not owed again
	view, _ = e.GoalView(ctx, g.ID)
	if len(view.Input.LeadPending) != 0 {
		t.Fatalf("pending after kickoff = %v", view.Input.LeadPending)
	}
	lf.mu.Lock()
	hint := lf.hints[0]
	lf.mu.Unlock()
	if !strings.Contains(hint, "You are the lead for goal "+string(g.ID)) {
		t.Fatalf("lead hint = %q", hint)
	}
}

func TestLeadNoteWakesTheLeadAndMayAddAnItem(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		if strings.Contains(prompt, "your note: also check Windows paths") {
			return []agent.Event{toolCall("1", "done_when_add", map[string]any{"says": "Windows paths work", "check": "go test ./winpath", "note": "also check Windows paths"})}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	tick(t, e, g.ID) // kickoff
	if err := e.GoalNote(ctx, g.ID, "also check Windows paths"); err != nil {
		t.Fatal(err)
	}
	tick(t, e, g.ID)
	view, _ := e.GoalView(ctx, g.ID)
	if len(view.DoneWhen) != 3 || view.DoneWhen[2].ID != "DW-3" {
		t.Fatalf("done-when = %+v", view.DoneWhen)
	}
	raw, _ := os.ReadFile(view.DocPath)
	checks, _, _ := spec.ParseChecks(string(raw))
	var have bool
	for _, c := range checks {
		if c.Name == "done-when DW-3" && c.Cmd == "go test ./winpath" {
			have = true
		}
	}
	if !have {
		t.Fatalf("the added item's command joins the goal's checks: %+v", checks)
	}
	_ = store
}

func TestGoalAnswerAndPlanCheckHooks(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		switch {
		case strings.Contains(prompt, "is asking a question"):
			return []agent.Event{toolCall("1", "card_answer", map[string]any{"answer": "json", "decision": "json output by default", "alternative": "table"})}
		case strings.Contains(prompt, "has finished its plan"):
			return []agent.Event{toolCall("1", "card_plan", map[string]any{"approve": false, "note": "keep the cache out of the export package"})}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	cards := goalCards(t, store, g.ID)

	ask := &Ask{Question: "Which output format?", Options: []AskOption{{Label: "table (recommended)"}, {Label: "json"}}}
	answer, ok, err := e.GoalAnswer(ctx, cards[0].ID, ask)
	if err != nil || !ok || answer != "json" {
		t.Fatalf("answer = %q ok=%v err=%v", answer, ok, err)
	}
	approve, note, ok, err := e.GoalPlanCheck(ctx, cards[0].ID)
	if err != nil || !ok || approve || note != "keep the cache out of the export package" {
		t.Fatalf("plan check = %v %q %v %v", approve, note, ok, err)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	var decision bool
	for _, en := range log {
		if en.Action == state.GoalDecision && en.Detail == "json output by default" && en.Card == cards[0].ID {
			decision = true
		}
	}
	if !decision {
		t.Fatalf("a user-visible answer is a decision for review: %+v", log)
	}

	// a card outside any goal is not the hooks' business
	plain := feature(40, "plain", domain.StageImplement)
	putFeature(t, store, plain)
	if _, ok, _ := e.GoalAnswer(ctx, plain.ID, ask); ok {
		t.Fatal("GoalAnswer must decline a card outside a goal")
	}
}

func TestGoalAnswerFallsBackToTheRecommendation(t *testing.T) {
	lf := newLeadFake(func(string) []agent.Event { return nil }) // the lead says nothing
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	cards := goalCards(t, store, g.ID)
	ask := &Ask{Question: "Which?", Options: []AskOption{{Label: "a"}, {Label: "b (recommended)"}}}
	answer, ok, err := e.GoalAnswer(ctx, cards[0].ID, ask)
	if err != nil || !ok || answer != "b (recommended)" {
		t.Fatalf("fallback answer = %q %v %v", answer, ok, err)
	}
}

func TestLeadStuckCardAndDropAttachedIsADecision(t *testing.T) {
	var drop bool
	lf := newLeadFake(func(prompt string) []agent.Event {
		if drop && strings.Contains(prompt, "is stuck") {
			return []agent.Event{toolCall("1", "card_send_back", map[string]any{"card": "FD-002", "note": "try the smaller approach"})}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	tick(t, e, g.ID) // kickoff + starts FD-002
	cards := goalCards(t, store, g.ID)
	if _, err := store.Transition(ctx, cards[0].ID, domain.StagePlan, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendPark(ctx, cards[0].ID, domain.StagePlan, state.ParkReasonGaveUp, "plan critique cap", "", e.now()); err != nil {
		t.Fatal(err)
	}
	drop = true
	r := tick(t, e, g.ID)
	if len(r.Start) != 1 || r.Start[0].ID != cards[0].ID || r.Start[0].Note != "try the smaller approach" {
		t.Fatalf("a send-back restarts the card with the note: %+v (actions %v)", r.Start, r.Actions)
	}
}

func TestLeadUnavailableWithoutTools(t *testing.T) {
	ag := agent.NewFake("") // no client tools, no MCP
	e, _, _, g := leadEngine(t, ag)
	view, _ := e.GoalView(context.Background(), g.ID)
	if view.Input.LeadAvailable {
		t.Fatal("a backend with no tool channel cannot lead")
	}
	if _, err := os.Stat(filepath.Join(view.DocPath)); err != nil {
		t.Fatal(err)
	}
}

// An MCP backend reaches the lead's tools over the tool endpoint: the
// same hello handshake the `gummi __mcp --feature` shim speaks, the goal
// tools listed, and a call dispatched to the turn.
func TestLeadToolEndpointServesGoalTools(t *testing.T) {
	e, _, _, g := leadEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	view, _ := e.GoalView(ctx, g.ID)
	lt := &leadTurn{e: e, view: view}
	path, teardown, err := e.startToolEndpoint(ctx, g.ID, "lead", lt.tools, lt.dispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	if r := c.hello(string(g.ID)); r["error"] != nil {
		t.Fatalf("hello: %v", r["error"])
	}
	id := c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "list_tools"})
	tools := c.read(id)["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"goal_status", "card_create", "card_answer", "decision_record", "goal_wrap_up"} {
		if !names[want] {
			t.Fatalf("lead tools lack %s: %v", want, names)
		}
	}
	id = c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "call_tool", Params: jsonRaw(`{"name":"goal_status","args":{}}`)})
	res := c.read(id)["result"].(map[string]any)["result"].(string)
	if !strings.Contains(res, "Goal "+string(g.ID)) || !strings.Contains(res, "DW-1") {
		t.Fatalf("goal_status = %q", res)
	}
	if r := dialSock(t, path).hello("GL-999"); r["error"] == nil {
		t.Fatal("a hello for another goal must be refused")
	}
}

func TestLeadDecisionRecordedOncePerAnswer(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		if strings.Contains(prompt, "is asking a question") {
			return []agent.Event{
				toolCall("1", "card_answer", map[string]any{"answer": "json", "decision": "json by default", "alternative": "table"}),
				toolCall("2", "decision_record", map[string]any{"card": "FD-002", "decision": "json output by default", "alternative": "table output"}),
			}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	ask := &Ask{Question: "Which format?", Options: []AskOption{{Label: "table"}, {Label: "json"}}}
	if _, _, err := e.GoalAnswer(ctx, "FD-002", ask); err != nil {
		t.Fatal(err)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	n := 0
	for _, en := range log {
		if en.Action == state.GoalDecision {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one call, one decision for review; got %d", n)
	}
}

func TestGoalReworkCarriesTheReviewsFindings(t *testing.T) {
	ag := agent.NewFake("")
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Blocking: a compiled binary `calc` was committed.\nVERDICT: changes"},
			{Kind: agent.EventIdle},
		}
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", Persist: true})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	cur, _ := store.GetFeature(ctx, g.ID)
	if err := e.RunCritique(cur, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, g.ID, StateDone)
	if err := e.RunWith(cur, "The critique found issues. Address each open thread."); err != nil {
		t.Fatal(err)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	last := log[len(log)-1]
	if last.Action != state.GoalRework || !strings.Contains(last.Detail, "compiled binary `calc` was committed") {
		t.Fatalf("the lead's rework carries what the review found: %+v", last)
	}
}

func TestLeadCardChecksShowsFailureOutput(t *testing.T) {
	e, store, _, g := leadEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	add := func(label, status, output string) {
		raw, _ := json.Marshal(map[string]string{"label": label})
		if err := store.AppendEvent(ctx, state.CardEvent{Feature: "FD-002", Stage: domain.StageImplement, Kind: state.EventTool, Status: status, At: e.now(), Payload: string(raw), Output: output}); err != nil {
			t.Fatal(err)
		}
	}
	add("check tracked-files-exact: FAIL (exit 2)", state.StatusFail, "old run")
	add("check calc-untracked: pass", state.StatusOK, "")
	add("check tracked-files-exact: FAIL (exit 2)", state.StatusFail, "sh: 1: Syntax error: \"(\" unexpected")
	view, _ := e.GoalView(ctx, g.ID)
	lt := &leadTurn{e: e, view: view}
	out, err := lt.dispatch(ctx, "card_checks", json.RawMessage(`{"card":"FD-002"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Syntax error") || strings.Contains(out, "old run") || !strings.Contains(out, "calc-untracked: pass") {
		t.Fatalf("card_checks = %q", out)
	}
}

// A lead that keeps sending a stuck card back does not keep it alive
// forever: turns spent on it count until it crosses a stage.
func TestSendBacksDoNotResetTheStuckCount(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		if strings.Contains(prompt, "is stuck") {
			return []agent.Event{toolCall("1", "card_send_back", map[string]any{"card": "FD-002", "note": "try again"})}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	tick(t, e, g.ID) // kickoff, starts FD-002
	if _, err := store.Transition(ctx, "FD-002", domain.StagePlan, "auto"); err != nil {
		t.Fatal(err)
	}
	var dropped bool
	for i := 0; i < 4 && !dropped; i++ {
		if err := store.AppendPark(ctx, "FD-002", domain.StagePlan, state.ParkReasonGaveUp, "critique cap", "", e.now()); err != nil {
			t.Fatal(err)
		}
		r := tick(t, e, g.ID)
		for _, a := range r.Actions {
			if a.Kind == goalpolicy.Drop && a.Card == "FD-002" {
				dropped = true
			}
		}
	}
	if !dropped {
		t.Fatal("after its lead turns a card stuck at the same stage is dropped, send-backs or not")
	}
}

// A card that just ended a turn is between two steps of its driving loop,
// however long ago it entered its stage or the goal last touched it.
func TestACardBetweenStepsIsNotStuck(t *testing.T) {
	e, store, _, g := leadEngine(t, newLeadFake(func(string) []agent.Event { return nil }))
	ctx := context.Background()
	tick(t, e, g.ID) // kickoff, starts FD-002
	if _, err := store.Transition(ctx, "FD-002", domain.StagePlan, "auto"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(5 * time.Minute)
	e.now = func() time.Time { return later }
	if err := store.AppendEvent(ctx, state.CardEvent{Feature: "FD-002", Stage: domain.StagePlan, Kind: state.EventStageExit, At: later, Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	stateOf := func() goalpolicy.CardState {
		goal, _ := store.GetFeature(ctx, g.ID)
		v, err := e.goalView(ctx, goal)
		if err != nil {
			t.Fatal(err)
		}
		gc, _ := v.Card("FD-002")
		return gc.State
	}
	if st := stateOf(); st != goalpolicy.Running {
		t.Fatalf("a card that just finished a turn is between steps, not %s", st)
	}
	later = later.Add(10 * time.Minute)
	if st := stateOf(); st != goalpolicy.Stuck {
		t.Fatalf("past the grace with nothing running it is stuck, not %s", st)
	}
}

// Answering a card's questions is the lead helping it along, not a try at
// unsticking it: a card stuck after its questions still gets a lead turn.
func TestAnsweringACardIsNotATryAtUnstickingIt(t *testing.T) {
	lf := newLeadFake(func(prompt string) []agent.Event {
		if strings.Contains(prompt, "is asking a question") {
			return []agent.Event{toolCall("1", "card_answer", map[string]any{"answer": "b"})}
		}
		return nil
	})
	e, store, _, g := leadEngine(t, lf)
	ctx := context.Background()
	tick(t, e, g.ID) // kickoff, starts FD-002
	if _, err := store.Transition(ctx, "FD-002", domain.StagePlan, "auto"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, ok, err := e.GoalAnswer(ctx, "FD-002", &Ask{Question: "Which?", Options: []AskOption{{Label: "a"}, {Label: "b"}}}); err != nil || !ok {
			t.Fatalf("answer: %v %v", ok, err)
		}
	}
	if err := store.AppendPark(ctx, "FD-002", domain.StagePlan, state.ParkReasonGaveUp, "critique cap", "", e.now()); err != nil {
		t.Fatal(err)
	}
	r := tick(t, e, g.ID)
	for _, a := range r.Actions {
		if a.Kind == goalpolicy.Drop {
			t.Fatalf("a card whose questions the lead answered is not out of tries: %v", r.Actions)
		}
	}
}

// A drop can land between two steps of a loop still holding the card; the
// next step refuses to run it.
func TestADroppedGoalCardDoesNotRunOn(t *testing.T) {
	e, store, _, g := leadEngine(t, newLeadFake(func(string) []agent.Event { return nil }))
	ctx := context.Background()
	tick(t, e, g.ID)
	card, err := store.GetFeature(ctx, "FD-002")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetGoalDropped(ctx, card.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(card); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("a dropped goal card must not run on: %v", err)
	}
	if _, ok, _ := e.GoalAnswer(ctx, card.ID, &Ask{Question: "Which?", Options: []AskOption{{Label: "a"}}}); ok {
		t.Fatal("the lead does not answer for a card its goal dropped")
	}
	if _, _, ok, _ := e.GoalPlanCheck(ctx, card.ID); ok {
		t.Fatal("the lead does not check the plan of a card its goal dropped")
	}
}

// A done-when check whose command cannot observe its item can be repaired,
// but only into a real check: one that fails on main, and passes on the
// goal branch once the item's cards are settled. The repair is a decision
// for review and settles an earlier not-met.
func TestLeadRepairsADoneWhenCheckOnlyIntoARealCheck(t *testing.T) {
	e, store, root, g := leadEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	fix := func(check string) (string, error) {
		view, err := e.GoalView(ctx, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		lt := &leadTurn{e: e, view: view}
		args, _ := json.Marshal(map[string]string{"item": "DW-1", "check": check, "reason": "the agreed command cannot see it"})
		return lt.dispatch(ctx, "done_when_check_fix", args)
	}
	if _, err := fix("true"); err == nil || !strings.Contains(err.Error(), "already passes on main") {
		t.Fatalf("a check that passes without the goal's work is refused: %v", err)
	}
	if _, err := fix("test -f cache.txt"); err == nil {
		t.Fatal("repairing to the agreed command is refused")
	}
	e.recordGoalChecks(g, []goalCheckResult{{Name: "done-when DW-1", OK: false, Status: "FAIL (exit 1)"}})
	view, _ := e.GoalView(ctx, g.ID)
	lt := &leadTurn{e: e, view: view}
	if _, err := lt.dispatch(ctx, "done_when_not_met", json.RawMessage(`{"item":"DW-1","reason":"the check cannot see it"}`)); err != nil {
		t.Fatal(err)
	}

	// once every card serving DW-1 is settled, the repair must pass on the goal branch
	if err := store.SetGoalDropped(ctx, "FD-002", e.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := fix("test -s cache.txt"); err == nil || !strings.Contains(err.Error(), "does not pass on the goal branch") {
		t.Fatalf("a repair that fails on the goal branch is refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, g.WorktreePath(), "cache.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := fix("test -s cache.txt")
	if err != nil {
		t.Fatalf("a real check is taken: %v", err)
	}
	if !strings.Contains(out, "D-") {
		t.Fatalf("the repair names its decision: %q", out)
	}
	doc, _ := os.ReadFile(filepath.Join(root, g.ArtifactPath()))
	if !strings.Contains(string(doc), "check: test -s cache.txt") || !strings.Contains(string(doc), "says: the cache file exists") {
		t.Fatalf("the doc carries the repaired command and the item's words unchanged:\n%s", doc)
	}
	checks, _, _ := spec.ParseChecks(string(doc))
	for _, c := range checks {
		if c.Name == "done-when DW-1" && c.Cmd != "test -s cache.txt" {
			t.Fatalf("the goal's check runs the repaired command: %+v", c)
		}
	}
	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	var decision bool
	for _, d := range rep.Decisions {
		decision = decision || (d.Item == "DW-1" && strings.Contains(d.Alternative, "test -f cache.txt"))
	}
	if !decision {
		t.Fatalf("the repair is a decision for review with the agreed command as the alternative: %+v", rep.Decisions)
	}
	for _, d := range rep.DoneWhen {
		// (DW-1 still reads not met here because this test dropped its card)
		if d.ID == "DW-1" && (d.Evidence == "the check cannot see it" || strings.HasPrefix(d.Evidence, "check ")) {
			t.Fatalf("a repair settles the not-met about the old command: %+v", d)
		}
	}
}

// An item marked not met is settled by a later check run that passes it,
// and not by one that came before the mark.
func TestAPassingCheckAfterANotMetSettlesIt(t *testing.T) {
	e, _, _, g := leadEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	status := func() string {
		rep, err := e.GoalReport(ctx, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rep.DoneWhen[0].Status
	}
	goal := g
	e.recordGoalChecks(goal, []goalCheckResult{{Name: "done-when DW-1", OK: true, Status: "pass"}})
	e.goalLog(ctx, g.ID, state.GoalPayload{Action: state.GoalNotMet, Item: "DW-1", Detail: "passes trivially", By: "lead"})
	if st := status(); st != DoneWhenNotMet {
		t.Fatalf("a not-met after the checks stands: %s", st)
	}
	e.recordGoalChecks(goal, []goalCheckResult{{Name: "done-when DW-1", OK: true, Status: "pass"}})
	if st := status(); st != DoneWhenMet {
		t.Fatalf("a later passing check settles it: %s", st)
	}
}

// A lead turn's prompt carries the contract it acts on, so answering a
// question or checking a plan does not start with tool rounds that fetch it.
func TestLeadPromptsCarryTheGoalAndThePlan(t *testing.T) {
	var prompts []string
	var mu sync.Mutex
	lf := newLeadFake(func(prompt string) []agent.Event {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return nil
	})
	e, store, root, g := leadEngine(t, lf)
	ctx := context.Background()
	cards := goalCards(t, store, g.ID)
	writeArtifact(t, root, cards[0], "# plan\n\n## Chosen approach\n\nKeep the cache under .cache/ — é\n")
	if _, _, err := e.GoalAnswer(ctx, cards[0].ID, &Ask{Question: "Which?", Options: []AskOption{{Label: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.GoalPlanCheck(ctx, cards[0].ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("two lead turns, got %d", len(prompts))
	}
	for _, p := range prompts {
		for _, want := range []string{"Export works with no network.", "No new dependencies.", "DW-1: the cache file exists", cards[0].Title} {
			if !strings.Contains(p, want) {
				t.Fatalf("a lead prompt carries the goal (%q):\n%s", want, p)
			}
		}
	}
	if !strings.Contains(prompts[1], "Keep the cache under .cache/") {
		t.Fatalf("a plan check carries the plan:\n%s", prompts[1])
	}
	if clip("éé", 3) != "é…" {
		t.Fatalf("a cut never splits a rune: %q", clip("éé", 3))
	}
}

// A goal's done-when checks come from its done-when block: an agent's
// rewrite of the gummi-checks copy cannot drop or change them.
func TestGoalDoneWhenChecksComeFromTheDoneWhenBlock(t *testing.T) {
	doc := testGoalDoc + "```gummi-checks\n- DW-1: true\n- name: build\n  cmd: go build ./...\n```\n"
	checks := withDoneWhenChecks(doc, []domain.Check{{Name: "DW-1", Cmd: "true"}, {Name: "build", Cmd: "go build ./..."}})
	if len(checks) != 2 || checks[0].Name != "done-when DW-1" || checks[0].Cmd != "test -f cache.txt" || checks[1].Name != "build" {
		t.Fatalf("checks = %+v", checks)
	}
	if got := withDoneWhenChecks(testGoalDoc, nil); len(got) != 1 || got[0].Name != "done-when DW-1" {
		t.Fatalf("a lost copy is restored from the done-when block: %+v", got)
	}
}
