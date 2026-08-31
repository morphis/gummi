package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// stretchThread is a card whose spec and plan stages ran on autopilot,
// which then parked at implement — and where a person came back and
// started talking. It is the shape the whole change exists for: the
// period is over, and everything after it belongs to the reader.
func stretchThread(t *testing.T) *Shell {
	t.Helper()
	m := populatedShell(100, 34)
	m.sel = 1
	id := m.rows[m.sel].F.ID

	base := time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)
	tt := func(mins int) time.Time { return base.Add(time.Duration(mins) * time.Minute) }
	enter := func(role string) string {
		p, _ := json.Marshal(map[string]string{"role": role, "model": "claude-sonnet"})
		return string(p)
	}
	exit, _ := json.Marshal(map[string]any{"verdict": "pass", "credits": 180})
	msg := func(author, content string) string {
		p, _ := json.Marshal(map[string]string{"author": author, "content": content})
		return string(p)
	}

	m.cardEvents[id] = []state.CardEvent{
		evTookOver(domain.GateFull, tt(0)),
		{Kind: state.EventStageEnter, Stage: domain.StageSpec, At: tt(1), Payload: enter("architect")},
		{Kind: state.EventMessage, Stage: domain.StageSpec, At: tt(2), Payload: msg("architect", "spec written.")},
		{Kind: state.EventStageExit, Stage: domain.StageSpec, At: tt(6), Payload: string(exit)},
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, tt(6)),

		{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: tt(7), Payload: enter("architect")},
		evAsk("stream rows, don't buffer", state.ActorAutopilot, tt(20)),
		{Kind: state.EventStageExit, Stage: domain.StagePlan, At: tt(24), Payload: string(exit)},
		evGate(domain.StagePlan, domain.StageImplement, state.ActorAutopilot, tt(24)),

		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: tt(25), Payload: enter("implementer")},
		{Kind: state.EventMessage, Stage: domain.StageImplement, At: tt(30), Payload: msg("implementer", "wired the theme layer through the pane.")},
		evPark(domain.StageImplement, "implement finished, review it", tt(31)),
		{Kind: state.EventMessage, Stage: domain.StageImplement, At: tt(40), Payload: msg("user", "hold on, I want to look at the diff first")},
		{Kind: state.EventMessage, Stage: domain.StageImplement, At: tt(41), Payload: msg("implementer", "sure — nothing else is running.")},
	}
	m.cardOpen = true
	return m
}

// TestStretchIsAboveYourOwnTurns is the reported bug, asserted directly.
// What autopilot decided has to sit above the conversation that followed
// it, because that is when it happened. The block this replaced was
// appended after the live stage and so was permanently below everything,
// including turns typed hours later.
func TestStretchIsAboveYourOwnTurns(t *testing.T) {
	m := stretchThread(t)
	out := ansi.Strip(m.threadView(96, 34))

	closing := strings.Index(out, "autopilot parked it")
	yours := strings.Index(out, "hold on, I want to look at the diff first")
	if closing < 0 || yours < 0 {
		t.Fatalf("thread is missing the period or the turn after it:\n%s", out)
	}
	if closing > yours {
		t.Fatalf("the period closes below the turn that followed it:\n%s", out)
	}
	if strings.Contains(out, "while you were away") {
		t.Fatalf("the pinned rollup is still being drawn:\n%s", out)
	}
}

// TestStretchKeepsDecisionsWithTheirStage: folding a stage to one
// receipt used to drop everything inside it, which is the whole reason a
// rollup at the end of the page existed. The decisions come back out of
// the fold and sit under the receipt they belong to.
func TestStretchKeepsDecisionsWithTheirStage(t *testing.T) {
	m := stretchThread(t)
	out := ansi.Strip(m.threadView(96, 34))

	for _, want := range []string{
		"── autopilot took over",
		"autopilot crossed spec → plan",
		"autopilot answered",
		"autopilot crossed plan → implement",
		"── autopilot parked it",
		"implement finished, review it",
		"2 gates · 1 answer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("thread is missing %q:\n%s", want, out)
		}
	}

	specReceipt := strings.Index(out, "spec · architect")
	specCrossing := strings.Index(out, "autopilot crossed spec → plan")
	if specReceipt < 0 || specCrossing < specReceipt {
		t.Errorf("a stage's decisions print under the receipt that names the stage, not above it:\n%s", out)
	}
}

// TestLegacyCardKeepsTheActorsOwnName is D2 and D3 together. A card with
// no takeover row — every card that predates this, and every card the
// review loop drove with autopilot switched off — gets no period, and
// its unattended crossings keep the name of whatever actually made them.
// Calling those autopilot's would claim a handover nobody made.
func TestLegacyCardKeepsTheActorsOwnName(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "reviewer"})
	exit, _ := json.Marshal(map[string]any{"verdict": "pass"})
	m.cardEvents[id] = []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StageReview, At: base, Payload: string(enter)},
		evGate(domain.StageReview, domain.StageFix, "review", base.Add(time.Minute)),
		{Kind: state.EventStageExit, Stage: domain.StageReview, At: base.Add(2 * time.Minute), Payload: string(exit)},
		{Kind: state.EventStageEnter, Stage: domain.StageFix, At: base.Add(3 * time.Minute), Payload: string(enter)},
	}
	m.cardOpen = true
	out := ansi.Strip(m.threadView(96, 30))

	if strings.Contains(out, "autopilot took over") {
		t.Errorf("a card nobody handed over grew a period:\n%s", out)
	}
	if !strings.Contains(out, "review crossed review → fix") {
		t.Errorf("the loop's own crossing lost its name:\n%s", out)
	}
}

// TestRunningStretchHasNoClose: a card autopilot is driving right now
// draws the rule that opens the period and nothing that ends it, because
// it has not ended.
func TestRunningStretchHasNoClose(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "implementer"})
	m.cardEvents[id] = []state.CardEvent{
		evTookOver(domain.GateFull, base),
		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: base.Add(time.Minute), Payload: string(enter)},
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, base.Add(2*time.Minute)),
	}
	m.cardOpen = true
	out := ansi.Strip(m.threadView(96, 30))

	if !strings.Contains(out, "── autopilot took over") {
		t.Fatalf("an open period still opens:\n%s", out)
	}
	for _, notWant := range []string{"autopilot parked it", "autopilot finished", "you took back control"} {
		if strings.Contains(out, notWant) {
			t.Errorf("an open period drew %q:\n%s", notWant, out)
		}
	}
}

// TestCorrectiveBadgeOnTheMasthead: the rework count is a whole-card
// fact and lives with the other whole-card facts. The word is required —
// roundLabel renders a bare "⟲ n of m" for the current loop, and two
// unlabelled badges of the same shape would be indistinguishable.
func TestCorrectiveBadgeOnTheMasthead(t *testing.T) {
	m := populatedShell(120, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	m.setRound(id, domain.RoundKindCorrective, 2)
	m.cardOpen = true

	out := ansi.Strip(m.threadView(116, 30))
	if !strings.Contains(out, "2 of 5 corrective") {
		t.Fatalf("masthead is missing the corrective badge:\n%s", out)
	}
}
