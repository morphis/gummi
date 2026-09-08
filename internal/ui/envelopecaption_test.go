package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestCreationFormsLabelTheEnvelopeField: the envelope input's unit and
// its `0 = uncapped` affordance were carried only by the input's
// Placeholder, which renders only while the field is empty — and the
// field is prefilled from envelopePrefill() every time the dialog opens.
// So the modal a first-time user met showed a bare "> 2400": no unit, and
// no sign that 0 meant anything but a rejected entry.
func TestCreationFormsLabelTheEnvelopeField(t *testing.T) {
	s := m0Styles()
	profiles := []string{"thrifty"}

	forms := []struct {
		name string
		view string
		want string
	}{
		{
			"feature",
			newFeatureForm(profiles, nil, true, 2400, nil).View(s, 100, 30),
			envelopeHintCapped,
		},
		{
			"bug",
			newBugForm(profiles, nil, true, 2400, nil).View(s, 100, 30),
			envelopeHintCapped,
		},
		{
			// research refuses 0 — an RS card carries no default budget —
			// so its caption must not offer uncapped
			"research",
			newRSForm(profiles, nil, true, 2400, nil).View(s, 100, 30),
			envelopeHintRequired,
		},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			out := ansi.Strip(f.view)
			if !strings.Contains(out, "2400") {
				t.Fatalf("the envelope field is not prefilled, so this test is not about the case that broke:\n%s", out)
			}
			if !strings.Contains(out, "envelope: "+f.want) {
				t.Errorf("the prefilled envelope field carries no label:\n%s", out)
			}
		})
	}
}
