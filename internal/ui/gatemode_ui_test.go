package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
)

// attendedModes are the two stored GateApproval values that both mean
// attended. The empty one is the point: it is storable
// (domain.ValidGateApproval), it is what every card `bugs new` mints
// carries, and domain.Feature.GateMode resolves it to attended — so every
// site that branches on the mode has to treat it exactly like the spelled
// value. A bare `== domain.GateAttended` does not, and read the whole
// unset half of the board as autopilot.
var attendedModes = []struct {
	stored string
	name   string
}{
	{domain.GateAttended, "attended"},
	{"", "unset"},
}

// TestEnvelopeResumeQuestionSkipsAttendedModes: the envelope dialog's
// "resume it?" follow-up belongs to autopilot cards. A card with an unset
// mode is not one, and answering yes there restarts a run nobody handed
// over.
func TestEnvelopeResumeQuestionSkipsAttendedModes(t *testing.T) {
	for _, mode := range attendedModes {
		t.Run(mode.name, func(t *testing.T) {
			f := parkedAutopilotFeature()
			f.GateApproval = mode.stored
			d := newEnvelopeDialog(f, func(int) tea.Cmd { return nil }, nil)
			typeInto(d, "4000")
			d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if d.askResume {
				t.Fatalf("askResume = true on a %s card, want false", mode.name)
			}
		})
	}
}

// TestChipRewindLineTreatsUnsetModeAsAttended: the rewind chip's
// autopilot line decides whether the reader is promised a stop at the
// design gate. On an attended card — stored either way — it must promise
// it.
func TestChipRewindLineTreatsUnsetModeAsAttended(t *testing.T) {
	out := reentry.Decide(reentry.Input{
		Stage: domain.StageVerify, Kind: domain.KindBug,
		Intent: reentry.RequirementMissing, Note: "x",
	})
	if out.Action != reentry.Rewind {
		t.Fatalf("fixture no longer produces a rewind: %+v", out)
	}
	p := &reentryReading{line: "x", out: out, goOnEnter: goOnEnter(out)}
	for _, mode := range attendedModes {
		t.Run(mode.name, func(t *testing.T) {
			r := featureRow{F: domain.Feature{
				ID: "BG-001", Kind: domain.KindBug, Stage: domain.StageVerify,
				GateApproval: mode.stored,
			}}
			body := strings.Join(chipDetails(r, p), " ")
			if !strings.Contains(body, "stops for you") {
				t.Errorf("a %s card's chip does not promise the stop:\n%s", mode.name, body)
			}
			if strings.Contains(body, "Autopilot is on") {
				t.Errorf("a %s card's chip claims autopilot is on:\n%s", mode.name, body)
			}
		})
	}
}

// TestUnsetModeIsNotRunningOnAutopilot covers the two live-state readers
// together, because they are the two places the misclassification was
// visible at once: the quit dialog listed a card as "running on
// autopilot" while that same card's masthead read "autopilot: off ·
// running". Neither may say autopilot holds a card whose mode is unset.
func TestUnsetModeIsNotRunningOnAutopilot(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageImplement)
	// the value every card `bugs new` mints stores, stated so the fixture
	// cannot drift if the default ever changes
	m.rows[0].F.GateApproval = ""
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	auto, plain := m.liveAutopilotSplit()
	if len(auto) != 0 {
		t.Errorf("the quit dialog lists an unset-mode card as running on autopilot: %v", auto)
	}
	if len(plain) != 1 || !strings.HasPrefix(plain[0], "FD-001") {
		t.Errorf("plain list = %v, want the card named with its stage", plain)
	}

	got := ansi.Strip(autopilotField(m.styles, m, m.rows[0].F))
	if got != "autopilot: off" {
		t.Errorf("masthead autopilot cell = %q, want a bare %q — autopilot holds nothing here",
			got, "autopilot: off")
	}
}

// TestAutopilotFieldStillReportsRealAutopilotWork is the other half: the
// GateMode read must not have turned the live suffix off for the cards it
// is actually about.
func TestAutopilotFieldStillReportsRealAutopilotWork(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageImplement)
	m.rows[0].F.GateApproval = domain.GateAutopilot
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	got := ansi.Strip(autopilotField(m.styles, m, m.rows[0].F))
	if !strings.HasPrefix(got, "autopilot: on") || !strings.Contains(got, "·") {
		t.Errorf("masthead autopilot cell = %q, want the live state of a card that IS on autopilot", got)
	}
	auto, _ := m.liveAutopilotSplit()
	if len(auto) != 1 {
		t.Errorf("autopilot list = %v, want the one card on autopilot", auto)
	}
}
