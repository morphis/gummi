package decisions

import (
	"testing"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

func TestRankPicksWhatStopsTheCardFirst(t *testing.T) {
	d := func(id, kind string) state.OpenDecision { return state.OpenDecision{ID: id, Kind: kind} }
	tests := []struct {
		name string
		in   []state.OpenDecision
		want string // winning id, "" for none
	}{
		{"none", nil, ""},
		{"idle alone is nobody waiting", []state.OpenDecision{d("i", state.DecisionKindIdle)}, ""},
		{"ask beats everything", []state.OpenDecision{d("g", state.DecisionKindGate), d("b", state.DecisionKindBudget), d("a", state.DecisionKindAsk)}, "a"},
		{"conflict ranks with ask, first wins", []state.OpenDecision{d("c", state.DecisionKindConflict), d("a", state.DecisionKindAsk)}, "c"},
		{"budget beats verify", []state.OpenDecision{d("v", state.DecisionKindVerify), d("b", state.DecisionKindBudget)}, "b"},
		{"verify beats gate", []state.OpenDecision{d("g", state.DecisionKindGate), d("v", state.DecisionKindVerify)}, "v"},
		{"unknown kinds never win", []state.OpenDecision{d("x", "mystery"), d("g", state.DecisionKindGate)}, "g"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Rank(tt.in)
			if ok != (tt.want != "") || got.ID != tt.want {
				t.Fatalf("Rank = %q, %v; want %q", got.ID, ok, tt.want)
			}
		})
	}
}

func TestAttentionLanes(t *testing.T) {
	tests := []struct {
		kind      string
		lane      Lane
		escalated bool
		ok        bool
	}{
		{state.DecisionKindAsk, LaneQuestion, false, true},
		{state.DecisionKindBudget, LaneBudget, false, true},
		{state.DecisionKindGate, LaneGate, false, true},
		{state.DecisionKindVerify, LaneGate, true, true},
		{state.DecisionKindConflict, LaneFailure, false, true},
		{state.DecisionKindIdle, "", false, false},
		{"mystery", "", false, false},
	}
	for _, tt := range tests {
		lane, esc, ok := Attention(tt.kind)
		if lane != tt.lane || esc != tt.escalated || ok != tt.ok {
			t.Errorf("Attention(%q) = %q, %v, %v; want %q, %v, %v", tt.kind, lane, esc, ok, tt.lane, tt.escalated, tt.ok)
		}
	}
}

func TestAskOptionsEndInTheChatRow(t *testing.T) {
	ask := &engine.Ask{Question: "Persist where?", Options: []engine.AskOption{
		{Label: "per-device", Detail: "local only"}, {Label: "synced"},
	}}
	got := AskOptions(ask)
	if len(got) != 3 {
		t.Fatalf("got %d options, want 2 real + the chat row: %+v", len(got), got)
	}
	if got[0] != (Option{Label: "per-device", Detail: "local only"}) || got[1].Chat {
		t.Errorf("real options reshaped: %+v", got[:2])
	}
	if chat := got[len(ask.Options)]; !chat.Chat || chat.Label != ChatLabel || chat.Detail != ChatDetail {
		t.Errorf("row at len(ask.Options) = %+v, want the chat row", chat)
	}
}

func TestAnswerText(t *testing.T) {
	single := &engine.Ask{Options: []engine.AskOption{{Label: "a"}, {Label: "b"}}}
	multi := &engine.Ask{MultiPick: true, Options: []engine.AskOption{{Label: "a"}, {Label: "b"}, {Label: "c"}}}
	tests := []struct {
		name   string
		ask    *engine.Ask
		cursor int
		picked map[int]bool
		want   string
	}{
		{"cursor on an option", single, 1, nil, "b"},
		{"cursor on the chat row", single, 2, nil, ""},
		{"negative cursor", single, -1, nil, ""},
		{"multi joins picks in option order", multi, 0, map[int]bool{2: true, 0: true}, "a, c"},
		{"a picked chat row adds nothing", multi, 1, map[int]bool{3: true}, "b"},
		{"multi with nothing picked takes the cursor", multi, 2, nil, "c"},
	}
	for _, tt := range tests {
		if got := AnswerText(tt.ask, tt.cursor, tt.picked); got != tt.want {
			t.Errorf("%s: AnswerText = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGateAnswerCrosses(t *testing.T) {
	gate := &engine.Ask{Gate: true}
	if !GateAnswerCrosses(gate, engine.GateAdvanceLabel) {
		t.Error("the gate's advance option did not cross")
	}
	if GateAnswerCrosses(gate, "not yet") || GateAnswerCrosses(&engine.Ask{}, engine.GateAdvanceLabel) || GateAnswerCrosses(nil, engine.GateAdvanceLabel) {
		t.Error("something other than a gate ask's advance option crossed")
	}
}

// TestDecisionPermissionRanksWithAsk: a guarded tool call's decision rides
// the ask lane — it ranks where an ask ranks, and its options are
// gummi's own approve/deny wording, with the free-form row after them
// like every ask has.
func TestDecisionPermissionRanksWithAsk(t *testing.T) {
	ask := &engine.Ask{Question: "Allow bash — make test?", Options: engine.PermissionAskOptions()}
	opts := AskOptions(ask)
	if len(opts) != 3 {
		t.Fatalf("options = %+v, want approve, deny and the chat row", opts)
	}
	if opts[0].Label != engine.PermissionApproveLabel || opts[1].Label != engine.PermissionDenyLabel {
		t.Errorf("options = %+v, want approve then deny", opts[:2])
	}
	if opts[0].Chat || opts[1].Chat || !opts[2].Chat {
		t.Errorf("chat row = %+v, want it on the appended row alone", opts)
	}
	// the ruling is the option's own label
	if got := AnswerText(ask, 0, nil); got != engine.PermissionApproveLabel {
		t.Errorf("approve answer = %q", got)
	}
	if got := AnswerText(ask, 1, nil); got != engine.PermissionDenyLabel {
		t.Errorf("deny answer = %q", got)
	}
	// it ranks in the ask lane: a question lane decision, ahead of a
	// gate's, and the attention lane is the question's
	decs := []state.OpenDecision{
		{ID: "gate", Kind: state.DecisionKindGate, Question: "advance?"},
		{ID: "perm", Kind: state.DecisionKindAsk, Question: ask.Question},
	}
	winner, ok := Rank(decs)
	if !ok || winner.ID != "perm" {
		t.Errorf("Rank = %+v (ok=%v), want the permission ahead of the gate", winner, ok)
	}
	lane, escalated, ok := Attention(string(state.DecisionKindAsk))
	if !ok || lane != LaneQuestion || escalated {
		t.Errorf("Attention = (%v, %v, %v), want the question lane", lane, escalated, ok)
	}
	// a gate ask and a permission can be told apart: a permission is
	// never a gate crossing
	if GateAnswerCrosses(ask, engine.PermissionApproveLabel) {
		t.Error("an approve ruling crossed a gate")
	}
}
