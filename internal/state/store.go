package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/workflow"
)

// ErrNotFound is returned when a feature ID has no row.
var ErrNotFound = errors.New("feature not found")

// Store persists features and their transition history in SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS features (
	id              TEXT PRIMARY KEY,
	num             INTEGER NOT NULL UNIQUE,
	title           TEXT NOT NULL,
	one_liner       TEXT NOT NULL DEFAULT '',
	slug            TEXT NOT NULL,
	stage           TEXT NOT NULL,
	skip_brainstorm INTEGER NOT NULL DEFAULT 0,
	skip_plan       INTEGER NOT NULL DEFAULT 0,
	profile         TEXT NOT NULL DEFAULT '',
	budget_envelope INTEGER NOT NULL DEFAULT 0,
	budget_spent    INTEGER NOT NULL DEFAULT 0,
	spend_credits   REAL NOT NULL DEFAULT 0,
	spend_in        INTEGER NOT NULL DEFAULT 0,
	spend_out       INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS transitions (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	feature_id TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	from_stage TEXT NOT NULL,
	to_stage   TEXT NOT NULL,
	actor      TEXT NOT NULL,
	at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS transitions_feature ON transitions(feature_id);

-- One row per feature that has (or had) an agent session; the durable
-- record used to restore the board after a restart.
CREATE TABLE IF NOT EXISTS sessions (
	feature_id    TEXT PRIMARY KEY REFERENCES features(id) ON DELETE CASCADE,
	stage         TEXT NOT NULL,
	role          TEXT NOT NULL,
	state         TEXT NOT NULL,
	spend_credits REAL NOT NULL DEFAULT 0,
	spend_in      INTEGER NOT NULL DEFAULT 0,
	spend_out     INTEGER NOT NULL DEFAULT 0,
	spend_model   TEXT NOT NULL DEFAULT '',
	activity      TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session_messages (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	feature_id TEXT NOT NULL REFERENCES sessions(feature_id) ON DELETE CASCADE,
	ord        INTEGER NOT NULL,
	author     TEXT NOT NULL,
	content    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_messages_feature ON session_messages(feature_id);

-- Line comments on a feature's worktree diff (DESIGN §6.1). Anchored by
-- content hash (not line number) so they survive minor rebases; an
-- annotation whose anchor no longer matches degrades to a file comment.
CREATE TABLE IF NOT EXISTS diff_annotations (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	feature_id TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	file       TEXT NOT NULL,
	anchor     TEXT NOT NULL,
	excerpt    TEXT NOT NULL DEFAULT '',
	comment    TEXT NOT NULL,
	resolved   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS diff_annotations_feature ON diff_annotations(feature_id);
`

// OpenStore opens (creating if needed) the SQLite store at dbPath.
func OpenStore(dbPath string) (*Store, error) {
	// _txlock=immediate: transactions declare write intent at BEGIN, so
	// two connections (TUI + a concurrent CLI invocation) queue on
	// busy_timeout instead of failing with BUSY_SNAPSHOT on upgrade.
	dsn := "file:" + url.PathEscape(dbPath) +
		"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening state db: %w", err)
	}
	// modernc/sqlite serializes writes; a single connection avoids
	// SQLITE_BUSY surprises between pooled connections.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating state db: %w", err)
	}
	// Additive column migrations for DBs created by an earlier version;
	// a duplicate-column error means the column already exists.
	for _, stmt := range migrations {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("migrating state db: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// migrations are idempotent ADD COLUMN statements applied on open.
var migrations = []string{
	`ALTER TABLE features ADD COLUMN spend_credits REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_in INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_out INTEGER NOT NULL DEFAULT 0`,
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// TransitionRecord is one audit-trail entry: who moved a feature
// between which stages, when.
type TransitionRecord struct {
	FeatureID domain.FeatureID
	From, To  domain.Stage
	Actor     string
	At        time.Time
}

const timeFmt = time.RFC3339Nano

// CreateFeature inserts a validated feature.
func (s *Store) CreateFeature(ctx context.Context, f *domain.Feature) error {
	if err := f.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO features (id, num, title, one_liner, slug, stage,
			skip_brainstorm, skip_plan, profile,
			budget_envelope, budget_spent, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(f.ID), f.Num, f.Title, f.OneLiner, f.Slug, string(f.Stage),
		f.Skip.Brainstorm, f.Skip.Plan, f.Profile,
		f.Budget.Envelope, f.Budget.Spent,
		f.CreatedAt.UTC().Format(timeFmt), f.UpdatedAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("creating %s: %w", f.ID, err)
	}
	return nil
}

const featureCols = `id, num, title, one_liner, slug, stage,
	skip_brainstorm, skip_plan, profile,
	budget_envelope, budget_spent, spend_credits, spend_in, spend_out,
	created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanFeature(r rowScanner) (domain.Feature, error) {
	var f domain.Feature
	var id, stage, created, updated string
	err := r.Scan(&id, &f.Num, &f.Title, &f.OneLiner, &f.Slug, &stage,
		&f.Skip.Brainstorm, &f.Skip.Plan, &f.Profile,
		&f.Budget.Envelope, &f.Budget.Spent,
		&f.Spend.Credits, &f.Spend.InputTokens, &f.Spend.OutputTokens,
		&created, &updated)
	if err != nil {
		return f, err
	}
	f.ID = domain.FeatureID(id)
	f.Stage = domain.Stage(stage)
	if f.CreatedAt, err = time.Parse(timeFmt, created); err != nil {
		return f, fmt.Errorf("feature %s: corrupt created_at %q: %w", id, created, err)
	}
	if f.UpdatedAt, err = time.Parse(timeFmt, updated); err != nil {
		return f, fmt.Errorf("feature %s: corrupt updated_at %q: %w", id, updated, err)
	}
	// A corrupt row (hand-edited DB, bad migration) must fail here, not
	// flow onward: IDs and slugs feed branch names and worktree paths.
	if err := f.Validate(); err != nil {
		return f, fmt.Errorf("corrupt feature row: %w", err)
	}
	return f, nil
}

// AddSpend accumulates a usage sample onto a feature's running total.
// It is a metering side-channel (does not touch updated_at or require
// full-feature validation), so it stays cheap and lock-light.
func (s *Store) AddSpend(ctx context.Context, id domain.FeatureID, credits float64, in, out int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE features SET
			spend_credits = spend_credits + ?,
			spend_in = spend_in + ?,
			spend_out = spend_out + ?
		WHERE id = ?`, credits, in, out, string(id))
	if err != nil {
		return fmt.Errorf("metering %s: %w", id, err)
	}
	return nil
}

// GetFeature loads one feature by ID.
func (s *Store) GetFeature(ctx context.Context, id domain.FeatureID) (domain.Feature, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE id = ?`, string(id))
	f, err := scanFeature(row)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return f, err
}

// ListFeatures returns all features ordered by number.
func (s *Store) ListFeatures(ctx context.Context) ([]domain.Feature, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+featureCols+` FROM features ORDER BY num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFeature rewrites a feature's mutable fields (everything except
// ID/Num/CreatedAt). The workflow stage must be changed via Transition,
// which records history; UpdateFeature refuses stage changes.
func (s *Store) UpdateFeature(ctx context.Context, f *domain.Feature) error {
	if err := f.Validate(); err != nil {
		return err
	}
	cur, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		return err
	}
	if cur.Stage != f.Stage {
		return fmt.Errorf("updating %s: stage changes must go through Transition (have %s, got %s)", f.ID, cur.Stage, f.Stage)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE features SET title=?, one_liner=?, slug=?,
			skip_brainstorm=?, skip_plan=?, profile=?,
			budget_envelope=?, budget_spent=?, updated_at=?
		WHERE id=?`,
		f.Title, f.OneLiner, f.Slug,
		f.Skip.Brainstorm, f.Skip.Plan, f.Profile,
		f.Budget.Envelope, f.Budget.Spent,
		now.Format(timeFmt), string(f.ID))
	if err != nil {
		return fmt.Errorf("updating %s: %w", f.ID, err)
	}
	return nil
}

// DeleteFeature removes a feature and (via cascade) its history.
func (s *Store) DeleteFeature(ctx context.Context, id domain.FeatureID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM features WHERE id=?`, string(id))
	if err != nil {
		return fmt.Errorf("deleting %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return nil
}

// Transition moves a feature to a new stage, enforcing the workflow's
// legal-transition table and appending to the audit trail, atomically.
func (s *Store) Transition(ctx context.Context, id domain.FeatureID, to domain.Stage, actor string) (domain.Feature, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Feature{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	row := tx.QueryRowContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE id = ?`, string(id))
	f, err := scanFeature(row)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return f, err
	}
	if err := workflow.CanTransition(f.Stage, to, f.Skip); err != nil {
		return f, fmt.Errorf("%s: %w", id, err)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET stage=?, updated_at=? WHERE id=?`,
		string(to), now.Format(timeFmt), string(id)); err != nil {
		return f, fmt.Errorf("transitioning %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transitions (feature_id, from_stage, to_stage, actor, at) VALUES (?,?,?,?,?)`,
		string(id), string(f.Stage), string(to), actor, now.Format(timeFmt)); err != nil {
		return f, fmt.Errorf("recording transition for %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return f, err
	}
	f.Stage = to
	f.UpdatedAt = now
	return f, nil
}

// mintRetries bounds MintFeatureNum's search for a free number.
const mintRetries = 1000

// MintFeatureNum advances the seq counter until it yields a number no
// stored feature uses, and returns it. This is DESIGN §10 decision 2's
// retry-on-conflict: .gummi/seq is committed to git, so merges and
// reverts can rewind it below numbers already minted; blindly trusting
// it would collide with the num UNIQUE constraint.
func (s *Store) MintFeatureNum(ctx context.Context, seqFile string) (int, error) {
	for range mintRetries {
		n, err := NextSeq(seqFile)
		if err != nil {
			return 0, err
		}
		var used int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM features WHERE num = ?`, n).Scan(&used); err != nil {
			return 0, err
		}
		if used == 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("no free feature number within %d attempts from %s; the seq counter looks badly out of sync with the state db", mintRetries, seqFile)
}

// History returns a feature's transitions, oldest first.
func (s *Store) History(ctx context.Context, id domain.FeatureID) ([]TransitionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT feature_id, from_stage, to_stage, actor, at
		FROM transitions WHERE feature_id = ? ORDER BY seq`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransitionRecord
	for rows.Next() {
		var r TransitionRecord
		var fid, from, to, at string
		if err := rows.Scan(&fid, &from, &to, &r.Actor, &at); err != nil {
			return nil, err
		}
		r.FeatureID = domain.FeatureID(fid)
		r.From, r.To = domain.Stage(from), domain.Stage(to)
		if r.At, err = time.Parse(timeFmt, at); err != nil {
			return nil, fmt.Errorf("corrupt transition timestamp %q: %w", at, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
