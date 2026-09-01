package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/ui/theme"
)

// TestEnvelopePrefillWithoutGummiEnvelope: a board started without
// GUMMI_ENVELOPE opens its dialogs on a real budget. Before, the zero
// value made every dialog open at 0 (uncapped) unless the operator
// remembered the env var.
func TestEnvelopePrefillWithoutGummiEnvelope(t *testing.T) {
	m := NewShell(theme.GummiDark(), "test")
	if got := m.envelopePrefill(); got != DefaultEnvelopeCredits {
		t.Errorf("prefill without GUMMI_ENVELOPE = %d, want %d", got, DefaultEnvelopeCredits)
	}
	// GUMMI_ENVELOPE still wins: the default is only a fallback.
	m.SetEnvelope(777)
	if got := m.envelopePrefill(); got != 777 {
		t.Errorf("prefill with GUMMI_ENVELOPE=777 = %d, want 777", got)
	}
}

// TestPrefillLeavesEstimationModeIntact: the prefill must not masquerade
// as an operator-chosen envelope. m.envelope stays 0 without
// GUMMI_ENVELOPE, which is what puts spec approval into scribe-estimation
// mode and keeps the blend unfloored — a default written into that field
// would switch estimation off for every workspace that never set the
// variable.
func TestPrefillLeavesEstimationModeIntact(t *testing.T) {
	m := NewShell(theme.GummiDark(), "test")
	if m.envelope != 0 {
		t.Errorf("shell envelope = %d, want 0 (no operator envelope set)", m.envelope)
	}
}

// TestCreationDialogsPrefillDefaultEnvelope: the default reaches the
// field the user actually reads, in every creation dialog that budgets.
func TestCreationDialogsPrefillDefaultEnvelope(t *testing.T) {
	s := theme.New(theme.GummiDark())
	want := strconv.Itoa(DefaultEnvelopeCredits)
	views := map[string]string{
		"feature":  newFeatureForm(nil, nil, true, DefaultEnvelopeCredits, nil).View(s, 80, 24),
		"bug":      newBugForm(nil, nil, true, DefaultEnvelopeCredits, nil).View(s, 80, 24),
		"research": newRSForm(nil, nil, true, DefaultEnvelopeCredits, nil).View(s, 80, 24),
	}
	for name, view := range views {
		if !strings.Contains(view, want) {
			t.Errorf("%s dialog does not prefill %s credits:\n%s", name, want, view)
		}
	}
}
