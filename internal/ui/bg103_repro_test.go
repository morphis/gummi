package ui

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG103EveryClosingRuleCarriesItsTime is BG-103's regression test.
// A board killed while a card was running unattended leaves no park and
// no handback — that is what "stopped without saying so" means — so the
// rule announcing it is the only record the run ended at all. It was
// also the only rule on the page drawn without a clock, because nothing
// dated it and the shared rule renderer drops the time at the zero
// value. The reader was told a run had been lost and not when.
//
// Asserted over every closing the vocabulary has rather than the one the
// drive walked: they all render through one function, they must all
// carry a time, and a closing added later must too.
func TestBG103EveryClosingRuleCarriesItsTime(t *testing.T) {
	at := time.Date(2026, 9, 4, 8, 38, 0, 0, time.UTC)
	clock := regexp.MustCompile(` \d\d:\d\d ──$`)

	for _, how := range []stretchClose{stretchParked, stretchFinished, stretchTakenBack, stretchOrphaned, stretchHandedOver} {
		st := autopilotStretch{closed: how, closedAt: at}
		got := ansi.Strip(stretchCloseLines(m0Styles(), st, 100)[0])
		if !clock.MatchString(got) {
			t.Errorf("%q carries no time: %q", stretchLabel(how), got)
		}
	}

	// and the one that had none: a card whose driver is gone, closed
	// from the log rather than from a stated ending.
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	f := mkFeature(t, store, 5, "network zone list project column", domain.StageReview)

	enter, _ := json.Marshal(map[string]string{"role": "reviewer", "model": "demo", "flavor": "stage"})
	took, _ := json.Marshal(state.AutopilotPayload{
		Event: state.AutopilotTookOver, Reason: "you handed it to autopilot", Mode: domain.GateFull,
	})
	crossed, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageImplement), To: string(domain.StageReview), Actor: state.ActorAutopilot,
	})
	// the log simply stops: the process died between entering the stage
	// and anything else, writing no park and no handback on its way out.
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "impl:enter"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventAutopilot, At: at, Payload: string(took), Dedupe: "ap:took"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: state.EventGate, At: at, Payload: string(crossed), Dedupe: "ap:gate"},
		{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "rev:enter"},
	}); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = sized.(*Shell)
	m.Attach(store, wt, ws)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true
	m = pump(t, m, m.loadCardEvents(f.ID))

	w, h := m.threadSize()
	body := ansi.Strip(m.threadView(w, h))
	label := stretchLabel(stretchOrphaned)
	var rule string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, label) {
			rule = strings.TrimRight(line, " ")
		}
	}
	if rule == "" {
		t.Fatalf("a card whose driver is gone never says its run stopped\n%s", body)
	}
	if !clock.MatchString(rule) {
		t.Errorf("the rule for a lost run does not say when it happened: %q\n%s", rule, body)
	}
}
