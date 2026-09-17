package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// "Seven landed, two kept, one given up on" is the answer a week has.
// "Ten cards" is not.

func TestWeekGroupsByEndingNotByCount(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	rep := weekReport{
		ByEnding: map[domain.Ending]int{
			domain.EndingLanded:    2,
			domain.EndingHandedOff: 1,
			domain.EndingDropped:   1,
		},
		Spend:  31.8,
		Rework: 6, Redone: 4, Open: 1,
		Cards: []weekCard{
			{ID: "FD-107", Title: "drop reason", Ending: domain.EndingLanded, Commit: "9f2c1ab4d5e6f7", Spend: 9.62},
			{ID: "FD-104", Title: "scratch tree", Ending: domain.EndingHandedOff, Spend: 1.2},
		},
		Costliest: weekCard{ID: "FD-107", Spend: 9.62},
		Cheapest:  weekCard{ID: "FD-104", Spend: 1.2},
	}
	d := &weekDialog{report: &rep}
	got := stripANSI(d.View(m.styles, 100, 40))
	for _, want := range []string{
		"landed", "handed off", "dropped",
		"branches kept, nothing merged",
		"6 rounds across 4 cards",
		"still in flight",
		"costliest", "FD-107",
		"9f2c1ab4d5e6",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("week view is missing %q:\n%s", want, got)
		}
	}
}

func TestWeekSaysSoWhenNothingSettled(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	d := &weekDialog{report: &weekReport{ByEnding: map[domain.Ending]int{}}}
	got := stripANSI(d.View(m.styles, 80, 24))
	if !strings.Contains(got, "nothing has settled") {
		t.Fatalf("an empty week says so plainly: %s", got)
	}
}

// The window is what makes it a review rather than a second archive.
func TestWeekWindowExcludesOlderCards(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		settledRow("FD-002", 2*24*time.Hour, false),
		settledRow("FD-003", 30*24*time.Hour, false),
	}
	msg, ok := m.measureWeek()().(weekReportMsg)
	if !ok {
		t.Fatal("measureWeek did not report")
	}
	if len(msg.report.Cards) != 1 || msg.report.Cards[0].ID != "FD-002" {
		t.Fatalf("cards = %+v, want only the recent one", msg.report.Cards)
	}
}
