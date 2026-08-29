package engine

// This file is quit-and-reopen's engine half: StopForQuit writes the
// marker that tells a reopen a card was stopped by the board quitting,
// not by a human's p; QuitStoppedCards reads it back once Restore has
// reloaded the sessions it stopped. The UI (internal/ui/shell.go,
// quitresume.go) owns the confirm dialogs on both ends — this file only
// ever runs when the user has already said yes.
//
// The event log, not a column on the sessions table, is the record of
// "why is this card paused": state.Store.QuitStopped (cardevents.go)
// answers the question from a park event's reason, so there is exactly
// one place that fact is written and one place it is read.

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/state"
)

// StopForQuit stops every live session belonging to an autopilot card —
// GateApproval anything but domain.GateOff, same as everywhere else the
// field is interpreted (domain.Feature.GateApproval's own doc: empty
// reads as GateGates) — and records why in the card-event log, so a
// later QuitStoppedCards can tell this card apart from one a human
// parked with p. A card driven by hand (GateOff) is left untouched: the
// process exiting stops it the same way it always did, and nothing about
// it needs to be offered back specially on reopen.
//
// Best-effort, like the rest of persistence (persist, persistDelete):
// a failure here must never block quitting, so every store write's
// error is swallowed. Idempotent — each park event carries a dedupe key
// scoped to the session generation, so calling this twice (e.g. the
// confirm dialog firing more than once) never writes a second marker.
func (e *Engine) StopForQuit(ctx context.Context) {
	e.mu.Lock()
	var targets []*Session
	for _, s := range e.live {
		switch s.State() {
		case StateRunning, StateQueued:
		default:
			continue
		}
		if s.Feature.GateApproval == domain.GateOff {
			continue
		}
		targets = append(targets, s)
	}
	e.mu.Unlock()

	if len(targets) == 0 {
		return
	}

	payload, err := json.Marshal(state.ParkPayload{Reason: state.ParkReasonQuit})
	if err != nil {
		return // unreachable (a fixed literal struct always marshals)
	}

	for _, s := range targets {
		if a := s.agent(); a != nil {
			_ = a.Interrupt(ctx)
		}
		e.mu.Lock()
		e.removeFromQueue(s.Feature.ID)
		e.mu.Unlock()
		s.setState(StatePaused)
		e.persist(s) // record the paused snapshot before finalizing, like Pause
		s.stop()
		e.freeSlot(s)

		if e.cfg.Store == nil {
			continue
		}
		_ = e.cfg.Store.AppendEvent(ctx, state.CardEvent{
			Feature: s.Feature.ID, Stage: s.Feature.Stage,
			Kind: state.EventPark, At: e.now(), Payload: string(payload),
			Dedupe: quitParkDedupe(s),
		})
	}
}

// quitParkDedupe keys StopForQuit's park event to this session
// generation (the same discriminator mirrorEvents uses for its own
// dedupe keys), so a repeated call appends the marker at most once per
// run rather than once per call.
func quitParkDedupe(s *Session) string {
	return strconv.FormatInt(s.startedAt.UnixNano(), 10) + ":quit-park"
}

// QuitStoppedCard is one card a reopen may offer to pick back up: what
// it was doing when the board stopped it, so the prompt can say
// something concrete instead of just naming the card.
type QuitStoppedCard struct {
	Feature domain.Feature
	// ParkedAt is when StopForQuit wrote the marker — the quit park
	// event's own timestamp, read back off the log rather than kept
	// anywhere else.
	ParkedAt time.Time
	// Corrective is the corrective-round budget already spent
	// (domain.RoundKindCorrective), the same count autopilotBody
	// (autopilot.go) reads against verdict.MaxRounds — the cap itself
	// is a ui-layer concern (verdict imports engine, so engine can't
	// import verdict back); the reopen prompt reads it the same way
	// autopilotBody does.
	Corrective int
}

// QuitStoppedCards is StopForQuit's read side: the autopilot cards the
// board stopped by quitting, restored by Restore into this process's
// live sessions. Call it once, after Restore, to build the reopen
// prompt. A quit-stopped card the store knows about but this process
// never restored (a stale row for a since-deleted or since-advanced
// feature — Restore's own skip rule) is silently omitted, the same way
// Restore itself drops it.
func (e *Engine) QuitStoppedCards(ctx context.Context) ([]QuitStoppedCard, error) {
	if e.cfg.Store == nil {
		return nil, nil
	}
	quit, err := e.cfg.Store.QuitStopped(ctx)
	if err != nil {
		return nil, err
	}
	if len(quit) == 0 {
		return nil, nil
	}

	e.mu.Lock()
	live := make(map[domain.FeatureID]*Session, len(quit))
	for id := range quit {
		if s := e.live[id]; s != nil {
			live[id] = s
		}
	}
	e.mu.Unlock()

	var out []QuitStoppedCard
	for id, s := range live {
		evs, err := e.cfg.Store.Events(ctx, id)
		if err != nil {
			return nil, err
		}
		var parkedAt time.Time
		if n := len(evs); n > 0 {
			parkedAt = evs[n-1].At
		}
		n, err := rounds.Load(ctx, e.cfg.Store, id, domain.RoundKindCorrective)
		if err != nil {
			return nil, err
		}
		out = append(out, QuitStoppedCard{Feature: s.Feature, ParkedAt: parkedAt, Corrective: n})
	}
	return out, nil
}
