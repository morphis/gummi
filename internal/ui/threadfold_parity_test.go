package ui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
)

// TestThreadfoldItemsSayWhatTheThreadSays holds the web face's item list
// to the TUI's own rendering of the same rows: every receipt threadfold
// hands a page carries the sentence this thread prints for that event,
// and every stretch rule the label the rule draws. Two faces of one card
// that word the same stop differently would be two things to learn.
func TestThreadfoldItemsSayWhatTheThreadSays(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	var evs []state.CardEvent
	add := func(stage domain.Stage, kind string, payload any) {
		raw, _ := json.Marshal(payload)
		at = at.Add(time.Minute)
		evs = append(evs, state.CardEvent{Seq: int64(len(evs) + 1), Feature: "FD-001", Stage: stage, Kind: kind, At: at, Payload: string(raw)})
	}
	plan, impl := domain.StagePlan, domain.StageImplement
	add(plan, state.EventStageEnter, threadfold.StageEnterPayload{Role: "architect", Model: "m"})
	add(plan, state.EventDecisionOpen, state.DecisionPayload{ID: "old", Kind: state.DecisionKindBudget, Question: "Out of credits"})
	add(plan, state.EventDecisionOpen, state.DecisionPayload{ID: "g1", Kind: state.DecisionKindGate, Question: "Approve?"})
	add(plan, state.EventGate, state.GatePayload{From: "plan", To: "implement", Actor: "user", ID: "g1"})
	add(impl, state.EventStageEnter, threadfold.StageEnterPayload{Role: "implementer", Model: "m"})
	add(impl, state.EventAsk, state.AskPayload{Question: "Persist where?", Answer: "per-device", Actor: "user", By: "user"})
	add(impl, state.EventAutopilot, state.AutopilotPayload{Mode: "full"})
	add(impl, state.EventAutopilot, state.AutopilotPayload{Event: state.AutopilotTookOver, Mode: "autopilot"})
	add(impl, state.EventGate, state.GatePayload{From: "implement", To: "verify", Actor: "autopilot"})
	add(impl, state.EventPark, state.ParkPayload{Reason: state.ParkReasonNeedsYou, Detail: "verify failed"})
	add(impl, state.EventPark, state.ParkPayload{Reason: state.ParkReasonGaveUp})
	add(impl, state.EventRebase, state.RebasePayload{Onto: "main", By: state.PersonActor("Simon")})
	add(impl, state.EventPause, state.PausePayload{By: state.ActorUser})

	s := m0Styles()
	answered := threadfold.AnsweredDecisions(evs)
	stretches := threadfold.Stretches(evs)
	bySeq := map[int64]int{}
	for i, ev := range evs {
		bySeq[ev.Seq] = i
	}
	receipts := 0
	for _, it := range threadfold.Items(evs, threadfold.Options{Live: true}) {
		switch it.T {
		case threadfold.ItemReceipt:
			receipts++
			// a receipt's key is its own event's; its Seq can be later (a
			// superseded decision is dated to what superseded it)
			seq, _ := strconv.ParseInt(strings.TrimPrefix(it.Key, "ev:"), 10, 64)
			i := bySeq[seq]
			ev := evs[i]
			tui := stretchDecisionLine(s, ev, threadfold.InStretch(stretches, i), 200)
			if tui == "" {
				tui = stageEventLine(s, ev, 200, "", answered)
			}
			tui = strings.TrimSpace(ansi.Strip(tui))
			if !strings.Contains(tui, it.Receipt.Text) {
				t.Errorf("seq %d: the thread prints %q, the item says %q", it.Seq, tui, it.Receipt.Text)
			}
		case threadfold.ItemStretch:
			var rule string
			st := stretches[0]
			if it.Stretch.Edge == "open" {
				rule = stretchOpenLine(s, st, 200)
			} else {
				rule = stretchCloseLines(s, st, 200)[0]
			}
			if !strings.Contains(ansi.Strip(rule), it.Stretch.Label) {
				t.Errorf("rule %q does not carry the item's label %q", ansi.Strip(rule), it.Stretch.Label)
			}
		}
	}
	// the superseded budget, the gate, the ask, the mode change, autopilot's
	// crossing, the second park (the first closes the stretch and is said
	// by its rule), the rebase and the pause
	if receipts != 8 {
		t.Errorf("got %d receipts, want 8", receipts)
	}
}
