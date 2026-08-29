package ui

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestGateActionOpensAutopilotOverlay: the "gate" card action (its label/
// why still come from cardActionsFor's gateLabelWhy) is superseded by the
// autopilot overlay rather than writing the store directly — it must
// raise the overlay, pre-selected on the card's current mode, and must
// not touch the store until the overlay's own confirm fires. That confirm
// is the one deliberation a loosening move gets; there is no second
// confirm layered underneath it.
func TestGateActionOpensAutopilotOverlay(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "auto card", Slug: "auto-card", Stage: domain.StageTodo, GateApproval: domain.GateGates}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{{F: f}}
	m.sel = 0

	if cmd := m.runCardAction(cardAction{id: "gate"}); cmd != nil {
		t.Fatal("the gate action should open the overlay, not return a command directly")
	}
	if m.Overlay.Contains("confirm-gate-auto") {
		t.Fatal("the old confirm-gate-auto dialog should never be raised any more")
	}
	d, ok := m.Overlay.Top().(*autopilotDialog)
	if !ok {
		t.Fatalf("top overlay is %T, want *autopilotDialog", m.Overlay.Top())
	}
	if d.cursor != autopilotCursorFor(domain.GateGates) {
		t.Fatalf("cursor = %d, want the card's current mode (%d)", d.cursor, autopilotCursorFor(domain.GateGates))
	}

	// unconfirmed: the store must be untouched.
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Fatalf("gate approval changed before the overlay was confirmed: %q", got.GateApproval)
	}
}

// TestGateActionOverlayCursorReadsEmptyAsGates: an empty GateApproval
// reads as gates everywhere else in the code; the overlay's starting
// cursor must agree.
func TestGateActionOverlayCursorReadsEmptyAsGates(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "empty card", Slug: "empty-card", Stage: domain.StageTodo}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{{F: f}}
	m.sel = 0

	m.runCardAction(cardAction{id: "gate"})
	d, ok := m.Overlay.Top().(*autopilotDialog)
	if !ok {
		t.Fatalf("top overlay is %T, want *autopilotDialog", m.Overlay.Top())
	}
	if want := autopilotCursorFor(domain.GateGates); d.cursor != want {
		t.Fatalf("cursor for empty mode = %d, want %d (gates)", d.cursor, want)
	}
}

// TestGateActionLabelReflectsCurrentMode: the action's label always
// names what pressing it will do, the same convention run/pause already
// use.
func TestGateActionLabelReflectsCurrentMode(t *testing.T) {
	label := func(mode string) string {
		r := featureRow{F: domain.Feature{Kind: domain.KindFeature, Stage: domain.StageTodo, GateApproval: mode}}
		in := nextInput{stage: domain.StageTodo, kind: domain.KindFeature}
		for _, a := range cardActionsFor(in, r) {
			if a.id == "gate" {
				return a.label
			}
		}
		t.Fatalf("no gate action found for mode %q", mode)
		return ""
	}
	if got := label(domain.GateGates); got != "require approval" {
		t.Errorf("label for explicit auto = %q, want %q", got, "require approval")
	}
	if got := label(domain.GateOff); got != "auto-approve gates" {
		t.Errorf("label for caller = %q, want %q", got, "auto-approve gates")
	}
	if got := label(""); got != "auto-approve gates" {
		t.Errorf("label for empty = %q, want %q", got, "auto-approve gates")
	}
}
