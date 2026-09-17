package ui

import (
	"strconv"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// The board used to keep every card it had ever finished, forever.
//
// There was no archive, no fold and no filter: the DONE group grew without
// bound, the status bar counted "37 done" with no upper limit, and the
// jump numbers and alt+j/alt+k walked through all of it. After two weeks
// of real use the board was mostly history, and the part someone was
// working on was the part they scrolled past it to reach.
//
// A settled card folds into one line. The rule is that **a card folds when
// it has nothing left to ask you** — and the asking, for the one question
// a finished card can still raise, moved to the group header rather than
// keeping a row per card in the live list. That distinction is the whole
// design: a landed card still holding a worktree IS unfinished business,
// but it is unfinished business about disk, not about the work, and there
// are always several of them (you land seven cards and clean none until
// Friday). Keeping each one in the live list would mean the archive never
// empties for anyone who does not tidy immediately — which is everyone.
//
// So the header carries it: "DONE · 35 earlier · 4 hold worktrees". One
// line, at the altitude the sweep acts on.

// archiveWindow is how recently a card must have closed to stay expanded
// under DONE. A day, because the thing being protected is "the work I
// might still want back", and that is measured in sessions rather than in
// card counts.
const archiveWindow = 24 * time.Hour

// settled reports whether the card has nothing left to ask: it reached
// done, by any route. The endings differ in what they left on disk, and
// none of that is a question about the WORK — which is why the predicate
// is this short and why the disk question is the header's job.
func (r featureRow) settled() bool { return r.F.Stage == domain.StageDone }

// boardGroup is the heading a row sits under on the board. It is a
// DISPLAY grouping read from stored facts, never a state: the workflow
// graph is compiled in and this adds nothing to it (DESIGN §3, §10.3).
type boardGroup string

// groupArchived is the one group that is not a heading: rows in it are
// folded away behind the archive line, and displayOrder skips them while
// the archive is closed.
const groupArchived boardGroup = "\x00archived"

// groupOf names the board heading a row belongs under.
//
// Everything that has not ended keeps the super-state grouping the board
// has always used. A card that HAS ended is grouped by how, because that
// is the only distinction left that changes what a reader would do with
// it.
// splitDone is whether the board has an archive at all — hoisted by
// callers that group every row, so the scan happens once per pass rather
// than once per row.
func (m *Shell) groupOf(r featureRow, splitDone bool) boardGroup {
	if !r.settled() {
		return boardGroup(upper(string(r.F.Stage.SuperState())))
	}
	if !m.recentlySettled(r) {
		return groupArchived
	}
	if r.F.GoalDropped() {
		// A drop is an ending nobody chose, and while it is recent it is
		// the one with an answer still outstanding: take it back, or let
		// it go. It gets a group of its own rather than being filed with
		// the work that finished — and it folds with everything else once
		// the window passes, because letting it go is what doing nothing
		// means.
		if r.F.GoalID != "" {
			return boardGroup("DROPPED BY " + string(r.F.GoalID))
		}
		return "DROPPED"
	}
	// "DONE · today" only earns its qualifier once something is folded
	// away behind it. On a board with nothing archived it is the same
	// group it has always been, and a label distinguishing it from an
	// absent second group would be noise claiming to be information.
	if splitDone {
		return "DONE · today"
	}
	return "DONE"
}

// archiveCount is how many cards are folded away (or would be, with the
// archive open). Zero means the board has no history worth hiding yet.
func (m *Shell) archiveCount() int {
	var n int
	for _, r := range m.rows {
		if r.settled() && !m.recentlySettled(r) {
			n++
		}
	}
	return n
}

// recentlySettled reports whether the card closed inside the archive
// window. A done card whose history carries no closing edge — a scaffold
// row, or a record older than the transitions table — counts as recent:
// the archive must never swallow a card it cannot date.
func (m *Shell) recentlySettled(r featureRow) bool {
	at := doneAt(r.History)
	if at.IsZero() {
		return true
	}
	return m.now().Sub(at) < archiveWindow
}

// archived reports whether the row is folded away right now.
func (m *Shell) archived(r featureRow) bool {
	// splitDone is irrelevant to this question: a row is archived by its
	// own age, and the flag only chooses a heading's wording.
	return !m.archiveOpen && m.groupOf(r, true) == groupArchived
}

// archiveLine is the folded archive's own row: how many cards are in it,
// and the one question they can still collectively ask.
//
// It is a header rather than a card so that nothing in the list is
// selectable-but-undrawn, and it is emitted whether the archive is open
// or closed — open, it heads the cards it holds; closed, it stands for
// them. Empty when there is nothing archived.
func (m *Shell) archiveLine() string {
	n := m.archiveCount()
	if n == 0 {
		return ""
	}
	var holding int
	for _, r := range m.rows {
		if r.settled() && !m.recentlySettled(r) && r.Landed {
			holding++
		}
	}
	// "EARLIER", not "DONE · earlier": the archive holds every ending a
	// card can have — landed, handed off and dropped alike — and heading
	// it with one of the three would misdescribe the other two.
	line := "EARLIER · " + strconv.Itoa(n) + " cards"
	if holding > 0 {
		// The cleanup ask, at the altitude it is acted on. Without this
		// the fold would be the second way a worktree gets forgotten.
		line += " · " + strconv.Itoa(holding) + " hold worktrees"
		if sz := m.archivedWorktreeSize(); sz != "" {
			line += ", " + sz
		}
	}
	if m.archiveOpen {
		return line + " · f folds"
	}
	return line + " · f opens"
}

// toggleArchive opens or closes the archive, keeping the selection on a
// row that is still drawn: closing it while the cursor sits inside would
// leave the cursor on a line nothing renders.
func (m *Shell) toggleArchive() {
	m.archiveOpen = !m.archiveOpen
	if m.archiveOpen {
		return
	}
	if r, ok := m.selected(); ok && m.archived(r) {
		if order := m.displayOrder(m.sortMode); len(order) > 0 {
			m.sel = order[len(order)-1]
		}
	}
}

// upper is strings.ToUpper for a group heading, kept local so the one
// call site reads as what it is.
func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// archivedWorktreeSize is the disk the archive's un-cleaned worktrees
// hold, as the header prints it. Empty when nothing has been measured —
// the figure is computed on demand by the close-out sweep rather than on
// the render path, where a filesystem walk per frame would be the wrong
// price for a line of text.
func (m *Shell) archivedWorktreeSize() string { return m.worktreeSizeText }
