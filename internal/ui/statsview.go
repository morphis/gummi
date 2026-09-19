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
	"github.com/morphis/gummi/internal/ui/theme"
)

// The stats tab: where a card's credits and hours went.
//
// The board already answers the card's other questions. The inbox says
// what needs you; the thread says what happened; the week view says what
// a run produced. None of them said how a card ran — which pass burned
// the money, how long it sat waiting on you, what it did with its hands.
// The nearest thing was the thread's folded receipt, one line per
// finished session, which is the right idea at the wrong scale: per
// session, never totalled and never compared.
//
// It is a tab and not a section of the thread for the same reason the
// artifact and the diff are tabs (cardtabs.go): reading is what you do
// before answering, and a reading surface offered among the answers is a
// row the reader has to rule out every visit. And it is a tab rather than
// part of the card's closing block because the most useful thing it says
// — that this card has already spent 43% of its credits doing something
// twice — is only worth knowing while the card is still running.
//
// Two rules govern what it draws. The redo gets a block of its own rather
// than a column in a table of eleven passes, because a report that makes
// you count rows to find the expensive mistake has buried its own
// headline; and where a backend cannot report something, the surface says
// so in the sentence where the number would have been, rather than
// rendering an empty list as a quiet zero.

// statsView is the mounted run tab.
type statsView struct {
	f      domain.Feature
	report cardrun.Run
	scroll int
}

type statsLoadedMsg struct {
	f      domain.Feature
	report cardrun.Run
	err    error
}

// openStats reads the card's record and mounts its run tab. Every read
// degrades to its zero value rather than failing the surface: a reader
// asking how a card ran must still get the part of the answer that is
// readable.
func (m *Shell) openStats(f domain.Feature) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if store == nil {
			return statsLoadedMsg{f: f, err: fmt.Errorf("no store to read the run from")}
		}
		ctx := context.Background()
		// The card is re-read rather than taken from the board row: the
		// row is a snapshot from the last board refresh, while everything
		// below is read now, and a one-shot that spent two credits in
		// between would leave the totals disagreeing with the breakdown
		// they are supposed to be the sum of. A ledger whose columns do
		// not add up is worse than no ledger.
		if fresh, err := store.GetFeature(ctx, f.ID); err == nil {
			f = fresh
		}
		evs, err := store.Events(ctx, f.ID)
		if err != nil {
			return statsLoadedMsg{f: f, err: err}
		}
		spend, err := store.SessionBreakdown(ctx, f.ID)
		if err != nil {
			spend = nil
		}
		rnds := map[domain.RoundKind]int{}
		for _, k := range []domain.RoundKind{
			domain.RoundKindPlan, domain.RoundKindReview, domain.RoundKindCorrective,
		} {
			if n, err := store.Rounds(ctx, f.ID, k); err == nil {
				rnds[k] = n
			}
		}
		baseline, err := store.CheckBaseline(ctx, f.ID)
		if err != nil {
			baseline = nil
		}
		return statsLoadedMsg{f: f, report: cardrun.Report(cardrun.Input{
			Feature: f, Events: evs, Spend: spend, Rounds: rnds, Baseline: baseline,
		})}
	}
}

func (m *Shell) statsLoaded(msg statsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	scroll := 0
	if m.stats != nil && m.stats.f.ID == msg.f.ID {
		scroll = m.stats.scroll
	}
	m.stats = &statsView{f: msg.f, report: msg.report, scroll: scroll}
	return nil
}

func (rv *statsView) bindings() []binding {
	return []binding{
		{key: "j/k", label: "scroll", help: "scroll the run"},
		{key: "r", label: "reload", help: "read the card's record again", bar: true},
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "back to the thread", bar: true},
	}
}

func (m *Shell) handleStatsKey(key string) tea.Cmd {
	rv := m.stats
	switch key {
	case "esc", "q":
		m.stats = nil
	case "j", "down":
		rv.scroll++
	case "k", "up":
		if rv.scroll > 0 {
			rv.scroll--
		}
	case "pgdown", "space":
		rv.scroll += 10
	case "pgup":
		rv.scroll = max(0, rv.scroll-10)
	case "r":
		return m.openStats(rv.f)
	}
	return nil
}

// statsViewRender draws the run, scrolled.
func (m *Shell) statsViewRender(w, h int) string {
	lines := statsLines(m.styles, m.stats.report, w)
	rv := m.stats
	if rv.scroll > len(lines)-1 {
		rv.scroll = max(0, len(lines)-1)
	}
	end := min(len(lines), rv.scroll+h)
	return strings.Join(lines[rv.scroll:end], "\n")
}

// statsBarWidth is how wide the magnitude bars are drawn, in columns. Wide
// enough for a share to read at a glance, narrow enough that the figure
// beside it still fits on a modest terminal.
const statsBarWidth = 24

// statsHeading is a section rule: a blank row and the section's name, the
// same shape every other pane in the board uses for its headers.
func statsHeading(s *theme.Styles, title string) []string {
	return []string{"", " " + s.PaneTitleActive.Render(strings.ToUpper(title))}
}

// statsLines renders the whole surface, top to bottom.
func statsLines(s *theme.Styles, r cardrun.Run, w int) []string {
	var out []string
	add := func(line string) { out = append(out, line) }
	clip := func(text string) string { return ansi.Truncate(text, max(w-1, 10), "…") }

	add(" " + s.CardTitle.Render(clip(string(r.ID)+"  "+r.Title)))
	head := string(r.Stage)
	if len(r.Sessions) > 0 {
		head += fmt.Sprintf(" · %d session%s", len(r.Sessions), plural(len(r.Sessions)))
	}
	add(" " + s.Muted.Render(clip(head)))
	if len(r.Sessions) == 0 {
		add("")
		add(" " + s.Subtle.Render("nothing has run on this card yet"))
		return out
	}

	out = append(out, statsMoneyLines(s, r, clip)...)
	out = append(out, statsRedoLines(s, r, clip)...)
	out = append(out, statsClockLines(s, r)...)
	out = append(out, statsHandsLines(s, r, clip)...)
	out = append(out, statsEnvelopeLines(s, r)...)
	return out
}

func statsMoneyLines(s *theme.Styles, r cardrun.Run, clip func(string) string) []string {
	out := statsHeading(s, "where it went")
	add := func(line string) { out = append(out, line) }
	for _, b := range r.Money.ByStage {
		add(clip(fmt.Sprintf("  %-11s %s %8.2f  %3.0f%%",
			b.Name, s.Info.Render(statsBar(b.Credits, r.Money.Credits)),
			b.Credits, share(b.Credits, r.Money.Credits)*100)))
	}
	add(clip(fmt.Sprintf("  %-11s %s %8.2f  %s",
		"", strings.Repeat(" ", statsBarWidth), r.Money.Credits, s.Muted.Render("credits"))))
	// An unsettled figure is marked where it stands rather than in a
	// footnote: a number a provider may still correct has to read as one.
	if r.Money.Estimated > 0 {
		add("  " + s.Warning.Render(fmt.Sprintf("~%.2f estimated", r.Money.Estimated)) +
			" " + s.Faint.Render("— not yet settled by the provider"))
	}
	if r.Money.Rework > 0 {
		add("  " + s.Info.Render("█") + s.Muted.Render(fmt.Sprintf(" first pass %.2f", r.Money.FirstPass)) +
			"   " + s.Warning.Render("█") +
			s.Muted.Render(fmt.Sprintf(" redone %.2f (%.0f%%)",
				r.Money.Rework, r.Money.ReworkShare()*100)))
	}
	return out
}

// statsRedoLines is the headline, and it earns its own block: a run report
// that makes you count rows to find the expensive mistake has buried its
// own point. It is absent entirely on a card that never did anything
// twice, which is most of them.
func statsRedoLines(s *theme.Styles, r cardrun.Run, clip func(string) string) []string {
	var redone []cardrun.Session
	for _, p := range r.Sessions {
		if p.Redo {
			redone = append(redone, p)
		}
	}
	if len(redone) == 0 {
		return nil
	}
	out := statsHeading(s, "the redo")
	// The first pass of each redone piece of work, so the comparison the
	// block exists to make is on the page rather than in the reader's head.
	first := map[string]cardrun.Session{}
	for _, p := range r.Sessions {
		k := string(p.Stage) + "\x00" + p.Role + "\x00" + p.Flavor
		if _, seen := first[k]; !seen {
			first[k] = p
		}
	}
	for _, p := range redone {
		line := fmt.Sprintf("  %s · %s · %s · %d turn%s · %s · %.2f%s",
			p.Stage, p.Role, p.RedoReason, p.Turns, plural(p.Turns),
			shortDur(p.Duration()), p.Credits, reconMark(p))
		k := string(p.Stage) + "\x00" + p.Role + "\x00" + p.Flavor
		if f, ok := first[k]; ok && p.Credits > f.Credits && f.Credits > 0 {
			line += "  ← cost more than the first"
		}
		out = append(out, clip(s.Warning.Render(line)))
	}
	out = append(out, clip(s.Muted.Render(fmt.Sprintf(
		"  %.2f of %.2f credits was work already done (%.0f%%)",
		r.Money.Rework, r.Money.Credits, r.Money.ReworkShare()*100))))
	if r.Money.Reproved > 0 && r.Money.Corrected > 0 {
		out = append(out, clip(s.Faint.Render(fmt.Sprintf(
			"  %.2f corrected after a verdict · %.2f re-proved over a new base",
			r.Money.Corrected, r.Money.Reproved))))
	}
	return out
}

func statsClockLines(s *theme.Styles, r cardrun.Run) []string {
	if r.Clock.Elapsed <= 0 {
		return nil
	}
	out := append(statsHeading(s, "the clock"),
		fmt.Sprintf("  %-15s %10s", "agent working", shortDur(r.Clock.Agent)),
		fmt.Sprintf("  %-15s %10s  %s", "waiting on you", shortDur(r.Clock.OnYou),
			s.Muted.Render(fmt.Sprintf("(%.0f%%)", r.Clock.OnYouShare()*100))),
	)
	// Only a card with unexplained time has to account for it; the rest
	// say nothing rather than a zero.
	if r.Clock.Idle > 0 {
		out = append(out, fmt.Sprintf("  %-15s %10s  %s", "nothing running", shortDur(r.Clock.Idle),
			s.Muted.Render(fmt.Sprintf("(%.0f%%)", r.Clock.IdleShare()*100))))
	}
	out = append(out, fmt.Sprintf("  %-15s %10s", "elapsed", shortDur(r.Clock.Elapsed)))
	if r.Clock.ToVerified > 0 {
		out = append(out, s.Faint.Render(fmt.Sprintf("  %-15s %10s", "to verified", shortDur(r.Clock.ToVerified))))
	}
	return out
}

func statsHandsLines(s *theme.Styles, r cardrun.Run, clip func(string) string) []string {
	out := statsHeading(s, "its hands")
	add := func(line string) { out = append(out, clip(line)) }

	add(fmt.Sprintf("  %-15s %d", "turns", r.Hands.Turns))
	// The honest shape of a missing record: where a backend cannot report
	// something, say so in the sentence the number would have taken. Three
	// of gummi's six backends report no tool outcomes, and a zero here
	// would be a claim about the card that only holds about the backend.
	if r.Hands.Tools == nil {
		add("  " + fmt.Sprintf("%-15s ", "tools") +
			s.Faint.Render("none recorded — this backend reports no tool calls"))
	} else {
		add(fmt.Sprintf("  %-15s %d call%s, %d failed", "tools",
			r.Hands.ToolCalls, plural(r.Hands.ToolCalls), r.Hands.ToolFails))
		for _, t := range r.Hands.Tools {
			line := fmt.Sprintf("    %-13s %d", t.Name, t.Calls)
			if t.Fails > 0 {
				line += fmt.Sprintf(", %d failed", t.Fails)
			}
			if t.Total > 0 {
				line += " · " + shortDur(t.Total)
			}
			add(s.Subtle.Render(line))
		}
	}
	for _, sk := range r.Hands.Skills {
		what := sk.Detail
		if what == "" {
			what = "(not recorded)"
		}
		add(fmt.Sprintf("  %-15s %s ×%d", "skill", what, sk.Calls))
	}
	for _, sa := range r.Hands.Subagents {
		add(fmt.Sprintf("  %-15s %d spawned", "subagents", sa.Calls))
	}
	for _, c := range r.Hands.Checks {
		line := fmt.Sprintf("  check %-9s %d run%s", c.Name, c.Runs, plural(c.Runs))
		if c.Fails > 0 {
			line += fmt.Sprintf(", %d failed", c.Fails)
		}
		if c.Excused {
			line += " (pre-existing, excused)"
		}
		add(line)
	}
	add(fmt.Sprintf("  %-15s %d — %d you, %d machine", "gates",
		r.Judgment.Gates.Total, r.Judgment.Gates.ByYou, r.Judgment.Gates.ByMachine))
	add(fmt.Sprintf("  %-15s %d — %d you, %d autopilot", "asks",
		r.Judgment.Asks.Total, r.Judgment.Asks.ByYou, r.Judgment.Asks.ByMachine))
	for _, p := range r.Judgment.Parks {
		detail := p.Detail
		if detail == "" {
			detail = p.Reason
		}
		add(s.Subtle.Render(fmt.Sprintf("  %-15s %s", "parked", detail)))
	}
	return out
}

func statsEnvelopeLines(s *theme.Styles, r cardrun.Run) []string {
	if r.Envelope.Granted <= 0 {
		return nil
	}
	out := statsHeading(s, "the envelope")
	line := fmt.Sprintf("  granted %d · spent %.0f · %.0f%% used",
		r.Envelope.Granted, r.Envelope.Spent, r.Envelope.Utilization()*100)
	if r.Envelope.Utilization() < 0.25 {
		out = append(out, s.Muted.Render(line))
		return out
	}
	out = append(out, line)
	return out
}

// statsBar draws a proportional magnitude bar. One hue for every stage:
// these are shares of one total, not different kinds of thing, so the
// form that fits is magnitude, and magnitude needs no palette.
func statsBar(v, total float64) string {
	if total <= 0 || v <= 0 {
		return strings.Repeat(" ", statsBarWidth)
	}
	n := int(v/total*float64(statsBarWidth) + 0.5)
	if n < 1 {
		n = 1
	}
	if n > statsBarWidth {
		n = statsBarWidth
	}
	return strings.Repeat("█", n) + strings.Repeat(" ", statsBarWidth-n)
}

func share(v, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return v / total
}

// reconMark flags a figure read off a pass's stage_exit rather than
// measured per usage sample — the only figure a card that ran before the
// session key has. It is printed rather than hidden because a
// reconstruction that looks like a measurement is the one thing a ledger
// must never be.
func reconMark(p cardrun.Session) string {
	if p.Reconstructed {
		return " ~"
	}
	return ""
}

// shortDur renders a span the way a person reads one.
//
// A span under a second reads as "<1s" rather than rounding to "0s": a
// zero beside a nonzero share ("0s, 47%") reads as a broken number, and
// the honest thing to say about a stage that took 300ms is that it was
// too quick to matter, not that it took no time at all.
func shortDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.1fm", d.Minutes())
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// statsHasRecord reports whether a card has anything to show on the tab —
// used by the tab bar, so it never offers a surface that would open on
// an empty page.
func statsHasRecord(r featureRow) bool {
	return r.F.Stage != domain.StageTodo || r.F.Spend.Credits > 0
}
