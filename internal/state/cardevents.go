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
	// EventTool is one tool call: its name, the salient argument, and —
	// for a call gummi itself ran, whose outcome is known the moment it
	// is recorded — its status and raw output (subject to
	// PruneStageOutput once the stage is no longer live).
	//
	// A call made by an agent is written the moment it is CALLED, before
	// anything knows how it went, because waiting for an outcome means
	// losing the call entirely on the backends that never report one.
	// Its outcome arrives separately, as EventToolResult.
	EventTool = "tool"
	// EventToolResult is the outcome of an EventTool, correlated to it by
	// the call id both payloads carry. It exists because card_events is
	// append-only: a row written when a call was still in flight cannot
	// later be amended with how it ended, so the ending is its own row.
	//
	// A reader wants FoldToolResults, which merges each result back onto
	// the call it belongs to and hands back a log in which a tool event
	// once again carries its own outcome. An unmerged result — a call
	// whose backend never reported one — is not an error but the truth
	// about that backend, and is reported as such rather than as a
	// success or a silence.
	EventToolResult = "tool_result"
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
	// EventDecisionOpen marks a card blocking on a human: a design gate,
	// an ask_user, a failed verify, a rebase conflict, an exhausted
	// envelope, or an idle card with nothing running. Its answer is the
	// existing gate or ask event carrying the same payload id — the open
	// record and its answer correlate, and neither is ever pruned (§10.18:
	// nothing may block a card without leaving a row).
	EventDecisionOpen = "decision_open"
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

// The other reasons a card stops and waits for someone. ParkReasonGaveUp
// is an automatic loop that reached its cap or could not read a verdict
// — it stopped at a decision only a human can take. ParkReasonNeedsYou
// is every other way a card lands in the needs-attention queue (a failed
// run, an exhausted envelope, a gate raised for review).
//
// Only ParkReasonQuit is load-bearing: QuitStopped looks for exactly it,
// and any other reason reads as a park nobody should silently undo. The
// rest exist so a card's history can answer "why did this stop" rather
// than leaving the run's end unexplained.
const (
	ParkReasonGaveUp   = "gave-up"
	ParkReasonNeedsYou = "needs-you"
)

// ParkPayload is the JSON shape of an EventPark event's Payload. The
// reason lives here rather than in the status column deliberately:
// status is the kind-outcome vocabulary (StatusOK/StatusFail, "not
// applicable" otherwise), and a park's reason is not an outcome — reusing
// status for it would mean two unrelated closed vocabularies sharing one
// column, free to collide as either grows.
type ParkPayload struct {
	Reason string `json:"reason"`
	// Detail is the sentence the user was shown when the card stopped,
	// kept verbatim so the history explains itself without the reader
	// having to reconstruct it from the reason code.
	Detail string `json:"detail,omitempty"`
}

// GatePayload is the JSON shape of an EventGate event's Payload: which
// design gate crossed (the stage left and the stage entered) and who
// crossed it. Actor mirrors the transitions table's own actor vocabulary
// (internal/state.Store.Transition's actor parameter) verbatim — "user"
// for a human crossing it by hand in the TUI, "caller" for a headless
// GateAttended run waiting on its caller, "auto" for the headless driver's
// unattended loop (internal/driver's d.actor). Only "auto" is a gate the
// card crossed on its own; the decision receipt (internal/ui/receipt.go)
// counts exactly that value and no other.
//
// ID correlates the crossing to the EventDecisionOpen it answers (the
// newest still-open gate decision when it crossed); empty on crossings
// raised before decisions were durable — old rows decode to zero values.
// Choice is deliberately zero on gate events: the crossing's own
// From→To edge IS the choice for a design gate, and no caller-side
// option vocabulary is invented for it here.
type GatePayload struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Actor string `json:"actor"`
	ID    string `json:"id,omitempty"`
	// By is the answerer, mirroring Actor. It exists so the answer record's
	// actor has one name across both payload kinds; the crossing has always
	// carried it explicitly (as Actor), so this stays zero rather than
	// duplicating it — a mapping, not a second source of truth.
	By string `json:"by,omitempty"`
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
//
// ID, Choice and By are additive (old rows decode to zero values): ID
// correlates the answer to its decision_open row, Choice names the chosen
// option when the answer was one of the offered options (empty for a
// free-form answer), and By is the answerer stated explicitly by the
// caller — the same value as Actor. Actor is inferred from the card's
// stored gate-approval mode and exists for history written before By
// did; every new write sets both, and By is what the receipt trusts.
type AskPayload struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Actor    string `json:"actor"`
	// By is the actor the answerer declared ("user" or "autopilot"),
	// set at the call site rather than inferred from the card's stored
	// gate-approval mode — a headless --autonomous run on a card stored
	// at "gates" takes its own answers, and the record must say so.
	By     string `json:"by,omitempty"`
	ID     string `json:"id,omitempty"`     // correlates to decision_open
	Choice string `json:"choice,omitempty"` // option id, not free text
}

// AutopilotTookOver and AutopilotHandedBack are the two values
// AutopilotPayload.Event can carry: the autonomous loop starting to
// drive a card unattended, and it stopping. See AutopilotPayload for why
// both get their own explicit row.
const (
	AutopilotTookOver   = "took-over"
	AutopilotHandedBack = "handed-back"
)

// AutopilotPayload is the JSON shape of an EventAutopilot event's
// Payload.
//
// card_events needs no migration to carry this: kind and payload are
// already generic TEXT columns (see the CREATE TABLE in store.go) shared
// by every event kind, so a new payload shape for an already-declared
// kind is purely additive at the Go layer.
//
// Why an explicit row rather than deriving the boundary from the first
// machine-actored gate/ask event: every derivation mis-attributes the
// case where a human drove a stage by hand and THEN pressed the
// autopilot switch at its gate — walking backwards to the enclosing
// stage would swallow that human-driven stage into the machine's
// stretch. For a record whose entire job is saying honestly what the
// machine did without you, a heuristic that quietly over-claims is the
// worst available failure.
//
// Why both directions are written and not just the takeover: the close
// can also be inferred from a park row or the next human-actored event,
// but gestures like turning the switch off write nothing at all, so
// those two get an explicit handed-back row. (Everything else here
// deliberately closes late rather than not at all.)
//
// Mode records which stop (domain.GateAttended or domain.GateAutopilot) was in
// force when it took over, because the same card can be handed over
// twice under different modes and this row is the only place that
// distinction survives.
//
// Event is additive, in the same sense as GatePayload.ID and
// AskPayload.ID/Choice/By: appendAutopilotEvent (below) predates the
// took-over/handed-back vocabulary and, on every SetGateApproval mode
// change, writes a row with only Mode set and Event left as "". That row
// records a different fact — the card's stored gate-approval mode
// changed to X — not a takeover or a handback, so a reader must check
// Event and not treat every EventAutopilot row as a boundary crossing.
type AutopilotPayload struct {
	Event  string `json:"event"`
	Reason string `json:"reason,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

// ToolPayload is the JSON shape of an EventTool and EventToolResult
// event's Payload.
//
// Label is the rendered activity line ("Bash  make test") and is what
// every display has read since tool events existed; it stays because a
// reader that only wants to print the line should not have to reassemble
// one. Tool, Detail and Call are the same fact taken apart, so that
// "which tools did this card use, how often, and how often did they
// fail" stops being a question you answer by splitting a human-readable
// string on a double space.
//
// Every field is additive: rows written before them decode to zero
// values, and a reader that finds Tool empty falls back to Label.
type ToolPayload struct {
	Label string `json:"label"`
	// Tool is the backend's own name for the tool ("Bash", "Read",
	// "Skill"). Empty on gummi's own activity notes, which are lines
	// rather than calls.
	Tool string `json:"tool,omitempty"`
	// Detail is the call's salient argument (the command, the path, the
	// skill). It is the one field the retention sweep removes, since it
	// is the only one that can be large and the only one that stops
	// mattering once a call has gone by without failing.
	Detail string `json:"detail,omitempty"`
	// Call is the backend's tool-call id, correlating an EventToolResult
	// to the EventTool it settles. Empty on a call gummi ran itself,
	// whose outcome was known when it was recorded and needs no second row.
	Call string `json:"call,omitempty"`
	// MS is how long the call took, in milliseconds, set on the result
	// row. Zero means not measured, which is every row written before
	// durations were, and every call whose backend reports no outcome.
	MS int64 `json:"ms,omitempty"`
}

// FoldToolResults merges every EventToolResult back onto the EventTool it
// settles and returns the log without the result rows — the shape every
// reader wants, in which a tool event carries its own status, output and
// duration again.
//
// The split exists only because the log is append-only and an outcome
// arrives after the call (see EventToolResult); nothing above the store
// should have to know that. A result whose call cannot be found is
// dropped rather than kept as a headless row: it describes a call this
// card's log does not contain, so there is nothing for a reader to say
// about it.
//
// The input is never modified; events with no tool rows at all come back
// as the same slice.
func FoldToolResults(evs []CardEvent) []CardEvent {
	results := 0
	for _, ev := range evs {
		if ev.Kind == EventToolResult {
			results++
		}
	}
	if results == 0 {
		return evs
	}
	// where each in-flight call's row landed in the output, by call id
	at := map[string]int{}
	out := make([]CardEvent, 0, len(evs)-results)
	for _, ev := range evs {
		if ev.Kind == EventTool {
			var p ToolPayload
			if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Call != "" {
				at[p.Call] = len(out)
			}
			out = append(out, ev)
			continue
		}
		if ev.Kind != EventToolResult {
			out = append(out, ev)
			continue
		}
		var p ToolPayload
		if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.Call == "" {
			continue
		}
		i, ok := at[p.Call]
		if !ok {
			continue
		}
		// the call keeps its identity (name, detail, position in the log)
		// and gains how it ended; only the duration comes off the result's
		// own payload, since the call could not have known it.
		call := out[i]
		var cp ToolPayload
		_ = json.Unmarshal([]byte(call.Payload), &cp)
		cp.MS = p.MS
		if merged, err := json.Marshal(cp); err == nil {
			call.Payload = string(merged)
		}
		call.Status = ev.Status
		call.Output = ev.Output
		out[i] = call
		delete(at, p.Call)
	}
	return out
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
//
// This predates AppendAutopilot's took-over/handed-back vocabulary and
// leaves Event unset on purpose — see the note on AutopilotPayload.Event.
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

// PruneStageOutput applies the log's retention rule to a stage that is
// no longer live: every event is kept forever, but the two fields that
// can grow without bound are kept only where they still earn their place.
//
//   - Raw output is blanked on everything that did not fail. Output is
//     the forensic field, and a call that passed has nothing to be
//     forensic about.
//   - A tool call's detail — the command, the path, the skill — is
//     dropped for the same reason and under the same test, which for an
//     agent's call means the outcome its EventToolResult reported. The
//     tool's name, its outcome and its duration are never pruned, so no
//     count, failure rate or timing decays; only the arguments thin out.
//
// This is the bargain the log already struck with raw output, applied
// one field further: what a long-lived card accumulates is bounded by
// how much of it went wrong rather than by how much of it happened.
func (s *Store) PruneStageOutput(ctx context.Context, id domain.FeatureID, stage domain.Stage) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE card_events SET output = ''
		WHERE feature_id = ? AND stage = ? AND kind IN (?, ?) AND status <> ?`,
		string(id), string(stage), EventTool, EventToolResult, StatusFail); err != nil {
		return fmt.Errorf("pruning stage output for %s/%s: %w", id, stage, err)
	}
	return s.pruneStageDetail(ctx, id, stage)
}

// pruneStageDetail removes the detail field from the stage's tool calls
// that did not fail. A call's outcome may live on its own result row
// (EventToolResult), so the failed set is collected first by call id and
// the calls are then rewritten in one transaction.
//
// The rewrite is done in Go rather than with SQLite's json_remove so the
// store depends on nothing beyond the SQL it already uses; the sweep runs
// once per stage, over one stage's rows, so the cost is paid where it is
// least visible.
func (s *Store) pruneStageDetail(ctx context.Context, id domain.FeatureID, stage domain.Stage) error {
	failed := map[string]bool{}
	fails, err := s.db.QueryContext(ctx, `
		SELECT payload FROM card_events
		WHERE feature_id = ? AND stage = ? AND kind IN (?, ?) AND status = ?`,
		string(id), string(stage), EventTool, EventToolResult, StatusFail)
	if err != nil {
		return fmt.Errorf("pruning stage detail for %s/%s: %w", id, stage, err)
	}
	for fails.Next() {
		var payload string
		if err := fails.Scan(&payload); err != nil {
			fails.Close()
			return err
		}
		var p ToolPayload
		if json.Unmarshal([]byte(payload), &p) == nil && p.Call != "" {
			failed[p.Call] = true
		}
	}
	if err := fails.Err(); err != nil {
		fails.Close()
		return err
	}
	fails.Close()

	type rewrite struct {
		seq     int64
		payload string
	}
	var todo []rewrite
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, status, payload FROM card_events
		WHERE feature_id = ? AND stage = ? AND kind = ?`,
		string(id), string(stage), EventTool)
	if err != nil {
		return fmt.Errorf("pruning stage detail for %s/%s: %w", id, stage, err)
	}
	for rows.Next() {
		var seq int64
		var status, payload string
		if err := rows.Scan(&seq, &status, &payload); err != nil {
			rows.Close()
			return err
		}
		var p ToolPayload
		if json.Unmarshal([]byte(payload), &p) != nil || p.Detail == "" {
			continue
		}
		if status == StatusFail || failed[p.Call] {
			continue
		}
		p.Detail = ""
		next, err := json.Marshal(p)
		if err != nil {
			continue
		}
		todo = append(todo, rewrite{seq, string(next)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(todo) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, r := range todo {
		if _, err := tx.ExecContext(ctx,
			`UPDATE card_events SET payload = ? WHERE seq = ?`, r.payload, r.seq); err != nil {
			return fmt.Errorf("pruning stage detail for %s/%s: %w", id, stage, err)
		}
	}
	return tx.Commit()
}

// appendGateEventTx records a stage crossing inside the caller's own
// transaction, so the event and the transition it describes commit
// together or not at all. Unlike the best-effort mirror writes, a failure
// here fails the crossing: a history that silently skipped a gate would
// under-report exactly the crossings nobody watched.
//
// answerID, when non-empty, correlates the crossing to the open
// EventDecisionOpen it answers — the newest still-open gate decision,
// looked up by the caller in this same transaction so the crossing and
// its answer cannot diverge. The dedupe key rides that id (scoped per
// generation when the decision was minted), so a retried transaction
// cannot leave two events behind, and two crossings of the same edge can
// no longer collide on a shared timestamp; crossings with no open
// decision to answer keep the crossing's own timestamp as their key.
func appendGateEventTx(ctx context.Context, tx *sql.Tx, id domain.FeatureID, from, to domain.Stage, actor string, at time.Time, answerID string) error {
	payload, err := json.Marshal(GatePayload{From: string(from), To: string(to), Actor: actor, ID: answerID})
	if err != nil {
		return fmt.Errorf("encoding gate event for %s: %w", id, err)
	}
	dedupe := "gate:" + string(from) + "->" + string(to) + ":" + at.Format(timeFmt)
	if answerID != "" {
		dedupe = "decision:" + answerID
	}
	if _, err := tx.ExecContext(ctx, appendEventSQL,
		string(id), string(from), EventGate, "", at.Format(timeFmt),
		string(payload), "", dedupe); err != nil {
		return fmt.Errorf("recording gate event for %s: %w", id, err)
	}
	return nil
}

// AppendPark records a card coming to a stop and waiting for someone,
// with why. Best-effort by contract: parking is something the caller has
// already decided and usually already told the user about, so a log
// failure must never unwind it — callers discard the error.
//
// dedupe may be empty, and usually is: two escalations on one card are
// two real events, and collapsing them would hide a loop that gave up
// twice. Pass a key only where the same stop can be recorded more than
// once (a repeated quit sweep, say).
func (s *Store) AppendPark(ctx context.Context, id domain.FeatureID, stage domain.Stage, reason, detail, dedupe string, at time.Time) error {
	payload, err := json.Marshal(ParkPayload{Reason: reason, Detail: detail})
	if err != nil {
		return fmt.Errorf("encoding park event for %s: %w", id, err)
	}
	return s.AppendEvent(ctx, CardEvent{
		Feature: id, Stage: stage, Kind: EventPark, At: at,
		Payload: string(payload), Dedupe: dedupe,
	})
}

// AppendAutopilot records the autonomous loop taking a card over (event
// = AutopilotTookOver) or handing it back (event = AutopilotHandedBack),
// with why and under which gate-approval mode. Best-effort by contract,
// like AppendPark: the takeover or handback is something the caller has
// already acted on, so a log failure must never unwind it — callers
// discard the error.
//
// dedupe may be empty; pass a key only where the same boundary can
// otherwise be recorded twice (e.g. a retried save re-observing the same
// takeover).
func (s *Store) AppendAutopilot(ctx context.Context, id domain.FeatureID, stage domain.Stage, event, reason, mode, dedupe string, at time.Time) error {
	payload, err := json.Marshal(AutopilotPayload{Event: event, Reason: reason, Mode: mode})
	if err != nil {
		return fmt.Errorf("encoding autopilot event for %s: %w", id, err)
	}
	return s.AppendEvent(ctx, CardEvent{
		Feature: id, Stage: stage, Kind: EventAutopilot, At: at,
		Payload: string(payload), Dedupe: dedupe,
	})
}
