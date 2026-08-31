package state

// Per-card "last seen" state: the durable record behind the card
// thread's unread divider (a later, UI-side phase — see the card_events
// note in store.go's schema for the table itself). It answers one
// question: how far through this card's own event log has whoever is
// looking at this machine's board already read?
//
// The value stored is a card_events.seq, not a timestamp, and that
// choice is load-bearing. seq is the total order card_events is itself
// read back in (Store.Events, ORDER BY seq) — a single global
// AUTOINCREMENT counter shared across every card, not a per-card
// sequence — so "everything up to seq N" is an exact, unambiguous cut
// of the log. A timestamp has neither property: two events in the same
// stage can share a wall-clock second, and the writer's clock and the
// reader's clock are never guaranteed to agree, so a timestamp cursor
// can silently swallow or replay events at the boundary. seq can't.
//
// It is also deliberately per-viewer rather than part of a card's
// shared meaning. gummi has no concept of separate users signed into
// the same board, so there is no "who" to attribute a read to beyond
// "whoever is driving this machine's database" — unlike gate_approval
// or a PR link, which describe the card itself, last-seen describes the
// reader. That is why it lives in its own table (card_last_seen) rather
// than as a features column: it would be misleading to scan it back on
// every GetFeature/ListFeatures call as if it were a fact about the
// card, when it is really a fact about this local viewer's history with
// the card.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// SetLastSeen records that a card's event log has been read through seq
// (inclusive) on this machine — the mark the card thread lays down when
// it opens or scrolls to the bottom. Like SetGateApproval it is a
// side-channel write: it neither touches updated_at nor moves the
// stage, and an upsert rather than a read-modify-write, so a concurrent
// mark can't be lost to a stale read. seq must be non-negative; 0 is the
// same value an absent row already reads as, so setting it explicitly
// to 0 is accepted but pointless rather than an error.
func (s *Store) SetLastSeen(ctx context.Context, id domain.FeatureID, seq int64) error {
	if seq < 0 {
		return fmt.Errorf("marking %s seen: refusing a negative seq %d", id, seq)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO card_last_seen (feature_id, seq) VALUES (?, ?)
		ON CONFLICT (feature_id) DO UPDATE SET seq = excluded.seq`,
		string(id), seq); err != nil {
		return fmt.Errorf("marking %s seen through seq %d: %w", id, seq, err)
	}
	return nil
}

// LastSeen reads a card's last-seen seq, and 0 when the card has never
// been marked seen. The two ways of meaning that agree by construction:
// a card with no row reads 0, and 0 is below card_events' lowest real
// seq, since an AUTOINCREMENT primary key starts at 1. So no caller has
// to tell "no row" apart from "seen through nothing yet" — there is
// nothing to tell apart.
func (s *Store) LastSeen(ctx context.Context, id domain.FeatureID) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx,
		`SELECT seq FROM card_last_seen WHERE feature_id = ?`, string(id)).Scan(&seq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading last-seen for %s: %w", id, err)
	}
	return seq, nil
}

// LastSeenSeqs reads every card's last-seen seq in one query, keyed by
// feature ID. It exists for the same reason QuitStopped and
// OpenDecisions read every card in one shot rather than one query per
// card: the board renders its whole card list at once, and looking up
// last-seen per card there would turn one query into one per row on
// screen. A card absent from the map has never been marked seen —
// exactly the same reading LastSeen gives a card with no row — so
// callers can use the zero value on a missing lookup without a second
// branch.
func (s *Store) LastSeenSeqs(ctx context.Context) (map[domain.FeatureID]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT feature_id, seq FROM card_last_seen`)
	if err != nil {
		return nil, fmt.Errorf("reading last-seen seqs: %w", err)
	}
	defer rows.Close()

	out := map[domain.FeatureID]int64{}
	for rows.Next() {
		var fid string
		var seq int64
		if err := rows.Scan(&fid, &seq); err != nil {
			return nil, err
		}
		out[domain.FeatureID(fid)] = seq
	}
	return out, rows.Err()
}
