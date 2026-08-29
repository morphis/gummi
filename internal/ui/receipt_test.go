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

// gateEvent/askEvent build a synthetic card_events row of the shape
// advance.go/asktool.go now write, for buildDecisionReceipt to read back.
func gateEvent(from, to domain.Stage, actor string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.GatePayload{From: string(from), To: string(to), Actor: actor})
	return state.CardEvent{Kind: state.EventGate, Stage: from, At: at, Payload: string(p)}
}

func askEvent(question, answer, actor string, at time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AskPayload{Question: question, Answer: answer, Actor: actor})
	return state.CardEvent{Kind: state.EventAsk, At: at, Payload: string(p)}
}

// TestBuildDecisionReceiptCountsOnlyAutopilot is rule 6 (gates) and the
// ask actor split (asktool.go's Answer): a gate a human crossed, or an
// answer a human typed, must never be counted as something autopilot
// decided while nobody was watching.
func TestBuildDecisionReceiptCountsOnlyAutopilot(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)
	events := []state.CardEvent{
		gateEvent(domain.StageSpec, domain.StagePlan, "auto", t0),
		gateEvent(domain.StagePlan, domain.StageImplement, "user", t0.Add(time.Minute)),
		gateEvent(domain.StageVerify, domain.StageDone, "caller", t0.Add(2*time.Minute)),
		askEvent("buffer or stream?", "stream rows, don't buffer", state.ActorAutopilot, t0.Add(3*time.Minute)),
		askEvent("which port?", "8080", state.ActorUser, t0.Add(4*time.Minute)),
	}
	r := buildDecisionReceipt(events, nil, 0, 0, 0)
	if len(r.gates) != 1 || r.gates[0].from != domain.StageSpec {
		t.Fatalf("gates = %+v, want exactly the one auto-crossed gate (spec)", r.gates)
	}
	if len(r.answers) != 1 || r.answers[0].answer != "stream rows, don't buffer" {
		t.Fatalf("answers = %+v, want exactly the one autopilot-taken answer", r.answers)
	}
}

// TestBuildDecisionReceiptCreditsFromSpendRollup is rule 4: the total is
// summed from the stage_spend rollup handed in, never derived from the
// event log — an event log with no stage_exit/credits payload at all
// still produces the right total as long as the rollup carries it.
func TestBuildDecisionReceiptCreditsFromSpendRollup(t *testing.T) {
	spend := map[domain.Stage]float64{domain.StageImplement: 400, domain.StageReview: 212}
	r := buildDecisionReceipt(nil, spend, 2400, 0, 0)
	if r.credits != 612 {
		t.Fatalf("credits = %g, want 612 (400+212 from the rollup)", r.credits)
	}
}

// TestDecisionReceiptEmptyRendersNothing is rule 3: a card that never
// ran itself — no autopilot gate, no autopilot answer, no corrective
// round spent — gets no box at all, not an empty one, even with credits
// and an envelope on record (an ordinary human-driven stage spends
// credits too).
func TestDecisionReceiptEmptyRendersNothing(t *testing.T) {
	r := buildDecisionReceipt(
		[]state.CardEvent{gateEvent(domain.StageSpec, domain.StagePlan, "user", time.Now())},
		map[domain.Stage]float64{domain.StageImplement: 40}, 2400, 0, 5)
	if !r.empty() {
		t.Fatalf("receipt = %+v, want empty (only a user-crossed gate and ordinary spend)", r)
	}
	if got := decisionReceiptBlock(m0Styles(), r); got != nil {
		t.Fatalf("decisionReceiptBlock(empty) = %v, want nil", got)
	}
}

// TestDecisionReceiptBlockRendersEachRow checks every row the mockup
// promises, gated on its own count: gates named by the stage crossed
// (from) with a time, the most recent autopilot answer quoted, the
// corrective count against verdict's cap (never hardcoded), and credits
// against the envelope.
func TestDecisionReceiptBlockRendersEachRow(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)
	events := []state.CardEvent{
		gateEvent(domain.StageSpec, domain.StagePlan, "auto", t0),
		gateEvent(domain.StagePlan, domain.StageImplement, "auto", t0.Add(18*time.Minute)),
		gateEvent(domain.StageVerify, domain.StageDone, "auto", t0.Add(4*time.Hour+32*time.Minute)),
		askEvent("buffer or stream?", "stream rows, don't buffer", state.ActorAutopilot, t0.Add(34*time.Minute)),
	}
	spend := map[domain.Stage]float64{domain.StageImplement: 400, domain.StageReview: 212}
	r := buildDecisionReceipt(events, spend, 2400, 2, 5)
	block := decisionReceiptBlock(m0Styles(), r)
	out := ansi.Strip(strings.Join(block, "\n"))

	for _, want := range []string{
		"what it decided while you were away",
		"crossed 3 gates", "spec 23:40", "plan 23:58", "verify 04:12",
		"took 1 answer", `"stream rows, don't buffer"`,
		"2 corrections", "2 of 5 spent",
		"612 credits", "of a 2400 envelope",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("decisionReceiptBlock missing %q in:\n%s", want, out)
		}
	}
}

// TestDecisionReceiptBlockOmitsZeroRows: a receipt with something to
// report in one dimension still withholds every other row that has
// nothing — no "0 corrections", no credits line with nothing metered.
func TestDecisionReceiptBlockOmitsZeroRows(t *testing.T) {
	events := []state.CardEvent{
		gateEvent(domain.StageSpec, domain.StagePlan, "auto", time.Now()),
	}
	r := buildDecisionReceipt(events, nil, 2400, 0, 5)
	out := ansi.Strip(strings.Join(decisionReceiptBlock(m0Styles(), r), "\n"))
	for _, notWant := range []string{"answer", "correction", "credits"} {
		if strings.Contains(out, notWant) {
			t.Errorf("decisionReceiptBlock rendered a zero row (%q) it should have withheld:\n%s", notWant, out)
		}
	}
}

// TestThreadDecisionReceiptGolden is the end-to-end render: a card page
// whose event log carries an autopilot gate crossing and an
// autopilot-taken answer, with corrective rounds and a stage_spend
// rollup set on the row — the receipt slot in thread.go should render
// between the live stage and the next card.
func TestThreadDecisionReceiptGolden(t *testing.T) {
	m := populatedShell(120, 34)
	id := m.rows[m.sel].F.ID
	m.rows[m.sel].F.Budget.Envelope = 2400
	m.rows[m.sel].StageSpend = []state.StageSpend{
		{Stage: domain.StageImplement, Credits: 400},
		{Stage: domain.StageReview, Credits: 212},
	}
	// the header's running total and the receipt's total describe the same
	// spend from two stores that the engine keeps in step, so the fixture
	// keeps them in step too — a golden showing a card that has spent both
	// 0 and 612 credits would teach the next reader a contradiction.
	m.rows[m.sel].F.Spend.Credits = 612
	m.setRound(id, domain.RoundKindCorrective, 2)

	t0 := time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)
	m.cardEvents[id] = []state.CardEvent{
		gateEvent(domain.StageSpec, domain.StagePlan, "auto", t0),
		askEvent("buffer or stream?", "stream rows, don't buffer", state.ActorAutopilot, t0.Add(34*time.Minute)),
	}

	golden.RequireEqual(t, []byte(ansi.Strip(m.threadView(116, 40))))
}

// The review loop crosses stages under its own actor ("review"), not the
// driver's "auto", and it does so without a human present — so those
// crossings belong in the receipt. Counting only "auto" was how the
// count came to miss exactly the crossings a card makes while running
// itself under the TUI.
func TestBuildDecisionReceiptCountsAutomaticLoopCrossings(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)
	r := buildDecisionReceipt([]state.CardEvent{
		gateEvent(domain.StageReview, domain.StageVerify, "review", t0),
	}, nil, 0, 0, 0)
	if len(r.gates) != 1 {
		t.Fatalf("gates = %+v, want the review loop's own crossing counted", r.gates)
	}
}
