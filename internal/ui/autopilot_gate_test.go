package ui

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// gateEventsFor filters id's event log to gate events raised in stage.
func gateEventsFor(t *testing.T, m *Shell, id domain.FeatureID, stage domain.Stage) []state.GatePayload {
	t.Helper()
	evs, err := m.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.GatePayload
	for _, ev := range evs {
		if ev.Kind != state.EventGate || ev.Stage != stage {
			continue
		}
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// askEventsFor filters id's event log to ask events.
func askEventsFor(t *testing.T, m *Shell, id domain.FeatureID) []state.AskPayload {
	t.Helper()
	evs, err := m.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.AskPayload
	for _, ev := range evs {
		if ev.Kind != state.EventAsk {
			continue
		}
		var p state.AskPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// hasInboxKind reports whether the inbox carries an item of kind for
// FD-001 — every test in this file's own subject (settleChat's own
// convention).
func hasInboxKind(m *Shell, kind attnKind) bool {
	for _, it := range m.inbox.list() {
		if it.Feature == "FD-001" && it.Kind == kind {
			return true
		}
	}
	return false
}

// --- item 2: full auto-answers its own question ---

// TestAutopilotFullAutoAnswersOwnQuestion: a card stored at full answers
// its own live ask with the recommended option, records the answer as
// state.ActorAutopilot, and never parks an attnQuestion item — DESIGN
// §10.17's "under full it may ... answer its own consequential
// questions", closing the gap the engine's unattendedAskHint already
// promised the agent (engine.go's GateFull check) but the TUI did not
// keep before this change.
func TestAutopilotFullAutoAnswersOwnQuestion(t *testing.T) {
	m, eng := agentWorkspace(t, askingFake())
	ctx := context.Background()
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateFull); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m) // attach; kickoff triggers the ask
	waitAsk(t, eng)
	// the auto-answer must not depend on what is on screen: prove it
	// fires with the card page closed, not because the pinned decision
	// happened to already be showing it.
	m.cardOpen = false

	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("autopilot did not answer the live ask")
		default:
			m = drainEngineLoop(t, m)
		}
	}

	if hasInboxKind(m, attnQuestion) {
		t.Error("a question item was parked despite full answering it itself")
	}

	answers := askEventsFor(t, m, "FD-001")
	if len(answers) == 0 {
		t.Fatal("no ask event recorded")
	}
	last := answers[len(answers)-1]
	if last.By != state.ActorAutopilot {
		t.Errorf("answer by = %q, want %q", last.By, state.ActorAutopilot)
	}
	// askingFake's options carry no "(recommended)" label, so the
	// recommended option is the first one offered.
	if last.Answer != "per-device" {
		t.Errorf("answer = %q, want the recommended (first) option %q", last.Answer, "per-device")
	}
}

// --- item 3: gates never answers its own question ---

// TestAutopilotGatesNeverAnswersOwnQuestion: gates crosses design gates
// on its own but "questions still stop for you" (autopilotStops' own
// words) — a live ask must still reach the inbox exactly as it does with
// no autopilot at all.
func TestAutopilotGatesNeverAnswersOwnQuestion(t *testing.T) {
	m, eng := agentWorkspace(t, askingFake())
	ctx := context.Background()
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateGates); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m)
	waitAsk(t, eng)
	m.cardOpen = false

	m = drainEngineLoop(t, m)

	if eng.Get("FD-001").Snapshot().PendingAsk == nil {
		t.Fatal("gates mode answered the question itself — it must still stop for it")
	}
	if !hasInboxKind(m, attnQuestion) {
		t.Error("gates mode's unanswered question did not reach the inbox")
	}
}

// --- item 4: gates crosses its own design gate ---

// TestAutopilotGatesCrossesCleanPlanCritique: a card stored at gates,
// whose plan critique comes back clean, crosses the approval gate to
// Implement on its own — no attnGate item parks, and the gate event's
// actor is autopilot, not a human.
func TestAutopilotGatesCrossesCleanPlanCritique(t *testing.T) {
	var critiques atomic.Int32
	m, eng := chatWorkspace(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))
	ctx := context.Background()
	m = advanceTo(t, m, domain.StagePlan)
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateGates); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m) // run plan
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage == domain.StagePlan {
		t.Fatal("gates did not cross the clean plan critique gate: still at plan")
	}
	gates := gateEventsFor(t, m, "FD-001", domain.StagePlan)
	if len(gates) == 0 {
		t.Fatal("no gate event recorded for the plan→implement crossing")
	}
	if last := gates[len(gates)-1]; last.Actor != state.ActorAutopilot {
		t.Errorf("gate actor = %q, want %q", last.Actor, state.ActorAutopilot)
	}

	// crossing is only half of what gates promises: the stage behind the
	// gate is autopilot's to start, so the card keeps moving rather than
	// stopping one stage further along. Where it comes to rest is the
	// verify gate — landing on main stays a keypress under every mode
	// (DESIGN §10.17) — so that is the one gate that may park.
	if m.rows[0].F.Stage != domain.StageVerify {
		t.Errorf("autopilot stopped at %s, want it to run on to the verify gate", m.rows[0].F.Stage)
	}
	// plan and implement are the two gates on that road autopilot itself
	// crosses. review→verify is not one of them: a clean review has
	// always auto-continued through the loop's own autoStep (actor
	// "review"), gate mode or not, so it leaves no autopilot-crossed gate
	// event and must not be asserted as one.
	for _, stage := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
		crossed := gateEventsFor(t, m, "FD-001", stage)
		if len(crossed) == 0 {
			t.Errorf("%s was never crossed on the way to verify", stage)
			continue
		}
		if last := crossed[len(crossed)-1]; last.Actor != state.ActorAutopilot {
			t.Errorf("%s gate actor = %q, want %q", stage, last.Actor, state.ActorAutopilot)
		}
	}
}

// --- item 5: off still parks exactly as today ---

// TestAutopilotOffParksCleanPlanCritique: a card stored at off never
// crosses a gate on its own — every gate stops for you, unconditionally.
func TestAutopilotOffParksCleanPlanCritique(t *testing.T) {
	var critiques atomic.Int32
	m, eng := chatWorkspace(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))
	ctx := context.Background()
	m = advanceTo(t, m, domain.StagePlan)
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateOff); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("off crossed the gate on its own: at %s", m.rows[0].F.Stage)
	}
	if !hasInboxKind(m, attnGate) {
		t.Error("off's clean critique did not park the approval gate")
	}
}

// --- item 6: a crossed gate closes its own decision row ---

// TestAutopilotCrossedGateClosesItsDecisionRow: crossing a gate through
// autopilotCrossGate opens the decision row (§10.18) before Advance runs
// and Store.Transition's own correlation closes it on a successful
// crossing — nothing should be left open for the card once the crossing
// lands.
func TestAutopilotCrossedGateClosesItsDecisionRow(t *testing.T) {
	var critiques atomic.Int32
	m, eng := chatWorkspace(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))
	ctx := context.Background()
	m = advanceTo(t, m, domain.StagePlan)
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateFull); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage == domain.StagePlan {
		t.Fatal("full did not cross the clean plan critique gate: still at plan")
	}

	// Every gate autopilot crossed on the way must have closed its own
	// row. The card runs on to the verify gate, which parks and is
	// legitimately open, so this asserts against the stages left behind
	// rather than against an empty map — an open decision at a stage the
	// card has moved past is precisely what OpenDecisions must not report.
	opens, err := m.store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range opens["FD-001"] {
		if d.Stage != m.rows[0].F.Stage {
			t.Errorf("a crossed gate left an open decision behind at %s: %+v", d.Stage, d)
		}
	}
}

// --- item 7: a blocked gate parks instead of crossing, even on full ---

// TestAutopilotBlockedGateParksEvenOnFull: a card whose forward edge
// enters its coding stage while a direct dependency is still unmet must
// park — autopilot cannot resolve an unmet dependency, so it may not
// cross a gate a human could not, no matter the mode.
func TestAutopilotBlockedGateParksEvenOnFull(t *testing.T) {
	var critiques atomic.Int32
	m, eng := chatWorkspace(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))
	ctx := context.Background()

	dep := domain.Feature{ID: "FD-002", Num: 2, Title: "dep", Slug: "dep", Stage: domain.StageTodo}
	if err := m.store.CreateFeature(ctx, &dep); err != nil {
		t.Fatal(err)
	}
	if err := m.store.AddDependency(ctx, "FD-001", "FD-002"); err != nil {
		t.Fatal(err)
	}

	m = advanceTo(t, m, domain.StagePlan)
	if err := m.store.SetGateApproval(ctx, "FD-001", domain.GateFull); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("full crossed a gate blocked by an unmet dependency: at %s", m.rows[0].F.Stage)
	}
	if !hasInboxKind(m, attnGate) {
		t.Error("the blocked gate did not park to the inbox")
	}
}

// --- the review pass's findings, held down ---

// TestAutopilotMarkClearsOnAnInteractiveCrossing: crossing onto an
// interactive stage returns a plain notice rather than the continue
// message, and the answering mark used to be cleared only by the
// messages that happened to be enumerated. The card then advertised
// "autopilot is taking this one" over a decision autopilot had already
// finished with, for the rest of the session.
func TestAutopilotMarkClearsOnAnInteractiveCrossing(t *testing.T) {
	m := populatedShell(100, 30)
	id := m.rows[m.sel].F.ID
	m.markAutopilotAnswering(id)
	if !m.autopilotAnswering[id] {
		t.Fatal("fixture did not mark the card")
	}
	// whatever the wrapped command came back as — here the plain notice a
	// crossing onto an interactive stage produces
	model, _ := m.update(autopilotSettledMsg{
		id:    id,
		inner: noticeMsg{text: string(id) + " → shape", reload: true},
	})
	m = model.(*Shell)
	if m.autopilotAnswering[id] {
		t.Error("the answering mark survived a crossing that ended in a plain notice")
	}
}

// TestAutopilotSettledStillHandlesItsInnerMessage: the wrapper must not
// swallow what it wraps — the notice, the reload and the inbox clear all
// still have to land.
func TestAutopilotSettledStillHandlesItsInnerMessage(t *testing.T) {
	m := populatedShell(100, 30)
	id := m.rows[m.sel].F.ID
	m.inbox.add(id, attnGate, "implement finished — review & advance")
	m.markAutopilotAnswering(id)

	model, _ := m.update(autopilotSettledMsg{
		id:    id,
		inner: noticeMsg{text: string(id) + " → review", reload: true, clearInbox: id},
	})
	m = model.(*Shell)
	if m.notice.text != string(id)+" → review" {
		t.Errorf("inner notice lost: %q", m.notice.text)
	}
	if _, ok := m.inbox.get(id); ok {
		t.Error("the inner message's inbox clear did not run")
	}
}

// TestAutopilotAnswerFailureParksTheQuestion: when the answer never
// reaches the agent — the session swapped out from under the command —
// the agent is still blocked on its question. Without a queue entry
// nothing on screen points at it.
func TestAutopilotAnswerFailureParksTheQuestion(t *testing.T) {
	m := populatedShell(100, 30)
	id := m.rows[m.sel].F.ID
	m.markAutopilotAnswering(id)

	model, _ := m.update(autopilotAnsweredMsg{
		id:     id,
		notice: noticeMsg{text: "no session for " + string(id), isErr: true},
		park:   "asks: persist where?",
	})
	m = model.(*Shell)
	if m.autopilotAnswering[id] {
		t.Error("the answering mark survived a failed answer")
	}
	it, ok := m.inbox.get(id)
	if !ok {
		t.Fatal("a failed auto-answer left the blocked question out of the queue")
	}
	if it.Kind != attnQuestion {
		t.Errorf("parked kind = %s, want question", it.Kind)
	}
	if it.Text != "asks: persist where?" {
		t.Errorf("parked text = %q, want the question", it.Text)
	}
}

// TestAutopilotAdvanceErrorParksTheCard: a crossing that fails outright
// (a store write, a worktree error) is not a blocked status, and the
// raise site skipped its own park on the strength of the attempt. An
// error that only became a notice left the card with an open decision
// row and nothing in the queue.
func TestAutopilotAdvanceErrorParksTheCard(t *testing.T) {
	m := populatedShell(100, 30)
	id := m.rows[m.sel].F.ID

	got := blockedMsg(state.ActorAutopilot, id, "boom")
	blocked, ok := got.(autopilotGateBlockedMsg)
	if !ok {
		t.Fatalf("autopilot's error routed to %T, want a park", got)
	}
	model, _ := m.update(blocked)
	m = model.(*Shell)
	if _, ok := m.inbox.get(id); !ok {
		t.Error("a failed autopilot crossing left the card out of the queue")
	}

	// a human's own advance keeps the plain error notice it always had
	if _, ok := blockedMsg("user", id, "boom").(noticeMsg); !ok {
		t.Error("a human's advance error stopped being a plain notice")
	}
}

// TestHandoverAtADesignGateCrossesAndStarts is the answer to "why is it
// just a setting": picking "let autopilot finish" at a spec gate has to
// cross that gate and start what is behind it. The switch used to read
// such a card as already underway — a design stage leaves no inbox item,
// which was the only thing its gate detection looked at — so it wrote
// the mode, moved nothing, and said so in a dialog sitting under a row
// promising that gates cross themselves from here.
func TestHandoverAtADesignGateCrossesAndStarts(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("the approach is a token swap."))
	m = advanceTo(t, m, domain.StageSpec)
	m = pump(t, m, m.loadRows)

	// attach the architect and let it finish a turn: the state the thread
	// renders a spec gate in, with nothing in the inbox
	m = openAndAttach(t, m)
	settleChat(t, eng)
	if hasInboxKind(m, attnGate) {
		t.Fatal("fixture parked an inbox gate; this test is about the case with none")
	}

	f := m.rows[0].F
	plan := m.planAutopilot(f)
	if plan.bucket != "gate" {
		t.Fatalf("a finished spec conversation read as %q, want a gate to cross", plan.bucket)
	}
	if plan.to != domain.StagePlan {
		t.Errorf("plan.to = %s, want plan", plan.to)
	}

	msg := m.startAutopilot(f, domain.GateGates, plan)()
	if nm, ok := msg.(noticeMsg); ok && nm.isErr {
		t.Fatalf("handover failed: %s", nm.text)
	}
	model, next := m.update(msg)
	m = model.(*Shell)
	m = pump(t, m, next)

	got, err := m.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Errorf("gate approval = %q, want gates", got.GateApproval)
	}
	if got.Stage == domain.StageSpec {
		t.Fatal("the handover wrote the mode but never crossed the gate")
	}
	if eng.Get(f.ID) == nil {
		t.Error("the handover crossed the gate but started nothing behind it")
	}
}

// TestHandoverNeverWalksPastUnstartedWork: a stage that has not run is
// not a gate. Handing over there means running it, and crossing would
// carry the card into review over an implement stage that never
// happened.
func TestHandoverNeverWalksPastUnstartedWork(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "unstarted", Slug: "unstarted", Stage: domain.StageImplement}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if plan := m.planAutopilot(f); plan.bucket == "gate" {
		t.Errorf("an implement stage that never ran read as a gate to cross: %+v", plan)
	}
}

// TestHandoverRefusesTheLandingGate: verify keeps its own rule under
// every mode — landing on main stays a keypress, so handing over there
// crosses nothing.
func TestHandoverRefusesTheLandingGate(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StageVerify, domain.StageReview} {
		if _, ok := autopilotHandoverEdge(domain.Feature{Stage: stage}); ok {
			t.Errorf("the handover offered to cross %s on its own", stage)
		}
	}
}
