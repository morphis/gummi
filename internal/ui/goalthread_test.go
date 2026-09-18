package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/state"
)

// goalEvent is one row of a goal's log as the store writes it: kind
// "goal" on the goal card itself, at whatever stage the goal stood at.
func goalEvent(t *testing.T, p state.GoalPayload) state.CardEvent {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("encoding goal payload: %v", err)
	}
	return state.CardEvent{Kind: state.EventGoal, Payload: string(payload)}
}

// A goal's log entries live on the goal card, so they land in the
// thread's implement block alongside the conversation. Each one has to
// say what it was — the whole log rendered as a column of the bare word
// "goal", which is what stageEventLine's default arm prints for a kind
// it has no renderer for.
func TestGoalLogEntryReadsAsASentenceInTheThread(t *testing.T) {
	s := m0Styles()
	ev := goalEvent(t, state.GoalPayload{
		Action: state.GoalLanded,
		Card:   "BG-003",
		Detail: "sort comparator no longer overflows",
		By:     "goal",
	})

	got := ansi.Strip(stageEventLine(s, ev, 80, "lead", map[string]bool{}))

	if strings.TrimSpace(got) == state.EventGoal {
		t.Fatalf("a goal log entry still renders as the bare kind name: %q", got)
	}
	for _, want := range []string{"landed", "BG-003", "sort comparator no longer overflows"} {
		if !strings.Contains(got, want) {
			t.Errorf("goal log line %q is missing %q", got, want)
		}
	}
}

// A decision for review is named D-N everywhere it is printed, and the
// thread is no exception.
func TestGoalDecisionLineCarriesItsNumber(t *testing.T) {
	s := m0Styles()
	ev := goalEvent(t, state.GoalPayload{
		Action:      state.GoalDecision,
		N:           2,
		Detail:      "kept the int64 fast path",
		Alternative: "promote everything to big.Int",
		By:          "lead",
	})

	got := ansi.Strip(stageEventLine(s, ev, 80, "lead", map[string]bool{}))

	if !strings.Contains(got, "D-2") {
		t.Fatalf("a decision for review lost its number: %q", got)
	}
}

// The checks entry's Detail is the verify stage's check results as JSON.
// The goal page unpacks it; a line of prose must not spill it.
func TestGoalChecksEntryKeepsItsJSONOutOfTheLine(t *testing.T) {
	s := m0Styles()
	ev := goalEvent(t, state.GoalPayload{
		Action: state.GoalChecks,
		Detail: `[{"item":"DW-1","ok":false,"cmd":"make test"}]`,
	})

	got := ansi.Strip(stageEventLine(s, ev, 80, "lead", map[string]bool{}))

	if strings.Contains(got, "{") || strings.Contains(got, "DW-1") {
		t.Fatalf("the checks entry spilled its JSON into the thread: %q", got)
	}
	if !strings.Contains(got, state.GoalChecks) {
		t.Fatalf("the checks entry lost its name: %q", got)
	}
}

// A payload that does not decode, or carries no action, still has to
// leave a row — falling back to the kind name is a poor line but an
// honest one, and DESIGN §10.18 wants nothing to happen unrecorded.
func TestUnreadableGoalPayloadStillLeavesARow(t *testing.T) {
	s := m0Styles()
	ev := state.CardEvent{Kind: state.EventGoal, Payload: "not json"}

	if got := ansi.Strip(stageEventLine(s, ev, 80, "lead", map[string]bool{})); strings.TrimSpace(got) == "" {
		t.Fatal("a goal entry that cannot be decoded left no row at all")
	}
}

// The guard that stops this class of bug coming back: every event kind
// the thread is asked to render needs an arm of its own. The default arm
// prints the raw kind name, which is how a goal's whole log came to read
// "goal" 58 times — so no kind here may reach it.
//
// stage_enter, stage_exit and tool_result never arrive at
// stageEventLine: the first two are consumed by the segmenter
// (threadSegments) that draws the session rules, and a tool_result is
// folded into the tool row it belongs to (stageEventLines' Output arm).
func TestEveryRenderedEventKindHasItsOwnArm(t *testing.T) {
	s := m0Styles()
	structural := map[string]bool{
		state.EventStageEnter: true,
		state.EventStageExit:  true,
		state.EventToolResult: true,
	}
	payloads := map[string]any{
		state.EventMessage:      messagePayload{Author: "assistant", Content: "verdict: pass"},
		state.EventTool:         toolPayload{Label: "Bash  make test"},
		state.EventAsk:          state.AskPayload{Question: "ship it?", Answer: "yes", Actor: state.ActorUser},
		state.EventGate:         state.GatePayload{From: "plan", To: "implement", Actor: state.ActorUser},
		state.EventPark:         state.ParkPayload{Reason: state.ParkReasonNeedsYou},
		state.EventDecisionOpen: state.DecisionPayload{ID: "d1", Kind: "gate", Question: "ship it?"},
		state.EventAutopilot:    state.AutopilotPayload{Mode: "autopilot"},
		state.EventGoal:         state.GoalPayload{Action: state.GoalLanded, Card: "BG-003"},
	}
	for _, kind := range []string{
		state.EventMessage, state.EventTool, state.EventToolResult,
		state.EventStageEnter, state.EventStageExit, state.EventGate,
		state.EventAsk, state.EventAutopilot, state.EventPark,
		state.EventDecisionOpen, state.EventGoal,
	} {
		if structural[kind] {
			continue
		}
		p, ok := payloads[kind]
		if !ok {
			t.Fatalf("event kind %q has no sample payload here — add one, and an arm in stageEventLine", kind)
		}
		payload, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("encoding %q payload: %v", kind, err)
		}
		ev := state.CardEvent{Kind: kind, Payload: string(payload)}
		got := ansi.Strip(stageEventLine(s, ev, 80, "implementer", map[string]bool{}))
		if strings.TrimSpace(got) == kind {
			t.Errorf("event kind %q falls through to the default arm and renders as its own name", kind)
		}
	}
}

// A raise and a re-estimated reserve are amount moves: the Detail beside
// them says why, so the line has to carry the numbers itself. A
// need-budget entry carries only what a card needs, which is not a move
// — printing it as one would claim the card had been funded with
// nothing.
func TestGoalAmountsShowOnlyWhenTheyAreAMove(t *testing.T) {
	s := m0Styles()
	raise := goalEvent(t, state.GoalPayload{
		Action: state.GoalRaised, Card: "BG-002", From: 1073, To: 1500,
		Detail: "verify redo after the cancel", By: "lead",
	})
	if got := ansi.Strip(stageEventLine(s, raise, 80, "lead", map[string]bool{})); !strings.Contains(got, "1073 → 1500") {
		t.Errorf("a raise lost its amounts: %q", got)
	}

	need := goalEvent(t, state.GoalPayload{
		Action: state.GoalNeedBudget, Card: "BG-004", To: 1500,
		Detail: "the envelope is spent", By: "goal",
	})
	if got := ansi.Strip(stageEventLine(s, need, 80, "lead", map[string]bool{})); strings.Contains(got, "→") {
		t.Errorf("a card that cannot be funded was drawn as an amount move: %q", got)
	}
}

// not-met, check-fixed and done-when all trade against a done-when item,
// and their Detail never names it — without the item the line says
// something was repaired but not what.
func TestGoalEntryNamesTheDoneWhenItemItIsAbout(t *testing.T) {
	s := m0Styles()
	ev := goalEvent(t, state.GoalPayload{
		Action: state.GoalCheckFixed, Item: "DW-1",
		Detail: "the check ran from the wrong tree", By: "lead",
	})

	if got := ansi.Strip(stageEventLine(s, ev, 80, "lead", map[string]bool{})); !strings.Contains(got, "DW-1") {
		t.Fatalf("a repaired check does not say which done-when item it was: %q", got)
	}
}
