package ui

// The decision receipt: what a card decided on its own while nobody was
// watching, folded into the thread between the live stage and the next
// card (thread.go's slot). It reports only autopilot's own choices —
// which design gates it crossed by itself, which questions it answered
// itself, how many corrective rounds it burned, and what that cost —
// never the work itself (the folded stage receipts above it already
// cover that) and never an action of its own (the next card below owns
// those; nothing here is offered twice). It renders nothing at all for a
// card that never ran itself: a row of zeroes would be worse than no
// row.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// humanGateActors are the ways a person crosses a gate themselves: "user"
// is the TUI's g, "caller" is the headless driver's attended mode. The
// receipt counts everything else as a crossing the card made on its own.
//
// It is written this way round deliberately. The machine actors are
// open-ended — "auto" is the driver's unattended loop, "review" is the
// automatic review→fix→verify chain, and any future loop names itself —
// so enumerating those is how the count silently starts under-reporting
// the day one is added. The human set is bounded by how a person can
// actually reach a gate, and that is the smaller, more stable thing to
// name.
var humanGateActors = map[string]bool{"user": true, "caller": true}

// receiptGate is one design gate autopilot crossed on its own.
type receiptGate struct {
	from domain.Stage
	at   time.Time
}

// receiptAnswer is one ask_user question autopilot answered on its own.
type receiptAnswer struct {
	answer string
	at     time.Time
}

// decisionReceipt is everything the receipt needs to decide whether it
// has something to report and, if so, render it.
type decisionReceipt struct {
	gates   []receiptGate
	answers []receiptAnswer
	// corrective/correctiveMax are internal/rounds' own
	// domain.RoundKindCorrective count and internal/verdict's cap for it
	// (rule 5) — read by the caller from the same in-memory cache
	// thread.go's own round badge already uses (Shell.round), so building
	// the receipt stays IO-free like every other thread render.
	corrective    int
	correctiveMax int
	// credits is the stage_spend rollup's total (rule 4: never re-derived
	// from the event log — stage_spend is the meter of record).
	credits  float64
	envelope int
}

// buildDecisionReceipt reads a card's event log for what autopilot
// decided on its own: gates whose actor was the unattended driver loop
// (any actor but a human), and ask_user answers taken automatically
// (state.ActorAutopilot) — never a gate a human crossed or an answer a
// human typed (rule 6, and the ask/actor split in
// internal/engine/asktool.go's Answer). events may be nil (not loaded
// yet, or none recorded); spend may be nil (no rollup loaded yet).
func buildDecisionReceipt(events []state.CardEvent, spend map[domain.Stage]float64, envelope, corrective, correctiveMax int) decisionReceipt {
	r := decisionReceipt{envelope: envelope, corrective: corrective, correctiveMax: correctiveMax}
	for _, ev := range events {
		switch ev.Kind {
		case state.EventGate:
			var p state.GatePayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || humanGateActors[p.Actor] {
				continue
			}
			r.gates = append(r.gates, receiptGate{from: domain.Stage(p.From), at: ev.At})
		case state.EventAsk:
			var p state.AskPayload
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || p.Actor != state.ActorAutopilot {
				continue
			}
			r.answers = append(r.answers, receiptAnswer{answer: p.Answer, at: ev.At})
		}
	}
	for _, c := range spend {
		r.credits += c
	}
	return r
}

// empty reports whether autopilot decided nothing worth showing (rule
// 3): a card with no autopilot-crossed gate, no autopilot-taken answer,
// and no corrective round spent renders no box at all, ever — credits
// alone (a normal agent stage spends them too, autopilot or not) are
// never enough on their own to earn the receipt a place on screen.
func (r decisionReceipt) empty() bool {
	return len(r.gates) == 0 && len(r.answers) == 0 && r.corrective == 0
}

// decisionReceiptBlock renders the receipt: a heading and one line per
// non-zero row — each row gated on its own count so a card that crossed
// gates but never answered a question (or vice versa) never shows a row
// of zeroes for the other. Nil when r.empty().
func decisionReceiptBlock(s *theme.Styles, r decisionReceipt) []string {
	if r.empty() {
		return nil
	}
	lines := []string{s.Subtitle.Render("what it decided while you were away")}
	if n := len(r.gates); n > 0 {
		parts := make([]string, n)
		for i, g := range r.gates {
			parts[i] = string(g.from) + " " + g.at.Format("15:04")
		}
		lines = append(lines, "  "+s.Subtle.Render("crossed "+itoa(n)+" gate"+plural(n))+
			s.Faint.Render(" — "+strings.Join(parts, " · ")))
	}
	if n := len(r.answers); n > 0 {
		last := r.answers[len(r.answers)-1]
		lines = append(lines, "  "+s.Subtle.Render("took "+itoa(n)+" answer"+plural(n))+
			s.Faint.Render(" — \""+sanitize(last.answer)+"\" "+last.at.Format("15:04")))
	}
	if r.corrective > 0 {
		lines = append(lines, "  "+s.Subtle.Render(itoa(r.corrective)+" correction"+plural(r.corrective))+
			s.Faint.Render(fmt.Sprintf(" — %d of %d spent", r.corrective, r.correctiveMax)))
	}
	if r.credits > 0 {
		label := fmt.Sprintf("%g credits", roundSpend(r.credits))
		line := "  " + s.Subtle.Render(label)
		if r.envelope > 0 {
			line += s.Faint.Render(fmt.Sprintf(" — of a %d envelope", r.envelope))
		}
		lines = append(lines, line)
	}
	return lines
}

// plural is "" for exactly one, "s" otherwise — the receipt's own rows
// are the only place in the thread that needs it.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
