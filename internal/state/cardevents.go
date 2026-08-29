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
