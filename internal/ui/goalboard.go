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
	landed := 0
	for _, c := range g.Cards {
		if c.State == "landed" {
			landed++
		}
	}
	tag := fmt.Sprintf("%d/%d done-when · %d/%d cards · %.0f/%d", met, total, landed, len(g.Cards), g.Budget.Total, g.Budget.Envelope)
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
