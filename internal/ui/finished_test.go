package ui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

func exitEvent(stage domain.Stage, verdict string, at time.Time) state.CardEvent {
	payload, _ := json.Marshal(map[string]any{"verdict": verdict, "credits": 1})
	return state.CardEvent{Feature: "FD-001", Stage: stage, Kind: state.EventStageExit, At: at, Payload: string(payload)}
}

// stageExited is the log's answer to "did this stage already finish": the
// newest exit for the stage, and only if it belongs to the stage's
// current generation — an exit written before the card last entered the
// stage is a different visit.
func TestStageExitedReadsTheLog(t *testing.T) {
	t0 := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	entered := []state.TransitionRecord{{From: domain.StageImplement, To: domain.StageVerify, At: t0}}

	if _, ok := stageExited(nil, entered, domain.StageVerify); ok {
		t.Error("an empty log reads as exited")
	}
	if v, ok := stageExited([]state.CardEvent{exitEvent(domain.StageVerify, "pass", t0.Add(time.Minute))}, entered, domain.StageVerify); !ok || v != verdictPass {
		t.Errorf("a pass after entry = %v,%v", v, ok)
	}
	if v, ok := stageExited([]state.CardEvent{exitEvent(domain.StageVerify, "fail", t0.Add(time.Minute))}, entered, domain.StageVerify); !ok || v != verdictFail {
		t.Errorf("a fail after entry = %v,%v", v, ok)
	}
	// the generation check: an exit OLDER than the newest entry into the
	// stage belongs to a visit that was sent back and is over
	stale := []state.CardEvent{exitEvent(domain.StageVerify, "pass", t0.Add(-time.Hour))}
	if _, ok := stageExited(stale, entered, domain.StageVerify); ok {
		t.Error("an exit from a previous generation reads as this one's")
	}
	// another stage's exit is not this stage's
	if _, ok := stageExited([]state.CardEvent{exitEvent(domain.StageImplement, "pass", t0.Add(time.Minute))}, entered, domain.StageVerify); ok {
		t.Error("implement's exit reads as verify's")
	}
	// no verdict recorded is still an exit, just an unclear one
	if v, ok := stageExited([]state.CardEvent{exitEvent(domain.StageVerify, "", t0.Add(time.Minute))}, entered, domain.StageVerify); !ok || v != verdictUnclear {
		t.Errorf("an exit with no verdict = %v,%v", v, ok)
	}
}

// The defect this fixes: a card whose verify ran and passed, then lost
// its session to a restart, offered "run verify — no active run" and
// took a typed line as that run's kickoff note. With the exit read from
// the log it presents as finished, offers "send it back", and narrates
// the pass.
func TestRestartedVerifyStillPresentsAsFinished(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("ok"))
	m = advanceTo(t, m, domain.StageVerify)
	// the landing gate owes a drafted Verification plan; with it blank the
	// stop is a blocked gate, which is a different (correct) answer set
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)
	id := m.rows[0].F.ID

	// before: no session, no gate item, nothing in the log — "run verify"
	in := m.nextInputFor(m.rows[0])
	if in.finished() {
		t.Fatal("a verify that never ran presents as finished")
	}
	if got := stageActions(in); len(got) == 0 || got[0].id != "run" {
		t.Fatalf("a never-run verify offers %+v, want run first", got)
	}

	// the stage runs and exits with a pass, and then the process restarts:
	// the log keeps the exit, memory keeps nothing
	if err := m.store.AppendEvent(context.Background(), exitEvent(domain.StageVerify, "pass", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	if !m.rows[0].Exited || m.rows[0].ExitVerdict != verdictPass {
		t.Fatalf("row did not read the exit: exited=%v verdict=%v", m.rows[0].Exited, m.rows[0].ExitVerdict)
	}
	in = m.nextInputFor(m.rows[0])
	if !in.finished() || in.verdict != verdictPass {
		t.Fatalf("finished=%v verdict=%v after restart, want finished pass", in.finished(), in.verdict)
	}
	acts := stageActions(in)
	var sendBack bool
	for _, a := range acts {
		if a.sendBack {
			sendBack = true
		}
		if a.id == "run" {
			t.Errorf("a finished verify still offers a run row: %+v", a)
		}
	}
	if !sendBack {
		t.Errorf("a finished verify offers no send-it-back: %+v", acts)
	}
	if d := m.openDecision(m.rows[0]); d == nil || d.kind != decisionVerify {
		t.Errorf("decision kind = %+v, want the verify decision", d)
	}
	if narr := m.cardNarration(in, m.rows[0]); len(narr) == 0 || narr[0].text != "Verify passed — the work is ready. Decide how it leaves gummi." {
		t.Errorf("narration = %+v, want the pass", narr)
	}
	_ = id
	_ = tea.KeyPressMsg{}
}
