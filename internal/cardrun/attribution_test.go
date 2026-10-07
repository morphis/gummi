package cardrun

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// keyOf is the session key the engine files a pass's spend under: the
// pass's start in Unix nanoseconds (engine's Session.generation), which
// is also what its stage_enter records.
func keyOf(at time.Time) string { return strconv.FormatInt(at.UnixNano(), 10) }

func enterEv(stage domain.Stage, role, flavor string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(map[string]string{"role": role, "flavor": flavor, "model": "m"})
	return state.CardEvent{Stage: stage, Kind: state.EventStageEnter, At: at, Payload: string(p)}
}

func exitEv(stage domain.Stage, credits float64, at time.Time) state.CardEvent {
	p, _ := json.Marshal(map[string]any{"verdict": "", "credits": credits})
	return state.CardEvent{Stage: stage, Kind: state.EventStageExit, At: at, Payload: string(p)}
}

func askEv(stage domain.Stage, id string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.DecisionPayload{ID: id, Kind: state.DecisionKindAsk, Question: "which store?"})
	return state.CardEvent{Stage: stage, Kind: state.EventDecisionOpen, At: at, Payload: string(p)}
}

func gateDecisionEv(stage domain.Stage, id string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.DecisionPayload{ID: id, Kind: state.DecisionKindGate, Question: "land it?"})
	return state.CardEvent{Stage: stage, Kind: state.EventDecisionOpen, At: at, Payload: string(p)}
}

func answerEv(stage domain.Stage, id string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AskPayload{ID: id, Answer: "sqlite", By: "user"})
	return state.CardEvent{Stage: stage, Kind: state.EventAsk, At: at, Payload: string(p)}
}

func crossEv(from, to domain.Stage, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.GatePayload{From: string(from), To: string(to), Actor: "user"})
	return state.CardEvent{Stage: from, Kind: state.EventGate, At: at, Payload: string(p)}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// A pass cut off before its first sample leaves no rollup row, and the
// pass that resumed it after a top-up files under its own key. Matched
// by order, the resumed pass's row went to the cut-off one and the
// resumed pass was rebuilt from its own stage_exit: the card's passes
// came to 72 while its counter, its board row and its run tab said 62.
// Matched by the key each pass's start stamps, every figure is 62.
func TestAPassClaimsTheRowsFiledUnderItsOwnStart(t *testing.T) {
	plan, cut, resumed, crit := base, base.Add(2*time.Minute), base.Add(time.Hour), base.Add(2*time.Hour)
	evs := []state.CardEvent{
		enterEv(domain.StagePlan, "architect", "stage", plan),
		exitEv(domain.StagePlan, 12, plan.Add(30*time.Second)),
		enterEv(domain.StageImplement, "implementer", "stage", cut),
		exitEv(domain.StageImplement, 0, cut.Add(time.Second)),
		enterEv(domain.StageImplement, "implementer", "stage", resumed),
		exitEv(domain.StageImplement, 12, resumed.Add(time.Minute)),
		enterEv(domain.StageImplement, "reviewer", "critique", crit),
		exitEv(domain.StageImplement, 12, crit.Add(time.Minute)),
	}
	spend := []state.StageSpend{
		{Stage: domain.StagePlan, Session: keyOf(plan), Role: "architect", Model: "m", Credits: 12, UpdatedAt: plan.Add(30 * time.Second)},
		{Stage: domain.StageImplement, Session: keyOf(resumed), Role: "implementer", Model: "m", Credits: 12, UpdatedAt: resumed.Add(time.Minute)},
		{Stage: domain.StageImplement, Session: keyOf(crit), Role: "reviewer", Model: "m", Credits: 12, UpdatedAt: crit.Add(time.Minute)},
		// the verify one-shot: no key, no pass
		{Stage: domain.StageVerify, Role: "scribe", Model: "m", Credits: 2, UpdatedAt: crit.Add(2 * time.Minute)},
	}
	run := Report(Input{Feature: card(38, 90), Events: evs, Spend: spend})

	var passes float64
	for _, s := range run.Sessions {
		passes += s.Credits
		if s.Reconstructed {
			t.Errorf("pass %s/%s at %v was reconstructed from its stage_exit — its row was there", s.Stage, s.Role, s.Started)
		}
	}
	if !near(passes, 36) {
		t.Errorf("passes = %.2f, want 36 — the cut-off pass spent nothing", passes)
	}
	if got := run.Sessions[1].Credits; got != 0 {
		t.Errorf("the cut-off pass = %.2f, want 0", got)
	}
	if got := run.Sessions[2].Credits; got != 12 {
		t.Errorf("the resumed pass = %.2f, want its own 12", got)
	}
	if got := run.Money.FirstPass + run.Money.Rework + run.Money.Elsewhere; !near(got, run.Money.Credits) {
		t.Errorf("first pass + rework + elsewhere = %.2f, want the counter's %.2f", got, run.Money.Credits)
	}
}

// Every credit of the card's counter lands in exactly one place — a
// pass or a charge — and the stage buckets add up to the same total.
// Each case is a shape of spend the report used to lose.
func TestEveryCreditOfTheCounterLandsSomewhere(t *testing.T) {
	enter := base
	created := base.Add(-time.Hour)
	passEvs := []state.CardEvent{
		enterEv(domain.StageImplement, "implementer", "stage", enter),
		exitEv(domain.StageImplement, 10, enter.Add(time.Hour)),
	}
	passRow := state.StageSpend{Stage: domain.StageImplement, Session: keyOf(enter), Role: "implementer", Model: "m", Credits: 10, UpdatedAt: enter.Add(time.Hour)}
	later := enter.Add(3 * time.Hour)

	cases := []struct {
		name      string
		counter   float64
		decompose float64
		evs       []state.CardEvent
		spend     []state.StageSpend
		// the charges the report must itemize: role → credits and moment
		want     map[string]float64
		wantAt   map[string]time.Time
		wantPass float64
	}{
		{
			name: "a one-shot scribe, which files under no session", counter: 12,
			evs:   passEvs,
			spend: []state.StageSpend{passRow, {Stage: domain.StageVerify, Role: "scribe", Model: "m", Credits: 2, UpdatedAt: later}},
			want:  map[string]float64{"scribe": 2}, wantAt: map[string]time.Time{"scribe": later}, wantPass: 10,
		},
		{
			name: "a goal's lead, which conducts every card's stages", counter: 46,
			evs:   passEvs,
			spend: []state.StageSpend{passRow, {Stage: domain.StageImplement, Role: "lead", Model: "m", Credits: 36, UpdatedAt: later}},
			want:  map[string]float64{"lead": 36}, wantAt: map[string]time.Time{"lead": later}, wantPass: 10,
		},
		{
			name: "a freeform card, whose sessions log no passes", counter: 48,
			spend: []state.StageSpend{
				{Stage: domain.StageOpen, Session: "1790538114877280085", Role: "implementer", Model: "m", Credits: 36, UpdatedAt: enter},
				{Stage: domain.StageOpen, Session: "1790538863860093166", Role: "implementer", Model: "m", Credits: 12, UpdatedAt: later},
			},
			want: map[string]float64{"implementer": 48},
		},
		{
			name: "a backend's helper call, filed under the pass's key by another role", counter: 11,
			evs: passEvs,
			spend: []state.StageSpend{
				passRow,
				{Stage: domain.StageImplement, Session: keyOf(enter), Role: "helper", Model: "h", Credits: 1, UpdatedAt: later},
			},
			want: map[string]float64{"helper": 1}, wantAt: map[string]time.Time{"helper": later}, wantPass: 10,
		},
		{
			name: "a decomposition at ingest, which books the counter alone", counter: 13, decompose: 3,
			evs: passEvs, spend: []state.StageSpend{passRow},
			want: map[string]float64{"decompose": 3}, wantAt: map[string]time.Time{"decompose": created}, wantPass: 10,
		},
		{
			name: "counter spend older than the rollup", counter: 15,
			evs: passEvs, spend: []state.StageSpend{passRow},
			want: map[string]float64{"unrecorded": 5}, wantAt: map[string]time.Time{"unrecorded": created}, wantPass: 10,
		},
		{
			name: "nothing but passes", counter: 10,
			evs: passEvs, spend: []state.StageSpend{passRow},
			want: map[string]float64{}, wantPass: 10,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := card(tc.counter, 500)
			f.CreatedAt = created
			f.Spend.DecomposeCredits = tc.decompose
			run := Report(Input{Feature: f, Events: tc.evs, Spend: tc.spend})
			m := run.Money

			if got := m.FirstPass + m.Rework; !near(got, tc.wantPass) {
				t.Errorf("passes = %.2f, want %.2f", got, tc.wantPass)
			}
			if got := m.FirstPass + m.Rework + m.Elsewhere; !near(got, tc.counter) {
				t.Errorf("first pass + rework + elsewhere = %.2f, want the counter's %.2f", got, tc.counter)
			}
			var stages float64
			for _, b := range m.ByStage {
				stages += b.Credits
			}
			if !near(stages, tc.counter) {
				t.Errorf("by-stage buckets = %.2f (%+v), want the counter's %.2f", stages, m.ByStage, tc.counter)
			}
			got := map[string]float64{}
			var charged float64
			for _, c := range m.Charges {
				got[c.Role] += c.Credits
				charged += c.Credits
				if at, ok := tc.wantAt[c.Role]; ok && !c.At.Equal(at) {
					t.Errorf("%s charged at %v, want %v", c.Role, c.At, at)
				}
			}
			if !near(charged, m.Elsewhere) {
				t.Errorf("charges = %.2f, want Elsewhere's %.2f", charged, m.Elsewhere)
			}
			for role, want := range tc.want {
				if !near(got[role], want) {
					t.Errorf("charge %s = %.2f, want %.2f (all: %+v)", role, got[role], want, m.Charges)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("charges = %+v, want only %+v", got, tc.want)
			}
		})
	}
}

// A question holds only the pass that asked it. One nobody answered —
// the session stopped mid-question and the card moved on — used to run
// to the end of the record, taking every later pass's time from the
// agent: a two-hour implementation read as ten minutes of work.
func TestAnUnansweredAskHoldsOnlyItsOwnPass(t *testing.T) {
	at := func(d time.Duration) time.Time { return base.Add(d) }
	cases := []struct {
		name  string
		evs   []state.CardEvent
		agent time.Duration
		onYou time.Duration
	}{
		{
			name: "stopped mid-question, then the card moved on",
			evs: []state.CardEvent{
				enterEv(domain.StagePlan, "architect", "stage", at(0)),
				askEv(domain.StagePlan, "q1", at(10*time.Minute)),
				exitEv(domain.StagePlan, 0, at(15*time.Minute)),
				enterEv(domain.StageImplement, "implementer", "stage", at(time.Hour)),
				exitEv(domain.StageImplement, 0, at(3*time.Hour)),
			},
			// the plan pass until it asked, and the whole implementation
			agent: 10*time.Minute + 2*time.Hour,
			// the question stood until the card left plan
			onYou: 50 * time.Minute,
		},
		{
			name: "its gate crossed by hand",
			evs: []state.CardEvent{
				enterEv(domain.StagePlan, "architect", "stage", at(0)),
				askEv(domain.StagePlan, "q1", at(10*time.Minute)),
				exitEv(domain.StagePlan, 0, at(20*time.Minute)),
				crossEv(domain.StagePlan, domain.StageImplement, at(30*time.Minute)),
				enterEv(domain.StageImplement, "implementer", "stage", at(40*time.Minute)),
				exitEv(domain.StageImplement, 0, at(3*time.Hour+40*time.Minute)),
			},
			agent: 10*time.Minute + 3*time.Hour,
			onYou: 20 * time.Minute,
		},
		{
			name: "a fresh pass of the same stage, which never saw the question",
			evs: []state.CardEvent{
				enterEv(domain.StagePlan, "architect", "stage", at(0)),
				askEv(domain.StagePlan, "q1", at(10*time.Minute)),
				exitEv(domain.StagePlan, 0, at(15*time.Minute)),
				enterEv(domain.StagePlan, "architect", "stage", at(time.Hour)),
				exitEv(domain.StagePlan, 0, at(2*time.Hour)),
			},
			agent: 10*time.Minute + time.Hour,
			// the card never left plan: the question is still the
			// reader's, from the asking through the gap to the new pass
			onYou: 50 * time.Minute,
		},
		{
			name: "answered in a later pass it was re-armed into",
			evs: []state.CardEvent{
				enterEv(domain.StagePlan, "architect", "stage", at(0)),
				askEv(domain.StagePlan, "q1", at(10*time.Minute)),
				exitEv(domain.StagePlan, 0, at(15*time.Minute)),
				enterEv(domain.StagePlan, "architect", "stage", at(time.Hour)),
				answerEv(domain.StagePlan, "q1", at(90*time.Minute)),
				exitEv(domain.StagePlan, 0, at(2*time.Hour)),
			},
			// blocked on it until the answer, whichever pass held it
			agent: 10*time.Minute + 30*time.Minute,
			onYou: 80 * time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := Report(Input{Feature: card(0, 100), Events: tc.evs})
			if run.Clock.Agent != tc.agent {
				t.Errorf("agent = %v, want %v", run.Clock.Agent, tc.agent)
			}
			if run.Clock.OnYou != tc.onYou {
				t.Errorf("on you = %v, want %v", run.Clock.OnYou, tc.onYou)
			}
		})
	}
}

// A decision whose stage the card has left is dead, not waiting — the
// rule state.OpenDecisions applies. A wait that outlived its stage drew
// a lane "parked on you" long after the inbox had stopped asking.
func TestADecisionDiesWithItsStage(t *testing.T) {
	at := func(d time.Duration) time.Time { return base.Add(d) }
	cases := []struct {
		name string
		evs  []state.CardEvent
		want []Span
	}{
		{
			name: "still at its stage: open to the right edge",
			evs: []state.CardEvent{
				gateDecisionEv(domain.StageImplement, "budget:1", at(0)),
				enterEv(domain.StageImplement, "implementer", "stage", at(time.Hour)),
			},
			want: []Span{{From: at(0)}},
		},
		{
			name: "the card moved on under it",
			evs: []state.CardEvent{
				gateDecisionEv(domain.StageImplement, "budget:1", at(0)),
				enterEv(domain.StageImplement, "implementer", "stage", at(time.Hour)),
				enterEv(domain.StageVerify, "reviewer", "stage", at(2*time.Hour)),
			},
			want: []Span{{From: at(0), To: at(2 * time.Hour)}},
		},
		{
			name: "carried out of its stage by a gate",
			evs: []state.CardEvent{
				gateDecisionEv(domain.StageVerify, "gate:1", at(0)),
				crossEv(domain.StageVerify, domain.StageImplement, at(time.Hour)),
			},
			want: []Span{{From: at(0), To: at(time.Hour)}},
		},
		{
			name: "an event filed under no stage moves nothing",
			evs: []state.CardEvent{
				gateDecisionEv(domain.StageVerify, "gate:1", at(0)),
				{Kind: "autopilot", At: at(time.Hour), Payload: `{}`},
			},
			want: []Span{{From: at(0)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecisionSpans(tc.evs)
			if len(got) != len(tc.want) {
				t.Fatalf("spans = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if !got[i].From.Equal(tc.want[i].From) || !got[i].To.Equal(tc.want[i].To) {
					t.Errorf("span %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// WorkingSpans is WorkingTime drawn: the interval less its asks, as the
// stretches that remain, summing to exactly what WorkingTime says.
func TestWorkingSpansAreTheIntervalLessItsAsks(t *testing.T) {
	at := func(d time.Duration) time.Time { return base.Add(d) }
	cases := []struct {
		name string
		asks []Span
		want []Span
	}{
		{"no question", nil, []Span{{at(0), at(time.Hour)}}},
		{
			"one in the middle",
			[]Span{{at(10 * time.Minute), at(20 * time.Minute)}},
			[]Span{{at(0), at(10 * time.Minute)}, {at(20 * time.Minute), at(time.Hour)}},
		},
		{"one still open", []Span{{From: at(50 * time.Minute)}}, []Span{{at(0), at(50 * time.Minute)}}},
		{"one before and past both edges", []Span{{at(-time.Hour), at(2 * time.Hour)}}, nil},
		{
			"two overlapping",
			[]Span{{at(5 * time.Minute), at(15 * time.Minute)}, {at(10 * time.Minute), at(20 * time.Minute)}},
			[]Span{{at(0), at(5 * time.Minute)}, {at(20 * time.Minute), at(time.Hour)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkingSpans(at(0), at(time.Hour), tc.asks)
			if len(got) != len(tc.want) {
				t.Fatalf("spans = %+v, want %+v", got, tc.want)
			}
			var sum time.Duration
			for i := range got {
				sum += got[i].To.Sub(got[i].From)
				if !got[i].From.Equal(tc.want[i].From) || !got[i].To.Equal(tc.want[i].To) {
					t.Errorf("span %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
			if w := WorkingTime(at(0), at(time.Hour), tc.asks); sum != w {
				t.Errorf("spans sum to %v, WorkingTime says %v", sum, w)
			}
		})
	}
}
