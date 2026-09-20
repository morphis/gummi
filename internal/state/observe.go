package state

// The store's observer: the seam board hooks ride. Every caller reaches
// a crossing, a park, a decision or a creation through this store, so
// reporting those here — once, post-commit, with the same payloads the
// log holds — is what lets the TUI, the headless driver and the CLI
// verbs fire hooks without each growing its own call sites (the same
// single-seam argument as the gate event itself).
//
// Two kinds below are observer-only synthetics: they exist so the
// reporter can say "this happened" without inventing log rows for it.
// They are never written to card_events.

import (
	"encoding/json"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// EventCreated is the observer-only kind reporting a card's creation.
// It is never written to card_events — the features row is the record —
// but a hook hearing "a card was minted" should not have to poll for it.
const EventCreated = "created"

// EventVerified is the observer-only kind reporting that a card's
// verify→done crossing landed a verified branch (the stamp Advance left
// before the crossing). Merge and hand-off both pass through it; the
// branch lives on the feature the hook enriches from.
const EventVerified = "verified"

// Observer receives committed events worth surfacing to hooks, in the
// shape of the log row that carries them (payloads are the log's own
// kind-specific JSON; EventCreated/EventVerified carry none). It is
// called synchronously on the committing goroutine and MUST be
// non-blocking and infallible — the hooks.Dispatcher's Observe is the
// reference implementation (queue-or-drop). The observer sees only
// events that were actually committed: a deduped no-op insert reports
// nothing, the same honesty the log itself keeps.
type Observer func(ev CardEvent)

// hookableKind is the set of event kinds the observer is called for.
// Everything session-shaped (messages, tool calls, stage_enter runs,
// ask answers) is deliberately absent: hooks are for state changes, not
// narration, and the log still records those for its own readers.
func hookableKind(kind string) bool {
	switch kind {
	case EventGate, EventPark, EventDecisionOpen, EventCreated, EventVerified:
		return true
	}
	return false
}

// SetObserver installs the observer. Call it once, after OpenStore and
// before the store is handed to any concurrent writer (the engine, the
// TUI), so the assignment never races an event being raised. A nil
// observer (the default) makes the reporting a no-op.
func (s *Store) SetObserver(o Observer) { s.observer = o }

// observe reports one committed event to the installed observer. The
// hookable filter lives here so the reporter — not each call site — owns
// the "what is worth hearing" question.
func (s *Store) observe(ev CardEvent) {
	if s.observer == nil || !hookableKind(ev.Kind) {
		return
	}
	s.observer(ev)
}

// observeTransition reports one committed crossing: the gate event (the
// same payload the log row holds) and, when the crossing landed a card
// with a verified stamp already on it, the synthetic verified event.
// called post-commit, so a failure here could only lose a notification.
func (s *Store) observeTransition(id domain.FeatureID, from, to domain.Stage, actor string, at time.Time, answerID string, verified bool) {
	payload, err := json.Marshal(GatePayload{From: string(from), To: string(to), Actor: actor, ID: answerID})
	if err != nil {
		return
	}
	s.observe(CardEvent{Feature: id, Stage: from, Kind: EventGate, At: at, Payload: string(payload)})
	if verified {
		s.observe(CardEvent{Feature: id, Stage: to, Kind: EventVerified, At: at})
	}
}
