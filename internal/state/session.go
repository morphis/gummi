package state

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morphia/gummi/internal/domain"
)

// SessionMessage is one persisted transcript turn.
type SessionMessage struct {
	Author  string // "user" | "assistant"
	Content string
}

// SessionSnapshot is the durable record of a feature's agent session,
// written by the engine and reloaded on restart. It uses primitive
// fields so the state layer needn't depend on engine/agent types.
type SessionSnapshot struct {
	Feature      domain.FeatureID
	Stage        domain.Stage
	Role         string
	State        string
	SpendCredits float64
	SpendIn      int64
	SpendOut     int64
	SpendModel   string
	Activity     []string
	Transcript   []SessionMessage
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
		INSERT INTO sessions (feature_id, stage, role, state,
			spend_credits, spend_in, spend_out, spend_model, activity, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(feature_id) DO UPDATE SET
			stage=excluded.stage, role=excluded.role, state=excluded.state,
			spend_credits=excluded.spend_credits, spend_in=excluded.spend_in,
			spend_out=excluded.spend_out, spend_model=excluded.spend_model,
			activity=excluded.activity, updated_at=excluded.updated_at`,
		string(snap.Feature), string(snap.Stage), snap.Role, snap.State,
		snap.SpendCredits, snap.SpendIn, snap.SpendOut, snap.SpendModel,
		strings.Join(snap.Activity, activitySep), time.Now().UTC().Format(timeFmt)); err != nil {
		return fmt.Errorf("saving session %s: %w", snap.Feature, err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_messages WHERE feature_id = ?`, string(snap.Feature)); err != nil {
		return err
	}
	for i, m := range snap.Transcript {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_messages (feature_id, ord, author, content) VALUES (?,?,?,?)`,
			string(snap.Feature), i, m.Author, m.Content); err != nil {
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
		SELECT s.feature_id, s.stage, s.role, s.state,
			s.spend_credits, s.spend_in, s.spend_out, s.spend_model, s.activity
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
		if err := rows.Scan(&fid, &stage, &snap.Role, &snap.State,
			&snap.SpendCredits, &snap.SpendIn, &snap.SpendOut, &snap.SpendModel, &activity); err != nil {
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
		`SELECT author, content FROM session_messages WHERE feature_id = ? ORDER BY ord`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionMessage
	for rows.Next() {
		var m SessionMessage
		if err := rows.Scan(&m.Author, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
