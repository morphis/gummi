package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// TestCreationFormsLabelTheEnvelopeField: the envelope's unit and its
// `0 = uncapped` affordance used to live only in the input's Placeholder,
// which renders only while the field is empty — and the field is
// prefilled every time the dialog opens. The door reads the value back
// with its unit on the collapsed "runs as" line, and labels the input
// when the options are expanded; research, which refuses 0, says
// "required" there instead.
func TestCreationFormsLabelTheEnvelopeField(t *testing.T) {
	s := m0Styles()
	profiles := []string{"thrifty"}
	for _, c := range []struct {
		kind domain.Kind
		want string
	}{
		{domain.KindFeature, envelopeHintCapped},
		{domain.KindBug, envelopeHintCapped},
		{domain.KindResearch, envelopeHintRequired},
	} {
		t.Run(string(c.kind), func(t *testing.T) {
			form := newCardForm(c.kind, profiles, nil, true, "", nil, 2400, nil)
			collapsed := ansi.Strip(form.View(s, 100, 30))
			if !strings.Contains(collapsed, "2400 credits") {
				t.Errorf("the collapsed readout does not carry the envelope with its unit:\n%s", collapsed)
			}
			form.HandleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
			expanded := ansi.Strip(form.View(s, 100, 30))
			if !strings.Contains(expanded, "2400") || !strings.Contains(expanded, c.want) {
				t.Errorf("the expanded envelope field carries no label %q:\n%s", c.want, expanded)
			}
		})
	}
}
