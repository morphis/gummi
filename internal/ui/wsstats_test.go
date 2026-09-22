package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/fleetrun"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// wsTestBase is the moment the test reports are measured against; every
// event below is placed against it by name.
var wsTestBase = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func wsTestWindow() fleetrun.Window {
	return fleetrun.Window{From: wsTestBase, To: wsTestBase.Add(24 * time.Hour)}
}

func wsTestCard(num int, title string) fleetrun.Card {
	id, _ := domain.NewFeatureID(num)
	return fleetrun.Card{
		Feature: domain.Feature{ID: id, Num: num, Title: title, Slug: "x", Stage: domain.StageImplement},
	}
}

func wsTestEvent(c fleetrun.Card, stage domain.Stage, kind, payload string, at time.Time) state.CardEvent {
	return state.CardEvent{Feature: c.Feature.ID, Stage: stage, Kind: kind, Payload: payload, At: at}
}

func wsEnter(role string) string {
	b, _ := json.Marshal(struct {
		Role  string `json:"role"`
		Model string `json:"model"`
	}{Role: role, Model: "fake-model"})
	return string(b)
}

func wsExit(credits float64) string {
	b, _ := json.Marshal(struct {
		Credits float64 `json:"credits"`
	}{Credits: credits})
	return string(b)
}

func wsDecision(id string) string {
	b, _ := json.Marshal(state.DecisionPayload{ID: id, Kind: state.DecisionKindGate, Question: "land it?"})
	return string(b)
}

// wsTestReport builds the fold two lanes wide: one card that ran and
// settled, one that is still running and parked on an unanswered
// decision. It is the shape the page's sections all have something to
// say about.
func wsTestReport() *fleetrun.Report {
	w := wsTestWindow()
	ran := wsTestCard(1, "dark mode")
	ran.Events = append(ran.Events,
		wsTestEvent(ran, domain.StageImplement, state.EventStageEnter, wsEnter("implementer"), wsTestBase),
		wsTestEvent(ran, domain.StageImplement, state.EventStageExit, wsExit(42), wsTestBase.Add(2*time.Hour)),
	)
	ran.LandedAt = wsTestBase.Add(3 * time.Hour)
	ran.Run = cardrun.Report(cardrun.Input{Feature: ran.Feature, Events: ran.Events})

	parked := wsTestCard(2, "csv export")
	parked.Events = append(parked.Events,
		wsTestEvent(parked, domain.StageImplement, state.EventStageEnter, wsEnter("implementer"), wsTestBase.Add(30*time.Minute)),
		wsTestEvent(parked, domain.StageImplement, state.EventDecisionOpen, wsDecision("d1"), wsTestBase.Add(2*time.Hour)),
	)
	parked.Run = cardrun.Report(cardrun.Input{Feature: parked.Feature, Events: parked.Events})

	rep := fleetrun.Fold(fleetrun.Input{
		Now:    w.To,
		Window: w,
		Cards:  []fleetrun.Card{ran, parked},
		Rows: []fleetrun.AllTimeRow{
			{Feature: ran.Feature, Landed: true},
			{Feature: parked.Feature},
		},
	})
	return &rep
}

// TestTheStatsTabDrawsTheTimeline walks the rendered page and asserts
// its sections: a lane per card, the running lane's block, the parked
// lane's clause, and the three ledgers the fold produced. The page is
// the tab's contract; these are the lines that must survive any
// restyling.
func TestTheStatsTabDrawsTheTimeline(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: wsTestReport()}
	out := stripANSI(m.wsStatsRender(120, 40))

	for _, want := range []string{
		"THE TIMELINE", "WHERE IT WENT", "THE CLOCK", "TOP CARDS",
		"FD-001", "FD-002", "42.00", "parked on you since 12:00",
		"█", "✔", "all-time", "2 cards",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 running") {
		t.Errorf("headline should count the one open session:\n%s", out)
	}
	// The legend only lists what survived the raster: the parked lane's
	// wait sits under its open session's block, so no ▒ is visible and
	// the legend must not promise one.
	if strings.Contains(out, "▒ on you") {
		t.Errorf("legend promises a wait the blocks overpaint:\n%s", out)
	}
	if !strings.Contains(out, "✔ landed") {
		t.Errorf("legend lost the landing mark it drew:\n%s", out)
	}
}

// TestTheStatsTabSaysNothingWhenTheWorkspaceIsQuiet: a board that never
// ran anything gets the sentence, not a page of empty sections.
func TestTheStatsTabSaysNothingWhenTheWorkspaceIsQuiet(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: &fleetrun.Report{}}
	out := stripANSI(m.wsStatsRender(120, 40))
	if !strings.Contains(out, "nothing has run in this workspace yet") {
		t.Errorf("empty workspace read as:\n%s", out)
	}
}

// TestTheStatsTabMeasuresBeforeItsFirstPaint: an unmeasured tab says it
// is measuring rather than inventing a page.
func TestTheStatsTabMeasuresBeforeItsFirstPaint(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, measuring: true}
	out := stripANSI(m.wsStatsRender(120, 40))
	if !strings.Contains(out, "measuring") {
		t.Errorf("unmeasured tab read as:\n%s", out)
	}
}

// TestStatsTabKeysWalkTheLanes: j/k and pgup/pgdn move the cursor and
// clamp it to the lanes there are; h/l zoom the window; f toggles the
// follow; none of it may panic on an unmeasured tab.
func TestStatsTabKeysWalkTheLanes(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: wsTestReport()}

	m.wsStatsKey("pgdown")
	if m.wsstats.cursor != 1 {
		t.Fatalf("pgdown moved the cursor to %d, want the last lane", m.wsstats.cursor)
	}
	m.wsStatsKey("pgdown")
	if m.wsstats.cursor != 1 {
		t.Errorf("pgdown past the last lane moved the cursor to %d, want clamped", m.wsstats.cursor)
	}
	m.wsStatsKey("k")
	if m.wsstats.cursor != 0 {
		t.Errorf("k moved the cursor to %d, want 0", m.wsstats.cursor)
	}

	before := m.wsstats.preset
	m.wsStatsKey("h")
	if m.wsstats.preset != before+1 {
		t.Errorf("h left the preset at %d, want %d", m.wsstats.preset, before+1)
	}
	m.wsStatsKey("l")
	if m.wsstats.preset != before {
		t.Errorf("l left the preset at %d, want %d", m.wsstats.preset, before)
	}

	m.wsStatsKey("f")
	if m.wsstats.follow {
		t.Error("f did not unpin the right edge")
	}
	if !m.wsstats.measuring {
		t.Error("f re-pinning measures — toggling it off does not")
	}

	// an unmeasured tab answers without panicking
	m2 := NewShell(theme.GummiDark(), "v0.1.0-test")
	m2.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true}
	m2.wsStatsKey("j")
	m2.wsStatsKey("enter")
}

// TestStatsTabEscReturnsToTheBoard and enter goes to a card — the two
// ways the tab hands the reader back to the evidence.
func TestStatsTabEscReturnsToTheBoard(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: wsTestReport()}
	m.wsStatsKey("esc")
	if m.tab != TabBoard {
		t.Fatalf("esc left tab = %v, want the board", m.tab)
	}
}

// TestStatsTabTickHoldsStillOffTheTab: the tick re-arms nothing for a
// tab that is not on screen, and it does not pile measures onto one
// already in flight.
func TestStatsTabTickHoldsStillOffTheTab(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: wsTestReport()}
	if _, ok := m.updateWsStats(wsStatsTickMsg{}); !ok {
		t.Fatal("the tick message was not claimed")
	}
	if m.wsstats.measuring {
		t.Error("the tick measured while the tab was not on screen")
	}
}

// TestTheStatsTabReadsTheRecord walks the real path — store to
// buildWsReport to fold — over a throwaway workspace, so the wiring the
// pure tests cannot see (the query, the row snapshot, the per-card
// reads) is exercised once.
func TestTheStatsTabReadsTheRecord(t *testing.T) {
	_, store, _ := uiRepo(t)
	ctx := context.Background()

	f := mkFeature(t, store, 1, "wired", domain.StageDone)
	at := wsTestBase
	for _, ev := range []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: wsEnter("implementer")},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventStageExit, At: at.Add(time.Hour), Payload: wsExit(9)},
	} {
		if err := store.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	rows := []featureRow{{F: f}}
	rep, err := buildWsReport(ctx, store, rows, wsTestBase.Add(-time.Minute), wsTestBase.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Lanes) != 1 {
		t.Fatalf("lanes = %d, want the one card that ran", len(rep.Lanes))
	}
	if rep.Credits != 9 {
		t.Errorf("credits = %.2f, want the exited pass's 9", rep.Credits)
	}
	if rep.AllTime.Cards != 1 {
		t.Errorf("all-time cards = %d, want the row the board holds", rep.AllTime.Cards)
	}

	// The window is what makes the lane: the same card measured against
	// a window its life falls outside of draws nothing.
	empty, err := buildWsReport(ctx, store, rows, wsTestBase.Add(48*time.Hour), wsTestBase.Add(72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Lanes) != 0 {
		t.Errorf("a card outside the window drew %d lanes, want none", len(empty.Lanes))
	}
	if empty.AllTime.Cards != 1 {
		t.Errorf("all-time lost the quiet card: %d cards", empty.AllTime.Cards)
	}
}

// TestStatsTabArrivesMeasured: entering the tab mounts it and fires the
// measure; the loaded message lands it. The round trip through the real
// commands, so gotoTab's arrival contract is asserted where it lives.
func TestStatsTabArrivesMeasured(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.now = func() time.Time { return wsTestBase.Add(24 * time.Hour) }
	m.rows = []featureRow{} // no cards yet: the measure returns an empty fold

	if cmd := m.ensureWsStats(); cmd == nil {
		t.Fatal("first arrival did not mount the tab and fire a measure")
	}
	if m.wsstats == nil || !m.wsstats.measuring {
		t.Fatal("the tab is not mounted mid-measure")
	}
	if cmd := m.ensureWsStats(); cmd != nil {
		t.Fatal("a second arrival re-measured an in-flight page")
	}

	// The measured report lands: the page stops claiming to be reading.
	msg := wsStatsLoadedMsg{rep: &fleetrun.Report{}}
	if _, ok := m.updateWsStats(msg); !ok {
		t.Fatal("the loaded message was not claimed")
	}
	if m.wsstats.measuring {
		t.Error("the tab is still marked measuring after its report landed")
	}
}

// TestTheStatsTabPrintsCostAndTokens: the ledger has to answer what the
// board cost in money and what it spent in tokens, for the window and
// for all time. Credits alone are a unit nobody is billed in, and a
// token figure without its cache share overstates what was paid for.
func TestTheStatsTabPrintsCostAndTokens(t *testing.T) {
	w := wsTestWindow()
	c := wsTestCard(1, "dark mode")
	c.Events = append(c.Events,
		wsTestEvent(c, domain.StageImplement, state.EventStageEnter, wsEnter("implementer"), wsTestBase),
		wsTestEvent(c, domain.StageImplement, state.EventStageExit, wsExit(120), wsTestBase.Add(2*time.Hour)),
	)
	rows := []state.StageSpend{{
		Stage: domain.StageImplement, Session: "s1", Role: "implementer", Model: "fake-model",
		Credits: 120, InputTokens: 600_000, CachedTokens: 400_000, OutputTokens: 50_000,
		UpdatedAt: wsTestBase,
	}}
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events, Spend: rows})
	c.Feature.Spend = domain.Spend{Credits: 120}
	rep := fleetrun.Fold(fleetrun.Input{
		Now: w.To, Window: w, Cards: []fleetrun.Card{c},
		Rows: []fleetrun.AllTimeRow{{Feature: c.Feature, StageSpend: rows}},
	})

	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, rep: &rep}
	out := stripANSI(m.wsStatsRender(120, 40))

	for _, want := range []string{
		"$1.20",              // 120 credits, in the unit the bill uses
		"1.0M in",            // fresh input plus what the cache served
		"(40% cached)",       // the share that changes what the credits mean
		"50.0k out",          //
		"window", "all-time", // one row per column, attributed apart
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ledger lacks %q:\n%s", want, out)
		}
	}
}

// TestTheLedgerSaysNothingAboutACacheItWasNeverTold: a backend that
// reports no cache reads leaves the clause off entirely — "(0% cached)"
// would read as a cache that missed every time.
func TestTheLedgerSaysNothingAboutACacheItWasNeverTold(t *testing.T) {
	if got := wsTokenClause(fleetrun.Tokens{Input: 1000, Output: 200}); got != "1.0k in · 200 out" {
		t.Errorf("token clause = %q, want no cache clause", got)
	}
	if got := wsTokenClause(fleetrun.Tokens{}); got != "" {
		t.Errorf("token clause of nothing = %q, want empty", got)
	}
	// A figure too small to be money reads as credits already; printing
	// the fallback beside the credits would say it twice.
	if got := wsDollars(0.01); got != "" {
		t.Errorf("sub-cent dollars = %q, want nothing", got)
	}
	if got := wsDollars(120); got != "$1.20" {
		t.Errorf("dollars = %q, want $1.20", got)
	}
}
