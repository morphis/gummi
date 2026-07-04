package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
)

func TestInboxDialogTopUpOnlyBudgetItems(t *testing.T) {
	items := []attnItem{
		{Feature: "FD-001", Kind: attnBudget, Text: "verify hit its budget"},
		{Feature: "FD-002", Kind: attnGate, Text: "review & advance"},
	}
	var toppedUp domain.FeatureID
	d := newInboxDialog(items,
		func(domain.FeatureID) tea.Cmd { return nil },
		func(domain.FeatureID) {},
		func(id domain.FeatureID) tea.Cmd { toppedUp = id; return nil },
	)

	// 'u' on the budget item invokes top-up and closes the dialog.
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if !closed || toppedUp != "FD-001" {
		t.Fatalf("top-up on budget item: closed=%v toppedUp=%q, want true/FD-001", closed, toppedUp)
	}

	// 'u' on a non-budget gate item does nothing.
	toppedUp = ""
	d2 := newInboxDialog(items,
		func(domain.FeatureID) tea.Cmd { return nil },
		func(domain.FeatureID) {},
		func(id domain.FeatureID) tea.Cmd { toppedUp = id; return nil },
	)
	d2.sel = 1 // the attnGate item
	closed, _ = d2.HandleKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if closed || toppedUp != "" {
		t.Fatalf("top-up on non-budget item acted: closed=%v toppedUp=%q, want false/empty", closed, toppedUp)
	}
}
