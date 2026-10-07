package fleetrun

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

func keyOf(at time.Time) string { return strconv.FormatInt(at.UnixNano(), 10) }

func askOpen(id string) string {
	b, _ := json.Marshal(state.DecisionPayload{ID: id, Kind: state.DecisionKindAsk, Question: "which store?"})
	return string(b)
}

// withRun folds the card's own record into its run, the way every
// surface does, after its counter is set.
func withRun(c Card, spend []state.StageSpend) Card {
	c.Run = cardrun.Report(cardrun.Input{Feature: c.Feature, Events: c.Events, Spend: spend})
	return c
}

// A lane is its card's spend sliced by time. Over a window that holds
// the card's whole life it is the card's own total — the figure its
// board row and its run tab print — and the all-time column and the
// window agree. Each case is a shape of spend the lane used to drop.
func TestALaneAddsUpToItsCard(t *testing.T) {
	enter := base.Add(time.Hour)
	passEvs := func(c Card) []state.CardEvent {
		return []state.CardEvent{
			ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), enter),
			ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(12), enter.Add(time.Hour)),
		}
	}
	passRow := state.StageSpend{Stage: domain.StageImplement, Session: keyOf(enter), Role: "implementer", Model: "m", Credits: 12, UpdatedAt: enter.Add(time.Hour)}
	lead := base.Add(5 * time.Hour)
	cases := []struct {
		name      string
		counter   float64
		decompose float64
		passes    bool
		rows      []state.StageSpend
	}{
		{"passes only", 12, 0, true, []state.StageSpend{passRow}},
		{"a verify one-shot beside the passes", 14, 0, true, []state.StageSpend{
			passRow,
			{Stage: domain.StageVerify, Role: "scribe", Model: "m", Credits: 2, UpdatedAt: lead},
		}},
		{"a goal's lead", 48, 0, true, []state.StageSpend{
			passRow,
			{Stage: domain.StageImplement, Role: "lead", Model: "m", Credits: 36, UpdatedAt: lead},
		}},
		{"a freeform card, which logs no passes", 48, 0, false, []state.StageSpend{
			{Stage: domain.StageOpen, Session: "1", Role: "implementer", Model: "m", Credits: 36, UpdatedAt: enter},
			{Stage: domain.StageOpen, Session: "2", Role: "implementer", Model: "m", Credits: 12, UpdatedAt: lead},
		}},
		{"a decomposition at ingest", 15, 3, true, []state.StageSpend{passRow}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := wsCard(1, "whole")
			c.Feature.CreatedAt = base.Add(time.Minute)
			c.Feature.Spend = domain.Spend{Credits: tc.counter, DecomposeCredits: tc.decompose}
			if tc.passes {
				c.Events = passEvs(c)
			}
			c = withRun(c, tc.rows)
			rep := fold([]Card{c}, AllTimeRow{Feature: c.Feature, StageSpend: tc.rows})

			l := laneOf(t, rep, c.Feature.ID)
			if l.Credits != tc.counter {
				t.Errorf("lane = %.2f, want the card's own %.2f", l.Credits, tc.counter)
			}
			if rep.Credits != rep.AllTime.Credits {
				t.Errorf("window = %.2f, all-time = %.2f — a window holding the card's whole life is its total", rep.Credits, rep.AllTime.Credits)
			}
			for name, bs := range map[string][]cardrun.Bucket{"window": rep.ByStage, "all-time": rep.AllTime.ByStage} {
				var sum float64
				for _, b := range bs {
					sum += b.Credits
				}
				if sum != tc.counter {
					t.Errorf("%s by-stage buckets = %.2f (%+v), want %.2f", name, sum, bs, tc.counter)
				}
			}
		})
	}
}

// Spend no pass holds is charged by its one moment: a lead turn written
// after the window closed is not in it, though the pass it sits beside is.
func TestSpendNoPassHoldsIsChargedByItsMoment(t *testing.T) {
	c := wsCard(2, "goal")
	enter := base.Add(time.Hour)
	c.Events = []state.CardEvent{
		ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), enter),
		ev(c, domain.StageImplement, state.EventStageExit, exitPayloadFor(12), enter.Add(time.Hour)),
	}
	rows := []state.StageSpend{
		{Stage: domain.StageImplement, Session: keyOf(enter), Role: "implementer", Model: "m", Credits: 12, UpdatedAt: enter.Add(time.Hour)},
		{Stage: domain.StageImplement, Role: "lead", Model: "m", Credits: 36, EstimatedCredits: 36, UpdatedAt: wsWindow.To.Add(time.Hour)},
	}
	c.Feature.Spend = domain.Spend{Credits: 48, EstimatedCredits: 36}
	c = withRun(c, rows)
	rep := fold([]Card{c}, AllTimeRow{Feature: c.Feature, StageSpend: rows})
	if rep.Credits != 12 {
		t.Errorf("window = %.2f, want the pass's 12 — the lead turn came after", rep.Credits)
	}
	if rep.Estimated != 0 {
		t.Errorf("window estimated = %.2f, want none of the lead's estimate", rep.Estimated)
	}

	all := Window{To: wsWindow.To.Add(2 * time.Hour)}
	whole := Fold(Input{Now: all.To, Window: all, Cards: []Card{c}, Rows: []AllTimeRow{{Feature: c.Feature, StageSpend: rows}}})
	if whole.Credits != 48 || whole.Estimated != 36 {
		t.Errorf("all history = %.2f (%.2f estimated), want 48 (36)", whole.Credits, whole.Estimated)
	}
}

// Peak concurrency and the busiest stretch are about agents working. A
// lane blocked on its own question is open on the record but nothing
// runs on it, and counting it made an hour of one agent and one waiting
// card read as two lanes at once — and a busiest stretch of 34 minutes
// of "agent time" in a window whose agents had worked for 9 seconds.
func TestBusiestAndPeakCountOnlyWorkingTime(t *testing.T) {
	a := wsCard(1, "working")
	a.Events = []state.CardEvent{
		ev(a, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour)),
		ev(a, domain.StageImplement, state.EventStageExit, exitPayloadFor(1), base.Add(2*time.Hour)),
	}
	a = withRun(a, nil)
	b := wsCard(2, "asking")
	b.Events = []state.CardEvent{
		ev(b, domain.StagePlan, state.EventStageEnter, enterFor("architect"), base.Add(50*time.Minute)),
		ev(b, domain.StagePlan, state.EventDecisionOpen, askOpen("q1"), base.Add(55*time.Minute)),
	}
	b = withRun(b, nil)

	rep := fold([]Card{a, b})
	if rep.PeakLanes != 1 {
		t.Errorf("peak = %d, want 1 — the asking lane was not running beside the other", rep.PeakLanes)
	}
	if want := time.Hour; rep.BusiestAgent != want {
		t.Errorf("busiest = %v, want %v — one agent's hour", rep.BusiestAgent, want)
	}
	if rep.BusiestAgent > rep.Agent {
		t.Errorf("busiest stretch holds %v of agent time, the whole window %v", rep.BusiestAgent, rep.Agent)
	}
}

// Running is the board's word. Without a board the record is read the
// way the board would: an open pass on a card nobody is being asked
// about. With one, the board's set is the answer — the header pill and
// the headline are one count.
func TestRunningIsTheBoardsWord(t *testing.T) {
	open := func(num int, withAsk bool) Card {
		c := wsCard(num, "open")
		c.Events = []state.CardEvent{ev(c, domain.StageImplement, state.EventStageEnter, enterFor("implementer"), base.Add(time.Hour))}
		if withAsk {
			c.Events = append(c.Events, ev(c, domain.StageImplement, state.EventDecisionOpen, askOpen("q"), base.Add(2*time.Hour)))
		}
		return withRun(c, nil)
	}
	working, asking := open(1, false), open(2, true)
	cases := []struct {
		name string
		busy map[domain.FeatureID]bool
		want map[domain.FeatureID]bool
	}{
		{
			"no board: the open pass runs, the asking one needs you", nil,
			map[domain.FeatureID]bool{working.Feature.ID: true},
		},
		{
			"the board says neither runs (a dead process left the pass open)",
			map[domain.FeatureID]bool{},
			map[domain.FeatureID]bool{},
		},
		{
			"the board says the one it says",
			map[domain.FeatureID]bool{asking.Feature.ID: true},
			map[domain.FeatureID]bool{asking.Feature.ID: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := Fold(Input{Now: wsWindow.To, Window: wsWindow, Cards: []Card{working, asking}, Busy: tc.busy})
			if rep.Running != len(tc.want) {
				t.Errorf("running = %d, want %d", rep.Running, len(tc.want))
			}
			for _, l := range rep.Lanes {
				if l.Running != tc.want[l.ID] {
					t.Errorf("%s running = %v, want %v", l.ID, l.Running, tc.want[l.ID])
				}
			}
		})
	}
}
