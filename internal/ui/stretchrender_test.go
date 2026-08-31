package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

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

// TestPeriodBeforeAnyStageDrawsBothRules is an edge the first cut of the
// placement got wrong. Handing a card in todo to autopilot writes the
// takeover before anything has entered a stage, and switching straight
// back off writes the handback there too — so both boundaries predate
// the first stage_enter. The opening rule was clamped forward onto the
// first stage and drawn; the closing rule matched no stage at all and
// was not, leaving a period on screen that never ended.
func TestPeriodBeforeAnyStageDrawsBothRules(t *testing.T) {
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect"})

	for _, tc := range []struct {
		name  string
		after []state.CardEvent
	}{
		{name: "one stage", after: nil},
		{name: "two stages", after: []state.CardEvent{
			{Kind: state.EventStageExit, Stage: domain.StageSpec, At: base.Add(20 * time.Minute),
				Payload: `{"verdict":"pass"}`},
			{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: base.Add(21 * time.Minute),
				Payload: string(enter)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := populatedShell(100, 30)
			m.sel = 1
			id := m.rows[m.sel].F.ID
			events := []state.CardEvent{
				evTookOver(domain.GateFull, base),
				evHandedBack("you turned autopilot off", base.Add(time.Minute)),
				{Kind: state.EventStageEnter, Stage: domain.StageSpec, At: base.Add(10 * time.Minute),
					Payload: string(enter)},
			}
			m.cardEvents[id] = append(events, tc.after...)
			m.cardOpen = true
			out := ansi.Strip(m.threadView(96, 30))

			if !strings.Contains(out, "── autopilot took over") {
				t.Fatalf("the period never opened:\n%s", out)
			}
			if !strings.Contains(out, "── you took back control") {
				t.Fatalf("the period opened and never closed:\n%s", out)
			}
		})
	}
}

// TestMidStageTakeoverOpensWhereItHappened: working a card by hand and
// then handing it over partway through a stage is ordinary. The rule
// saying so belongs after the turns you typed before pressing the
// switch, not hoisted to the top of the stage above them — that is the
// same misplacement the whole change exists to remove, one level down.
func TestMidStageTakeoverOpensWhereItHappened(t *testing.T) {
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "implementer"})

	m := populatedShell(100, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	m.cardEvents[id] = []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: base, Payload: string(enter)},
		evMessage("implementer", "worked on this by hand first", base.Add(time.Minute)),
		evTookOver(domain.GateFull, base.Add(2*time.Minute)),
		evGate(domain.StageImplement, domain.StageReview, state.ActorAutopilot, base.Add(3*time.Minute)),
	}
	m.cardOpen = true
	out := ansi.Strip(m.threadView(96, 30))

	manual := strings.Index(out, "worked on this by hand first")
	opened := strings.Index(out, "── autopilot took over")
	if manual < 0 || opened < 0 {
		t.Fatalf("thread is missing the manual turn or the period:\n%s", out)
	}
	if opened < manual {
		t.Fatalf("the period opens above work that predates it:\n%s", out)
	}
}

// TestPeriodWithNoStagesStillDraws: a card can be handed to autopilot
// and taken back without any stage ever starting — the advance fails, or
// no agent is configured. Both placement paths key off stage segments,
// so with none the record of the card changing hands was dropped
// entirely and the body fell through to "nothing has run yet".
func TestPeriodWithNoStagesStillDraws(t *testing.T) {
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	m := populatedShell(100, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	m.cardEvents[id] = []state.CardEvent{
		evTookOver(domain.GateFull, base),
		evHandedBack("you turned autopilot off", base.Add(time.Minute)),
	}
	m.cardOpen = true
	out := ansi.Strip(m.threadView(96, 30))

	if !strings.Contains(out, "── autopilot took over") {
		t.Fatalf("a card with no stages lost the record of being handed over:\n%s", out)
	}
	if !strings.Contains(out, "── you took back control") {
		t.Fatalf("a card with no stages lost the record of being handed back:\n%s", out)
	}
}

// TestUnreadPeriodScrollsTheThreadToIt is the test whose absence let the
// whole open-at-the-period feature ship as a no-op, twice over: the
// render re-asked "what is unread" after the mark had already been
// advanced, and the live stage — where a period usually both opens and
// parks — had no way to report where it had drawn the rule. Nothing
// exercised markSeen and threadScroll together, so neither showed up.
//
// It asserts the observable thing: arriving at a card with unread
// autopilot history does not leave you at the bottom.
func TestUnreadPeriodScrollsTheThreadToIt(t *testing.T) {
	m := stretchThread(t)
	id := m.rows[m.sel].F.ID
	events := withSeqs(m.cardEvents[id])
	m.cardEvents[id] = events

	if cmd := m.markSeen(id, events); cmd != nil {
		cmd() // the store write; irrelevant here, but drain it
	}
	if m.anchorTo != id {
		t.Fatalf("anchorTo = %q, want %q — the card has an unread period", m.anchorTo, id)
	}

	_ = m.threadView(96, 20)

	if m.threadScroll == 0 {
		t.Fatal("the thread stayed at the bottom: the period was drawn but never anchored to")
	}
	if m.anchorTo != "" {
		t.Fatalf("anchorTo = %q, want cleared — the jump happens once, on arrival", m.anchorTo)
	}
}

// TestSecondVisitDoesNotJump: the mark is what stops the jump repeating.
// Having read the card once, opening it again lands at the newest line
// like any other card.
func TestSecondVisitDoesNotJump(t *testing.T) {
	m := stretchThread(t)
	id := m.rows[m.sel].F.ID
	events := withSeqs(m.cardEvents[id])
	m.cardEvents[id] = events

	if cmd := m.markSeen(id, events); cmd != nil {
		cmd()
	}
	_ = m.threadView(96, 20)
	m.threadScroll = 0

	if cmd := m.markSeen(id, events); cmd != nil {
		cmd()
	}
	if m.anchorTo != "" {
		t.Fatalf("anchorTo = %q, want none — this card has been read", m.anchorTo)
	}
	_ = m.threadView(96, 20)
	if m.threadScroll != 0 {
		t.Fatalf("threadScroll = %d, want 0 — a card already read opens at its newest line", m.threadScroll)
	}
}

// TestStretchThreadGolden is the review surface for this whole change.
// The substring assertions above pin the facts that matter one at a
// time; this pins the frame — spacing, indentation, where each rule sits
// relative to the receipts and the conversation, and the column the
// stamps align in. AGENTS.md is explicit that goldens are how a UI diff
// is reviewed, and a change about where things sit on a page cannot be
// reviewed by grep.
func TestStretchThreadGolden(t *testing.T) {
	m := stretchThread(t)
	golden.RequireEqual(t, []byte(ansi.Strip(m.threadView(96, 34))))
}

// TestStretchNarrowGolden is the same card at 56 columns. The rules
// dash-fill to the width they are given and the stamped decision lines
// choose between padding to the right margin and dropping the stamp
// entirely; neither branch was exercised anywhere until this.
func TestStretchNarrowGolden(t *testing.T) {
	m := stretchThread(t)
	golden.RequireEqual(t, []byte(ansi.Strip(m.threadView(56, 34))))
}

// TestPeriodThatDecidedNothingWithholdsOnlyTheTally is D9 at the render
// layer. The derivation test beside it only checks the struct; this
// checks the screen, which is where the rule actually has to hold: the
// rules and the reason still draw, and only the row that would have read
// as a count of nothing is missing.
func TestPeriodThatDecidedNothingWithholdsOnlyTheTally(t *testing.T) {
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "implementer"})
	m := populatedShell(100, 30)
	m.sel = 1
	id := m.rows[m.sel].F.ID
	m.cardEvents[id] = []state.CardEvent{
		evTookOver(domain.GateFull, base),
		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: base.Add(time.Minute), Payload: string(enter)},
		evMessage("implementer", "did the work", base.Add(2*time.Minute)),
		evPark(domain.StageImplement, "implement finished, review it", base.Add(3*time.Minute)),
	}
	m.cardOpen = true
	out := ansi.Strip(m.threadView(96, 30))

	if !strings.Contains(out, "── autopilot took over") || !strings.Contains(out, "── autopilot parked it") {
		t.Fatalf("a period that decided nothing still draws its rules:\n%s", out)
	}
	if !strings.Contains(out, "implement finished, review it") {
		t.Fatalf("the reason is not a tally and is not withheld with it:\n%s", out)
	}
	for _, notWant := range []string{"0 gates", "0 answers", "gate ·", "· 0"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("a row of zeroes was drawn (%q):\n%s", notWant, out)
		}
	}
}

// TestMastheadSaysWhenAutopilotIsRunning is P6, and it is the only
// autopilot fact that stays pinned — because it is the only one about
// now. It has to be read from live session state every frame, so that it
// cannot outlive its own truth the way the deleted rollup did.
func TestMastheadSaysWhenAutopilotIsRunning(t *testing.T) {
	s := m0Styles()
	f := domain.Feature{ID: "FD-001", Kind: domain.KindFeature, GateApproval: domain.GateFull}
	m := populatedShell(120, 30)

	// no session: the mode alone, no claim about now
	if got := ansi.Strip(autopilotField(s, m, f)); strings.Contains(got, "running") {
		t.Fatalf("autopilotField = %q, want no claim that a card with no session is running", got)
	}
	if !strings.Contains(ansi.Strip(autopilotField(s, m, f)), "autopilot: full") {
		t.Fatalf("autopilotField = %q, want the stored mode", ansi.Strip(autopilotField(s, m, f)))
	}

	// off never claims it either, whatever is running
	off := f
	off.GateApproval = domain.GateOff
	if got := ansi.Strip(autopilotField(s, m, off)); strings.Contains(got, "running") {
		t.Fatalf("autopilotField(off) = %q, want no running claim", got)
	}
}
