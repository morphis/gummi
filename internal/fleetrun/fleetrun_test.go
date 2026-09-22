package fleetrun

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// base is the fold's reference moment. The window in most tests below is
// [base, base+24h); the events are placed against it by name, so a
// reader can check each figure against the layout in one glance.
var base = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

var wsWindow = Window{From: base, To: base.Add(24 * time.Hour)}

func wsCard(num int, title string) Card {
	id, _ := domain.NewFeatureID(num)
	return Card{Feature: domain.Feature{ID: id, Num: num, Title: title, Slug: "x", Stage: domain.StageImplement, Kind: domain.KindFeature}}
}

// exitPayloadFor is a stage_exit's payload with a cost. Reading a pass's
// cost from the payload is the reconstructed path — the tests here have
// no rollup to attach unless they say so — and cardrun marks such a
// pass Reconstructed, which the fold never looks at; a cost is a cost.
func exitPayloadFor(credits float64) string {
	b, _ := json.Marshal(struct {
		Verdict string  `json:"verdict"`
		Credits float64 `json:"credits"`
	}{Verdict: "approve", Credits: credits})
	return string(b)
}

func enterFor(role string) string {
	b, _ := json.Marshal(struct {
		Role  string `json:"role"`
		Model string `json:"model"`
	}{Role: role, Model: "fake-model"})
	return string(b)
}

func decisionOpen(id string) string {
	b, _ := json.Marshal(state.DecisionPayload{ID: id, Kind: state.DecisionKindGate, Question: "land it?"})
	return string(b)
}

func ev(c Card, stage domain.Stage, kind, payload string, at time.Time) state.CardEvent {
	return state.CardEvent{Feature: c.Feature.ID, Stage: stage, Kind: kind, Payload: payload, At: at}
}

func fold(cards []Card, rows ...AllTimeRow) Report {
	if len(rows) == 0 {
		for _, c := range cards {
			rows = append(rows, AllTimeRow{Feature: c.Feature})
		}
	}
	return Fold(Input{Now: wsWindow.To, Window: wsWindow, Cards: cards, Rows: rows})
}

// laneOf finds one card's lane or fails looking.
func laneOf(t *testing.T, rep Report, want domain.FeatureID) Lane {
	t.Helper()
	for _, l := range rep.Lanes {
		if l.ID == want {
			return l
		}
	}
	t.Fatalf("no lane for card %s in %d lanes", want, len(rep.Lanes))
	return Lane{}
}

// TestTheWindowChargesAPassToWhereItStarted pins the attribution rule:
// a pass counts to the window it started in, never to the one it ended
// in, and a pass that only clips the window's edge is drawn but not
// charged. A rule that did the second would double-count the same
// credits across two windows; a rule that did the first would let one
// window hold the same pass twice.
func TestTheWindowChargesAPassToWhereItStarted(t *testing.T) {
	c := wsCard(1, "windowing")

	// started before the window, ends inside it: drawn clipped, not charged.
	pre := base.Add(-30 * time.Minute)
	c.Events = append(c.Events,
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), pre),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(5), base.Add(20*time.Minute)),
	)
	// started inside the window: charged.
	c.Events = append(c.Events,
		ev(c, domain.StageVerify, state.EventStageEnter, enterFor("implementer"), base.Add(2*time.Hour)),
		ev(c, domain.StageVerify, state.EventStageExit, exitPayloadFor(3), base.Add(2*time.Hour+30*time.Minute)),
	)
	// started after the window closes: neither drawn nor charged.
	late := base.Add(25 * time.Hour)
	c.Events = append(c.Events,
		ev(c, domain.StagePlan, state.EventStageEnter, enterFor("architect"), late),
		ev(c, domain.StagePlan, state.EventStageExit, exitPayloadFor(9), late.Add(time.Hour)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	l := laneOf(t, rep, c.Feature.ID)
	if l.Credits != 3 {
		t.Errorf("window credits = %.2f, want 3 — the pass that started inside", l.Credits)
	}
	if len(l.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 — the pre-window pass clipped to the edge, the late one absent", len(l.Blocks))
	}
	if !l.Blocks[0].From.Equal(base) {
		t.Errorf("clipped block starts at %v, want the window's left edge", l.Blocks[0].From)
	}
	if rep.Credits != 3 {
		t.Errorf("folded credits = %.2f, want 3", rep.Credits)
	}
}

// TestAnOpenSessionRunsToTheRightEdge: the one thing a timeline owes a
// reader watching a lane is that its block is still growing. An open
// session's block runs to the window's right edge, the lane reads as
// running, and its agent time counts to the edge — where the card's own
// tab would still show nothing (its clock stops at the last closed
// session, fleetrun's doc owns the difference).
func TestAnOpenSessionRunsToTheRightEdge(t *testing.T) {
	c := wsCard(2, "running")
	c.Events = append(c.Events,
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	l := laneOf(t, rep, c.Feature.ID)
	if len(l.Blocks) != 1 || !l.Blocks[0].Open {
		t.Fatalf("blocks = %+v, want one open block", l.Blocks)
	}
	if !l.Blocks[0].To.Equal(wsWindow.To) {
		t.Errorf("open block ends at %v, want the right edge %v", l.Blocks[0].To, wsWindow.To)
	}
	if !l.Running || rep.Running != 1 {
		t.Errorf("running lane = %v, folded count = %d, want one of each", l.Running, rep.Running)
	}
	if rep.Agent != 23*time.Hour {
		t.Errorf("agent = %v, want the open session's whole stretch to the edge", rep.Agent)
	}
}

// TestConcurrencyCountsHalfOpenIntervals: two lanes where one ends
// exactly as the other starts ran one at a time, not two. The peak is
// the board's parallelism; inflating it by a shared boundary would be a
// fact about the sweep, not about the board.
func TestConcurrencyCountsHalfOpenIntervals(t *testing.T) {
	touching := func() []Card {
		a := wsCard(1, "ends")
		a.Events = append(a.Events,
			ev(a, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base),
			ev(a, domain.StageImplement, state.EventStageExit, exitPayloadFor(1), base.Add(time.Hour)),
		)
		a.Run = cardrun.Report(cardrun.Input{Feature: a.Feature, Events: a.Events})
		b := wsCard(2, "starts")
		b.Events = append(b.Events,
			ev(b, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour)),
			ev(b, domain.StageImplement, state.EventStageExit, exitPayloadFor(1), base.Add(2*time.Hour)),
		)
		b.Run = cardrun.Report(cardrun.Input{Feature: b.Feature, Events: b.Events})
		return []Card{a, b}
	}
	if rep := fold(touching()); rep.PeakLanes != 1 {
		t.Errorf("touching lanes: peak = %d, want 1", rep.PeakLanes)
	}

	overlapping := touching()
	overlapping[1].Events[0].At = base.Add(30 * time.Minute)
	overlapping[1].Run = cardrun.Report(cardrun.Input{Feature: overlapping[1].Feature, Events: overlapping[1].Events})
	if rep := fold(overlapping); rep.PeakLanes != 2 {
		t.Errorf("overlapping lanes: peak = %d, want 2", rep.PeakLanes)
	}
}

// TestTheBusiestStretchIsNamed: the hour with the most agent time wins,
// by the bucket the window is read in. A lane running the whole window
// keeps every hour above zero, so the winner is the hour something else
// ran beside it.
func TestTheBusiestStretchIsNamed(t *testing.T) {
	a := wsCard(1, "all day")
	a.Events = append(a.Events,
		ev(a, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base),
	)
	a.Run = cardrun.Report(cardrun.Input{Feature: a.Feature, Events: a.Events})
	b := wsCard(2, "one hour")
	b.Events = append(b.Events,
		ev(b, domain.StageVerify, state.EventStageEnter, enterFor("implementer"), base.Add(2*time.Hour)),
		ev(b, domain.StageVerify, state.EventStageExit, exitPayloadFor(1), base.Add(3*time.Hour)),
	)
	b.Run = cardrun.Report(cardrun.Input{Feature: b.Feature, Events: b.Events})

	rep := fold([]Card{a, b})
	if !rep.Busiest.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("busiest = %v, want the hour lane two ran in (%v)", rep.Busiest, base.Add(2*time.Hour))
	}
	if rep.PeakLanes != 2 {
		t.Errorf("peak = %d, want 2", rep.PeakLanes)
	}
}

// TestAWaitingLaneNamesItsPark: a decision nobody answered is the wait
// the reader is standing in. Its span runs to the right edge, the lane
// says when it started, and the clock charges it to you — but never
// past the residual, because a decision can stand open while a session
// runs and that time is the agent's.
func TestAWaitingLaneNamesItsPark(t *testing.T) {
	c := wsCard(3, "parked")
	c.Events = append(c.Events,
		ev(c, domain.StagePlan, state.EventStageEnter, enterFor("architect"), base),
		ev(c, domain.StagePlan, state.EventStageExit, exitPayloadFor(1), base.Add(time.Hour)),
		ev(c, domain.StagePlan, state.EventDecisionOpen, decisionOpen("d1"), base.Add(4*time.Hour)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	l := laneOf(t, rep, c.Feature.ID)
	if !l.OpenWaitFrom.Equal(base.Add(4 * time.Hour)) {
		t.Errorf("open wait from %v, want when the decision was raised", l.OpenWaitFrom)
	}
	if len(l.Waits) != 1 || !l.Waits[0].To.Equal(wsWindow.To) {
		t.Fatalf("waits = %+v, want one span running to the right edge", l.Waits)
	}
	if rep.OnYou != 20*time.Hour {
		t.Errorf("on you = %v, want the wait to the edge (%v)", rep.OnYou, 20*time.Hour)
	}
}

// TestARedoThatCostsMoreIsTheNote: the top-cards note is the same
// comparison the card's run tab makes — a redo costing more than the
// pass it redid — read at fleet grain. A lane that merely reworked
// without getting dearer says what it did instead of nothing.
func TestARedoThatCostsMoreIsTheNote(t *testing.T) {
	c := wsCard(4, "reworked twice")
	for i, credits := range []float64{4, 6} {
		at := base.Add(time.Duration(14+i) * time.Hour)
		c.Events = append(c.Events,
			ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), at),
			ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(credits), at.Add(30*time.Minute)),
		)
	}
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	l := laneOf(t, rep, c.Feature.ID)
	if l.Note != "the redo cost more than the first pass" {
		t.Errorf("note = %q, want the cost comparison", l.Note)
	}
	if l.Redo != 6 || rep.Rework != 6 || rep.Corrected != 6 {
		t.Errorf("redo = %.2f, folded rework = %.2f, corrected = %.2f — want 6 in each", l.Redo, rep.Rework, rep.Corrected)
	}
	if len(rep.Top) != 1 || rep.Top[0].ID != l.ID {
		t.Errorf("top = %+v, want the reworked card alone", rep.Top)
	}
}

// TestARebasedLaneSaysSo: two passes of the same work after a rebase are
// not a verdict's doing, and the note must not call them one.
func TestARebasedLaneSaysSo(t *testing.T) {
	c := wsCard(5, "rebased")
	rebaseFor := func(role string) string {
		b, _ := json.Marshal(struct {
			Role   string `json:"role"`
			Flavor string `json:"flavor"`
		}{Role: role, Flavor: "rebase"})
		return string(b)
	}
	c.Events = append(c.Events,
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour)),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(2), base.Add(2*time.Hour)),
		ev(c, domain.StageImplement, state.EventStageEnter, rebaseFor("implementer"), base.Add(3*time.Hour)),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(0), base.Add(4*time.Hour)),
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(5*time.Hour)),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(2), base.Add(6*time.Hour)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	l := laneOf(t, rep, c.Feature.ID)
	if l.Note != "re-proved once after a rebase" {
		t.Errorf("note = %q, want the reproof", l.Note)
	}
	if rep.Reproved != 2 || rep.Corrected != 0 {
		t.Errorf("reproved = %.2f, corrected = %.2f — the second pass is a reproof, not a fix", rep.Reproved, rep.Corrected)
	}
}

// TestTheAllTimeLedgerNeedsNoWindow: the counters are already totals,
// so a card with nothing in the window still counts there — the fold
// draws its lanes from the window and its ledger from the board.
func TestTheAllTimeLedgerNeedsNoWindow(t *testing.T) {
	c := wsCard(6, "quiet")
	c.Feature.Spend = domain.Spend{Credits: 12, EstimatedCredits: 2}
	c.Feature.Stage = domain.StageDone
	c.Feature.LandedSHA = "abc123"
	row := AllTimeRow{Feature: c.Feature}

	rep := fold(nil, row)
	if len(rep.Lanes) != 0 {
		t.Fatalf("lanes = %d, want none — nothing ran in the window", len(rep.Lanes))
	}
	if rep.AllTime.Credits != 12 || rep.AllTime.Estimated != 2 {
		t.Errorf("all-time credits = %.2f/%.2f, want 12/2", rep.AllTime.Credits, rep.AllTime.Estimated)
	}
	if rep.AllTime.Settled != 1 || rep.AllTime.Landed != 1 {
		t.Errorf("settled = %d, landed = %d, want one of each", rep.AllTime.Settled, rep.AllTime.Landed)
	}
	// A board the ledger can count is not an empty workspace: its
	// all-time column still draws, whatever the window says.
	if rep.Empty() {
		t.Error("a board with cards but no window activity reads as empty")
	}
}

// TestAFoldOfNothingIsNothing: the empty case a fresh workspace hits
// first, and the one an empty state must survive without arithmetic.
func TestAFoldOfNothingIsNothing(t *testing.T) {
	rep := fold(nil)
	if !rep.Empty() {
		t.Error("a fold of no cards and no rows is not empty")
	}
	if rep.PeakLanes != 0 || rep.Running != 0 || len(rep.Top) != 0 {
		t.Errorf("empty fold carried figures: peak %d running %d top %d", rep.PeakLanes, rep.Running, len(rep.Top))
	}
}

// TestAnAllHistoryRateRunsFromFirstActivity: an "all" window with a
// first card an hour old reports a rate over that hour, not over the
// zero it would have divided by otherwise.
func TestAnAllHistoryRateRunsFromFirstActivity(t *testing.T) {
	c := wsCard(7, "young")
	all := Window{To: base.Add(time.Hour)}
	c.Events = append(c.Events,
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(2), base.Add(30*time.Minute)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := Fold(Input{Now: all.To, Window: all, Cards: []Card{c}})
	if rep.RateSpan != time.Hour {
		t.Errorf("rate span = %v, want first activity to now", rep.RateSpan)
	}
	if rep.Credits != 2 {
		t.Errorf("credits = %.2f, want 2", rep.Credits)
	}
}

// TestTokensAreChargedWhereTheCreditsAre pins the token side to the
// credit side at both scales. The window's tokens must cover exactly
// the passes its credit figure covers — a pass that started before the
// window is drawn and charged to neither — while the all-time count is
// read off the rollup rows, so spend no pass claims (a one-shot's) is
// in it. The two columns disagreeing is the point: they answer
// different questions, and each must answer its own the same way its
// credits do.
func TestTokensAreChargedWhereTheCreditsAre(t *testing.T) {
	c := wsCard(10, "metered")
	pre := base.Add(-2 * time.Hour)
	inside := base.Add(time.Hour)
	c.Events = append(c.Events,
		ev(c, domain.StagePlan, state.EventStageEnter, enterFor("architect"), pre),
		ev(c, domain.StagePlan, state.EventStageExit, exitPayloadFor(4), pre.Add(time.Hour)),
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), inside),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(6), inside.Add(time.Hour)),
	)
	rows := []state.StageSpend{
		{Stage: domain.StagePlan, Session: "s1", Role: "architect", Model: "m",
			Credits: 4, InputTokens: 5000, OutputTokens: 900, UpdatedAt: pre},
		{Stage: domain.StageImplement, Session: "s2", Role: "implementer", Model: "m",
			Credits: 6, InputTokens: 1000, CachedTokens: 400, OutputTokens: 200, UpdatedAt: inside},
		// A one-shot: no session key, so no pass holds it. All-time has
		// it; the window cannot, having nothing to date it by.
		{Stage: domain.StageImplement, Role: "scribe", Model: "m",
			Credits: 1, InputTokens: 300, OutputTokens: 50, UpdatedAt: inside},
	}
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events, Spend: rows})
	c.Feature.Spend = domain.Spend{Credits: 11}

	rep := fold([]Card{c}, AllTimeRow{Feature: c.Feature, StageSpend: rows})

	want := Tokens{Input: 1000, Cached: 400, Output: 200}
	if rep.Tokens != want {
		t.Errorf("window tokens = %+v, want %+v — only the pass that started inside", rep.Tokens, want)
	}
	if l := laneOf(t, rep, c.Feature.ID); l.Tokens != want {
		t.Errorf("lane tokens = %+v, want the same %+v the report summed", l.Tokens, want)
	}
	if got, want := rep.Tokens.CacheReadRatio(), 400.0/1400.0; got != want {
		t.Errorf("cache read ratio = %.4f, want %.4f", got, want)
	}
	all := Tokens{Input: 6300, Cached: 400, Output: 1150}
	if rep.AllTime.Tokens != all {
		t.Errorf("all-time tokens = %+v, want %+v — every rollup row, the one-shot included", rep.AllTime.Tokens, all)
	}
	if rep.AllTime.Tokens.Total() != 7850 {
		t.Errorf("all-time total = %d, want 7850", rep.AllTime.Tokens.Total())
	}
}

// TestATokenlessBackendSaysNothingRatherThanZero: a backend that never
// reports cache reads must not be made to say its cache missed. The
// ratio is zero either way; Zero is what tells a display which of the
// two it is holding.
func TestATokenlessBackendSaysNothingRatherThanZero(t *testing.T) {
	c := wsCard(11, "silent")
	c.Events = append(c.Events,
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour)),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(3), base.Add(2*time.Hour)),
	)
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events})

	rep := fold([]Card{c})
	if rep.Credits != 3 {
		t.Fatalf("credits = %.2f, want 3 — the pass still cost what it cost", rep.Credits)
	}
	if !rep.Tokens.Zero() || rep.Tokens.CacheReadRatio() != 0 {
		t.Errorf("tokens = %+v, want nothing said", rep.Tokens)
	}
}
