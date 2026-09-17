package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// statsRender renders the surface and strips styling, so the assertions
// below are about what the reader is told rather than about colour.
func statsRender(r cardrun.Run) string {
	lines := statsLines(theme.New(theme.GummiDark()), r, 100)
	return ansi.Strip(strings.Join(lines, "\n"))
}

// bounced is the shape the whole surface exists for: a card that did the
// same work twice, where the second attempt cost more than the first.
func bounced() cardrun.Run {
	pass := func(stage domain.Stage, role, flavor string, mins int, credits float64, redo bool, reason string) cardrun.Session {
		start := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		return cardrun.Session{
			Stage: stage, Role: role, Flavor: flavor, Turns: 20,
			Started: start, Ended: start.Add(time.Duration(mins) * time.Minute), Closed: true,
			Credits: credits, Redo: redo, RedoReason: reason,
		}
	}
	return cardrun.Run{
		ID: "BG-004", Title: "the rerun edge is unreachable", Stage: domain.StageDone,
		Sessions: []cardrun.Session{
			pass(domain.StageImplement, "implementer", "stage", 44, 12.24, false, ""),
			pass(domain.StageImplement, "reviewer", "critique", 28, 3.41, false, ""),
			pass(domain.StageImplement, "implementer", "stage", 32, 12.51, true, cardrun.Corrected),
		},
		Money: cardrun.Money{
			Credits: 28.16, FirstPass: 15.65, Rework: 12.51, Corrected: 12.51,
			ByStage: []cardrun.Bucket{{Name: "implement", Credits: 28.16}},
		},
		Clock:    cardrun.Clock{Agent: 104 * time.Minute, Elapsed: 300 * time.Minute, Waiting: 196 * time.Minute},
		Envelope: cardrun.Envelope{Granted: 1350, Spent: 28.16},
	}
}

// The redo gets a block of its own, and the comparison it exists to make
// is on the page: a report that leaves the reader to notice for themselves
// that the second attempt cost more has buried its own headline.
func TestRunViewNamesTheRedoAndItsCost(t *testing.T) {
	out := statsRender(bounced())

	if !strings.Contains(out, "THE REDO") {
		t.Fatalf("no redo block on a card that did the work twice:\n%s", out)
	}
	if !strings.Contains(out, "cost more than the first") {
		t.Errorf("the redo cost 12.51 against 12.24 and the surface does not say so:\n%s", out)
	}
	if !strings.Contains(out, "12.51 of 28.16 credits was work already done (44%)") {
		t.Errorf("the rework total is missing or wrong:\n%s", out)
	}
	// and the split is on the headline row too, where the money is
	if !strings.Contains(out, "first pass 15.65") || !strings.Contains(out, "redone 12.51 (44%)") {
		t.Errorf("the spend split is missing from the money block:\n%s", out)
	}
}

// A card that never did anything twice gets no redo block at all. The
// block is the exception, not a row that renders empty.
func TestRunViewOmitsTheRedoBlockWhenNothingWasRedone(t *testing.T) {
	r := bounced()
	for i := range r.Sessions {
		r.Sessions[i].Redo = false
		r.Sessions[i].RedoReason = ""
	}
	r.Money.Rework, r.Money.Corrected = 0, 0
	r.Money.FirstPass = r.Money.Credits

	out := statsRender(r)
	if strings.Contains(out, "THE REDO") {
		t.Errorf("a clean card was given a redo block:\n%s", out)
	}
	if strings.Contains(out, "redone") {
		t.Errorf("a clean card was told about rework it did not do:\n%s", out)
	}
}

// Where a backend cannot report something, the surface says so in the
// sentence the number would have taken. Three of gummi's six backends
// report no tool outcomes; rendering that as "0 calls" would be a claim
// about the card that only holds about the backend.
func TestRunViewSaysWhenTheBackendRecordedNoTools(t *testing.T) {
	out := statsRender(bounced())
	if !strings.Contains(out, "none recorded — this backend reports no tool calls") {
		t.Errorf("an absent tool record was not named as absent:\n%s", out)
	}
	if strings.Contains(out, "0 calls") {
		t.Errorf("an absent tool record rendered as a zero:\n%s", out)
	}

	r := bounced()
	r.Hands.Tools = []cardrun.ToolUse{{Name: "Bash", Calls: 3, Fails: 1, Total: 2 * time.Second}}
	r.Hands.ToolCalls, r.Hands.ToolFails = 3, 1
	out = statsRender(r)
	if !strings.Contains(out, "3 calls, 1 failed") {
		t.Errorf("a present tool record was not counted:\n%s", out)
	}
	if strings.Contains(out, "none recorded") {
		t.Errorf("a card with tool calls was told it had no record:\n%s", out)
	}
}

// A figure read off a pass's stage_exit rather than measured per usage
// sample is marked. A reconstruction that looks like a measurement is the
// one thing a ledger must never be.
func TestRunViewMarksReconstructedFigures(t *testing.T) {
	r := bounced()
	r.Sessions[2].Reconstructed = true
	if out := statsRender(r); !strings.Contains(out, "12.51 ~") {
		t.Errorf("a reconstructed figure is presented as measured:\n%s", out)
	}
	if out := statsRender(bounced()); strings.Contains(out, "12.51 ~") {
		t.Errorf("a measured figure was marked as reconstructed:\n%s", out)
	}
}

// An unsettled credit figure reads as unsettled without a footnote: a
// number a provider may still correct has to look like one.
func TestRunViewMarksEstimatedSpend(t *testing.T) {
	r := bounced()
	r.Money.Estimated = 4.5
	if out := statsRender(r); !strings.Contains(out, "~4.50 estimated") {
		t.Errorf("estimated spend was not marked:\n%s", out)
	}
	if out := statsRender(bounced()); strings.Contains(out, "estimated") {
		t.Errorf("a fully metered card was told part of it was an estimate:\n%s", out)
	}
}

// A card nothing has run on says so rather than rendering a page of
// zeroes with bars of nothing.
func TestRunViewEmptyCard(t *testing.T) {
	out := statsRender(cardrun.Run{ID: "FD-009", Title: "not started", Stage: domain.StageTodo})
	if !strings.Contains(out, "nothing has run on this card yet") {
		t.Fatalf("an unrun card was not named as unrun:\n%s", out)
	}
	for _, unwanted := range []string{"WHERE IT WENT", "THE CLOCK", "THE ENVELOPE"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("an unrun card was given the %q block:\n%s", unwanted, out)
		}
	}
}

// A span under a second says so rather than rounding to "0s": a zero
// beside a nonzero share reads as a broken number.
func TestRunViewSubSecondDurations(t *testing.T) {
	if got := shortDur(300 * time.Millisecond); got != "<1s" {
		t.Errorf("shortDur(300ms) = %q, want %q", got, "<1s")
	}
	if got := shortDur(0); got != "—" {
		t.Errorf("shortDur(0) = %q, want an em dash for nothing measured", got)
	}
	if got := shortDur(90 * time.Minute); got != "1h30m" {
		t.Errorf("shortDur(90m) = %q, want %q", got, "1h30m")
	}
}

// The tab exists for a card with a record and not for one without, and the
// bar and the chord agree about which is which.
func TestRunTabOfferedOnlyWithARecord(t *testing.T) {
	todo := featureRow{F: domain.Feature{ID: "FD-001", Stage: domain.StageTodo}}
	if statsHasRecord(todo) {
		t.Error("a card still in todo with no spend was offered a run tab")
	}
	spent := todo
	spent.F.Spend.Credits = 2
	if !statsHasRecord(spent) {
		t.Error("a todo card that has already spent credits was denied its run tab")
	}
	running := featureRow{F: domain.Feature{ID: "FD-001", Stage: domain.StageImplement}}
	if !statsHasRecord(running) {
		t.Error("a running card was denied its run tab")
	}
}
