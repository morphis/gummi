package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// stageGlyph is the card marker: readable by shape as well as color.
func stageGlyph(s domain.Stage) string {
	switch s.SuperState() {
	case domain.SuperTodo:
		return "○"
	case domain.SuperInProgress:
		return "●"
	case domain.SuperReviewVerify:
		return "◐"
	case domain.SuperDone:
		return "✔"
	}
	return "?"
}

// boardView renders the kanban column: features grouped by super-state
// with stage-colored glyphs, IDs, titles, and profile tags.
func (m *Shell) boardView(w int) string {
	s := m.styles
	if len(m.rows) == 0 {
		var b strings.Builder
		b.WriteString("\n " + s.PaneTitle.Render("BOARD") + "\n\n")
		b.WriteString(" " + s.Faint.Render("nothing on the board yet") + "\n")
		b.WriteString(" " + s.Muted.Render("press ") + s.KeyHint.Render("n") + s.Muted.Render(" new feature · ") + s.KeyHint.Render("B") + s.Muted.Render(" new bug") + "\n")
		return b.String()
	}

	// Render from displayOrder so the printed 1..9 shortcuts are, by
	// construction, the indices jumpSel selects.
	var b strings.Builder
	b.WriteString("\n")
	var lastSuper domain.SuperState
	for shortcut, i := range m.displayOrder() {
		r := m.rows[i]
		if super := r.F.Stage.SuperState(); shortcut == 0 || super != lastSuper {
			if shortcut > 0 {
				b.WriteString("\n")
			}
			b.WriteString(" " + s.PaneTitle.Render(strings.ToUpper(string(super))) + "\n")
			lastSuper = super
		}
		b.WriteString(m.cardLine(r, shortcut+1, i == m.sel, w) + "\n")
	}
	return b.String()
}

// cardLine renders one feature card row, truncated to w.
func (m *Shell) cardLine(r featureRow, shortcut int, selected bool, w int) string {
	s := m.styles
	glyph := s.Stage(r.F.Stage).Render(stageGlyph(r.F.Stage))
	// a live agent session marks the card by scheduling state; a plan-loop
	// session also names its leg (the stage alone can't distinguish them)
	loop := ""
	if sess := m.sessionFor(r.F.ID); sess != nil {
		switch sess.State() {
		case engine.StateRunning:
			if sess.Busy() {
				glyph = s.Info.Render(m.spinner())
			}
			if word := m.planLoopWord(sess); word != "" {
				loop = " " + s.Faint.Render(word)
			}
		case engine.StateQueued:
			glyph = s.Warning.Render("◔")
		}
	}
	cursor := " "
	if selected {
		cursor = s.Cursor.Render("▸")
	}
	num := s.Faint.Render(shortcutLabel(shortcut))
	// a bug's ID reads in a warm tint so bugs stand out among features in
	// the shared board (the BG- prefix already distinguishes them).
	id := s.CardID.Render(string(r.F.ID))
	if r.F.Kind == domain.KindBug {
		id = s.Warning.Render(string(r.F.ID))
	}
	title := s.CardTitle.Render(r.F.Title)
	tag := ""
	if r.F.Profile != "" {
		tag = " " + s.ProfileTag.Render("["+r.F.Profile+"]")
	}
	wtMark := ""
	if r.HasWorktree {
		wtMark = " " + s.Faint.Render("⎇")
	}
	// a landed branch is cleanup-ready (press c) — flag it so it stands out
	landed := ""
	if r.Landed {
		landed = " " + s.Success.Render("landed")
	}
	cost := ""
	if !r.F.Spend.Zero() {
		cost = " " + s.Faint.Render(spendTick(r.F.Spend))
	}
	line := cursor + num + " " + glyph + " " + id + " " + title + loop + tag + wtMark + landed + cost
	return ansi.Truncate(line, w, "…")
}

// spendTick is the compact cost marker on a card: Copilot credits when
// any were metered, else BYOK tokens.
func spendTick(sp domain.Spend) string {
	if sp.Credits > 0 {
		return fmt.Sprintf("%gcr", roundSpend(sp.Credits))
	}
	tk := sp.InputTokens + sp.OutputTokens
	if tk >= 1000 {
		return fmt.Sprintf("%.1fktk", float64(tk)/1000)
	}
	return fmt.Sprintf("%dtk", tk)
}

// roundSpend rounds credits to one decimal for display.
func roundSpend(c float64) float64 {
	return float64(int(c*10+0.5)) / 10
}

// shortcutLabel shows 1..9 jump keys; features beyond nine get a dot.
func shortcutLabel(n int) string {
	if n >= 1 && n <= 9 {
		return string(rune('0' + n))
	}
	return "·"
}

// boardCounts summarizes the board for the status bar.
func (m *Shell) boardCounts() string {
	if len(m.rows) == 0 {
		return "0 features"
	}
	counts := map[domain.SuperState]int{}
	for _, r := range m.rows {
		counts[r.F.Stage.SuperState()]++
	}
	var parts []string
	for _, super := range domain.SuperStates {
		if n := counts[super]; n > 0 {
			parts = append(parts, formatCount(super, n))
		}
	}
	return strings.Join(parts, " · ")
}

func formatCount(super domain.SuperState, n int) string {
	c := strconv.Itoa(n)
	switch super {
	case domain.SuperTodo:
		return c + " todo"
	case domain.SuperInProgress:
		return c + " active"
	case domain.SuperReviewVerify:
		return c + " in review"
	case domain.SuperDone:
		return c + " done"
	}
	return ""
}
