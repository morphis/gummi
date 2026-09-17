package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The week view answers the one question no screen in gummi answered:
// was running this worth it?
//
// A goal's hand-over is the best ending in the product — every promise it
// made, met or not, with the evidence beside it, the cards and their
// landed commits, the budget tree. An ordinary card got the word
// "landed", and a whole week of them got nothing at all. The shape that
// answers "what did this produce" already existed and exactly one kind of
// card was allowed to use it.
//
// This is that shape at a third scale. It reads the same facts the
// closing block reads, over every card that settled inside the window,
// and groups them by ending — because "seven landed, two kept, one
// dropped" is the answer, and a single count of ten is not.

// weekWindow is how far back the view looks. A week, because that is the
// unit people actually report in, and because anything longer stops being
// a review and becomes an archive — which the board already has.
const weekWindow = 7 * 24 * time.Hour

// weekCard is one settled card as the view lists it.
type weekCard struct {
	ID         domain.FeatureID
	Title      string
	Ending     domain.Ending
	Commit     string
	Spend      float64
	Corrective int
	At         time.Time

	// Rework is what the card spent doing work it had already done, and
	// Waiting/Elapsed how much of its life nothing was running. Both come
	// from cardrun, which is the same derivation the card's own run tab
	// reads — so a week cannot disagree with the cards it is made of.
	Rework   float64
	Waiting  time.Duration
	Elapsed  time.Duration
	Envelope int
}

// weekReport is the whole view's data, measured once when it opens.
type weekReport struct {
	Cards []weekCard
	// ByEnding counts each ending. It is the headline: a week is not "ten
	// cards", it is seven landed, two kept and one given up on.
	ByEnding map[domain.Ending]int
	Spend    float64
	Rework   int
	// Redone is how many cards had any rework at all, which is the
	// denominator Rework needs to mean anything.
	Redone    int
	Open      int
	Costliest weekCard
	Cheapest  weekCard

	// The three ratios the week could not produce until a card's run was
	// derivable: what share of the week's spend went on work already done,
	// what share of its cards' lives was spent waiting on a person, and how
	// much of the envelopes granted was actually used.
	//
	// They belong here rather than on a card because each is only an
	// answer at this scale: one card at 43% rework is an anecdote, and a
	// week at 9% is a fact about how the board is running.
	ReworkCredits float64
	Waiting       time.Duration
	Elapsed       time.Duration
	Envelope      int
}

// ReworkShare, WaitingShare and EnvelopeShare are the week's three
// ratios. Each is zero when its denominator is, so a quiet week renders
// without a division.
func (r weekReport) ReworkShare() float64 {
	if r.Spend <= 0 {
		return 0
	}
	return r.ReworkCredits / r.Spend
}

func (r weekReport) WaitingShare() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Waiting) / float64(r.Elapsed)
}

func (r weekReport) EnvelopeShare() float64 {
	if r.Envelope <= 0 {
		return 0
	}
	return r.Spend / float64(r.Envelope)
}

// weekReportMsg carries a measured report to the open view.
type weekReportMsg struct{ report weekReport }

// weekDialog is the view. It holds the report and nothing else: there is
// nothing to decide here, so there is nothing to keep.
type weekDialog struct{ report *weekReport }

func (m *Shell) openWeek() tea.Cmd {
	d := &weekDialog{}
	m.Overlay.Push(d)
	return m.measureWeek()
}

func (d *weekDialog) ID() string { return "week" }

// HandleKey: esc closes it. Nothing else, deliberately — the view reports
// and does not act, and a key that acted from here would be acting on a
// card the reader cannot see the state of.
func (d *weekDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	if key.String() == "esc" || key.String() == "q" || key.String() == "enter" {
		return true, nil
	}
	return false, nil
}

// measureWeek builds the report off the render loop: it reads each
// settled card's rework total from the store, which is a query per card
// and belongs nowhere near a frame.
func (m *Shell) measureWeek() tea.Cmd {
	rows := append([]featureRow(nil), m.rows...)
	rs, now, store := m.roundStore, m.now(), m.store
	return func() tea.Msg {
		ctx := context.Background()
		rep := weekReport{ByEnding: map[domain.Ending]int{}}
		for _, r := range rows {
			if !r.settled() {
				if r.F.Stage != domain.StageTodo {
					rep.Open++
				}
				continue
			}
			at := doneAt(r.History)
			if at.IsZero() || now.Sub(at) > weekWindow {
				continue
			}
			c := weekCard{
				ID: r.F.ID, Title: r.F.Title, Ending: r.F.Ending(r.Landed),
				Commit: r.F.LandedSHA, Spend: r.F.Spend.Credits, At: at,
			}
			if rs != nil {
				c.Corrective, _ = rounds.Load(ctx, rs, r.F.ID, domain.RoundKindCorrective)
			}
			if store != nil {
				if run, ok := weekRun(ctx, store, r.F); ok {
					c.Rework, c.Waiting, c.Elapsed = run.Money.Rework, run.Clock.Waiting, run.Clock.Elapsed
					c.Envelope = run.Envelope.Granted
				}
			}
			rep.Cards = append(rep.Cards, c)
			rep.ByEnding[c.Ending]++
			rep.Spend += c.Spend
			rep.Rework += c.Corrective
			rep.ReworkCredits += c.Rework
			rep.Waiting += c.Waiting
			rep.Elapsed += c.Elapsed
			rep.Envelope += c.Envelope
			if c.Corrective > 0 {
				rep.Redone++
			}
			// Both extremes ignore cards that spent nothing: a card that
			// never ran is not "the cheapest", it is a card that never
			// ran, and reporting it as the week's bargain would be the
			// most misleading line on the screen.
			if c.Spend > 0 {
				if c.Spend > rep.Costliest.Spend {
					rep.Costliest = c
				}
				if rep.Cheapest.ID == "" || c.Spend < rep.Cheapest.Spend {
					rep.Cheapest = c
				}
			}
		}
		sort.SliceStable(rep.Cards, func(a, b int) bool { return rep.Cards[a].At.After(rep.Cards[b].At) })
		return weekReportMsg{report: rep}
	}
}

func (d *weekDialog) View(s *theme.Styles, _, _ int) string {
	if d.report == nil {
		return closeOutFrame(s, "this week", s.Faint.Render("measuring…"))
	}
	rep := d.report
	if len(rep.Cards) == 0 {
		return closeOutFrame(s, "this week",
			s.Base.Render("nothing has settled in the last seven days."))
	}
	var b strings.Builder

	// The headline is the endings, not the total: "seven landed, two kept,
	// one given up on" is the answer; "ten cards" is not.
	for _, e := range []domain.Ending{domain.EndingLanded, domain.EndingHandedOff, domain.EndingDropped} {
		n := rep.ByEnding[e]
		if n == 0 {
			continue
		}
		fmt.Fprintf(&b, "   %s  %s  %s\n",
			s.CardTitle.Render(pad(endingWord(e))),
			s.Base.Render(padLeft(strconv.Itoa(n), 3)),
			s.Faint.Render(weekEndingNote(e, rep)))
	}
	if rep.Open > 0 {
		fmt.Fprintf(&b, "   %s  %s  %s\n", s.CardTitle.Render(pad("open")),
			s.Base.Render(padLeft(strconv.Itoa(rep.Open), 3)),
			s.Faint.Render("still in flight"))
	}

	b.WriteString("\n")
	fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("cost")), s.Base.Render(money(rep.Spend)))
	if rep.Rework > 0 {
		redone := fmt.Sprintf("%d round%s across %d card%s",
			rep.Rework, plural(rep.Rework), rep.Redone, plural(rep.Redone))
		// A count of rounds says how often; the credits say what it cost,
		// which is the number that decides whether anything should change.
		if rep.ReworkCredits > 0 {
			redone += fmt.Sprintf(" · %s (%.0f%%)", money(rep.ReworkCredits), rep.ReworkShare()*100)
		}
		fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("redone")), s.Base.Render(redone))
	}
	if rep.Elapsed > 0 && rep.Waiting > 0 {
		fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("waiting")),
			s.Base.Render(fmt.Sprintf("%s of %s — %.0f%% of the week's card time",
				shortDur(rep.Waiting), shortDur(rep.Elapsed), rep.WaitingShare()*100)))
	}
	if rep.Envelope > 0 {
		fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("envelopes")),
			s.Faint.Render(fmt.Sprintf("%d granted · %.0f%% used", rep.Envelope, rep.EnvelopeShare()*100)))
	}
	if rep.Costliest.ID != "" {
		fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("costliest")),
			s.Faint.Render(string(rep.Costliest.ID)+" · "+money(rep.Costliest.Spend)))
	}
	if rep.Cheapest.ID != "" && rep.Cheapest.ID != rep.Costliest.ID {
		fmt.Fprintf(&b, "   %s  %s\n", s.CardTitle.Render(pad("cheapest")),
			s.Faint.Render(string(rep.Cheapest.ID)+" · "+money(rep.Cheapest.Spend)))
	}

	b.WriteString("\n")
	for _, c := range rep.Cards {
		line := "   " + s.CardID.Render(string(c.ID)) + "  " + s.Base.Render(c.Title)
		tail := endingWord(c.Ending)
		if c.Commit != "" {
			tail += " · " + shortSHA(c.Commit)
		}
		b.WriteString(line + "  " + s.Faint.Render(tail) + "\n")
	}
	b.WriteString("\n   " + s.Faint.Render("esc close"))
	return closeOutFrame(s, "this week · "+strconv.Itoa(len(rep.Cards))+" cards", b.String())
}

// weekEndingNote is the clause beside each ending's count — what that
// ending actually left behind, said once rather than implied by the word.
func weekEndingNote(e domain.Ending, rep *weekReport) string {
	switch e {
	case domain.EndingLanded:
		var n int
		for _, c := range rep.Cards {
			if c.Ending == domain.EndingLanded && c.Commit != "" {
				n++
			}
		}
		if n == 0 {
			return "on the base branch"
		}
		return fmt.Sprintf("on the base branch · %d commit%s gummi wrote", n, plural(n))
	case domain.EndingHandedOff:
		return "branches kept, nothing merged"
	case domain.EndingDropped:
		return "a goal gave up on them"
	}
	return ""
}

// padLeft right-aligns a short number so the counts line up.
func padLeft(s string, n int) string {
	for len(s) < n {
		s = " " + s
	}
	return s
}

// weekRun derives one settled card's run the same way its own run tab
// does, so the week's ratios and the card's page can never disagree.
//
// It reads the card's whole event log, which is why measureWeek runs off
// the render loop. A card whose record cannot be read is skipped rather
// than counted as zero: a week that quietly averages in an unreadable
// card reports a number nothing produced.
func weekRun(ctx context.Context, store *state.Store, f domain.Feature) (cardrun.Run, bool) {
	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		return cardrun.Run{}, false
	}
	spend, err := store.SessionBreakdown(ctx, f.ID)
	if err != nil {
		spend = nil
	}
	return cardrun.Report(cardrun.Input{Feature: f, Events: evs, Spend: spend}), true
}
