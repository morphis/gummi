package state

// The open decision: the durable half of every human checkpoint. A
// decision_open row records that a card is blocked on a person and what
// it is waiting for; the gate and ask events already in the log become
// its answers, correlated by the decision's id. Options are never
// stored — a workflow decision's answers are regenerated at render time
// from the card's own state, and an agent question's options die with
// the process — so the record carries the question, its kind and its
// stage, and nothing else free to drift.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// DecisionPayload is the JSON shape of an EventDecisionOpen event's
// Payload: the card is blocked on a person, here is what it is waiting
// for. Options are deliberately absent — they are regenerated at render
// time (workflow decisions from the card's own guidance, agent questions
// from the live ask) and freezing them would create a second source of
// truth free to drift from the card's actual state.
type DecisionPayload struct {
	// ID is the decision's identity: an ask's tool-call id (or its minted
	// stand-in on the convention path), or a minted, generation-scoped gate
	// id. It is the dedupe key of this row and the correlating id the
	// answering gate/ask event carries.
	ID string `json:"id"`
	// Kind is the closed decision vocabulary: gate, ask, verify, conflict,
	// budget, idle. Autopilot may answer every kind but budget (§10.17).
	Kind string `json:"kind"`
	// Question is what the card is waiting on, in the words it was shown.
	// Options are never stored, only regenerated.
	Question string `json:"question"`
	// FreeForm and Multi mirror an ask's own flags, so a restored ask can
	// be re-armed honestly (prose-only once the options are gone).
	FreeForm bool `json:"freeform,omitempty"`
	Multi    bool `json:"multi,omitempty"`
	// Anchor is an ask's spec anchor, so a re-armed answer lands where the
	// agent asked for it to land.
	Anchor string `json:"anchor,omitempty"`
}

// OpenDecision is one still-open decision as OpenDecisions reports it.
type OpenDecision struct {
	ID       string
	Kind     string // the DecisionPayload kind: gate|ask|verify|conflict|budget|idle
	Question string
	Stage    domain.Stage // the stage the card was waiting in
	At       time.Time
}

// The decision kinds, as the record's own closed vocabulary. These are
// the string forms the render-time projection classifies (internal/ui's
// decisionKind) and gatepolicy's outcomes map to.
const (
	DecisionKindGate     = "gate"
	DecisionKindAsk      = "ask"
	DecisionKindVerify   = "verify"
	DecisionKindConflict = "conflict"
	DecisionKindBudget   = "budget"
	DecisionKindIdle     = "idle"
)

// OpenDecision records a card blocking on a human. The decision id is the
// dedupe key, so a re-raise of the same decision id (a retried save, a
// driver retry) is a no-op, and the id is scoped per session generation by
// every caller that mints one — a natural key would no-op the second gate
// after a bounce, the inverse trap. Best-effort at the call sites that
// mirror best-effort events; the answering paths treat it as theirs.
func (s *Store) OpenDecision(ctx context.Context, id domain.FeatureID, stage domain.Stage, p DecisionPayload, at time.Time) error {
	if p.ID == "" {
		return fmt.Errorf("opening decision for %s: empty decision id", id)
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding decision_open for %s: %w", id, err)
	}
	return s.AppendEvent(ctx, CardEvent{
		Feature: id, Stage: stage, Kind: EventDecisionOpen, At: at,
		Payload: string(payload), Dedupe: p.ID,
	})
}

// newestOpenGateDecisionTx returns the id of the newest decision_open of
// kind "gate" that no later answer has closed, read inside the caller's
// own transaction, or "" when none is waiting. This is the crossing's
// correlation: the transition row and the gate event that answers the
// decision commit together, so a crossed gate and its answered decision
// cannot diverge. A malformed payload reads as nothing rather than
// failing the crossing — the decision row stays open and a later
// crossing correlates to it instead.
func (s *Store) newestOpenGateDecisionTx(ctx context.Context, tx *sql.Tx, id domain.FeatureID) string {
	rows, err := tx.QueryContext(ctx, `
		SELECT seq, payload FROM card_events
		WHERE feature_id = ? AND kind = ? ORDER BY seq DESC`,
		string(id), EventDecisionOpen)
	if err != nil {
		return "" // best-effort: the crossing still records its own event
	}
	defer rows.Close()

	var answerID string
	answered := map[string]bool{}
	for rows.Next() {
		var seq int64
		var payload string
		if err := rows.Scan(&seq, &payload); err != nil {
			return ""
		}
		var p DecisionPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			continue
		}
		if p.Kind != DecisionKindGate {
			continue
		}
		if answerID == "" && !answered[p.ID] {
			answerID = p.ID
		}
	}
	if err := rows.Err(); err != nil {
		return ""
	}
	return answerID
}

// correlatingID reads an answer event's payload for the decision id it
// closes. Both answer kinds (gate, ask) carry it as the same field; a
// malformed or pre-decision payload reads as uncorrelated.
func correlatingID(kind, payload string) string {
	if kind != EventGate && kind != EventAsk {
		return ""
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return ""
	}
	return p.ID
}

// OpenDecisions reports each card's still-open decisions, oldest first
// per card. A decision is open while its answer has not happened — no
// later gate or ask event carries its id — and while the card still sits
// at the stage it was raised in: a stage that has moved on abandons the
// decision, per the reopen path in DESIGN §6.3 (a restored ask is
// re-armed into its stage's session and answered there; one whose stage
// exited can never be answered and reads as abandoned, not as waiting).
// Cards with no open decision are absent from the map.
func (s *Store) OpenDecisions(ctx context.Context) (map[domain.FeatureID][]OpenDecision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, feature_id, stage, at, payload
		FROM card_events WHERE kind = ? ORDER BY seq`, EventDecisionOpen)
	if err != nil {
		return nil, fmt.Errorf("reading open decisions: %w", err)
	}
	defer rows.Close()

	type pending struct {
		dec   OpenDecision
		seq   int64
		stage domain.Stage
	}
	pendingByFeature := map[domain.FeatureID][]pending{}
	for rows.Next() {
		var fid, stage, at, payload string
		var seq int64
		if err := rows.Scan(&seq, &fid, &stage, &at, &payload); err != nil {
			return nil, err
		}
		var p DecisionPayload
		// A malformed payload (should never happen — OpenDecision always
		// marshals DecisionPayload) reads as nothing rather than failing
		// the whole query, the same contract QuitStopped's park scan keeps.
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			continue
		}
		atT, err := time.Parse(timeFmt, at)
		if err != nil {
			return nil, fmt.Errorf("corrupt card_events timestamp %q: %w", at, err)
		}
		pendingByFeature[domain.FeatureID(fid)] = append(
			pendingByFeature[domain.FeatureID(fid)], pending{
				dec:   OpenDecision{ID: p.ID, Kind: p.Kind, Question: p.Question, Stage: domain.Stage(stage), At: atT},
				seq:   seq,
				stage: domain.Stage(stage),
			})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The answer half: each card's later gate/ask events close its
	// decisions, and the features row is the truth about whether the
	// decision's stage still exists to be answered in.
	out := make(map[domain.FeatureID][]OpenDecision, len(pendingByFeature))
	for fid, pendings := range pendingByFeature {
		var curStage string
		if err := s.db.QueryRowContext(ctx,
			`SELECT stage FROM features WHERE id = ?`, string(fid)).Scan(&curStage); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // the card is gone; nothing of it can be waiting
			}
			return nil, err
		}

		answered, err := s.answeredIDs(ctx, fid, pendings[0].seq)
		if err != nil {
			return nil, err
		}
		cur := domain.Stage(curStage)
		for _, d := range pendings {
			if answered[d.dec.ID] {
				continue // answered: it lives in the history now
			}
			if d.stage != cur {
				continue // the stage moved on: the decision is abandoned
			}
			out[fid] = append(out[fid], d.dec)
		}
	}
	return out, nil
}

// answeredIDs returns the decision ids a card's later gate/ask events
// close, read from its own log after the earliest decision row. A
// malformed or pre-decision payload reads as uncorrelated.
func (s *Store) answeredIDs(ctx context.Context, id domain.FeatureID, afterSeq int64) (map[string]bool, error) {
	ansRows, err := s.db.QueryContext(ctx, `
		SELECT kind, payload FROM card_events
		WHERE feature_id = ? AND seq > ? AND (kind = ? OR kind = ?)`,
		string(id), afterSeq, EventGate, EventAsk)
	if err != nil {
		return nil, err
	}
	defer ansRows.Close()

	answered := map[string]bool{}
	for ansRows.Next() {
		var kind, payload string
		if err := ansRows.Scan(&kind, &payload); err != nil {
			return nil, err
		}
		if id := correlatingID(kind, payload); id != "" {
			answered[id] = true
		}
	}
	if err := ansRows.Err(); err != nil {
		return nil, err
	}
	return answered, nil
}
