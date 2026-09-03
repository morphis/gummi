package state

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// SessionMessage is one persisted transcript turn.
type SessionMessage struct {
	Author  string // "user" | "assistant" | "system" | "tool"
	Content string
	// Tool-authored turns only: the call's outcome ("", "ok", "fail")
	// and its captured output, so the evidence survives a restart.
	ToolStatus string
	ToolOutput string
}

// SessionSnapshot is the durable record of a feature's agent session,
// written by the engine and reloaded on restart. It uses primitive
// fields so the state layer needn't depend on engine/agent types.
type SessionSnapshot struct {
	Feature domain.FeatureID
	Stage   domain.Stage
	Role    string
	// Flavor is the session's pass: "stage" (the stage's own work),
	// "critique" (the plan-critique pass), or "rebase" (the rebase-resolve
	// pass). Persisted so Restore can recover a session's identity without
	// re-deriving it from role/stage. Empty for legacy rows predating the
	// column.
	Flavor       string
	State        string
	AgentSession string // backend session id (its on-disk log), "" if none
	SpendCredits float64
	SpendIn      int64
	SpendOut     int64
	SpendModel   string
	Activity     []string
	// Error is the last run error's text, persisted so a failed run can be
	// reconstructed into the needs-attention queue after a restart. Empty
	// for clean sessions.
	Error string
	// Verdict is the structured review/critique verdict submitted via
	// submit_verdict, persisted so a resumed process judges a finished
	// session the way the live one did instead of re-deriving Unclear.
	Verdict string
	// VerdictFloor is gummi's own deterministic ceiling on that verdict,
	// stamped when a live gummi-check fails or an environment gate blocks
	// (engine.setVerdictFloor), and VerdictFloorReason is the sentence
	// saying which. Persisted for the same reason Verdict is: without
	// them a restarted process knows a verify was blocked — the durable
	// escalation flag says so — but not what to fix, which is the half a
	// reader actually acts on. Empty for legacy rows and for any session
	// gummi never overruled.
	VerdictFloor       string
	VerdictFloorReason string
	// StartedAt is when this session generation began (RFC3339Nano),
	// stamped once when the session is first created and carried
	// unchanged across saves within the same generation. It doubles as
	// an idempotency discriminator for mirrored event-log writes, and
	// anchors the session boundary when a card's history is rendered.
	// Empty for legacy rows saved before this column existed.
	StartedAt  string
	Transcript []SessionMessage
}

// activitySep joins activity lines for storage; tool-call strings never
// contain a newline (they are short labels), so this round-trips.
const activitySep = "\n"

// SaveSession upserts a session snapshot and replaces its transcript,
// atomically. The feature must exist (FK).
func (s *Store) SaveSession(ctx context.Context, snap SessionSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (feature_id, stage, role, flavor, state, agent_session,
			spend_credits, spend_in, spend_out, spend_model, activity, error, verdict,
			verdict_floor, verdict_floor_reason, updated_at, started_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(feature_id) DO UPDATE SET
			stage=excluded.stage, role=excluded.role, flavor=excluded.flavor,
			state=excluded.state, agent_session=excluded.agent_session,
			spend_credits=excluded.spend_credits, spend_in=excluded.spend_in,
			spend_out=excluded.spend_out, spend_model=excluded.spend_model,
			activity=excluded.activity, error=excluded.error, verdict=excluded.verdict,
			verdict_floor=excluded.verdict_floor, verdict_floor_reason=excluded.verdict_floor_reason,
			updated_at=excluded.updated_at, started_at=excluded.started_at`,
		string(snap.Feature), string(snap.Stage), snap.Role, snap.Flavor, snap.State, snap.AgentSession,
		snap.SpendCredits, snap.SpendIn, snap.SpendOut, snap.SpendModel,
		strings.Join(snap.Activity, activitySep), snap.Error, snap.Verdict,
		snap.VerdictFloor, snap.VerdictFloorReason, time.Now().UTC().Format(timeFmt), snap.StartedAt); err != nil {
		return fmt.Errorf("saving session %s: %w", snap.Feature, err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_messages WHERE feature_id = ?`, string(snap.Feature)); err != nil {
		return err
	}
	for i, m := range snap.Transcript {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_messages (feature_id, ord, author, content, tool_status, tool_output)
			VALUES (?,?,?,?,?,?)`,
			string(snap.Feature), i, m.Author, m.Content, m.ToolStatus, m.ToolOutput); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteSession removes a feature's persisted session (and messages).
func (s *Store) DeleteSession(ctx context.Context, id domain.FeatureID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE feature_id = ?`, string(id))
	return err
}

// LoadSessions returns every persisted session with its transcript,
// ordered by feature number.
func (s *Store) LoadSessions(ctx context.Context) ([]SessionSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.feature_id, s.stage, s.role, s.flavor, s.state, s.agent_session,
			s.spend_credits, s.spend_in, s.spend_out, s.spend_model, s.activity, s.error, s.verdict,
			s.verdict_floor, s.verdict_floor_reason, s.started_at
		FROM sessions s JOIN features f ON f.id = s.feature_id
		ORDER BY f.num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionSnapshot
	for rows.Next() {
		var snap SessionSnapshot
		var fid, stage, activity string
		if err := rows.Scan(&fid, &stage, &snap.Role, &snap.Flavor, &snap.State, &snap.AgentSession,
			&snap.SpendCredits, &snap.SpendIn, &snap.SpendOut, &snap.SpendModel, &activity, &snap.Error, &snap.Verdict,
			&snap.VerdictFloor, &snap.VerdictFloorReason, &snap.StartedAt); err != nil {
			return nil, err
		}
		snap.Feature = domain.FeatureID(fid)
		snap.Stage = domain.Stage(stage)
		if activity != "" {
			snap.Activity = strings.Split(activity, activitySep)
		}
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		msgs, err := s.loadMessages(ctx, out[i].Feature)
		if err != nil {
			return nil, err
		}
		out[i].Transcript = msgs
	}
	return out, nil
}

func (s *Store) loadMessages(ctx context.Context, id domain.FeatureID) ([]SessionMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT author, content, tool_status, tool_output
		FROM session_messages WHERE feature_id = ? ORDER BY ord`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionMessage
	for rows.Next() {
		var m SessionMessage
		if err := rows.Scan(&m.Author, &m.Content, &m.ToolStatus, &m.ToolOutput); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
