package engine

import (
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
)

// A card's session-less work, made visible.
//
// Check discovery and its baseline are one-shot passes: they hold a
// feature rather than a Session, which is what lets them outlive the gate
// they fire at. The cost of that was invisibility. Nothing in gummi asks
// "is this card working?" one way — the goal's conductor asks the engine's
// live-session map (Engine.Get), while the board and `gummi status` ask
// the card's live FILE, across process boundaries — and a one-shot pass
// was absent from both for the minutes it runs.
//
// Measured on canonical/lxd: 4.6 minutes and 95 credits at one card's plan
// gate, 14–25% of what each card in that drive cost. In that window the
// board drew a live card as "autopilot stopped without saying so" beside a
// masthead reading "autopilot: on" and a spend still climbing, offered
// "run implement — no active run — start the stage", and counted 0 of 2
// autopilot lanes in use; a goal declared its own healthy child card
// "stuck: stopped with nothing running" and spent a lead turn restarting
// it, 90 seconds (goalIdleGrace) into a pass that takes three times that.
//
// beginOneShot answers both questions for the duration: it counts the pass
// on the engine, which is what Engine.Get's callers consult, and binds the
// card's live file, which is what every other process consults. A pass in
// flight then looks like what it is — the card, working.

// beginOneShot marks a session-less pass as running for f and returns the
// function that ends it. Safe to nest: discovery and baseline both call
// it, and a card re-entered at a later gate counts again.
func (e *Engine) beginOneShot(f domain.Feature, role string) func() {
	e.mu.Lock()
	if e.oneShots == nil {
		e.oneShots = map[domain.FeatureID]int{}
	}
	e.oneShots[f.ID]++
	e.mu.Unlock()

	var w *livelog.Writer
	if e.cfg.Workspace.Root != "" {
		if lw, err := livelog.Create(e.cfg.Workspace.LiveFile(f.ID), livelog.Record{
			Feature: string(f.ID),
			Stage:   string(f.Stage),
			Role:    role,
		}); err == nil {
			w = lw
		}
	}
	return func() {
		if w != nil {
			// the terminal record, as a session writes on its way out: a
			// file closed without one reads as a live run whose process
			// merely went quiet.
			w.Emit(livelog.Record{Kind: livelog.KindStopped})
			w.Close()
		}
		e.mu.Lock()
		if n := e.oneShots[f.ID]; n <= 1 {
			delete(e.oneShots, f.ID)
		} else {
			e.oneShots[f.ID] = n - 1
		}
		e.mu.Unlock()
	}
}

// OneShotRunning reports whether a session-less pass is running for id in
// THIS process. The live file answers the same question across processes;
// this one exists for the in-process callers that reach for Engine.Get.
func (e *Engine) OneShotRunning(id domain.FeatureID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.oneShots[id] > 0
}
