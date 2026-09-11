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
//
// The four pipeline states are one filling circle — empty, full, half,
// then the tick — so their shapes read as progress along a single route.
// Research is a diamond rather than a fifth point on that circle because
// it is not a stage of the same journey: it runs its own route and
// delivers a document, not a branch, and a shape borrowed from the
// pipeline would place it somewhere in that sequence.
//
// "?" is the marker for a stage this function has no wording for, so it
// must never be reachable for a super-state the board actually groups by
// (BG-083 — a research card wore it).
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
// gummi-checks baseline is running, a one-shot scribe pass (check
// discovery or the envelope estimate) is in flight, its live engine
// session is mid-turn, or another gummi process is driving it mid-turn.
// Busy() alone decides the local-session side, regardless of scheduling
// state — a StateInteractive chat session mid-reply is just as busy as a
// StateRunning autonomous one, matching thread.go's own gate (snap.Busy,
// no state filter) so a card's board row and its own thread view never
// disagree about whether it's working.
//
// A row driven abroad is, by ForeignDriver's own pid exclusion, never
// also backed by a local session, and baselining and scribing are local,
// in-process actions.
//
// It is a pure function of (m.rows, m.sessionFor(row.F.ID), m.baselining,
// m.scribing, m.consultSending) — no other mutable state feeds it — so it
// renders identically whenever called twice within one frame.
func (m *Shell) cardBusy(r featureRow) bool {
	if m.baselining[r.F.ID] {
		return true
	}
	if m.scribing[r.F.ID] > 0 {
		return true
	}
	if m.consultSending[r.F.ID] != "" {
		return true
	}
	sess := m.sessionFor(r.F.ID)
	return (sess != nil && sess.Busy()) || (r.DrivenAbroad && r.Foreign.Busy)
}

// scribeSettled decrements a card's in-flight scribe-pass count by one,
// removing the entry entirely once it reaches zero so "in flight" stays
// testable as key-presence (m.baselining's own idiom).
func (m *Shell) scribeSettled(id domain.FeatureID) {
	if m.scribing[id] <= 1 {
		delete(m.scribing, id)
		return
	}
	m.scribing[id]--
}

// needsAttention reports whether a card has a pending item in the
// needs-attention queue and, if so, the kind-specific glyph attnIcon
// already draws for it in the inbox tab — the same lookup planLoopLeg
// (loopline.go) uses for the dashboard's plan-loop breadcrumb, reached
// here for the board row itself.
//
// It is a pure function of (m.inbox, r.F.ID): calling it twice in the
// same frame for the same row returns the same result.
func (m *Shell) needsAttention(r featureRow) (icon string, ok bool) {
	it, ok := m.inbox.get(r.F.ID)
	if !ok {
		return "", false
	}
	return attnIcon(m.styles, it.Kind), true
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
	sess := m.sessionFor(r.F.ID)
	if sess != nil && sess.State() == engine.StateQueued {
		glyph = s.Warning.Render("◔")
	} else if icon, ok := m.needsAttention(r); ok {
		// needs-you outranks busy: the user can act on a raised gate, not
		// on a check run still going underneath it — showing the spinner
		// here would tell them to wait when they should instead look.
		loop = " " + icon
	} else if m.cardBusy(r) {
		// only the selected card's glyph advances — the rest freeze to the
		// spinner's first frame so a board with several busy cards reads as
		// busy (mark plus word on every row) without every glyph moving in
		// lockstep off the shared clock.
		loop = " " + s.Info.Render(m.spinnerGlyph(selected)) + " " + faint.Render(m.cardBusyWord(r))
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
	// autopilot currently has this card between stages — an open period on
	// the same event log the card thread draws, never the cursor glyph
	// (reserved as the focus marker) and never the Info tier (already
	// carrying the spinner, the elsewhere badge, the gates badge and the PR
	// badge on this line). It sits next to the elsewhere badge because the
	// two are closest in meaning: both say a machine, not the person
	// reading, currently has the card.
	if r.AutopilotDriving {
		badge += " " + s.Subtle.Render("◐ autopilot")
	}
	// an autopilot card runs its gates unattended, which is worth flagging
	// at a glance — it is the opted-in state, and the only one where the
	// board is moving without you. Attended is the default (and what
	// empty reads as), so it badges nothing: a marker on every card is
	// not a marker.
	if r.F.GateApproval == domain.GateAutopilot {
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
	// the user's own explicit "stop for now" — independent of needsAttention
	// and cardBusy, since a paused card can still sit on an unresolved gate
	// from before it was paused. A parked card (never started) has no
	// session at all and renders no mark, so the two read distinctly.
	paused := ""
	if sess != nil && sess.State() == engine.StatePaused {
		paused = " " + s.Warning.Render("⏸")
	}
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
	// r.Landed answers "is there still a worktree to clean up" — msgs.go
	// only computes it when the worktree exists (canHaveLanded gates the
	// walk behind wt.Exists), so it drops back to false the moment clean-up
	// deletes the branch: a card that landed and one that was abandoned
	// then read identically in DONE (round 2 UX drive, §8). The badge is
	// answering a different, permanent question — "did this ever land" —
	// which domain.Feature.LandedSHA already carries independently of the
	// worktree: SquashMerge stamps it once, at merge time, and nothing
	// ever clears it. Showing "landed" off either signal keeps the r.Landed
	// fast-path (no extra work on the common case, worktree still present)
	// while surviving clean-up.
	landed := ""
	if r.Landed || r.F.LandedSHA != "" {
		landed = " " + s.Success.Render("landed")
	} else if r.F.HandedOff() {
		// The other half of the same permanent question. Without it a
		// deliberate hand-off and an abandoned card read identically in
		// DONE — both are "done, never landed" — which is the whole
		// distinction the ending was invented to make. It is deliberately
		// not Success paint: nothing merged, and the row should not claim
		// the same outcome as one that did.
		landed = " " + s.Info.Render("handed off")
	}
	// a card linked to an outbound PR gets a compact badge, kept visually
	// separate from (and never replacing) the landed marker — the read is
	// off the already-in-memory row, never a gh call.
	pr := ""
	if b := r.F.PullRequest.Badge(); b != "" {
		pr = " " + s.Info.Render(b)
	}
	cost := ""
	live := m.liveCardSpent(r.F.ID)
	if !r.F.Spend.Zero() || live > 0 {
		cost = " " + faint.Render(spendTick(r.F.Spend, live))
	}
	// The row used to be assembled title-first and cut from the right by a
	// single ansi.Truncate: at narrow widths the title (the one field the
	// user can always re-read on the card page) kept every column it asked
	// for, while the operational-status tail lost badges in append order —
	// landed and the PR link, the two that change what the user should DO
	// with the card, went first. Budget instead: give the non-negotiable
	// prefix and the title what they need, then shed the tail
	// least-important-first until the row fits, landed surviving longest.
	prefix := cursor + num + " " + glyph + " " + id + badge + " "
	tail := func() string { return loop + paused + tag + wtMark + landed + pr + cost }
	dropOrder := []*string{&cost, &tag, &wtMark, &pr, &landed}
	for i := 0; i < len(dropOrder) && ansi.StringWidth(prefix+tail()) > w-8; i++ {
		*dropOrder[i] = ""
	}
	title = ansi.Truncate(title, max(w-ansi.StringWidth(prefix+tail()), 0), "…")
	line := ansi.Truncate(prefix+title+tail(), w, "…")
	if selected {
		return s.Band(line, w, paneFocused)
	}
	return line
}

// boardGlyphLegend explains the board row's glyph vocabulary: the four
// stage shapes (stageGlyph), the queued marker, and the badges cardLine
// appends after the id. It exists because the round 2 UX drive found no
// legend anywhere — a row can carry any of ○ ● ◐ ✔ ⚡ ⎇ ✗ ⏸ ✉ ⟲ ~ and the
// ? help overlay documents keys only, never what the marks mean.
//
// Shaped as [][2]string, the same pair helpRows (keymap.go) produces, so
// the board's ? overlay (built by helpOverlay, also keymap.go) can show
// it by appending this slice to helpRows(bs) before building the
// helpDialog — e.g. rows: append(helpRows(bs), boardGlyphLegend()...) —
// or rendering it as its own titled section if helpDialog grows one.
// keymap.go and dialogs.go are outside this package's write scope for
// this pass, so the wiring itself is left to their owner.
//
// Two marks in the drive's list are not defined here and are omitted:
// ⟲ (corrective-round counter, thread.go) and ~ (estimated-spend prefix,
// spendformat.go's estMark) — whoever owns those files is best placed to
// fold them into this same legend or its own.
func boardGlyphLegend() [][2]string {
	return [][2]string{
		{"○ ● ◐ ✔", "stage: todo · in progress · review/verify · done"},
		{"◔", "queued — waiting for a driver"},
		{"⚡", "autopilot gate-approval (gates cross unattended)"},
		{"⎇", "worktree present"},
		{"⏸", "paused — a run the user stopped"},
		{"✉ ✗ ? $", "needs you: gate · run failure · question · budget"},
		{"landed", "the branch was squash-merged onto the base branch"},
		{"handed off", "the card was closed with its branch deliberately kept — nothing landed"},
	}
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
// that is not the settled total: either a token-derived estimate
// (estMark) or, while live is nonzero, the driving session's own running
// credit-equivalent (Session.CardSpent, reached through
// Shell.liveCardSpent — the same accessor the card masthead's budget
// line already reads). Without the live branch the board's cost column
// held the row's last-reload snapshot for as long as a turn ran — a
// live drive watched one sit at "~317.4cr" for seven minutes while the
// card page counted the same turn up to 24.7 credits, reading as "nothing
// is happening" on a card that was actively spending. live wins outright
// over the stored figure while a session is running, the same precedence
// budgetSummary (spendformat.go) already gives it for the masthead.
//
// The unit is spelled "credits" in full rather than the old "cr"
// suffix, which was invented for this one line — nowhere else in the
// product abbreviates it (the masthead's budget line and the dashboard's
// spend rollup both say "credits"). The column is also the first thing
// cardLine sheds once a row runs out of room, so the extra characters
// cost nothing the layout wasn't already prepared to give up.
func spendTick(sp domain.Spend, live float64) string {
	if live > 0 {
		return fmt.Sprintf("~%g credits", roundSpend(live))
	}
	if sp.Credits > 0 {
		return fmt.Sprintf("%s%g credits", estMark(sp), roundSpend(sp.Credits))
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
		// "cards", not "features": every other surface calls them cards,
		// and this pill is the very first words a new user reads under an
		// empty board.
		return "no cards yet"
	}
	counts := map[domain.SuperState]int{}
	for _, r := range m.rows {
		counts[r.F.Stage.SuperState()]++
	}
	var parts []string
	for _, super := range domain.SuperStates {
		// An unnamed super-state contributes nothing rather than an empty
		// segment: strings.Join below would otherwise render it as a
		// separator with a hole in it, which is how a missing count shows
		// up on the bar — as punctuation, not as an absence anyone can
		// read. formatCount names every super-state today; this keeps the
		// bar honest if a new one is added before its wording is.
		if n := counts[super]; n > 0 {
			if txt := formatCount(super, n); txt != "" {
				parts = append(parts, txt)
			}
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
//
// The second pool is named "autopilot", matching the `autopilot_lanes`
// config key, the masthead's own "autopilot: on" field and this same
// line's ⚡ badge — it used to say "unattended", a word that appeared
// nowhere else the concept was named and read as unexplained jargon
// next to those three. Its population is exactly the cards the ⚡ badge
// marks — engine.lanePoolFor sends only GateAutopilot here, and the
// empty default every TUI-created card stores pools as attended — so
// every surface that names this pool agrees on which cards it means.
//
// The label did not always agree even with itself. This comment used to
// justify the old "unattended" label by claiming lanePoolFor pooled
// every non-attended card here "including the empty default", and that
// the badge lit for GateAttended. Both were the pre-GateMode
// classification read backwards; the badge (below) has only ever lit
// for GateAutopilot.
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
