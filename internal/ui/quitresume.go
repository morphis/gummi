package ui

// This file is quit-and-reopen's other half: shell.go's quitCmd writes
// the marker on the way out (engine.Engine.StopForQuit); this reads it
// back on the way in. maybeOfferQuitResume runs once, from Init, after
// the engine has restored its sessions — it is the only place this
// dialog is ever pushed, and its confirm button is the only thing that
// may ever restart a quit-stopped card. "Not now" (or esc) leaves every
// card exactly where Restore put it: paused, reachable by hand like any
// other paused card.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verdict"
)

const quitResumeDialogWidth = 62

// maybeOfferQuitResume pushes the reopen prompt when engine.Restore just
// brought back cards StopForQuit stopped on the way out. Called once
// from Init; nothing else raises this dialog, and nothing about opening
// it starts anything on its own — that is the confirm button's job
// alone.
func (m *Shell) maybeOfferQuitResume() {
	if m.engine == nil {
		return
	}
	cards, err := m.engine.QuitStoppedCards(context.Background())
	if err != nil || len(cards) == 0 {
		return
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].Feature.ID < cards[j].Feature.ID })
	since := compactSince(m.now().Sub(earliestParked(cards))) + " ago"
	m.Overlay.Push(newQuitResumeDialog(cards, since, func() tea.Cmd {
		return m.resumeQuitStopped(cards)
	}))
}

// earliestParked is the oldest ParkedAt across the offered cards — every
// card StopForQuit stops in one call parks within the same instant, so
// this is really just "since you quit", read off whichever event landed
// first.
func earliestParked(cards []engine.QuitStoppedCard) time.Time {
	oldest := cards[0].ParkedAt
	for _, c := range cards[1:] {
		if c.ParkedAt.Before(oldest) {
			oldest = c.ParkedAt
		}
	}
	return oldest
}

// resumeQuitStopped restarts every offered card, batched into one
// command — the "Resume both" confirm.
func (m *Shell) resumeQuitStopped(cards []engine.QuitStoppedCard) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(cards))
	for _, c := range cards {
		cmds = append(cmds, m.resumeCard(c.Feature))
	}
	return tea.Batch(cmds...)
}

// resumeCard restarts one quit-stopped card in place. StopForQuit only
// ever stops a live Running/Queued session (engine/quit.go), which means
// the card was mid-stage, not parked at a gate — so planAutopilot
// (autopilot.go) resolves it to the "running" bucket, whose own start
// path is a no-op by design (nothing to start — the card was already
// going). What resumes a paused mid-stage session is the board's
// ordinary resume-in-place path, runStage (shell.go), the same one
// `enter` already uses on a paused autonomous-stage card. The
// todo/gate buckets are handled through the switch's own start path
// (startAutopilot) for completeness, though StopForQuit's own shape
// never actually produces them.
func (m *Shell) resumeCard(f domain.Feature) tea.Cmd {
	plan := m.planAutopilot(f)
	if plan.bucket == "todo" || plan.bucket == "gate" {
		return m.startAutopilot(f, f.GateApproval, plan)
	}
	return m.runStage(f)
}

// quitResumeDialog is the "pick up where you left off?" prompt: the
// cards StopForQuit stopped, named the same way the quit dialog named
// them on the way out, with what each was doing.
type quitResumeDialog struct {
	cards    []engine.QuitStoppedCard
	since    string
	buttons  *buttonRow
	onResume func() tea.Cmd
}

func newQuitResumeDialog(cards []engine.QuitStoppedCard, since string, onResume func() tea.Cmd) *quitResumeDialog {
	return &quitResumeDialog{
		cards: cards, since: since, onResume: onResume,
		buttons: newButtonRow(button{label: "Not now"}, button{label: resumeLabel(len(cards))}),
	}
}

// resumeLabel words the confirm button to how many cards it restarts.
func resumeLabel(n int) string {
	switch n {
	case 1:
		return "Resume"
	case 2:
		return "Resume both"
	default:
		return fmt.Sprintf("Resume all %d", n)
	}
}

// ID implements overlay.Dialog.
func (d *quitResumeDialog) ID() string { return "quit-resume" }

// HandleKey implements overlay.Dialog.
func (d *quitResumeDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "n", "N":
		return true, nil
	case "left", "h", "shift+tab":
		d.buttons.Move(-1)
		return false, nil
	case "right", "l", "tab":
		d.buttons.Move(1)
		return false, nil
	case "enter":
		if d.buttons.Cursor() == 1 {
			return true, d.onResume()
		}
		return true, nil
	}
	return false, nil
}

// cardLine renders one offered card: id, title, stage, and — only when
// it spent any — the corrective-round budget, worded the same way
// autopilotBody (autopilot.go) already does: rounds spent against
// verdict.MaxRounds(domain.RoundKindCorrective), never hardcoded.
func cardLine(c engine.QuitStoppedCard, idWidth, titleWidth int) string {
	line := "  " + padRight(string(c.Feature.ID), idWidth) + "  " +
		padRight(c.Feature.Title, titleWidth) + "  " + string(c.Feature.Stage)
	if c.Corrective > 0 {
		line += fmt.Sprintf(" · %d of %d corrections spent", c.Corrective, verdict.MaxRounds(domain.RoundKindCorrective))
	}
	return line
}

// View implements overlay.Dialog.
func (d *quitResumeDialog) View(s *theme.Styles, w, h int) string {
	width := min(quitResumeDialogWidth, max(w-8, 30))

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("pick up where you left off?") + "\n\n")

	noun := "cards were"
	if len(d.cards) == 1 {
		noun = "card was"
	}
	summary := fmt.Sprintf("%d %s stopped when you quit, %s.", len(d.cards), noun, d.since)
	for _, l := range strings.Split(wrapText(summary, width), "\n") {
		b.WriteString(s.Subtle.Render(l) + "\n")
	}
	b.WriteString("\n")

	idWidth, titleWidth := 0, 0
	for _, c := range d.cards {
		idWidth = max(idWidth, ansi.StringWidth(string(c.Feature.ID)))
		titleWidth = max(titleWidth, ansi.StringWidth(c.Feature.Title))
	}
	for _, c := range d.cards {
		b.WriteString(s.Subtle.Render(cardLine(c, idWidth, titleWidth)) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(d.buttons.View(s, true) + "\n")
	b.WriteString("\n" + s.Faint.Render("←/→ choose · enter select · esc not now"))
	return s.DialogFrame.Render(b.String())
}
