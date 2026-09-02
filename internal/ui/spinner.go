package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// spinnerFrames is the braille activity cycle. Every busy marker in the
// UI renders the Shell's shared frame, so they all spin in lockstep off
// a single clock.
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// spinnerInterval paces the animation: fast enough to read as motion,
// slow enough not to wake the terminal for nothing.
const spinnerInterval = 120 * time.Millisecond

// spinnerTickMsg advances the shared spinner frame.
type spinnerTickMsg struct{}

// spinnerTick schedules the next frame advance.
func spinnerTick() tea.Cmd {
	return subscription(tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} }))
}

// spinner returns the current frame of the shared activity spinner.
func (m *Shell) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// spinnerGlyph returns the advancing frame when animate is true, and the
// spinner's fixed first frame otherwise — a busy marker that isn't the
// board's selected card, or any marker at all with motion disabled.
func (m *Shell) spinnerGlyph(animate bool) string {
	if animate {
		return m.spinner()
	}
	return spinnerFrames[0]
}

// spinnerActive reports whether anything on screen animates: a busy
// agent session (chat header, board card, dashboard activity) or a
// running ingest pass (feed header, status pill). While true, Update
// keeps exactly one tick loop alive; when it goes false the loop stops
// so an idle board schedules no wake-ups.
func (m *Shell) spinnerActive() bool {
	if !m.motionEnabled {
		return false
	}
	if m.ingestRun != nil || m.mergePrep || m.squashPrep || len(m.baselining) > 0 || len(m.scribing) > 0 {
		return true
	}
	// the board session is not one of m.engine.Sessions() below — those
	// are card-scoped, and a board session is bound to the workspace
	// instead (engine/boardsession.go) — so its own busy turn has to be
	// checked separately, or the shared spinner glyph in its thread would
	// just sit frozen on one frame while it thinks.
	if m.board != nil && m.board.Snapshot().Busy {
		return true
	}
	// a foreign-driven card's busy state lives only in its row snapshot —
	// unlike the local sources above, no live engine session covers
	// another process's session, so this is the one place that state can
	// be read from.
	for _, r := range m.rows {
		if r.DrivenAbroad && r.Foreign.Busy {
			return true
		}
	}
	if m.engine == nil {
		return false
	}
	for _, s := range m.engine.Sessions() {
		if s.Busy() {
			return true
		}
	}
	return false
}
