package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestDesignKickoffDoesNotInventAReader locks the opener against the mode.
// It is the first thing a session reads, and on an autopilot card it used
// to open "The user just opened the design chat … put the most
// consequential open question to the user first" — to a card nobody
// opened and nobody will read. On the lxd autopilot drive all four cards
// duly asked, and all four were bounced by the ask toll: a tool call and
// a model turn each, spent to be told not to ask.
func TestDesignKickoffDoesNotInventAReader(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		f := feature(1, "Dark mode", domain.StagePlan)
		f.Kind = kind

		f.GateApproval = domain.GateAttended
		attended := designKickoff(f)
		if !strings.Contains(attended, "user") {
			t.Errorf("%s attended: the opener stopped addressing the person who is there:\n%s",
				kind, attended)
		}

		f.GateApproval = domain.GateAutopilot
		auto := designKickoff(f)
		for _, claim := range []string{"The user just opened", "to the user first"} {
			if strings.Contains(auto, claim) {
				t.Errorf("%s autopilot: opener still says %q to a card with no reader:\n%s",
					kind, claim, auto)
			}
		}
		if !strings.Contains(auto, "unattended") {
			t.Errorf("%s autopilot: opener does not say the card is unattended:\n%s", kind, auto)
		}
		// Same job either way: the stage still has to converge and write.
		if len(auto) < 120 {
			t.Errorf("%s autopilot: opener lost its instructions:\n%s", kind, auto)
		}
	}
}
