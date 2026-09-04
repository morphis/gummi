package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG097ClosedPeriodSaysItEndedInTheLiveStage is BG-097's regression
// test. An autopilot period almost always ends in the stage the card is
// now sitting in — that is what parking means — and that stage is the
// one the thread draws from the session's own snapshot rather than from
// the event log. The snapshot branch drew the rule that opens a period
// and none of the rules that close one, on the premise that a card with
// a session is a card still being driven. A session object outlives the
// run that filled it: the engine keeps a finished one, and a restart
// restores it. So the newest thing the page said about an unattended run
// was that it had started, on a card that had been parked for hours.
//
// The fixture is the shape the drive found and then reproduced twice: a
// period opened in an earlier stage, so the folded loop draws its
// opening rule, and closed in the last one, where only the live block
// can. The session is restored from the store rather than started, which
// is exactly the post-restart state — the one the reader who was away
// comes back to.
func TestBG097ClosedPeriodSaysItEndedInTheLiveStage(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	at := time.Date(2026, 9, 4, 8, 7, 0, 0, time.UTC)

	f := mkFeature(t, store, 1, "profile applied across projects", domain.StageVerify)

	enter := func(role string) string {
		b, _ := json.Marshal(map[string]string{"role": role, "model": "demo", "flavor": "stage"})
		return string(b)
	}
	says := func(text string) string {
		b, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": text})
		return string(b)
	}
	exit := func() string {
		b, _ := json.Marshal(map[string]any{"credits": 18, "verdict": ""})
		return string(b)
	}
	took, _ := json.Marshal(state.AutopilotPayload{
		Event: state.AutopilotTookOver, Reason: "you handed it to autopilot", Mode: domain.GateFull,
	})
	crossed, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageImplement), To: string(domain.StageVerify), Actor: state.ActorAutopilot,
	})
	parked, _ := json.Marshal(state.ParkPayload{
		Reason: "needs-you", Detail: "verify passed — land it when you are ready",
	})

	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: enter("implementer"), Dedupe: "impl:enter"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventAutopilot, At: at.Add(time.Second), Payload: string(took), Dedupe: "ap:took"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventMessage, At: at.Add(2 * time.Second), Payload: says("wrote the change"), Dedupe: "impl:said"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventStageExit, At: at.Add(3 * time.Second), Payload: exit(), Dedupe: "impl:exit"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventGate, At: at.Add(4 * time.Second), Payload: string(crossed), Dedupe: "ap:gate"},
		{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventStageEnter, At: at.Add(5 * time.Second), Payload: enter("reviewer"), Dedupe: "verify:enter"},
		{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventMessage, At: at.Add(6 * time.Second), Payload: says("VERDICT: pass"), Dedupe: "verify:said"},
		{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventStageExit, At: at.Add(7 * time.Second), Payload: exit(), Dedupe: "verify:exit"},
		{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventPark, At: at.Add(8 * time.Second), Payload: string(parked), Dedupe: "verify:park"},
	}); err != nil {
		t.Fatal(err)
	}

	// the session the run left behind, restored the way a restarted board
	// restores it — done, holding the verify transcript, driving nothing.
	if err := store.SaveSession(ctx, state.SessionSnapshot{
		Feature: f.ID, Stage: domain.StageVerify, Role: "reviewer", State: "done",
	}); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt, Workspace: ws, Model: "demo", Persist: true,
	})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = sized.(*Shell)
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true
	m = pump(t, m, m.loadCardEvents(f.ID))

	w, h := m.threadSize()
	body := ansi.Strip(m.threadView(w, h))

	if m.sessionFor(f.ID) == nil {
		t.Fatalf("precondition: no restored session, so the branch under test never runs\n%s", body)
	}
	open := strings.Index(body, "autopilot took over")
	if open < 0 {
		t.Fatalf("precondition: the period never opens on the page\n%s", body)
	}
	// "autopilot finished" is the wording a period earns by reaching the
	// landing gate with a verdict that is not a failure (stretchLabel).
	// Asserting the wording and not merely the presence of a rule is the
	// point: a close that says the wrong thing is its own defect.
	closed := strings.Index(body, "autopilot finished")
	if closed < 0 {
		t.Fatalf("a parked card's thread never says autopilot stopped — the period opens and never ends\n%s", body)
	}
	if closed < open {
		t.Fatalf("the closing rule is drawn above the opening rule\n%s", body)
	}
}
