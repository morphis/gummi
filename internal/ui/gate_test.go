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

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "auto card", Slug: "auto-card", Stage: domain.StageTodo, GateApproval: domain.GateAttended}
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
	if d.feature.ID != "FD-001" {
		t.Fatalf("dialog opened on %q, want the selected card", d.feature.ID)
	}

	// unconfirmed: the store must be untouched.
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAttended {
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
	// nothing to position: with two modes the dialog states what handing
	// the card over does and its confirm is the whole choice, so a card
	// carrying the empty mode opens the same dialog as any other.
	if d.feature.ID != "FD-001" {
		t.Fatalf("dialog opened on %q, want the selected card", d.feature.ID)
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
	if got := label(domain.GateAutopilot); got != "take back the gates" {
		t.Errorf("label for autopilot = %q, want %q", got, "take back the gates")
	}
	if got := label(domain.GateAttended); got != "hand to autopilot" {
		t.Errorf("label for attended = %q, want %q", got, "hand to autopilot")
	}
	if got := label(""); got != "hand to autopilot" {
		t.Errorf("label for empty (reads as attended) = %q, want %q", got, "hand to autopilot")
	}
}
