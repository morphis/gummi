package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// A finished card used to say nothing at all: stageActions returned early
// for it and narrationStop refused it, so the one surface holding the
// commit, the branch's fate and the cost showed none of them. These tests
// pin the three halves of the fix — the answer set, the paragraph, and
// the decision that carries both.

func TestClosedCardOffersAnswersForItsEnding(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   nextInput
		want []string
	}{
		{
			// landed is the worktree-backed fact: it is only computed
			// where a worktree exists, so it is also "there is something
			// to clean up".
			"landed, worktree still there",
			nextInput{stage: domain.StageDone, ending: domain.EndingLanded, landed: true, hasWorktree: true},
			[]string{"newbug", "clean"},
		},
		{
			// nothing to clean up, so the row that would offer it is gone
			// rather than present and refusing.
			"landed and cleaned",
			nextInput{stage: domain.StageDone, ending: domain.EndingLanded},
			[]string{"newbug"},
		},
		{
			// m has always worked here. The card it happened to never said so.
			"handed off",
			nextInput{stage: domain.StageDone, ending: domain.EndingHandedOff},
			[]string{"merge", "newbug"},
		},
		{
			"dropped by its goal",
			nextInput{stage: domain.StageDone, ending: domain.EndingDropped, droppedBy: "GL-004"},
			[]string{"newbug"},
		},
		{
			// a goal's follow-up is not a bug card: its work is other cards.
			"a goal",
			nextInput{stage: domain.StageDone, kind: domain.KindGoal, ending: domain.EndingLanded},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, a := range stageActions(tc.in) {
				got = append(got, a.id)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("answers = %v, want %v", got, tc.want)
			}
		})
	}
}

// The landed commit has been recorded at every merge since SquashMerge
// first stamped it, and was shown by no surface in the TUI.
func TestClosingParagraphNamesTheCommitAndTheCost(t *testing.T) {
	in := nextInput{
		stage:   domain.StageDone,
		ending:  domain.EndingLanded,
		base:    "main",
		commit:  "9f2c1ab4d5e6f7a8b9c0",
		spend:   2.03,
		endedAt: time.Date(2026, 9, 16, 13, 2, 0, 0, time.UTC),
	}
	got := plainClaims(closingClaims(in))
	for _, want := range []string{"Landed on main", "9f2c1ab4d5e6", "gone"} {
		if !strings.Contains(got, want) {
			t.Fatalf("closing paragraph %q is missing %q", got, want)
		}
	}

	in.hasWorktree = true
	if !strings.Contains(plainClaims(closingClaims(in)), "clean up removes both") {
		t.Fatal("a landed card still holding a worktree must say so — that is the cleanup ask")
	}

	in.corrective = 2
	if got := plainClaims(closingClaims(in)); !strings.Contains(got, "2 rounds redone") {
		t.Fatalf("rework is the number worth reading beside the total: %q", got)
	}
}

func TestClosingParagraphNamesTheOtherTwoEndings(t *testing.T) {
	handed := nextInput{stage: domain.StageDone, ending: domain.EndingHandedOff, branch: "gummi/FD-042-x", base: "main"}
	if got := plainClaims(closingClaims(handed)); !strings.Contains(got, "gummi/FD-042-x is yours") ||
		!strings.Contains(got, "still available") {
		// landing a handed-off card has always been legal and was never offered
		t.Fatalf("hand-off paragraph = %q", got)
	}
	dropped := nextInput{stage: domain.StageDone, ending: domain.EndingDropped, droppedBy: "GL-004", branch: "gummi/FD-109-y"}
	if got := plainClaims(closingClaims(dropped)); !strings.Contains(got, "Dropped by GL-004") {
		t.Fatalf("a drop names the goal that made it: %q", got)
	}
}

// The cost half of invariant 4, stated where the closing block can break
// it: a finished card's paragraph is a pure function of its own record.
func TestClosingParagraphIsFree(t *testing.T) {
	in := nextInput{stage: domain.StageDone, ending: domain.EndingLanded, spend: 1}
	if approveShaped(in) {
		t.Fatal("a closed card must never be worth a model turn")
	}
	if !narrationStop(in) {
		t.Fatal("a closed card has one thing to say and must be allowed to say it")
	}
}

// doneAt reads the LAST done edge: a card can be closed, adopted back and
// closed again, and the date on screen is the current ending's.
func TestDoneAtTakesTheLatestClosing(t *testing.T) {
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	last := time.Date(2026, 9, 16, 13, 2, 0, 0, time.UTC)
	hist := []state.TransitionRecord{
		{From: domain.StageVerify, To: domain.StageDone, At: first},
		{From: domain.StageDone, To: domain.StageVerify, At: first.Add(time.Hour)},
		{From: domain.StageVerify, To: domain.StageDone, At: last},
	}
	if got := doneAt(hist); !got.Equal(last) {
		t.Fatalf("doneAt = %v, want the latest closing %v", got, last)
	}
	if got := doneAt(nil); !got.IsZero() {
		t.Fatalf("no history, no date: %v", got)
	}
}

// The decision is what puts the paragraph and the answers on the page at
// all. A closed card used to be excluded from it by name.
func TestClosedCardRaisesItsOwnDecision(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	row := m.rows[m.sel]
	row.F.Stage = domain.StageDone
	row.F.LandedSHA = "9f2c1ab4d5e6f7a8b9c0"
	row.HasWorktree = true

	d := m.openDecision(row)
	if d == nil {
		t.Fatal("a closed card raises no decision — its page is blank again")
	}
	if d.kind != decisionClosed {
		t.Fatalf("decision kind = %q, want closed", d.kind)
	}
	if !strings.Contains(d.question, "landed") {
		t.Fatalf("the head states the ending: %q", d.question)
	}
}

// plainClaims flattens a paragraph to one string for assertion.
func plainClaims(cs []claim) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.text)
	}
	return strings.Join(parts, " ")
}
