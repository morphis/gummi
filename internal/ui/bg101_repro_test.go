package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG101NoTakeoverWithNothingRunning is BG-101's regression test.
// Pointing the switch at a card whose run is paused recorded that the
// card had been handed to the machine. Nothing started, so nothing ever
// closed that handover, and the thread drew a run that opened and
// immediately reported itself lost — the wording kept for a process that
// died mid-run — over a card that had simply been sitting there.
//
// The dialog set it up, calling the same paused card "already underway"
// while the card page one keystroke away said nothing was running. Both
// came from reading "this card has a session" as "a machine is working
// on this card": the engine keeps a session after its run ends, and a
// restart restores one for a card that has been waiting for hours.
//
// The fixture is that restored session, since it is the state a reader
// actually meets, and both halves are asserted from it: the log gains no
// handover, and the dialog does not claim the card is underway.
func TestBG101NoTakeoverWithNothingRunning(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	f := mkFeature(t, store, 1, "storage volume snapshot expiry", domain.StagePlan)

	if err := store.SaveSession(ctx, state.SessionSnapshot{
		Feature: f.ID, Stage: domain.StagePlan, Role: "architect", State: "paused",
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
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)

	if m.sessionFor(f.ID) == nil {
		t.Fatal("precondition: no restored session, so the confusion under test cannot arise")
	}
	if m.sessionWorking(f.ID) {
		t.Fatal("precondition: the restored session reports itself as working")
	}

	plan := m.planAutopilot(f)
	if plan.bucket != "running" {
		t.Fatalf("bucket = %q, want the switch's catch-all", plan.bucket)
	}
	if head := autopilotHeader(f, plan); strings.Contains(head, "already underway") {
		t.Errorf("the dialog calls a card with nothing running underway: %q", head)
	}
	body := strings.Join(autopilotBody(f, plan, domain.GateFull), " ")
	if strings.Contains(body, "what "+string(f.ID)+" is doing right now") {
		t.Errorf("the dialog describes work in progress on a card doing nothing: %q", body)
	}

	if msg := m.startAutopilot(f, domain.GateFull, plan)(); msg == nil {
		t.Fatal("the switch returned nothing")
	}
	events, err := m.store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind != state.EventAutopilot {
			continue
		}
		// SetGateApproval writes a mode-change row of its own with an
		// empty Event; only a row claiming a boundary is the defect.
		var p state.AutopilotPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.Event == state.AutopilotTookOver {
			t.Fatalf("a card with nothing running records a handover to autopilot: %s", ev.Payload)
		}
	}

	// and the page draws no period at all, which is the half a log-level
	// assertion cannot see
	stretches := liveStretches(f, events, ws)
	if len(stretches) != 0 {
		t.Fatalf("the thread would draw %d period(s) on a card that sat still: %+v", len(stretches), stretches)
	}
}
