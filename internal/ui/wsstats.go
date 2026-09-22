package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/fleetrun"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The stats tab: where the whole workspace's credits and hours went.
//
// The scales below it each answer their own question. The card's run tab
// (statsview.go) says how one card ran; the week dialog (week.go) says
// what a week of settled cards produced; the inbox says what needs a
// human. Nothing said how the board as a whole is running — where the
// fleet's money went, how much of its time was spent waiting on a
// person, and, above all, when: which lanes ran, when they ran, and
// what they were waiting on while the rest ran. That last one is a
// timeline, and no amount of totals adds up to one.
//
// It is a tab and not a board surface for the reason the board is the
// backlog (DESIGN §6): one list on screen at a time, and a reading
// surface offered among the answers is a row the reader has to rule out
// every visit. A tab is also the mechanism tabs.go was built around — a
// tabDef edit, not a refactor.
//
// The fold behind it is internal/fleetrun, which states the two rules
// this page's numbers live by: a pass is charged to the window it
// started in, and the window clock counts an open session to the right
// edge. The page adds only rendering and the window control; every
// figure it prints is the fold's, so it cannot disagree with the card's
// own run tab except where the two ask different questions on purpose.

// wsWindows is the window presets the h/l keys zoom through, shortest
// first. Zero is the workspace's whole history.
var wsWindows = []time.Duration{
	6 * time.Hour,
	24 * time.Hour,
	7 * 24 * time.Hour,
	30 * 24 * time.Hour,
	0,
}

// wsDefaultPreset is the window the tab opens on: a day, because that
// is the span a running board actually moves in — a week answers "was
// it worth it" (the week dialog's question), not "how is it going".
const wsDefaultPreset = 1

// wsLanePage is how far pgup/pgdn walk the lane cursor.
const wsLanePage = 5

// wsStatsInterval paces the tab's own refresh while it is on screen.
// The board reloads on events; this page reads the record, so it rides
// a slow tick instead — a running lane's block grows, a wait is
// answered, and none of that arrives as an event the tab can hear.
const wsStatsInterval = 15 * time.Second

// wsStatsView is the mounted stats tab.
type wsStatsView struct {
	preset int
	// follow pins the right edge to now on every re-measure. Off, the
	// window holds still at its last right edge while re-loads keep
	// repainting within it — reading the past without it sliding away.
	follow bool
	cursor int
	rep    *fleetrun.Report
	// measuring marks a load in flight, so the header can say the page
	// is being read rather than leave a stale figure standing as if
	// current.
	measuring bool
}

type wsStatsLoadedMsg struct {
	rep *fleetrun.Report
	err error
}

type wsStatsTickMsg struct{}

func wsStatsTick() tea.Cmd {
	return subscription(tea.Tick(wsStatsInterval, func(time.Time) tea.Msg { return wsStatsTickMsg{} }))
}

// ensureWsStats mounts the tab on first arrival and measures it.
// Arrival work belongs to gotoTab, the one route every way into a tab
// shares.
func (m *Shell) ensureWsStats() tea.Cmd {
	if !m.attached() {
		return nil
	}
	if m.wsstats == nil {
		m.wsstats = &wsStatsView{preset: wsDefaultPreset, follow: true, measuring: true}
		return m.measureWsStats()
	}
	return nil
}

// measureWsStats reads the workspace's records off the render loop: one
// workspace-wide event query to find the window's cards, then the same
// per-card reads the card's run tab and the week view already pay. A
// window is a bounded set of cards, which is why this is affordable;
// the all-time column deliberately costs nothing per card (its rows
// come off the board snapshot).
func (m *Shell) measureWsStats() tea.Cmd {
	if !m.attached() || m.wsstats == nil {
		return nil
	}
	store := m.store
	rows := append([]featureRow(nil), m.rows...)
	v := m.wsstats
	now := m.now()
	from := time.Time{}
	if d := wsWindows[v.preset]; d > 0 {
		from = now.Add(-d)
	}
	return func() tea.Msg {
		ctx := context.Background()
		rep, err := buildWsReport(ctx, store, rows, from, now)
		if err != nil {
			return wsStatsLoadedMsg{err: err}
		}
		return wsStatsLoadedMsg{rep: rep}
	}
}

// buildWsReport gathers the fold's inputs: the window's cards whole
// (the fold does its own windowing) and every row's counters for the
// all-time ledger. Every read degrades to being skipped rather than
// failing the page: a reader asking how the workspace ran must still
// get the part of the answer that is readable.
func buildWsReport(ctx context.Context, store *state.Store, rows []featureRow, from, now time.Time) (*fleetrun.Report, error) {
	evs, err := store.WorkspaceEvents(ctx, from)
	if err != nil {
		return nil, err
	}
	active := map[domain.FeatureID]bool{}
	for _, ev := range evs {
		active[ev.Feature] = true
	}
	var cards []fleetrun.Card
	for i := range rows {
		r := rows[i]
		if !active[r.F.ID] {
			continue
		}
		log, err := store.Events(ctx, r.F.ID)
		if err != nil {
			continue
		}
		spend, err := store.SessionBreakdown(ctx, r.F.ID)
		if err != nil {
			spend = nil
		}
		cards = append(cards, fleetrun.Card{
			Feature:  r.F,
			Landed:   r.Landed,
			LandedAt: doneAt(r.History),
			Run:      cardrun.Report(cardrun.Input{Feature: r.F, Events: log, Spend: spend}),
			Events:   log,
		})
	}
	all := make([]fleetrun.AllTimeRow, 0, len(rows))
	for i := range rows {
		r := rows[i]
		all = append(all, fleetrun.AllTimeRow{Feature: r.F, Landed: r.Landed, StageSpend: r.StageSpend})
	}
	rep := fleetrun.Fold(fleetrun.Input{Now: now, Window: fleetrun.Window{From: from, To: now}, Cards: cards, Rows: all})
	return &rep, nil
}

func (m *Shell) wsStatsLoaded(msg wsStatsLoadedMsg) tea.Cmd {
	if m.wsstats == nil {
		return nil
	}
	v := m.wsstats
	v.measuring = false
	if msg.err != nil {
		m.notice = noticeMsg{text: "stats: " + sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	v.rep = msg.rep
	v.cursor = clamp(v.cursor, 0, max(len(v.rep.Lanes)-1, 0))
	return nil
}

// updateWsStats handles the tab's own messages: a landed measurement,
// and the tick that keeps a followed window honest while the tab is on
// screen.
func (m *Shell) updateWsStats(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case wsStatsLoadedMsg:
		return m.wsStatsLoaded(msg), true
	case wsStatsTickMsg:
		cmd := tea.Cmd(nil)
		if m.attached() && m.tab == TabStats && m.wsstats != nil &&
			m.wsstats.rep != nil && m.wsstats.follow && !m.wsstats.measuring {
			m.wsstats.measuring = true
			cmd = m.measureWsStats()
		}
		return cmd, true
	}
	return nil, false
}

func (m *Shell) wsStatsBindings() []binding {
	return []binding{
		{key: "j/k ↓↑", label: "lane", help: "select a card's lane"},
		{key: "enter", label: "open", help: "open the selected card on the board", bar: true},
		{key: "h/l", label: "window", help: "longer / shorter window", bar: true},
		{key: "f", label: "follow", help: "pin the right edge to now (or let it hold still)"},
		{key: "r", label: "reload", help: "measure the workspace again", bar: true},
		{key: "esc", label: "board", help: "back to the board", bar: true},
		{key: "?", label: "help", bar: true},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, stats, inbox, agent)", bar: true},
		{key: tabChords, label: "tab", help: "jump straight to board / stats / inbox / agent"},
	}
}

func (m *Shell) wsStatsKey(key string) tea.Cmd {
	v := m.wsstats
	if v == nil {
		return nil
	}
	switch key {
	case "esc":
		return m.gotoTab(TabBoard)
	case "j", "down":
		v.cursor = clamp(v.cursor+1, 0, max(m.wsLaneCount()-1, 0))
	case "k", "up":
		v.cursor = clamp(v.cursor-1, 0, max(m.wsLaneCount()-1, 0))
	case "pgdown", "space":
		v.cursor = clamp(v.cursor+wsLanePage, 0, max(m.wsLaneCount()-1, 0))
	case "pgup":
		v.cursor = clamp(v.cursor-wsLanePage, 0, max(m.wsLaneCount()-1, 0))
	case "enter":
		if v.rep != nil && v.cursor < len(v.rep.Lanes) {
			return m.wsJump(v.rep.Lanes[v.cursor].ID)
		}
	case "h":
		if v.preset < len(wsWindows)-1 {
			v.preset++
			v.measuring = true
			return m.measureWsStats()
		}
	case "l":
		if v.preset > 0 {
			v.preset--
			v.measuring = true
			return m.measureWsStats()
		}
	case "f":
		v.follow = !v.follow
		if v.follow {
			v.measuring = true
			return m.measureWsStats()
		}
	case "r":
		v.measuring = true
		return m.measureWsStats()
	}
	return nil
}

// wsLaneCount is how many lanes the cursor may walk — zero while the
// report is out or empty, so the cursor never points past what is on
// screen.
func (m *Shell) wsLaneCount() int {
	if m.wsstats == nil || m.wsstats.rep == nil {
		return 0
	}
	return len(m.wsstats.rep.Lanes)
}

// wsJump selects the named card on the board and opens its page — the
// inbox tab's same move (inboxJump), for the same reason: the evidence
// lives in the thread, so reading never acts.
func (m *Shell) wsJump(id domain.FeatureID) tea.Cmd {
	m.setTab(TabBoard)
	for i, r := range m.rows {
		if r.F.ID == id {
			m.sel = i
			m.syncActionFocus()
			return m.openCard()
		}
	}
	return nil
}

// wsLabelWidth is the lane label's column: wide enough for an id and a
// clipped title, narrow enough that the timeline keeps most of the
// terminal.
const wsLabelWidth = 20

// wsOrigin is the left edge the timeline draws from: the window's own —
// or, on an all-history window whose From is the zero time, the earliest
// moment the lanes know about. An axis that started in the year one
// would spend its whole width on before-the-workspace and squeeze every
// lane that ever ran into the last column.
func wsOrigin(rep *fleetrun.Report) time.Time {
	if !rep.Window.From.IsZero() || len(rep.Lanes) == 0 {
		return rep.Window.From
	}
	earliest := time.Time{}
	take := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	for _, l := range rep.Lanes {
		for _, b := range l.Blocks {
			take(b.From)
		}
		for _, sp := range l.Waits {
			take(sp.From)
		}
		for _, g := range l.Gates {
			take(g)
		}
		take(l.LandedAt)
	}
	if earliest.IsZero() {
		return rep.Window.To
	}
	return earliest
}

// wsStatsRender draws the tab, everything on one screen: the timeline
// gets what the other sections leave, rather than the page growing a
// second scroll a j/k would have to be aimed at.
func (m *Shell) wsStatsRender(w, h int) string {
	v := m.wsstats
	if v == nil || v.rep == nil {
		var b strings.Builder
		b.WriteString(" " + m.styles.PaneTitleActive.Render("STATS") + "\n\n")
		if v != nil && v.measuring {
			b.WriteString(" " + m.styles.Faint.Render("measuring…") + "\n")
		} else {
			b.WriteString(" " + m.styles.Faint.Render("nothing measured yet — r measures") + "\n")
		}
		return b.String()
	}
	rep := v.rep
	if rep.Empty() {
		return " " + m.styles.Faint.Render("nothing has run in this workspace yet")
	}
	s := m.styles

	head := []string{m.wsHeaderLine(w)}
	head = append(head, m.wsHeadlineLines(rep)...)
	var tail []string
	tail = append(tail, m.wsMoneyLines(rep, w)...)
	tail = append(tail, m.wsClockLines(rep)...)
	tail = append(tail, m.wsTopLines(rep, w)...)
	// The rows the timeline is fixed to spend: its title above, and the
	// axis, its tick labels and the legend below. The lanes get what is
	// left — a timeline that scrolled its own ledgers off the pane would
	// be a page that needs a second scroll j/k is already spent on.
	laneBudget := h - len(head) - 5 - len(tail)
	var lanes []string
	var flags wsLegendFlags
	if laneBudget > 1 {
		lanes, flags = m.wsLaneLines(rep, w, laneBudget)
	} else {
		lanes = []string{" " + s.Faint.Render("the timeline needs a taller pane")}
	}

	out := append([]string{}, head...)
	out = append(out, " "+s.PaneTitleActive.Render("THE TIMELINE")+"  "+
		s.Faint.Render("h/l window · f follow-now · j/k lanes · enter opens"))
	out = append(out, lanes...)
	out = append(out, m.wsAxisLines(rep, w, flags)...)
	out = append(out, "")
	out = append(out, tail...)
	if len(out) > h {
		out = out[:h]
	}
	return strings.Join(out, "\n")
}

// wsHeaderLine is the tab's title row: the name, the card count, the
// window pills, and the word for what f is currently doing to the right
// edge.
func (m *Shell) wsHeaderLine(w int) string {
	s := m.styles
	var pills []string
	for i, d := range wsWindows {
		seg := " " + wsWindowLabel(d) + " "
		if i == m.wsstats.preset {
			seg = s.Band(seg, 0, true)
		} else {
			seg = s.Faint.Render(seg)
		}
		pills = append(pills, seg)
	}
	head := " " + s.PaneTitleActive.Render("stats")
	if n := m.wsstats.rep.AllTime.Cards; n > 0 {
		head += s.Faint.Render(fmt.Sprintf(" · %d card%s", n, plural(n)))
	}
	line := head + "  " + strings.Join(pills, " ")
	if m.wsstats.follow {
		line += "   " + s.Faint.Render("following now")
	} else {
		line += "   " + s.Faint.Render("right edge held")
	}
	return ansi.Truncate(line, max(w-1, 10), "…")
}

// wsWindowName is the window as the words beside it use it — "24h",
// or "all time", which no pill abbreviates well.
func wsWindowName(w fleetrun.Window) string {
	if w.From.IsZero() {
		return "all time"
	}
	return wsWindowLabel(w.To.Sub(w.From))
}

func wsWindowLabel(d time.Duration) string {
	switch d {
	case 6 * time.Hour:
		return "6h"
	case 24 * time.Hour:
		return "24h"
	case 7 * 24 * time.Hour:
		return "7d"
	case 30 * 24 * time.Hour:
		return "30d"
	}
	return "all"
}

// wsHeadlineLines is the one line that says how the fleet is doing, and
// the estimated footnote when there is one. Every clause is conditional:
// a board with nothing running reads as quiet, not as a row of zeros.
// The arithmetic is credits throughout; the money is printed beside it
// and never instead of it, the same pairing the ledger below uses, so
// the page never carries a figure in a unit nothing else on it adds up
// in.
func (m *Shell) wsHeadlineLines(rep *fleetrun.Report) []string {
	s := m.styles
	var parts []string
	if rep.Credits > 0 {
		head := fmt.Sprintf("%.2f credits", rep.Credits)
		if d := wsDollars(rep.Credits); d != "" {
			head += " " + d
		}
		parts = append(parts, s.CardTitle.Render(head))
		if rep.RateSpan > 0 {
			parts = append(parts, fmt.Sprintf("%.1f/h", rep.Credits/(rep.RateSpan.Hours())))
		}
	}
	if tok := rep.Tokens.Total(); tok > 0 {
		parts = append(parts, s.Muted.Render(humanTokens(tok)+" tok"))
	}
	if rep.Rework > 0 && rep.Credits > 0 {
		parts = append(parts, s.Muted.Render(fmt.Sprintf("rework %.0f%%", rep.Rework/rep.Credits*100)))
	}
	if rep.Elapsed > 0 && rep.OnYou > 0 {
		parts = append(parts, s.Muted.Render(fmt.Sprintf("on you %.0f%%", share(float64(rep.OnYou), float64(rep.Elapsed))*100)))
	}
	if rep.Running > 0 {
		parts = append(parts, s.Info.Render(fmt.Sprintf("⬤ %d running", rep.Running)))
	}
	if n := m.inbox.len(); n > 0 {
		parts = append(parts, s.Warning.Render(fmt.Sprintf("✉ %d need you", n)))
	}
	var out []string
	if len(parts) == 0 {
		out = append(out, " "+s.Faint.Render("nothing ran in this window"))
	} else {
		out = append(out, " "+strings.Join(parts, s.Faint.Render(" · ")))
	}
	if rep.Estimated > 0 {
		out = append(out, " "+s.Warning.Render(fmt.Sprintf("~%.2f estimated", rep.Estimated))+
			" "+s.Faint.Render("— not yet settled by the provider"))
	}
	return out
}

// wsLaneRow is one display row of the timeline: the lane it belongs to
// (for windowing by cursor) and the row itself.
type wsLaneRow struct {
	lane int
	text string
}

// wsLegendFlags is what the lanes actually drew — the legend renders
// from what is on screen, so it never promises a mark nobody can see. A
// wait a session overpaints is not on screen, and the legend says so by
// not listing it.
type wsLegendFlags struct {
	stages         map[domain.Stage]bool
	running, waits bool
	gates, landed  bool
}

// wsLaneLines renders the lanes around the cursor: one row per card,
// the window rasterised one cell per column, and — under a lane with a
// wait nobody has answered — the clause saying when it started. A lane's
// own row must never grow past the pane: the band a selected lane wears
// truncates to the width, and a clause folded into the row would die on
// exactly the lane the reader is looking at.
func (m *Shell) wsLaneLines(rep *fleetrun.Report, w, budget int) ([]string, wsLegendFlags) {
	s := m.styles
	laneW := max(w-wsLabelWidth-2, 10)
	from, to := wsOrigin(rep), rep.Window.To
	span := to.Sub(from)
	if span <= 0 {
		span = time.Nanosecond
	}
	colOf := func(t time.Time) int {
		c := int(float64(t.Sub(from)) / float64(span) * float64(laneW))
		return clamp(c, 0, laneW-1)
	}
	var flags wsLegendFlags
	flags.stages = map[domain.Stage]bool{}

	var rows []wsLaneRow
	for i, l := range rep.Lanes {
		// Every cell is exactly one character wide — an empty cell is a
		// space, never the empty string, because a raster joined from
		// empty strings would collapse each row to its painted cells and
		// draw the whole day squashed against the label.
		cells := make([]string, laneW)
		for c := range cells {
			cells[c] = " "
		}
		for _, sp := range l.Waits {
			c1, c2 := colOf(sp.From), colOf(sp.To)
			for c := c1; c <= c2; c++ {
				if cells[c] == " " {
					cells[c] = s.Warning.Render("▒")
				}
			}
		}
		for _, g := range l.Gates {
			c := colOf(g)
			if cells[c] == " " || cells[c] == s.Warning.Render("▒") {
				cells[c] = s.Info.Render("◆")
			}
		}
		if !l.LandedAt.IsZero() {
			c := colOf(l.LandedAt)
			if cells[c] == " " || cells[c] == s.Warning.Render("▒") || cells[c] == s.Info.Render("◆") {
				cells[c] = s.Success.Render("✔")
			}
		}
		for _, b := range l.Blocks {
			c1, c2 := colOf(b.From), colOf(b.To)
			style := s.Stage(b.Stage)
			if b.Open {
				style = s.Info
				flags.running = true
			} else {
				flags.stages[b.Stage] = true
			}
			for c := c1; c <= c2; c++ {
				cells[c] = style.Render("█")
			}
		}
		// What the legend promises is what survived the raster: a wait a
		// session overpaints is not on screen, and the legend says so by
		// not listing it.
		for _, c := range cells {
			switch c {
			case s.Warning.Render("▒"):
				flags.waits = true
			case s.Info.Render("◆"):
				flags.gates = true
			case s.Success.Render("✔"):
				flags.landed = true
			}
		}

		idW := ansi.StringWidth(string(l.ID))
		idStyle := s.CardID
		if l.Kind == domain.KindResearch {
			idStyle = s.CardIDResearch
		}
		left := idStyle.Render(string(l.ID)) +
			s.Faint.Render(" "+clipPlain(l.Title, max(wsLabelWidth-idW-2, 0)))
		line := padRightANSI(left, wsLabelWidth) + s.Separator.Render("│") + strings.Join(cells, "")
		if i == m.wsstats.cursor {
			line = s.Band(line, w, true)
			if !l.OpenWaitFrom.IsZero() {
				rows = append(rows, wsLaneRow{i, line})
				rows = append(rows, wsLaneRow{i, "    " +
					s.Warning.Render("← parked on you since "+wsStamp(rep, l.OpenWaitFrom))})
				continue
			}
		}
		rows = append(rows, wsLaneRow{i, line})
	}
	if len(rows) == 0 {
		return []string{" " + s.Faint.Render("nothing ran in this window")}, flags
	}
	return wsLaneWindow(rows, m.wsstats.cursor, budget), flags
}

// wsLaneWindow returns at most budget rows, kept around the selected
// lane's row — a cursor past the fold is acted on while off-screen, and
// a timeline that scrolled its cursor out from under the reader would
// be worse than a list that did.
func wsLaneWindow(rows []wsLaneRow, cursor, budget int) []string {
	if budget <= 0 {
		return nil
	}
	primary := 0
	for i, r := range rows {
		if r.lane == cursor {
			primary = i
			break
		}
	}
	off := clamp(primary-(budget-1)/2, 0, max(len(rows)-budget, 0))
	end := min(off+budget, len(rows))
	out := make([]string, 0, end-off)
	for _, r := range rows[off:end] {
		out = append(out, r.text)
	}
	return out
}

// clipPlain shortens plain text to a display width, no style attached.
func clipPlain(t string, n int) string {
	return ansi.Truncate(t, max(n, 0), "…")
}

// padRightANSI pads a styled string to a display width.
func padRightANSI(str string, n int) string {
	if wd := ansi.StringWidth(str); wd < n {
		return str + strings.Repeat(" ", n-wd)
	}
	return str
}

// wsStamp formats a moment the way the window reads: clock times on a
// window short enough to be read in clocks, day and clock otherwise.
func wsStamp(rep *fleetrun.Report, t time.Time) string {
	if span := rep.Window.To.Sub(rep.Window.From); span <= 48*time.Hour {
		return t.Format("15:04")
	}
	return t.Format("Mon 02 15:04")
}

// wsAxisLines draws the ruler the lanes sit on and its legend: the now
// marker at the right end, ticks at round steps below, and one entry
// per kind of thing the lanes actually drew.
func (m *Shell) wsAxisLines(rep *fleetrun.Report, w int, flags wsLegendFlags) []string {
	s := m.styles
	laneW := max(w-wsLabelWidth-2, 10)
	from, to := wsOrigin(rep), rep.Window.To
	span := to.Sub(from)
	if span <= 0 {
		span = time.Nanosecond
	}
	rule := []rune(strings.Repeat("─", laneW))
	// The tick labels sit in a fixed-width buffer, not a joined list of
	// prefixed strings: an entry's padding is its absolute column, and
	// joined after the label before it, that column doubles with every
	// tick until the right-hand labels walk off the pane.
	labels := []rune(strings.Repeat(" ", laneW))
	for _, tick := range wsTicks(from, to) {
		c := clamp(int(float64(tick.Sub(from))/float64(span)*float64(laneW)), 0, laneW-1)
		rule[c] = '┼'
		label := []rune(wsTickLabel(span, tick))
		pos := clamp(c-len(label)/2, 0, laneW-len(label))
		copy(labels[pos:], label)
	}
	axis := padRightANSI("", wsLabelWidth) + s.Separator.Render("└"+string(rule)+"┤") +
		" " + s.Faint.Render("❯ "+to.Format("15:04"))
	labelRow := padRightANSI("", wsLabelWidth+1) + s.Faint.Render(string(labels))
	return []string{axis, labelRow, " " + m.wsLegend(rep, flags, laneW)}
}

// wsTickSteps are the round steps ticks may sit on, smallest first. A
// tick at 14:48 is a fact about five-way division; a tick at 12:00 is
// one a reader can aim at from memory of the day.
var wsTickSteps = []time.Duration{
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour,
}

// wsTicks places the axis ticks on the roundest step that still keeps
// the count civil, aligned to the clock rather than to the axis's left
// edge — the caller passes the effective origin (wsOrigin), since an
// all-history window's own From is the zero time. A span that defeats
// every step falls back to even division — a workspace that old earns
// it.
func wsTicks(from, to time.Time) []time.Time {
	span := to.Sub(from)
	if span <= 0 {
		return nil
	}
	var step time.Duration
	for _, s := range wsTickSteps {
		if span/s <= 6 {
			step = s
			break
		}
	}
	if step == 0 {
		// years of history: divide evenly and stop being precious about
		// the labels
		var out []time.Time
		for k := 1; k <= 4; k++ {
			out = append(out, from.Add(span*time.Duration(k)/5))
		}
		return out
	}
	t0 := from.Truncate(step)
	if t0.Before(from) {
		t0 = t0.Add(step)
	}
	var out []time.Time
	for t := t0; t.Before(to); t = t.Add(step) {
		out = append(out, t)
	}
	return out
}

// wsTickLabel formats a tick by the window's own scale: clocks on a
// short window, weekdays across a week, dates across a month or more.
func wsTickLabel(span time.Duration, t time.Time) string {
	switch {
	case span <= 48*time.Hour:
		return t.Format("15:04")
	case span <= 10*24*time.Hour:
		return t.Format("Mon 02")
	default:
		return t.Format("Jan 02")
	}
}

// wsLegend is the timeline's key, rendered from what the lanes drew —
// the flags the raster collected — so an empty legend never promises a
// mark nobody can see.
func (m *Shell) wsLegend(rep *fleetrun.Report, flags wsLegendFlags, laneW int) string {
	s := m.styles
	var parts []string
	for _, st := range domain.Stages {
		if flags.stages[st] {
			parts = append(parts, s.Stage(st).Render("█ "+string(st)))
		}
	}
	if flags.running {
		parts = append(parts, s.Info.Render("█ running"))
	}
	if flags.waits {
		parts = append(parts, s.Warning.Render("▒ on you"))
	}
	if flags.gates {
		parts = append(parts, s.Info.Render("◆ gate"))
	}
	if flags.landed {
		parts = append(parts, s.Success.Render("✔ landed"))
	}
	line := strings.Join(parts, "  ")
	return ansi.Truncate(line, max(laneW, 10), "…")
}

// wsMoneyLines is where the credits went, two columns of the same
// shape: the window's passes on the left, the workspace's own counters
// on the right. The columns never try to agree — one is attributed to a
// window by a stated rule, the other needs no rule at all (fleetrun's
// doc owns both sentences).
func (m *Shell) wsMoneyLines(rep *fleetrun.Report, w int) []string {
	s := m.styles
	if rep.Credits == 0 && rep.AllTime.Credits == 0 && rep.Tokens.Zero() && rep.AllTime.Tokens.Zero() {
		return nil
	}
	out := []string{" " + s.PaneTitleActive.Render("WHERE IT WENT") +
		"  " + s.Faint.Render(wsWindowName(rep.Window)+" · passes started in the window")}
	colW := max((w-2)/2-2, 30)
	barW := 12
	left := wsBucketLines(s, rep.ByStage, rep.Credits, barW)
	right := wsBucketLines(s, rep.AllTime.ByStage, rep.AllTime.Credits, barW)
	if len(left) == 0 {
		left = []string{s.Faint.Render("nothing ran in this window")}
	}
	if len(right) == 0 {
		right = []string{s.Faint.Render("no spend recorded")}
	}
	head := padRightANSI(s.Faint.Render("  window"), colW) + s.Faint.Render(fmt.Sprintf("  all-time · %d card%s", rep.AllTime.Cards, plural(rep.AllTime.Cards)))
	out = append(out, head)
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padRightANSI(l, colW)+r)
	}
	out = append(out, m.wsTotalLines(rep, w)...)
	foot := "  "
	if rep.Rework > 0 {
		foot += fmt.Sprintf("rework %.2f of %.2f (%.0f%%) — %.2f corrected · %.2f re-proved",
			rep.Rework, rep.Credits, share(rep.Rework, rep.Credits)*100, rep.Corrected, rep.Reproved)
		out = append(out, s.Muted.Render(foot))
	}
	return out
}

// wsTotalLines is what the two columns come to: the credits, the money
// they are worth, and the tokens they were spent with — one row per
// column, full width rather than inside the columns, because a token
// clause squeezed into half a modest terminal is the clause that gets
// truncated first.
//
// The rows are labelled "window" and "all-time" for the reason the
// columns above are: the two figures are attributed differently on
// purpose (fleetrun's doc owns both rules), so a reader who finds them
// disagreeing is reading two answers to two questions, not one error.
func (m *Shell) wsTotalLines(rep *fleetrun.Report, w int) []string {
	s := m.styles
	// The figure sits in the bucket rows' own credits column — a total
	// that did not line up under the rows it totals is one a reader has
	// to check by eye instead of by adding.
	row := func(label string, credits float64, tok fleetrun.Tokens) string {
		line := fmt.Sprintf("  %-10s %s %8.2f credits", label, strings.Repeat(" ", 12), credits)
		if d := wsDollars(credits); d != "" {
			line += "  " + d
		}
		if c := wsTokenClause(tok); c != "" {
			line += s.Faint.Render("  ·  ") + s.Muted.Render(c)
		}
		return ansi.Truncate(line, max(w-1, 10), "…")
	}
	var out []string
	if rep.Credits > 0 || !rep.Tokens.Zero() {
		out = append(out, row("window", rep.Credits, rep.Tokens))
	}
	if rep.AllTime.Credits > 0 || !rep.AllTime.Tokens.Zero() {
		out = append(out, row("all-time", rep.AllTime.Credits, rep.AllTime.Tokens))
	}
	return out
}

// wsTokenClause names a token count the way a reader spends it: what
// went in, what of that the prompt cache served, and what came back.
// The cache share is there because it is the one part of a token figure
// that changes what the credits beside it mean — a window mostly served
// from cache bought its tokens far cheaper than its count suggests. A
// backend that reports no cache reads says nothing rather than "0%",
// which would read as a cache that missed.
func wsTokenClause(t fleetrun.Tokens) string {
	if t.Zero() {
		return ""
	}
	in := humanTokens(t.Input+t.Cached) + " in"
	if t.Cached > 0 {
		in += fmt.Sprintf(" (%.0f%% cached)", t.CacheReadRatio()*100)
	}
	return in + " · " + humanTokens(t.Output) + " out"
}

// wsDollars is a credit figure as money, or empty when it is too small
// for domain.FormatDollars to render as money at all — that fallback
// prints credits, and a "(0.05 credits)" beside a credits figure would
// say the same thing twice.
func wsDollars(credits float64) string {
	d := domain.FormatDollars(credits)
	if !strings.HasPrefix(d, "$") {
		return ""
	}
	return d
}

// wsBucketLines is one column's rows: name, a magnitude bar, the
// figure, its share. One hue per bar — these are shares of one total,
// the rule statsview's own bars state.
func wsBucketLines(s *theme.Styles, bs []cardrun.Bucket, total float64, barW int) []string {
	var out []string
	for _, b := range bs {
		out = append(out, fmt.Sprintf("  %-10s %s %8.2f  %3.0f%%",
			b.Name, wsBar(s, b.Credits, total, barW), b.Credits, share(b.Credits, total)*100))
	}
	return out
}

// wsBar is statsBar at timeline-column width.
func wsBar(s *theme.Styles, v, total float64, width int) string {
	n := int(share(v, total)*float64(width) + 0.5)
	if v > 0 && n < 1 {
		n = 1
	}
	return s.Info.Render(strings.Repeat("█", max(n, 0))) + strings.Repeat(" ", max(width-n, 0))
}

// wsClockLines is where the hours went, over the window, with the two
// figures only a fleet can produce: how many lanes ran at once, and
// which stretch of the window took the most of it. A quiet window says
// nothing rather than printing zeros.
func (m *Shell) wsClockLines(rep *fleetrun.Report) []string {
	s := m.styles
	if rep.Elapsed <= 0 {
		return nil
	}
	out := []string{" " + s.PaneTitleActive.Render("THE CLOCK") +
		"  " + s.Faint.Render(wsWindowName(rep.Window))}
	peak := ""
	if rep.PeakLanes > 0 {
		peak = fmt.Sprintf("peak %d lane%s at once", rep.PeakLanes, plural(rep.PeakLanes))
		if !rep.Busiest.IsZero() {
			peak += " · busiest " + wsStretchLabel(rep)
		}
	}
	out = append(out, fmt.Sprintf("  %-15s %10s  %s", "agent working", shortDur(rep.Agent), s.Muted.Render(peak)))
	out = append(out, fmt.Sprintf("  %-15s %10s  %s", "waiting on you", shortDur(rep.OnYou),
		s.Muted.Render(fmt.Sprintf("(%.0f%%)", share(float64(rep.OnYou), float64(rep.Elapsed))*100))))
	if rep.Idle > 0 {
		out = append(out, fmt.Sprintf("  %-15s %10s  %s", "nothing running", shortDur(rep.Idle),
			s.Muted.Render(fmt.Sprintf("(%.0f%%)", share(float64(rep.Idle), float64(rep.Elapsed))*100))))
	}
	return out
}

// wsStretchLabel names the busiest stretch by the window's own scale.
func wsStretchLabel(rep *fleetrun.Report) string {
	t := rep.Busiest
	if rep.BusiestLen >= 24*time.Hour {
		return t.Format("Mon 02")
	}
	return t.Format("15:04") + "–" + t.Add(rep.BusiestLen).Format("15:04")
}

// wsTopLines names the window's costliest lanes, with the diagnosis
// each earned — the timeline's headline cases, so nobody counts rows to
// find them.
func (m *Shell) wsTopLines(rep *fleetrun.Report, w int) []string {
	s := m.styles
	if len(rep.Top) == 0 {
		return nil
	}
	out := []string{" " + s.PaneTitleActive.Render("TOP CARDS") +
		"  " + s.Faint.Render(wsWindowName(rep.Window))}
	maxCredits := rep.Top[0].Credits
	for _, l := range rep.Top {
		bar := wsBar(s, l.Credits, maxCredits, 8)
		line := "  " + s.CardID.Render(string(l.ID)) + "  " + bar +
			fmt.Sprintf(" %8.2f", l.Credits)
		if l.Note != "" {
			line += "  " + s.Muted.Render(l.Note)
		}
		out = append(out, ansi.Truncate(line, max(w-1, 10), "…"))
	}
	return out
}
