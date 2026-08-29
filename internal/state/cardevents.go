package state

// The card event log: the append-only, per-card history behind
// card_events. Every message, tool call, stage boundary, gate crossing,
// ask, autopilot decision, and park is recorded here as one event. A
// card's whole history is read from this log rather than from the
// ephemeral sessions/session_messages rows, which hold only the live
// stage. Retention: every event is kept forever; raw tool
// output is kept only for the live stage and for anything that failed
// (see PruneStageOutput).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// Event kinds: the closed vocabulary stored in card_events.kind.
const (
	// EventMessage is a chat turn (user, assistant, or system) in a stage
	// session.
	EventMessage = "message"
	// EventTool is one tool call and its outcome, including raw output
	// (subject to PruneStageOutput once the stage is no longer live).
	EventTool = "tool"
	// EventStageEnter marks a card entering a stage.
	EventStageEnter = "stage_enter"
	// EventStageExit marks a card leaving a stage.
	EventStageExit = "stage_exit"
	// EventGate marks a design-gate crossing (human or auto approval).
	EventGate = "gate"
	// EventAsk marks an ask_user round trip: the agent's question and,
	// once answered, the human's reply.
	EventAsk = "ask"
	// EventAutopilot marks an autonomous-loop decision (e.g. what to run
	// next) made without a human in the loop.
	EventAutopilot = "autopilot"
	// EventPark marks a card being parked (taken out of the autonomous
	// loop pending human attention).
	EventPark = "park"
)

// Event outcomes: the closed vocabulary stored in card_events.status.
// The empty string means "not applicable" (most kinds carry no outcome).
const (
	StatusOK   = "ok"
	StatusFail = "fail"
)

// ParkReasonQuit is the EventPark payload reason meaning a card was
// stopped because the board process quit, not because a human parked it
// with p. It is the one closed value QuitStopped looks for; any other
// (or absent) reason reads as a human park.
const ParkReasonQuit = "quit"

// ParkPayload is the JSON shape of an EventPark event's Payload. The
// reason lives here rather than in the status column deliberately:
// status is the kind-outcome vocabulary (StatusOK/StatusFail, "not
// applicable" otherwise), and a park's reason is not an outcome — reusing
// status for it would mean two unrelated closed vocabularies sharing one
// column, free to collide as either grows.
type ParkPayload struct {
	Reason string `json:"reason"`
}

// GatePayload is the JSON shape of an EventGate event's Payload: which
// design gate crossed (the stage left and the stage entered) and who
// crossed it. Actor mirrors the transitions table's own actor vocabulary
// (internal/state.Store.Transition's actor parameter) verbatim — "user"
// for a human crossing it by hand in the TUI, "caller" for a headless
// GateOff run waiting on its caller, "auto" for the headless driver's
// unattended loop (internal/driver's d.actor). Only "auto" is a gate the
// card crossed on its own; the decision receipt (internal/ui/receipt.go)
// counts exactly that value and no other.
type GatePayload struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Actor string `json:"actor"`
}

// ActorAutopilot and ActorUser are the two actors an EventAsk's Payload
// (AskPayload.Actor) can name: an ask_user answer taken automatically,
// unattended, versus one a human actually typed. Distinguishing the two
// is the whole reason AskPayload carries an actor at all — the decision
// receipt's "took N answers" only means something once an automatic
// answer is told apart from a typed one.
const (
	ActorAutopilot = "autopilot"
	ActorUser      = "user"
)

// AskPayload is the JSON shape of an EventAsk event's Payload: the
// question an agent asked, the answer it got, and who answered —
// ActorAutopilot or ActorUser (see those constants).
type AskPayload struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Actor    string `json:"actor"`
}

// AutopilotPayload is the JSON shape of an EventAutopilot event's
// Payload: the gate-approval mode a card was set to.
type AutopilotPayload struct {
	Mode string `json:"mode"`
}

// CardEvent is one row of a card's event log.
type CardEvent struct {
	Seq     int64
	Feature domain.FeatureID
	Stage   domain.Stage
	Kind    string
	Status  string // "", StatusOK, or StatusFail
	At      time.Time
	Payload string // JSON, kind-specific
	Output  string // raw tool output; prunable (see PruneStageOutput)
	// Dedupe is a caller-chosen idempotency key. A non-empty value makes
	// the append a no-op if an event with the same (feature, dedupe) was
	// already recorded; "" means always append.
	Dedupe string
}

// AppendEvent inserts one card event, honoring ev.Dedupe.
func (s *Store) AppendEvent(ctx context.Context, ev CardEvent) error {
	if _, err := s.db.ExecContext(ctx, appendEventSQL,
		string(ev.Feature), string(ev.Stage), ev.Kind, ev.Status,
		ev.At.UTC().Format(timeFmt), ev.Payload, ev.Output, ev.Dedupe); err != nil {
		return fmt.Errorf("appending event for %s: %w", ev.Feature, err)
	}
	return nil
}

// AppendEvents inserts a batch of card events in a single transaction —
// the engine mirror's per-save call, so a save never leaves a partial
// batch visible to a concurrent reader. Each event honors its own
// Dedupe, same as AppendEvent.
func (s *Store) AppendEvents(ctx context.Context, evs []CardEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	for _, ev := range evs {
		if _, err := tx.ExecContext(ctx, appendEventSQL,
			string(ev.Feature), string(ev.Stage), ev.Kind, ev.Status,
			ev.At.UTC().Format(timeFmt), ev.Payload, ev.Output, ev.Dedupe); err != nil {
			return fmt.Errorf("appending event for %s: %w", ev.Feature, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("appending events: %w", err)
	}
	return nil
}

// appendEventSQL inserts one card_events row, deduplicating on
// (feature_id, dedupe) when dedupe is non-empty.
//
// The WHERE clause repeated inside the ON CONFLICT target below (guarding
// against a non-empty dedupe) is required, not decoration: SQLite only
// matches an ON CONFLICT target against a partial unique index when the
// partial index's predicate is repeated verbatim in the conflict target.
// Without it, SQLite reports "ON CONFLICT clause does not match any
// PRIMARY KEY or UNIQUE constraint" at runtime, because card_events_dedupe
// (see the schema block) is itself a partial index over a non-empty
// dedupe, not a plain unique index over the whole table.
const appendEventSQL = `
	INSERT INTO card_events (feature_id, stage, kind, status, at, payload, output, dedupe)
	VALUES (?,?,?,?,?,?,?,?)
	ON CONFLICT(feature_id, dedupe) WHERE dedupe <> '' DO NOTHING`

// Events returns all events recorded for a card, oldest first.
func (s *Store) Events(ctx context.Context, id domain.FeatureID) ([]CardEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, feature_id, stage, kind, status, at, payload, output, dedupe
		FROM card_events WHERE feature_id = ? ORDER BY seq`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CardEvent
	for rows.Next() {
		var ev CardEvent
		var fid, stage, at string
		if err := rows.Scan(&ev.Seq, &fid, &stage, &ev.Kind, &ev.Status, &at,
			&ev.Payload, &ev.Output, &ev.Dedupe); err != nil {
			return nil, err
		}
		ev.Feature = domain.FeatureID(fid)
		ev.Stage = domain.Stage(stage)
		if ev.At, err = time.Parse(timeFmt, at); err != nil {
			return nil, fmt.Errorf("corrupt card_events timestamp %q: %w", at, err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// QuitStopped reports the cards whose most recent event in the log is a
// park with reason ParkReasonQuit — stopped because the board process
// exited, not because a human parked them with p. seq is a single global
// order over every card's events (card_events.seq, an autoincrement PK),
// so "most recent" is exactly "the row with this feature's greatest
// seq". The result holds true only for a card in that shape: a plain
// park (any other reason, e.g. a human's) is false, a card that never
// parked is false, and a quit-park followed by anything at all — a later
// stage_enter from being resumed, or a later park with a different
// reason — is false, because that later event is what MAX(seq) now
// finds instead. A card absent from the returned map is exactly the same
// as one mapped to false; the map only ever holds true entries.
func (s *Store) QuitStopped(ctx context.Context) (map[domain.FeatureID]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT feature_id, kind, payload FROM card_events
		WHERE seq IN (SELECT MAX(seq) FROM card_events GROUP BY feature_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[domain.FeatureID]bool{}
	for rows.Next() {
		var fid, kind, payload string
		if err := rows.Scan(&fid, &kind, &payload); err != nil {
			return nil, err
		}
		if kind != EventPark {
			continue
		}
		var p ParkPayload
		// A malformed payload (should never happen — AppendEvent's callers
		// always marshal ParkPayload) reads as "not a quit park" rather
		// than erroring the whole query: unmarshal failure and reason=""
		// mean the same thing here.
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			continue
		}
		if p.Reason == ParkReasonQuit {
			out[domain.FeatureID(fid)] = true
		}
	}
	return out, rows.Err()
}

// appendAutopilotEvent records a gate-approval mode change in the card's
// own event log. SetGateApproval is the single write path every caller
// (TUI and driver) already funnels through, so calling this there means
// no future caller can change a card's mode without it appearing in the
// card's own history. Best-effort: a log failure never unwinds the
// already-committed mode change. Deduped at second granularity on the
// (card, mode) pair, loose enough that a caller retrying the identical
// SetGateApproval call within the same second can't double-write, while
// a later, deliberate change back to the same mode still gets its own
// event.
func (s *Store) appendAutopilotEvent(ctx context.Context, id domain.FeatureID, mode string) {
	payload, err := json.Marshal(AutopilotPayload{Mode: mode})
	if err != nil {
		return
	}
	now := time.Now().UTC()
	_ = s.AppendEvent(ctx, CardEvent{
		Feature: id, Kind: EventAutopilot, At: now, Payload: string(payload),
		Dedupe: string(id) + ":autopilot:" + mode + ":" + now.Format("2006-01-02T15:04:05"),
	})
}

// PruneStageOutput blanks the raw output of a stage's successful tool
// events, once that stage is no longer live. Retention rule: every event
// is kept forever; raw output is kept only for the live stage and for
// anything that failed, so a long-lived card's log doesn't accumulate
// unbounded tool output for stages it has already moved past.
func (s *Store) PruneStageOutput(ctx context.Context, id domain.FeatureID, stage domain.Stage) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE card_events SET output = ''
		WHERE feature_id = ? AND stage = ? AND kind = ? AND status <> ?`,
		string(id), string(stage), EventTool, StatusFail); err != nil {
		return fmt.Errorf("pruning stage output for %s/%s: %w", id, stage, err)
	}
	return nil
}

// appendGateEventTx records a stage crossing inside the caller's own
// transaction, so the event and the transition it describes commit
// together or not at all. Unlike the best-effort mirror writes, a failure
// here fails the crossing: a history that silently skipped a gate would
// under-report exactly the crossings nobody watched.
//
// The dedupe key is the crossing's own timestamp, which the transition
// row shares, so a retried transaction cannot leave two events behind.
func appendGateEventTx(ctx context.Context, tx *sql.Tx, id domain.FeatureID, from, to domain.Stage, actor string, at time.Time) error {
	payload, err := json.Marshal(GatePayload{From: string(from), To: string(to), Actor: actor})
	if err != nil {
		return fmt.Errorf("encoding gate event for %s: %w", id, err)
	}
	dedupe := "gate:" + string(from) + "->" + string(to) + ":" + at.Format(timeFmt)
	if _, err := tx.ExecContext(ctx, appendEventSQL,
		string(id), string(from), EventGate, "", at.Format(timeFmt),
		string(payload), "", dedupe); err != nil {
		return fmt.Errorf("recording gate event for %s: %w", id, err)
	}
	return nil
}
