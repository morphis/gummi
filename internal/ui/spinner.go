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
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// spinner returns the current frame of the shared activity spinner.
func (m *Shell) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// spinnerActive reports whether anything on screen animates: a busy
// agent session (chat header, board card, dashboard activity) or a
// running ingest pass (feed header, status pill). While true, Update
// keeps exactly one tick loop alive; when it goes false the loop stops
// so an idle board schedules no wake-ups.
func (m *Shell) spinnerActive() bool {
	if m.ingestRun != nil {
		return true
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
