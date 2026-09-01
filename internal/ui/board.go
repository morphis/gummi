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

// cardBusy reports whether a card has a real busy source: its
// gummi-checks baseline is running, or its live engine session is
// mid-turn. Busy() alone decides the session side, regardless of
// scheduling state — a StateInteractive chat session mid-reply is just
// as busy as a StateRunning autonomous one, matching thread.go's own
// gate (snap.Busy, no state filter) so a card's board row and its own
// thread view never disagree about whether it's working.
//
// It is a pure function of (m.rows, m.sessionFor(row.F.ID), m.baselining)
// — no other mutable state feeds it — so it renders identically whenever
// called twice within one frame.
func (m *Shell) cardBusy(r featureRow) bool {
	if m.baselining[r.F.ID] {
		return true
	}
	sess := m.sessionFor(r.F.ID)
	return sess != nil && sess.Busy()
}

// boardView renders the kanban column: features grouped by super-state
// with stage-colored glyphs, IDs, titles, and profile tags.
//
// focused says whether the column owns the arrow keys right now. The
// board never loses its selection — moving into the card's action list
// or opening the spec leaves the card selected — so without this the
// column looked exactly as live as it does when j/k actually move it.
// Focused, the selected card wears the bright band and the group headers
// take the accent; unfocused, both go quiet.
func (m *Shell) boardView(w int, focused bool) string {
	s := m.styles
	paneTitle := s.PaneTitle
	if focused {
		paneTitle = s.PaneTitleActive
	}
	if len(m.rows) == 0 {
		var b strings.Builder
		b.WriteString("\n " + paneTitle.Render("BOARD") + "\n\n")
		b.WriteString(" " + s.Faint.Render("nothing on the board yet") + "\n")
		b.WriteString(" " + s.Muted.Render("press ") + s.KeyHint.Render("n") + s.Muted.Render(" new feature · ") + s.KeyHint.Render("B") + s.Muted.Render(" new bug · ") + s.KeyHint.Render("R") + s.Muted.Render(" new research") + "\n")
		return b.String()
	}

	// Render from displayOrder so the printed 1..9 shortcuts are, by
	// construction, the indices jumpSel selects.
	var b strings.Builder
	b.WriteString("\n")
	var lastSuper domain.SuperState
	for shortcut, i := range m.displayOrder(m.sortMode) {
		r := m.rows[i]
		if super := r.F.Stage.SuperState(); shortcut == 0 || super != lastSuper {
			if shortcut > 0 {
				b.WriteString("\n")
			}
			b.WriteString(" " + paneTitle.Render(strings.ToUpper(string(super))) + "\n")
			lastSuper = super
		}
		b.WriteString(m.cardLine(r, shortcut+1, i == m.sel, focused, w) + "\n")
	}
	return b.String()
}

// cardLine renders one feature card row, truncated to w. A selected row
// is painted as a full-width band (theme.Band) rather than marked by the
// ▸ alone: paneFocused picks the bright band or the quiet one.
//
// The band swallows the faintest text tiers (theme.BandTextDim), so a
// selected row lifts its own metadata — the shortcut number, the profile
// tag, the worktree mark and the cost tick would otherwise be invisible
// on exactly the row the eye was sent to.
func (m *Shell) cardLine(r featureRow, shortcut int, selected, paneFocused bool, w int) string {
	s := m.styles
	faint := s.Faint
	if selected {
		faint = s.BandTextDim
	}
	glyph := s.Stage(r.F.Stage).Render(stageGlyph(r.F.Stage))
	// the stage glyph is never overwritten — busy or not, it stays the
	// card's shape-plus-colour stage marker (board.go's own promise). A
	// queued session gets its own distinct marker instead; a busy card
	// gets a trailing spinner+word in the loop slot, checked after (so a
	// session that is somehow both queued and busy still reads queued).
	loop := ""
	if sess := m.sessionFor(r.F.ID); sess != nil && sess.State() == engine.StateQueued {
		glyph = s.Warning.Render("◔")
	} else if m.cardBusy(r) {
		loop = " " + s.Info.Render(m.spinner()) + " " + faint.Render(m.cardBusyWord(r))
	}
	// the marker sits flush against the shortcut number, so it can't use
	// BandMarker's padded form — same two styles, one column.
	cursor := " "
	switch {
	case selected && paneFocused:
		cursor = s.SelMarker.Render("▸")
	case selected:
		cursor = s.SelMarkerDim.Render("▸")
	}
	num := faint.Render(shortcutLabel(shortcut))
	// a bug's ID reads in a warm tint so bugs stand out among features in
	// the shared board (the BG- prefix already distinguishes them).
	id := s.CardID.Render(string(r.F.ID))
	switch r.F.Kind {
	case domain.KindBug:
		id = s.Warning.Render(string(r.F.ID))
	case domain.KindResearch:
		id = s.CardIDResearch.Render(string(r.F.ID))
	}
	badge := ""
	// a card the Advance gate would block on an unmet direct dependency
	// shows a blocked badge alongside the severity badge — read-only
	// feedback on the gate, never a state it owns.
	if r.DepBlocked {
		badge = " " + s.Warning.Render("⛔")
	}
	// a card another gummi process is driving: this board can watch it
	// (enter / t) but must not touch it, and says so rather than
	// presenting an actively-changing card as idle.
	if r.DrivenAbroad {
		badge += " " + s.Info.Render("◉ elsewhere")
	}
	// an explicit "gates" gate-approval mode crosses this card's design
	// gates unattended, worth flagging at a glance. Only the explicit
	// value badges: empty reads as domain.GateGates too everywhere else in
	// the code, but every TUI-created card stores empty, and badging that
	// as well would light up the whole board as if each card had opted in
	// to something nobody chose.
	if r.F.GateApproval == domain.GateGates {
		badge += " " + s.Info.Render("⚡")
	}
	if sev := r.F.Severity; sev != "" {
		badge += " " + s.SeverityBadgeStyle(sev).Render(severityAbbrev(sev))
	}
	// a card's managed repository badge, naming the configured repo, so
	// multi-repo boards read at a glance. Cards in the workspace default
	// repo render no badge (the default is implicit); it is metadata only,
	// no filtering or grouping is implied.
	if r.F.Repo != "" {
		badge += " " + s.RepoBadge.Render("["+r.F.Repo+"]")
	}
	title := s.CardTitle.Render(r.F.Title)
	tag := ""
	if r.F.Profile != "" {
		profile := s.ProfileTag
		if selected {
			profile = faint
		}
		tag = " " + profile.Render("["+r.F.Profile+"]")
	}
	wtMark := ""
	if r.HasWorktree {
		wtMark = " " + faint.Render("⎇")
	}
	// a landed branch is cleanup-ready (press c) — flag it so it stands out
	landed := ""
	if r.Landed {
		landed = " " + s.Success.Render("landed")
	}
	// a card linked to an outbound PR gets a compact badge, kept visually
	// separate from (and never replacing) the landed marker — the read is
	// off the already-in-memory row, never a gh call.
	pr := ""
	if b := r.F.PullRequest.Badge(); b != "" {
		pr = " " + s.Info.Render(b)
	}
	cost := ""
	if !r.F.Spend.Zero() {
		cost = " " + faint.Render(spendTick(r.F.Spend))
	}
	line := ansi.Truncate(cursor+num+" "+glyph+" "+id+badge+" "+title+loop+tag+wtMark+landed+pr+cost, w, "…")
	if selected {
		return s.Band(line, w, paneFocused)
	}
	return line
}

// severityAbbrev is the compact badge text for a bug's severity level;
// called only for non-empty severities.
func severityAbbrev(sev domain.Severity) string {
	switch sev {
	case domain.SeverityCritical:
		return "CRIT"
	case domain.SeverityHigh:
		return "HIGH"
	case domain.SeverityMedium:
		return "MED"
	default:
		return "LOW"
	}
}

// spendTick is the compact cost marker on a card: Copilot credits when
// any were metered, else BYOK tokens. A "~" prefix flags a credit figure
// with a token-derived (estimated) component.
func spendTick(sp domain.Spend) string {
	if sp.Credits > 0 {
		return fmt.Sprintf("%s%gcr", estMark(sp), roundSpend(sp.Credits))
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
	if m.engine != nil {
		if lanes := laneCountsText(m.engine.LaneCounts()); lanes != "" {
			parts = append(parts, lanes)
		}
	}
	return strings.Join(parts, " · ")
}

// laneCountsText renders the two attention pools in the board's compact
// count shape: "attended 1/1 · autopilot 2/2". Empty when neither pool
// has a cap to report or anything running in it — an uncapped, idle
// engine has nothing to say here, and "attended 0 · autopilot 0" beside
// a card count reads like a contradiction rather than an absence.
func laneCountsText(lc engine.LaneCounts) string {
	if lc.AttendedMax <= 0 && lc.AutopilotMax <= 0 &&
		lc.AttendedRunning == 0 && lc.AutopilotRunning == 0 {
		return ""
	}
	return laneCountText("attended", lc.AttendedRunning, lc.AttendedMax) + " · " +
		laneCountText("autopilot", lc.AutopilotRunning, lc.AutopilotMax)
}

// laneCountText renders one pool's running/cap pair. An uncapped pool
// (max <= 0 — see engine.LaneCounts) has no total to divide by, so it
// shows the running count alone.
func laneCountText(name string, running, max int) string {
	if max <= 0 {
		return fmt.Sprintf("%s %d", name, running)
	}
	return fmt.Sprintf("%s %d/%d", name, running, max)
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
