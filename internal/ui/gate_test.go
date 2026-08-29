package ui

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestGateToggleTighteningAppliesImmediately: auto -> caller is the safer
// direction (a human checkpoints every design gate from here on), so it
// must not stop to ask — the same way the board never confirms a plain
// pause.
func TestGateToggleTighteningAppliesImmediately(t *testing.T) {
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

	cmd := m.runCardAction(cardAction{id: "gate"})
	if m.Overlay.HasDialogs() {
		t.Fatal("tightening (auto -> caller) should not raise a confirm dialog")
	}
	if cmd == nil {
		t.Fatal("expected a command that writes the new mode")
	}
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("gate toggle failed: %s", nm.text)
		}
	}
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateOff {
		t.Fatalf("gate approval = %q, want %q", got.GateApproval, domain.GateOff)
	}
}

// TestGateToggleLooseningConfirmsFirst: caller -> auto removes the human
// checkpoint at every design gate this card crosses from here on, so it
// must confirm first — and must not write anything until that confirm
// fires, the same contract confirmDuplicate and the board's delete/
// clean-up confirms already hold.
func TestGateToggleLooseningConfirmsFirst(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "caller card", Slug: "caller-card", Stage: domain.StageTodo, GateApproval: domain.GateOff}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{{F: f}}
	m.sel = 0

	if cmd := m.runCardAction(cardAction{id: "gate"}); cmd != nil {
		t.Fatal("loosening should raise a confirm rather than return a command directly")
	}
	if !m.Overlay.Contains("confirm-gate-auto") {
		t.Fatal("loosening (caller -> auto) did not open its confirm dialog")
	}

	// unconfirmed: the store must be untouched.
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateOff {
		t.Fatalf("gate approval changed before confirm: %q", got.GateApproval)
	}

	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok {
		t.Fatalf("top overlay is %T, want *confirmDialog", m.Overlay.Top())
	}
	if cmd := d.onConfirm(); cmd != nil {
		if msg := cmd(); msg != nil {
			if nm, ok := msg.(noticeMsg); ok && nm.isErr {
				t.Fatalf("gate toggle failed: %s", nm.text)
			}
		}
	}
	got, err = store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Fatalf("gate approval after confirm = %q, want %q", got.GateApproval, domain.GateGates)
	}
}

// TestGateToggleLooseningFromEmptyConfirmsToo: an empty GateApproval
// reads as auto already, but the toggle's two destinations are only
// caller and auto — starting from empty, the toggle still lands on auto
// (making it explicit) and still goes through the confirm, exactly as
// described for "caller/empty -> auto".
func TestGateToggleLooseningFromEmptyConfirmsToo(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "empty card", Slug: "empty-card", Stage: domain.StageTodo}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{{F: f}}
	m.sel = 0

	if cmd := m.runCardAction(cardAction{id: "gate"}); cmd != nil {
		t.Fatal("loosening from empty should raise a confirm rather than return a command directly")
	}
	if !m.Overlay.Contains("confirm-gate-auto") {
		t.Fatal("loosening from empty did not open its confirm dialog")
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
