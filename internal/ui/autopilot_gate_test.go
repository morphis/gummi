package ui

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
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
