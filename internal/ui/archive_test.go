package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The board used to keep every card it had ever finished, with the jump
// numbers and alt+j/alt+k walking through all of it.

// settledRow builds a done card that closed `ago` before now.
func settledRow(id domain.FeatureID, ago time.Duration, landed bool) featureRow {
	return featureRow{
		F: domain.Feature{ID: id, Stage: domain.StageDone, LandedSHA: "abc123"},
		History: []state.TransitionRecord{{
			From: domain.StageVerify, To: domain.StageDone, At: time.Now().Add(-ago),
		}},
		Landed:      landed,
		HasWorktree: landed,
	}
}

func TestArchiveFoldsWhatSettledEarlier(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-001", Stage: domain.StageImplement}},
		settledRow("FD-002", time.Hour, false),
		settledRow("FD-003", 72*time.Hour, false),
		settledRow("FD-004", 96*time.Hour, true),
	}

	if got := m.archiveCount(); got != 2 {
		t.Fatalf("archiveCount = %d, want 2", got)
	}
	// folded: the two old ones are not in the order at all, which is what
	// keeps the jump numbers contiguous
	order := m.displayOrder(SortCreation)
	if len(order) != 2 {
		t.Fatalf("folded order = %v, want the live card and today's", order)
	}
	// and opening it brings them back
	m.toggleArchive()
	if order := m.displayOrder(SortCreation); len(order) != 4 {
		t.Fatalf("open order = %v, want every row", order)
	}
}

// The fold must never be the second way a worktree gets forgotten: the
// cleanup ask moves to the header rather than keeping a row per card in
// the live list.
func TestArchiveHeaderCarriesTheCleanupAsk(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		settledRow("FD-003", 72*time.Hour, true),
		settledRow("FD-004", 96*time.Hour, true),
		settledRow("FD-005", 96*time.Hour, false),
	}
	line := m.archiveLine()
	for _, want := range []string{"EARLIER", "3 cards", "2 hold worktrees", "f opens"} {
		if !strings.Contains(line, want) {
			t.Fatalf("archive line %q is missing %q", line, want)
		}
	}
	m.toggleArchive()
	if !strings.Contains(m.archiveLine(), "f folds") {
		t.Fatalf("an open archive says how to close it: %q", m.archiveLine())
	}
}

// A board with nothing archived is the board it has always been: the
// qualifier only earns its place once something is folded behind it.
func TestDoneHeadingStaysPlainWithoutAnArchive(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{settledRow("FD-002", time.Hour, false)}
	if got := m.groupOf(m.rows[0], m.archiveCount() > 0); got != "DONE" {
		t.Fatalf("group = %q, want DONE", got)
	}
	if m.archiveLine() != "" {
		t.Fatalf("no archive, no line: %q", m.archiveLine())
	}

	m.rows = append(m.rows, settledRow("FD-003", 96*time.Hour, false))
	if got := m.groupOf(m.rows[0], m.archiveCount() > 0); got != "DONE · today" {
		t.Fatalf("group = %q, want DONE · today", got)
	}
}

// A done card the archive cannot date must never be swallowed.
func TestUndatableCardStaysVisible(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{{F: domain.Feature{ID: "FD-009", Stage: domain.StageDone}}}
	if m.archived(m.rows[0]) {
		t.Fatal("a done card with no closing edge on record was folded away")
	}
}

// A drop is the one ending with an answer still outstanding, so it gets a
// group of its own — and folds with everything else once the window
// passes, because letting it go is what doing nothing means.
func TestDroppedCardGroupsUnderItsGoalThenFolds(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	recent := settledRow("FD-007", time.Hour, false)
	recent.F.GoalID = "GL-004"
	recent.F.GoalDroppedAt = time.Now().Add(-time.Hour)
	recent.F.LandedSHA = ""
	old := settledRow("FD-008", 96*time.Hour, false)
	old.F.GoalID = "GL-004"
	old.F.GoalDroppedAt = time.Now().Add(-96 * time.Hour)
	old.F.LandedSHA = ""
	m.rows = []featureRow{recent, old}

	if got := m.groupOf(m.rows[0], true); got != "DROPPED BY GL-004" {
		t.Fatalf("group = %q, want DROPPED BY GL-004", got)
	}
	if got := m.groupOf(m.rows[1], true); got != groupArchived {
		t.Fatalf("an old drop folds with everything else: %q", got)
	}
}

// Closing the archive while the cursor is inside it must not leave the
// cursor on a line nothing draws.
func TestClosingTheArchiveMovesTheCursorOut(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-001", Stage: domain.StageImplement}},
		settledRow("FD-003", 96*time.Hour, false),
	}
	m.toggleArchive() // open
	m.sel = 1         // inside it
	m.toggleArchive() // close
	if m.archived(m.rows[m.sel]) {
		t.Fatalf("selection %d is on a row nothing renders", m.sel)
	}
}
