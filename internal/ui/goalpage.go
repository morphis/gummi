package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The goal page: one goal at a glance — whether it is running or ready for
// you, its done-when list, its cards as a tree, its budget, the decisions
// the lead made for you, the findings it declined, what it found along the
// way, the try-it guide, and the lead's log. It is the surface the
// hand-over is read on, and the one a person opens to look in on a running
// goal without pausing anything.

// goalPageView is the mounted goal page.
type goalPageView struct {
	goal   domain.Feature
	report engine.GoalReport
	log    []state.GoalEntry
	scroll int
	// cursor is the card in the cards section enter opens, an index into
	// report.Cards. The page is a document with exactly one actionable
	// region, so the cursor lives only there: j/k walk the cards and,
	// past either end, go back to scrolling the document. That is what
	// keeps the board's grammar (j/k select, enter opens, esc goes back)
	// true on a page that also has to be read top to bottom.
	cursor int
	// reveal asks the next render to scroll the selected card into view.
	// The cursor moves in the key handler, which does not know the
	// height; the render does, and is the only place that can honestly
	// say whether the card is on screen.
	reveal bool
}

type goalPageLoadedMsg struct {
	goal   domain.Feature
	report engine.GoalReport
	log    []state.GoalEntry
	err    error
}

// openGoalPage loads and mounts f's goal page.
func (m *Shell) openGoalPage(f domain.Feature) tea.Cmd {
	if !f.IsGoal() {
		return nil
	}
	eng, store := m.engine, m.store
	return func() tea.Msg {
		if eng == nil {
			return goalPageLoadedMsg{goal: f, err: fmt.Errorf("no engine to read the goal through")}
		}
		ctx := context.Background()
		r, err := eng.GoalReport(ctx, f.ID)
		if err != nil {
			return goalPageLoadedMsg{goal: f, err: err}
		}
		log, err := store.GoalLog(ctx, f.ID)
		return goalPageLoadedMsg{goal: f, report: r, log: log, err: err}
	}
}

func (m *Shell) goalPageLoaded(msg goalPageLoadedMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	scroll, cursor := 0, 0
	if m.goalPage != nil && m.goalPage.goal.ID == msg.goal.ID {
		scroll, cursor = m.goalPage.scroll, m.goalPage.cursor
	}
	// a reload can shorten the card list (a drop leaves it listed, but an
	// attach that failed does not), so the kept cursor is clamped rather
	// than trusted
	if cursor >= len(msg.report.Cards) {
		cursor = max(0, len(msg.report.Cards)-1)
	}
	m.goalPage = &goalPageView{goal: msg.goal, report: msg.report, log: msg.log, scroll: scroll, cursor: cursor}
	return nil
}

func (gp *goalPageView) bindings() []binding {
	bs := []binding{
		{key: "j/k", label: "scroll", help: "scroll the goal page"},
		{key: "r", label: "reload", help: "read the goal again", bar: true},
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "close the goal page (also q)", bar: true},
	}
	if len(gp.report.Cards) > 0 {
		bs[0] = binding{key: "j/k", label: "select", help: "walk the goal's cards; past either end, scroll the page"}
		bs = append([]binding{{key: "enter", label: "watch", help: "open the selected card and watch it run — read-only, the goal's lead drives it", bar: true}}, bs...)
	}
	return bs
}

func (m *Shell) handleGoalPageKey(key string) tea.Cmd {
	gp := m.goalPage
	cards := len(gp.report.Cards)
	switch key {
	case "esc", "q":
		m.goalPage = nil
	case "enter", "right", "l":
		return m.watchGoalCard()
	case "j", "down":
		// the cursor first, the document second: walking off the last
		// card resumes plain scrolling, so everything below the cards —
		// the budget, the decisions, the log — is still reachable a line
		// at a time.
		if gp.cursor < cards-1 {
			gp.cursor++
			gp.reveal = true
			return nil
		}
		gp.scroll++
	case "k", "up":
		if gp.cursor > 0 {
			gp.cursor--
			gp.reveal = true
			return nil
		}
		if gp.scroll > 0 {
			gp.scroll--
		}
	case "pgdown", "space":
		gp.scroll += 10
	case "pgup":
		gp.scroll = max(0, gp.scroll-10)
	case "r":
		return m.openGoalPage(gp.goal)
	}
	return nil
}

// watchGoalCard opens the card under the page's cursor, to watch it run.
// The board's selection moves to it and its goal is unfolded behind, so
// the board the card page sits on agrees with the card in front of the
// reader; goalReturn is what brings esc back here rather than dropping
// them on that board. openThread, not openCard, because a goal whose
// cards a headless driver is running needs the foreign tail — the same
// routing t already does, for the same reason.
func (m *Shell) watchGoalCard() tea.Cmd {
	gp := m.goalPage
	if gp == nil || gp.cursor >= len(gp.report.Cards) {
		return nil
	}
	card := gp.report.Cards[gp.cursor]
	i := m.rowIndex(card.ID)
	if i < 0 {
		// the board filters cards out (the archive fold, a load that has
		// not caught up with a card the lead minted a moment ago), and
		// the page lists them from the goal's own report either way
		m.notice = noticeMsg{text: string(card.ID) + " is not on the board yet — reload with r", isErr: true}
		return nil
	}
	goal := gp.goal
	m.goalPage = nil
	m.goalReturn = goal.ID
	m.sel = i
	m.syncActionFocus()
	if m.goalOpen == nil {
		m.goalOpen = map[domain.FeatureID]bool{}
	}
	m.goalOpen[goal.ID] = true
	return m.openThread(m.rows[i].F)
}

// backToGoalPage reopens the goal page a watched card was entered from.
// esc on that card means "back where I came from", and where they came
// from was the goal, not the board it is a row on — landing them on the
// board would make watching a second card a fresh hunt through the fold
// every time. Returns nil when the card was not entered that way, which
// is every other esc.
func (m *Shell) backToGoalPage() tea.Cmd {
	id := m.goalReturn
	m.goalReturn = ""
	if id == "" {
		return nil
	}
	// the card that was entered from the page can be left behind without
	// this esc — a jump from the inbox, a notice that moved the
	// selection — and returning to the goal from a card that is no longer
	// one of its own would be answering a question nobody asked
	if r, ok := m.selected(); !ok || r.F.GoalID != id {
		return nil
	}
	i := m.rowIndex(id)
	if i < 0 {
		return nil
	}
	m.sel = i
	m.syncActionFocus()
	return m.openGoalPage(m.rows[i].F)
}

// goalPageRender draws the page, scrolled. A cursor move asks to be
// revealed (goalPageView.reveal): the selected card is scrolled to the
// nearer edge of the window, so walking the list never leaves the reader
// looking at a highlight that is off screen.
func (m *Shell) goalPageRender(w, h int) string {
	gp := m.goalPage
	lines, cardAt := goalPageLines(m.styles, gp, w)
	if gp.reveal && gp.cursor < len(cardAt) {
		gp.reveal = false
		if at := cardAt[gp.cursor]; at < gp.scroll {
			gp.scroll = at
		} else if at >= gp.scroll+h {
			gp.scroll = at - h + 1
		}
	}
	if gp.scroll > len(lines)-1 {
		gp.scroll = max(0, len(lines)-1)
	}
	end := min(len(lines), gp.scroll+h)
	return strings.Join(lines[gp.scroll:end], "\n")
}

// goalPageLines renders the goal page as lines, top to bottom. cardAt
// tags each card in report.Cards with the line its row landed on — the
// cards are the one region with a cursor, and the render is the only
// place that knows where they ended up, since everything above them
// wraps to the width.
func goalPageLines(s *theme.Styles, gp *goalPageView, w int) ([]string, []int) {
	r := gp.report
	var out []string
	cardAt := make([]int, 0, len(r.Cards))
	add := func(line string) { out = append(out, line) }
	section := func(title string) {
		add("")
		add(" " + s.PaneTitleActive.Render(strings.ToUpper(title)))
	}
	clip := func(text string, indent int) string {
		room := w - indent - 1
		if room < 10 {
			room = 10
		}
		return ansi.Truncate(text, room, "…")
	}
	// wrap adds text in full, over as many lines as it takes: the evidence
	// for a done-when item, a drop's reason and a decision are what the
	// hand-over is read for, and a clipped one hides exactly its point
	wrap := func(text string, indent int, st func(...string) string) {
		room := max(w-indent-1, 10)
		for _, line := range strings.Split(ansi.Wrap(text, room, ""), "\n") {
			add(strings.Repeat(" ", indent) + st(line))
		}
	}
	plain := func(s ...string) string { return strings.Join(s, "") }

	met, total := r.Met()
	status := "running"
	switch {
	case r.Ready:
		status = "ready for you"
	case r.WrappingUp:
		status = "wrapping up"
	case r.Stage == domain.StagePlan:
		status = "agreeing the plan"
	case r.Stage == domain.StageDone:
		status = "done"
	}
	stateStyle := s.Info
	if r.Ready {
		stateStyle = s.Success
	}
	add(" " + s.Info.Render(string(r.ID)) + " " + s.CardTitle.Render(clip(r.Title, 12)))
	head := " " + stateStyle.Render(status)
	if r.Partial != "" {
		head += s.Warning.Render(" — partial: " + r.Partial)
	}
	head += s.Faint.Render(fmt.Sprintf(" · %d of %d done-when met · %d lanes", met, total, r.Lanes))
	add(head)

	section("done when")
	if len(r.DoneWhen) == 0 {
		add("   " + s.Faint.Render("nothing agreed yet"))
	}
	for _, d := range r.DoneWhen {
		mark, st := s.Faint.Render("?"), s.Faint
		switch d.Status {
		case engine.DoneWhenMet:
			mark, st = s.Success.Render("✓"), s.Success
		case engine.DoneWhenNotMet:
			mark, st = s.Error.Render("✗"), s.Error
		}
		add("   " + mark + " " + d.ID + " " + clip(d.Says, 10))
		detail := d.How
		if d.Evidence != "" {
			detail = d.Status + ": " + d.Evidence
		}
		wrap(detail, 7, st.Render)
	}

	// One repository says nothing worth a section. Several is the thing a
	// reader has to know at the hand-over, because the goal lands once in
	// each and can be in some of them and not the others.
	if len(r.Repos) > 1 {
		section("repositories")
		for _, rp := range r.Repos {
			mark, st := s.Faint.Render("○"), s.Faint
			if rp.Landed {
				mark, st = s.Success.Render("✓"), s.Success
			}
			name := rp.Name
			if name == "" {
				name = "default"
			}
			tail := " · not landed"
			if rp.Landed {
				tail = " · landed"
			}
			if rp.Home {
				tail += " · the goal's own"
			}
			add("   " + mark + " " + name + st.Render(tail))
		}
	}

	section("cards")
	if len(r.Cards) == 0 {
		add("   " + s.Faint.Render("none yet"))
	} else {
		add("   " + s.Faint.Render("enter watches the selected card — read-only, its lead drives it"))
	}
	for ci, c := range r.Cards {
		cardAt = append(cardAt, len(out))
		glyph := s.Faint.Render("○")
		switch c.State {
		case "landed":
			glyph = s.Success.Render("✓")
		case "running", "verified":
			glyph = s.Info.Render("◐")
		case "dropped":
			glyph = s.Faint.Render("⊘")
		case "stuck", "exhausted":
			glyph = s.Warning.Render("!")
		}
		tail := fmt.Sprintf(" · %s · %.0f/%d", c.State, c.Spent, c.Envelope)
		if len(r.Repos) > 1 && c.Repo != "" {
			tail = " · " + c.Repo + tail
		}
		if len(c.Serves) > 0 {
			tail += " · " + strings.Join(c.Serves, ",")
		}
		cursor := "  "
		if ci == gp.cursor {
			cursor = s.SelMarker.Render("▸ ")
		}
		add(" " + cursor + glyph + " " + string(c.ID) + " " + clip(c.Title+s.Faint.Render(tail), 12))
		switch {
		case c.Commit != "":
			add("       " + s.Faint.Render(clip("landed as "+shortHash(c.Commit)+" "+c.Subject, 8)))
			for _, line := range strings.Split(c.Stat, "\n") {
				if strings.TrimSpace(line) != "" {
					add("       " + s.Faint.Render(clip(strings.TrimSpace(line), 8)))
				}
			}
		case c.Reason != "":
			wrap(c.Reason, 7, s.Faint.Render)
		}
	}

	section("budget")
	b := r.Budget
	add(fmt.Sprintf("   %d credits · goal %.0f · cards %.0f · reserve %d · left to give %.0f", b.Envelope, b.Own, b.CardSpent, b.Reserve, b.Available))

	if len(r.Decisions) > 0 {
		section("decisions for review")
		for _, d := range r.Decisions {
			wrap(d.Ref+" "+d.Detail, 3, plain)
			if d.Alternative != "" {
				wrap("not: "+d.Alternative, 7, s.Faint.Render)
			}
		}
	}
	if len(r.Declined) > 0 {
		section("declined findings")
		for _, d := range r.Declined {
			wrap(string(d.Card)+" "+d.Finding, 3, plain)
			wrap(d.Detail, 7, s.Faint.Render)
		}
	}
	if len(r.Found) > 0 {
		section("found along the way")
		for _, d := range r.Found {
			wrap(string(d.Card)+" "+d.Detail, 3, plain)
		}
	}
	if r.TryIt != "" {
		section("try it")
		for _, line := range strings.Split(r.TryIt, "\n") {
			wrap(line, 3, plain)
		}
	}
	if len(gp.log) > 0 {
		section("log")
		start := max(0, len(gp.log)-40)
		for _, en := range gp.log[start:] {
			line := en.At.Local().Format("15:04") + " " + en.Action
			if en.Card != "" {
				line += " " + string(en.Card)
			}
			if en.N > 0 {
				line += " " + en.DecisionRef()
			}
			if en.Detail != "" && en.Action != state.GoalChecks {
				first, _, _ := strings.Cut(en.Detail, "\n")
				line += s.Faint.Render(" " + first)
			}
			add("   " + clip(line, 3))
		}
	}
	return out, cardAt
}

// --- reversing a decision ------------------------------------------------------

// openReverseDecision offers f's decisions for review to reverse.
func (m *Shell) openReverseDecision(f domain.Feature) tea.Cmd {
	store, eng := m.store, m.engine
	return func() tea.Msg {
		log, err := store.GoalLog(context.Background(), f.ID)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		var decisions []state.GoalEntry
		for _, en := range log {
			if en.Action == state.GoalDecision {
				decisions = append(decisions, en)
			}
		}
		if len(decisions) == 0 {
			return noticeMsg{text: string(f.ID) + " has no decisions for review"}
		}
		return reverseDialogMsg{f: f, decisions: decisions, eng: eng}
	}
}

type reverseDialogMsg struct {
	f         domain.Feature
	decisions []state.GoalEntry
	eng       *engine.Engine
}

// reverseDialog picks one decision for review to reverse.
type reverseDialog struct {
	f         domain.Feature
	decisions []state.GoalEntry
	cursor    int
	eng       *engine.Engine
}

func (d *reverseDialog) ID() string { return "reverse-decision" }

func (d *reverseDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "j", "down":
		if d.cursor < len(d.decisions)-1 {
			d.cursor++
		}
	case "k", "up":
		if d.cursor > 0 {
			d.cursor--
		}
	case "enter":
		dec := d.decisions[d.cursor]
		f, eng := d.f, d.eng
		return true, func() tea.Msg {
			if err := eng.ReverseGoalDecision(context.Background(), f.ID, dec.DecisionRef(), ""); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return noticeMsg{text: string(f.ID) + ": " + dec.DecisionRef() + " reversed — the goal went back to its cards", reload: true, clearInbox: f.ID}
		}
	}
	return false, nil
}

func (d *reverseDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.PaneTitleActive.Render("reverse a decision for review") + "\n")
	b.WriteString(s.Faint.Render("the lead takes the other way and redoes what it touched") + "\n\n")
	for i, dec := range d.decisions {
		cursor := "  "
		if i == d.cursor {
			cursor = s.SelMarker.Render("▸ ")
		}
		line := dec.DecisionRef() + " " + dec.Detail
		if dec.Alternative != "" {
			line += s.Faint.Render(" → " + dec.Alternative)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + s.Faint.Render("j/k choose · enter reverse · esc cancel"))
	return b.String()
}
