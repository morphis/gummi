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
	scroll := 0
	if m.goalPage != nil && m.goalPage.goal.ID == msg.goal.ID {
		scroll = m.goalPage.scroll
	}
	m.goalPage = &goalPageView{goal: msg.goal, report: msg.report, log: msg.log, scroll: scroll}
	return nil
}

func (gp *goalPageView) bindings() []binding {
	return []binding{
		{key: "j/k", label: "scroll", help: "scroll the goal page"},
		{key: "r", label: "reload", help: "read the goal again", bar: true},
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "close the goal page (also q)", bar: true},
	}
}

func (m *Shell) handleGoalPageKey(key string) tea.Cmd {
	gp := m.goalPage
	switch key {
	case "esc", "q":
		m.goalPage = nil
	case "j", "down":
		gp.scroll++
	case "k", "up":
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

// goalPageRender draws the page, scrolled.
func (m *Shell) goalPageRender(w, h int) string {
	lines := goalPageLines(m.styles, m.goalPage, w)
	gp := m.goalPage
	if gp.scroll > len(lines)-1 {
		gp.scroll = max(0, len(lines)-1)
	}
	end := min(len(lines), gp.scroll+h)
	return strings.Join(lines[gp.scroll:end], "\n")
}

// goalPageLines renders the goal page as lines, top to bottom.
func goalPageLines(s *theme.Styles, gp *goalPageView, w int) []string {
	r := gp.report
	var out []string
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
		add("       " + st.Render(clip(detail, 8)))
	}

	section("cards")
	if len(r.Cards) == 0 {
		add("   " + s.Faint.Render("none yet"))
	}
	for _, c := range r.Cards {
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
		if len(c.Serves) > 0 {
			tail += " · " + strings.Join(c.Serves, ",")
		}
		add("   " + glyph + " " + string(c.ID) + " " + clip(c.Title+s.Faint.Render(tail), 12))
		switch {
		case c.Commit != "":
			add("       " + s.Faint.Render(clip("landed as "+shortHash(c.Commit)+" "+c.Subject, 8)))
		case c.Reason != "":
			add("       " + s.Faint.Render(clip(c.Reason, 8)))
		}
	}

	section("budget")
	b := r.Budget
	add(fmt.Sprintf("   %d credits · goal %.0f · cards %.0f · reserve %d · left to give %.0f", b.Envelope, b.Own, b.CardSpent, b.Reserve, b.Available))

	if len(r.Decisions) > 0 {
		section("decisions for review")
		for _, d := range r.Decisions {
			line := d.Ref + " " + d.Detail
			if d.Alternative != "" {
				line += s.Faint.Render(" (not: " + d.Alternative + ")")
			}
			add("   " + clip(line, 3))
		}
	}
	if len(r.Declined) > 0 {
		section("declined findings")
		for _, d := range r.Declined {
			add("   " + clip(string(d.Card)+" "+d.Finding+s.Faint.Render(" — "+d.Detail), 3))
		}
	}
	if len(r.Found) > 0 {
		section("found along the way")
		for _, d := range r.Found {
			add("   " + clip(string(d.Card)+" "+d.Detail, 3))
		}
	}
	if r.TryIt != "" {
		section("try it")
		for _, line := range strings.Split(r.TryIt, "\n") {
			add("   " + clip(line, 3))
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
	return out
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
