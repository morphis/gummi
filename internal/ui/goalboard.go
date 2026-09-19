package ui

import (
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// rowIndex is the index of card id in m.rows, -1 when it is not loaded.
func (m *Shell) rowIndex(id domain.FeatureID) int {
	for i, r := range m.rows {
		if r.F.ID == id {
			return i
		}
	}
	return -1
}

// goalCardCount is how many of the board's rows belong to f, zero for
// anything but a goal. It is the loaded board's count rather than the
// store's, which is what a dialog drawn on the render path can ask for;
// the delete itself reads the store.
func (m *Shell) goalCardCount(f domain.Feature) int {
	if !f.IsGoal() {
		return 0
	}
	n := 0
	for _, r := range m.rows {
		if r.F.GoalID == f.ID {
			n++
		}
	}
	return n
}

// isFoldedChild reports whether rows[idx] is a goal card shown under its
// loaded goal.
func (m *Shell) isFoldedChild(idx int) bool {
	if idx < 0 || idx >= len(m.rows) || m.rows[idx].F.GoalID == "" {
		return false
	}
	return m.rowIndex(m.rows[idx].F.GoalID) >= 0
}

// toggleGoalFold folds or unfolds the selected goal's cards, reporting
// whether the selection had a goal to fold. On a goal's card it folds the
// goal and moves the selection to it, so the cursor is never left on a row
// that is no longer drawn.
func (m *Shell) toggleGoalFold() bool {
	r, ok := m.selected()
	if !ok {
		return false
	}
	goal := r.F.ID
	if !r.F.IsGoal() {
		if !m.isFoldedChild(m.sel) {
			return false
		}
		goal = r.F.GoalID
	}
	if m.goalOpen == nil {
		m.goalOpen = map[domain.FeatureID]bool{}
	}
	m.goalOpen[goal] = !m.goalOpen[goal]
	if !m.goalOpen[goal] {
		if i := m.rowIndex(goal); i >= 0 {
			m.sel = i
		}
	}
	return true
}

// goalRowTag is a goal row's progress on the board: done-when met, cards
// landed, budget spent, and whether it is waiting for you. Folded, it also
// says how many cards are under it.
func goalRowTag(s *theme.Styles, r featureRow, open bool) string {
	if !r.F.IsGoal() || r.Goal == nil {
		return ""
	}
	g := r.Goal
	met, total := g.Met()
	landed, cards := 0, 0
	for _, c := range g.Cards {
		switch c.State {
		case "landed":
			landed++
		case "dropped":
			continue // no longer part of the goal's work
		}
		cards++
	}
	tag := fmt.Sprintf("%d/%d done-when · %d/%d cards · %.0f/%d", met, total, landed, cards, g.Budget.Total, g.Budget.Envelope)
	switch {
	case total == 0 && len(g.Cards) == 0:
		// nothing agreed yet: "0/0 done-when · 0/0 cards" would read as a
		// goal with nothing to do rather than one without a plan
		tag = fmt.Sprintf("no plan yet · %.0f/%d", g.Budget.Total, g.Budget.Envelope)
	case len(g.Cards) == 0:
		// a drafted plan: its cards are made when it is approved
		tag = fmt.Sprintf("%d done-when · cards made at approval · %.0f/%d", total, g.Budget.Total, g.Budget.Envelope)
	}
	switch {
	case g.Ready && g.Partial != "":
		tag += " · ready, partial"
	case g.Ready:
		tag += " · ready for you"
	case g.WrappingUp:
		tag += " · wrapping up"
	}
	fold := "▸"
	if open {
		fold = "▾"
	}
	if len(g.Cards) == 0 {
		fold = ""
	} else {
		fold += " "
	}
	return s.Faint.Render(fold + tag)
}
