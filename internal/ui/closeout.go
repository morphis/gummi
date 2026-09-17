package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The close-out pass is the end of a session, as one ritual.
//
// Autopilot never landing is the right guarantee. Its consequence is that
// a night of autonomous work produces five cards parked at verify and a
// morning of five separate visits: find the card, read it, g, review the
// message, approve — and later, c. The inbox holds all five stops and, by
// design, will not act on them; that rule is right for ambiguous
// decisions and wrong for the one decision that is identical on every
// card.
//
// So: one pass, entered on purpose with C and left with esc, that walks
// the verified cards one at a time and then ends on the cleanup sweep.
// Landing and tidying are the same ritual because that is when people
// actually do both — at the end of a session, not at the moment each card
// happens to finish.
//
// Two things it is NOT, deliberately:
//
//   - It is not a batch. There is exactly one confirm per card and the
//     drafted commit message is still the review. The moment this becomes
//     "land all five", the quality floor that justifies running gummi at
//     all is gone. Skip is bound as cheaply as land for the same reason.
//   - It is not a queue it owns. The pass is a VIEW over the verified
//     set, recomputed from the board's rows every frame — so landing a
//     card through the ordinary merge dialog simply removes it from the
//     list, and a card that arrives while the pass is open joins it.
//     Nothing here can go stale against the board.

// closeOutPhase is which half of the ritual is on screen.
type closeOutPhase int

const (
	// phaseLand walks the cards whose branches are ready.
	phaseLand closeOutPhase = iota
	// phaseSweep is the cleanup that follows, once nothing is left to land.
	phaseSweep
)

// closeOutDialog is the pass. It holds only what the board cannot: which
// cards the reader chose to skip this time round, and the sweep's
// measured figures once something has measured them.
type closeOutDialog struct {
	m       *Shell
	phase   closeOutPhase
	skipped map[domain.FeatureID]bool
	// sweep is the measured cleanup plan, nil until the sweep phase asks
	// for it. Measuring walks each worktree, so it happens once, on
	// arrival at the phase that shows it.
	sweep    *sweepPlan
	sweeping bool
}

func (m *Shell) openCloseOut() tea.Cmd {
	d := &closeOutDialog{m: m, skipped: map[domain.FeatureID]bool{}}
	if len(d.readyCards()) == 0 {
		d.phase = phaseSweep
	}
	m.Overlay.Push(d)
	if d.phase == phaseSweep {
		return d.measure()
	}
	return nil
}

func (d *closeOutDialog) ID() string { return "close-out" }

// readyCards is the verified set: cards stopped at the landing gate with
// their branch ready, minus the ones skipped this time round.
//
// VerifiedAt is the signal because it is stamped at exactly that stop
// (advance.go), survives restarts, and belongs to the card rather than to
// any session — so a card verified by a headless run in another process
// is in this list too.
func (d *closeOutDialog) readyCards() []featureRow {
	var out []featureRow
	for _, r := range d.m.rows {
		if r.F.Stage != domain.StageVerify || r.F.VerifiedAt.IsZero() || d.skipped[r.F.ID] {
			continue
		}
		if r.F.InGoal() {
			// a goal card's branch is its goal's to land, never a person's
			continue
		}
		out = append(out, r)
	}
	return out
}

func (d *closeOutDialog) current() (featureRow, bool) {
	ready := d.readyCards()
	if len(ready) == 0 {
		return featureRow{}, false
	}
	return ready[0], true
}

func (d *closeOutDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	if d.phase == phaseSweep {
		return d.handleSweepKey(key)
	}
	r, ok := d.current()
	if !ok {
		// nothing left to land: fall through to the sweep rather than
		// closing, because that is the other half of the same ritual
		d.phase = phaseSweep
		return false, d.measure()
	}
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		// the ordinary merge flow, message dialog and all. It opens ON TOP
		// of this one; when it finishes, the card leaves readyCards and
		// this pass shows the next.
		return false, d.m.prepareMerge(r.F, true)
	case "h":
		return false, d.m.prepareHandOff(r.F)
	case "s":
		d.skipped[r.F.ID] = true
		if len(d.readyCards()) == 0 {
			d.phase = phaseSweep
			return false, d.measure()
		}
		return false, nil
	case "d":
		// reading the diff is why someone would hesitate, so it is one key
		// away rather than a reason to leave
		return true, d.m.openDiff(r.F)
	}
	return false, nil
}

func (d *closeOutDialog) handleSweepKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		if d.sweep == nil || len(d.sweep.Clean) == 0 || d.sweeping {
			return true, nil
		}
		d.sweeping = true
		return true, d.m.runSweep(*d.sweep)
	}
	return false, nil
}

// measure builds the sweep plan off the render loop.
func (d *closeOutDialog) measure() tea.Cmd {
	rows := append([]featureRow(nil), d.m.rows...)
	m := d.m
	return func() tea.Msg {
		return sweepPlannedMsg{plan: m.planSweep(context.Background(), rows)}
	}
}

func (d *closeOutDialog) View(s *theme.Styles, width, height int) string {
	_ = height
	if d.phase == phaseSweep {
		return d.sweepView(s, width, height)
	}
	ready := d.readyCards()
	r, ok := d.current()
	if !ok {
		return closeOutFrame(s, "close out", s.Base.Render("nothing is waiting to land."))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s\n", s.CardID.Render(string(r.F.ID)), r.F.Title)
	fmt.Fprintf(&b, "   %s\n\n", s.Faint.Render(
		"verified · "+featureSpend(r.F.Spend)+" · lands on "+r.baseBranch()))
	if n := len(ready); n > 1 {
		b.WriteString(s.Faint.Render(fmt.Sprintf("   %d more after this one\n\n", n-1)))
	}
	b.WriteString(s.Base.Render("   gummi drafts the commit message; you review and approve it.\n\n"))
	b.WriteString(s.Faint.Render(
		"   enter land   ·   h hand off   ·   s skip   ·   d diff   ·   esc leave"))
	return closeOutFrame(s, fmt.Sprintf("close out · %d to land", len(ready)), b.String())
}

func (d *closeOutDialog) sweepView(s *theme.Styles, _, _ int) string {
	if d.sweep == nil {
		return closeOutFrame(s, "close out · sweep", s.Faint.Render("measuring…"))
	}
	var b strings.Builder
	if len(d.sweep.Clean) == 0 {
		b.WriteString(s.Base.Render("nothing to clean up — every landed card is already tidy.\n"))
	} else {
		for _, c := range d.sweep.Clean {
			fmt.Fprintf(&b, "   %s   %s\n", s.CardID.Render(string(c.ID)),
				s.Faint.Render("worktree + branch   "+humanBytes(c.Bytes)))
		}
		fmt.Fprintf(&b, "\n   %s\n", s.Faint.Render(
			"frees "+humanBytes(d.sweep.Bytes)+" · keeps every record · nothing unlands"))
	}
	if len(d.sweep.Held) > 0 {
		b.WriteString("\n" + s.Faint.Render("   held back\n"))
		for _, h := range d.sweep.Held {
			fmt.Fprintf(&b, "   %s   %s\n", s.CardID.Render(string(h.ID)), s.Faint.Render(h.Why))
		}
	}
	b.WriteString("\n")
	if len(d.sweep.Clean) > 0 {
		fmt.Fprintf(&b, "   %s", s.Faint.Render(
			fmt.Sprintf("enter clean up %d   ·   esc done for now", len(d.sweep.Clean))))
	} else {
		b.WriteString("   " + s.Faint.Render("esc done for now"))
	}
	return closeOutFrame(s, "close out · sweep", b.String())
}

// closeOutFrame paints the pass in the shared dialog frame, with its
// title on the first line: the title says which half of the ritual is on
// screen and how much is left, which is the one thing a reader wants
// before reading anything else.
func closeOutFrame(s *theme.Styles, title, body string) string {
	return s.DialogFrame.Render(s.Subtle.Bold(true).Render(title) + "\n\n" + body)
}
