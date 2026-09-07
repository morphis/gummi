package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
)

// clearPlanGate is a card parked at its design gate with the artifact's
// required sections drafted, the page open and the composer focused —
// the stop where "go on" is the approval.
func clearPlanGate(t *testing.T, intent string) *Shell {
	t.Helper()
	m, _ := chatWorkspace(t, classifyingAgent(intent))
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)
	return openCardPage(t, m)
}

func typeAndSend(t *testing.T, m *Shell, line string) *Shell {
	t.Helper()
	m = typeString(t, m, line)
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// "looks right, go" at a clear design gate is the approval — reached in
// words, confirmed on y and never on enter, because crossing starts the
// implement run and that spends.
func TestProceedAtAClearGateAdvancesOnY(t *testing.T) {
	m := clearPlanGate(t, "proceed")
	m = typeAndSend(t, m, "looks right, go")
	p := m.reentryPending
	if p == nil || p.out.Action != reentry.Advance || p.out.Target != domain.StageImplement {
		t.Fatalf("no advance chip: %+v", p)
	}
	if p.goOnEnter {
		t.Fatal("an advance into a run spends; it must not go on enter")
	}
	if got := strings.TrimSpace(m.threadInput.Value()); got != "looks right, go" {
		t.Errorf("the chip took the line out of the composer: %q", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.reentryPending == nil || m.rows[0].F.Stage != domain.StagePlan {
		t.Fatal("enter took a chip that spends")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = pump(t, m, m.loadRows)
	if got := m.rows[0].F.Stage; got != domain.StageImplement {
		t.Errorf("y did not cross the gate (at %s)", got)
	}
	if got := m.threadInput.Value(); got != "" {
		t.Errorf("the composer kept the line after the act: %q", got)
	}
}

// "go on" at a gate that is held shut is answered with the blocker, said
// at the moment it was asked for. Nothing moves, nothing is sent.
func TestProceedAtABlockedGateSaysWhy(t *testing.T) {
	m, _ := chatWorkspace(t, classifyingAgent("proceed"))
	// a card with NO artifact yet reads as unblocked (a missing artifact
	// must never wedge a gate); the blank template is what blocks it, so
	// open the spec once to materialise the draft, then reload
	m = pump(t, m, m.openSpec(m.rows[0].F))
	m.spec = nil
	m = pump(t, m, m.loadRows)
	if len(m.rows[0].Undrafted) == 0 {
		t.Fatalf("fixture gate is not blocked: %+v", m.rows[0])
	}
	m = openCardPage(t, m)
	m = typeAndSend(t, m, "go")
	if m.reentryPending != nil {
		t.Fatal("a blocked gate raised a chip")
	}
	if !strings.Contains(m.notice.text, "can't go on yet") {
		t.Errorf("the blocker was not said back: %+v", m.notice)
	}
	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Errorf("a blocked proceed moved the card to %s", got)
	}
}

// Anything that types withdraws the chip: it was a reading of a line
// that no longer exists. The picker's own keys do nothing at all.
func TestEditingWithdrawsTheChipAndDigitsDoNothing(t *testing.T) {
	m := clearPlanGate(t, "proceed")
	m = typeAndSend(t, m, "go")
	if m.reentryPending == nil {
		t.Fatal("no chip")
	}
	m = press(t, m, tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.reentryPending == nil {
		t.Error("a digit took the chip down")
	}
	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Errorf("a digit under a chip moved the card to %s", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.reentryPending != nil {
		t.Error("typing did not withdraw the chip")
	}
	if got := m.threadInput.Value(); !strings.HasSuffix(got, "gox") {
		t.Errorf("the key that withdrew the chip did not type: %q", got)
	}
}

// Landing is the one act that leaves the card, and it is reachable from
// prose only behind y — enter does nothing — and through the same
// landing path pressing the row takes.
func TestLandingFromProseIsYOnly(t *testing.T) {
	m, _ := chatWorkspace(t, classifyingAgent("proceed"))
	m = advanceTo(t, m, domain.StageVerify)
	draftRequiredSections(t, m)
	if err := m.store.AppendEvent(context.Background(), exitEvent(domain.StageVerify, "pass", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = openCardPage(t, m)
	in := m.nextInputFor(m.rows[0])
	if fwd, _, blocked, _ := stopForward(in); fwd != domain.StageDone || blocked != "" {
		t.Fatalf("fixture is not at a clear landing gate: forward=%s blocked=%q acts=%+v", fwd, blocked, stageActions(in))
	}
	m = typeAndSend(t, m, "ship it")
	p := m.reentryPending
	if p == nil || p.out.Action != reentry.Advance || p.out.Target != domain.StageDone {
		t.Fatalf("no landing chip: %+v", p)
	}
	if p.goOnEnter {
		t.Fatal("the landing went on enter")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.reentryPending == nil {
		t.Fatal("enter took the landing chip")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.reentryPending != nil {
		t.Error("y did not take the landing chip")
	}
}

// The chip says what autopilot will do with the gate a rewind lands on,
// because on autopilot the "stops for you" a reader expects never
// happens.
func TestChipSaysWhenAutopilotWillCrossTheGate(t *testing.T) {
	out := reentry.Decide(reentry.Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: reentry.RequirementMissing, Note: "x"})
	p := &reentryReading{line: "x", out: out, goOnEnter: goOnEnter(out)}
	attended := featureRow{F: domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageVerify, GateApproval: domain.GateAttended}}
	auto := attended
	auto.F.GateApproval = "autopilot"
	if got := strings.Join(chipDetails(attended, p), " "); !strings.Contains(got, "stops for you") || strings.Contains(got, "Autopilot is on") {
		t.Errorf("attended card's chip: %q", got)
	}
	if got := strings.Join(chipDetails(auto, p), " "); !strings.Contains(got, "Autopilot is on") {
		t.Errorf("autopilot card's chip does not say the gate crosses itself: %q", got)
	}
}

// A spend that starts now defaults to y; a move without one goes on
// enter. Stated over every outcome the router can produce.
func TestOnlyMovesWithoutASpendGoOnEnter(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		for _, intent := range reentry.Vocabulary() {
			for _, in := range []reentry.Input{
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: "x"},
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: "x", Forward: domain.StageImplement},
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: "x", Forward: domain.StageDone},
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: "x", Rerun: true},
			} {
				out := reentry.Decide(in)
				spends := out.Action == reentry.RerunInPlace || out.Action == reentry.Advance
				if goOnEnter(out) == spends && out.Action != reentry.Turn {
					t.Errorf("%s/%s: %s goOnEnter=%v", stage, intent, out.Action, goOnEnter(out))
				}
			}
		}
	}
}

func chipFixture(t *testing.T, w, h int) *Shell {
	t.Helper()
	m := clearPlanGate(t, "requirement_missing")
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = model.(*Shell)
	// the real flow reads a line the picker has already rendered; a chip
	// set before the decision was ever synced is withdrawn as belonging
	// to a stop that is gone, so render once first
	_ = m.View()
	out := reentry.Decide(reentry.Input{Stage: domain.StageImplement, Kind: domain.KindFeature, Intent: reentry.RequirementMissing, Note: "the persistence step was never in the spec"})
	m.threadInput.SetValue("the persistence step was never in the spec")
	m.reentryPending = &reentryReading{line: "the persistence step was never in the spec", out: out, goOnEnter: goOnEnter(out)}
	return m
}

func TestChipGolden(t *testing.T) {
	m := chipFixture(t, 120, 32)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestChipNarrowGolden(t *testing.T) {
	m := chipFixture(t, 64, 16)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestChipSpendGolden(t *testing.T) {
	m := clearPlanGate(t, "proceed")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = model.(*Shell)
	_ = m.View()
	fwd, rerun, blocked, label := stopForward(m.nextInputFor(m.rows[0]))
	out := reentry.Decide(reentry.Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: reentry.Proceed, Note: "looks right, go", Forward: fwd, Rerun: rerun, Blocked: blocked})
	m.threadInput.SetValue("looks right, go")
	m.reentryPending = &reentryReading{line: "looks right, go", out: out, forward: label, goOnEnter: goOnEnter(out)}
	golden.RequireEqual(t, []byte(m.View().Content))
}
