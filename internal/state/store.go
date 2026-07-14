package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
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
	spend_est       REAL NOT NULL DEFAULT 0,
	spend_in        INTEGER NOT NULL DEFAULT 0,
	spend_out       INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	kind            TEXT NOT NULL DEFAULT 'feature',
	external_ref    TEXT NOT NULL DEFAULT '',
	skip_triage     INTEGER NOT NULL DEFAULT 0,
	skip_diagnose   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS features_external_ref ON features(external_ref);
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
	agent_session TEXT NOT NULL DEFAULT '',
	spend_credits REAL NOT NULL DEFAULT 0,
	spend_in      INTEGER NOT NULL DEFAULT 0,
	spend_out     INTEGER NOT NULL DEFAULT 0,
	spend_model   TEXT NOT NULL DEFAULT '',
	activity      TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session_messages (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	feature_id  TEXT NOT NULL REFERENCES sessions(feature_id) ON DELETE CASCADE,
	ord         INTEGER NOT NULL,
	author      TEXT NOT NULL,
	content     TEXT NOT NULL,
	tool_status TEXT NOT NULL DEFAULT '',
	tool_output TEXT NOT NULL DEFAULT ''
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

-- Realized spend rolled up per (feature, stage, model, role): the
-- breakdown behind features.spend_* (which stays the fast feature-level
-- total). Forward-only — transitions never carried cost, so rows begin at
-- deploy. credits use the same credit-equivalent as the feature total, so
-- the breakdown sums back to features.spend_credits.
--
-- This is also the durable record of role→model routing: the sessions
-- table is cleared as each stage completes, so a finished feature's
-- routing (which model served which role at which stage) survives only
-- here. The helper role captures a backend's internal side-model calls
-- so they don't mis-attribute to the stage's working role.
CREATE TABLE IF NOT EXISTS stage_spend (
	feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	stage      TEXT    NOT NULL,
	model      TEXT    NOT NULL,
	role       TEXT    NOT NULL,
	credits     REAL    NOT NULL DEFAULT 0,
	est_credits REAL    NOT NULL DEFAULT 0,
	input_tok   INTEGER NOT NULL DEFAULT 0,
	cached_tok  INTEGER NOT NULL DEFAULT 0,
	output_tok  INTEGER NOT NULL DEFAULT 0,
	updated_at  TEXT    NOT NULL,
	PRIMARY KEY (feature_id, stage, model, role)
);

-- Baseline outcome of the artifact's gummi-checks, captured once on
-- the fresh worktree at approval, before any feature changes. Verify
-- diffs live results against it so pre-existing failures read as
-- FAIL (pre-existing) and only regressions count against the feature.
CREATE TABLE IF NOT EXISTS check_baseline (
	feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	name       TEXT    NOT NULL,
	cmd        TEXT    NOT NULL,
	ok         INTEGER NOT NULL,
	exit_code  INTEGER NOT NULL DEFAULT 0,
	output     TEXT    NOT NULL DEFAULT '',
	ran_at     TEXT    NOT NULL,
	PRIMARY KEY (feature_id, name)
);
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
	// After the column migrations: the rebuild copies every current
	// column, so est_credits must already exist on an old DB.
	if err := rebuildStageSpendPK(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating state db: %w", err)
	}
	return &Store{db: db}, nil
}

// rebuildStageSpendPK migrates stage_spend rows keyed (feature_id,
// stage, model) to the four-column key that includes role. One session
// can emit usage for several models under one role while another role
// works the same stage and model, and with role outside the key the
// upsert's role overwrite clobbered the earlier attribution. SQLite
// cannot alter a primary key in place, so this is the standard
// transactional table rebuild; the old key is a strict subset of the
// new one, so every existing row is admitted unchanged. Idempotent: a
// table whose key already includes role is left alone.
func rebuildStageSpendPK(db *sql.DB) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info('stage_spend') WHERE pk > 0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	roleKeyed := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == "role" {
			roleKeyed = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if roleKeyed {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, stmt := range []string{
		`CREATE TABLE stage_spend_new (
			feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
			stage      TEXT    NOT NULL,
			model      TEXT    NOT NULL,
			role       TEXT    NOT NULL,
			credits     REAL    NOT NULL DEFAULT 0,
			est_credits REAL    NOT NULL DEFAULT 0,
			input_tok   INTEGER NOT NULL DEFAULT 0,
			cached_tok  INTEGER NOT NULL DEFAULT 0,
			output_tok  INTEGER NOT NULL DEFAULT 0,
			updated_at  TEXT    NOT NULL,
			PRIMARY KEY (feature_id, stage, model, role)
		)`,
		`INSERT INTO stage_spend_new
			(feature_id, stage, model, role, credits, est_credits,
			 input_tok, cached_tok, output_tok, updated_at)
		 SELECT feature_id, stage, model, role, credits, est_credits,
			 input_tok, cached_tok, output_tok, updated_at
		 FROM stage_spend`,
		`DROP TABLE stage_spend`,
		`ALTER TABLE stage_spend_new RENAME TO stage_spend`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrations are idempotent ADD COLUMN statements applied on open.
var migrations = []string{
	`ALTER TABLE features ADD COLUMN spend_credits REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_in INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_out INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN kind TEXT NOT NULL DEFAULT 'feature'`,
	`ALTER TABLE features ADD COLUMN external_ref TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN skip_triage INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN skip_diagnose INTEGER NOT NULL DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS features_external_ref ON features(external_ref)`,
	`ALTER TABLE sessions ADD COLUMN agent_session TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_messages ADD COLUMN tool_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_messages ADD COLUMN tool_output TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN spend_est REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE stage_spend ADD COLUMN est_credits REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE sessions ADD COLUMN error TEXT NOT NULL DEFAULT ''`,
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
	// Persist a concrete kind so the column never holds '' — the empty
	// default reads as a feature everywhere, but a stored value keeps
	// queries and the audit trail unambiguous.
	kind := f.Kind
	if kind == "" {
		kind = domain.KindFeature
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO features (id, num, title, one_liner, slug, stage,
			skip_brainstorm, skip_plan, profile,
			budget_envelope, budget_spent, created_at, updated_at,
			kind, external_ref, skip_triage, skip_diagnose)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(f.ID), f.Num, f.Title, f.OneLiner, f.Slug, string(f.Stage),
		f.Skip.Brainstorm, f.Skip.Plan, f.Profile,
		f.Budget.Envelope, f.Budget.Spent,
		f.CreatedAt.UTC().Format(timeFmt), f.UpdatedAt.UTC().Format(timeFmt),
		string(kind), f.ExternalRef, f.Skip.Triage, f.Skip.Diagnose)
	if err != nil {
		return fmt.Errorf("creating %s: %w", f.ID, err)
	}
	return nil
}

const featureCols = `id, num, title, one_liner, slug, stage,
	skip_brainstorm, skip_plan, profile,
	budget_envelope, budget_spent, spend_credits, spend_est, spend_in, spend_out,
	created_at, updated_at,
	kind, external_ref, skip_triage, skip_diagnose`

type rowScanner interface{ Scan(dest ...any) error }

func scanFeature(r rowScanner) (domain.Feature, error) {
	var f domain.Feature
	var id, stage, created, updated, kind string
	err := r.Scan(&id, &f.Num, &f.Title, &f.OneLiner, &f.Slug, &stage,
		&f.Skip.Brainstorm, &f.Skip.Plan, &f.Profile,
		&f.Budget.Envelope, &f.Budget.Spent,
		&f.Spend.Credits, &f.Spend.EstimatedCredits, &f.Spend.InputTokens, &f.Spend.OutputTokens,
		&created, &updated,
		&kind, &f.ExternalRef, &f.Skip.Triage, &f.Skip.Diagnose)
	if err != nil {
		return f, err
	}
	f.ID = domain.FeatureID(id)
	f.Kind = domain.Kind(kind)
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
// estimated is the token-derived portion of credits (the whole sample when
// the provider reported no cost, zero when it did), kept as its own
// accumulator so displays can label estimates. It is a metering
// side-channel (does not touch updated_at or require full-feature
// validation), so it stays cheap and lock-light.
func (s *Store) AddSpend(ctx context.Context, id domain.FeatureID, credits, estimated float64, in, out int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE features SET
			spend_credits = spend_credits + ?,
			spend_est = spend_est + ?,
			spend_in = spend_in + ?,
			spend_out = spend_out + ?
		WHERE id = ?`, credits, estimated, in, out, string(id))
	if err != nil {
		return fmt.Errorf("metering %s: %w", id, err)
	}
	return nil
}

// StageSpend is one (stage, model) rollup row from stage_spend: the
// realized cost a stage incurred on a given model, in credits and tokens.
type StageSpend struct {
	Stage            domain.Stage
	Model            string
	Role             string
	Credits          float64
	EstimatedCredits float64 // token-derived subset of Credits
	InputTokens      int64
	CachedTokens     int64
	OutputTokens     int64
	UpdatedAt        time.Time
}

// RecordStageSpend accumulates one usage sample onto the (feature, stage,
// model, role) rollup behind features.spend_* — the per-stage breakdown.
// credits is the same credit-equivalent AddSpend receives, so the
// breakdown sums back to the feature total. Like AddSpend it is a cheap
// metering side-channel (an UPSERT, no validation). An empty model is
// stored as "unknown" so the row is never keyed on ” and the breakdown
// still accounts for the spend. Role is part of the key: one model can
// serve two roles on the same stage (e.g. a critique pass reusing the
// plan stage), and each keeps its own attribution.
func (s *Store) RecordStageSpend(ctx context.Context, id domain.FeatureID, stage domain.Stage, role, model string, credits, estimated float64, in, cached, out int64) error {
	if model == "" {
		model = "unknown"
	}
	now := time.Now().UTC().Format(timeFmt)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO stage_spend
			(feature_id, stage, model, role, credits, est_credits, input_tok, cached_tok, output_tok, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(feature_id, stage, model, role) DO UPDATE SET
			credits     = credits     + excluded.credits,
			est_credits = est_credits + excluded.est_credits,
			input_tok   = input_tok   + excluded.input_tok,
			cached_tok  = cached_tok  + excluded.cached_tok,
			output_tok  = output_tok  + excluded.output_tok,
			updated_at  = excluded.updated_at`,
		string(id), string(stage), model, role, credits, estimated, in, cached, out, now)
	if err != nil {
		return fmt.Errorf("metering stage %s/%s for %s: %w", stage, model, id, err)
	}
	return nil
}

// StageBreakdown returns a feature's per-stage/model spend rollup, ordered
// by workflow stage position then descending credits (so each stage's
// dominant model leads). It is the read behind the dashboard breakdown.
func (s *Store) StageBreakdown(ctx context.Context, id domain.FeatureID) ([]StageSpend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stage, model, role, credits, est_credits, input_tok, cached_tok, output_tok, updated_at
		FROM stage_spend WHERE feature_id = ?`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StageSpend
	for rows.Next() {
		var r StageSpend
		var stage, updated string
		if err := rows.Scan(&stage, &r.Model, &r.Role, &r.Credits, &r.EstimatedCredits,
			&r.InputTokens, &r.CachedTokens, &r.OutputTokens, &updated); err != nil {
			return nil, err
		}
		r.Stage = domain.Stage(stage)
		if r.UpdatedAt, err = time.Parse(timeFmt, updated); err != nil {
			return nil, fmt.Errorf("corrupt stage_spend timestamp %q: %w", updated, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := stageOrder(out[i].Stage), stageOrder(out[j].Stage)
		if si != sj {
			return si < sj
		}
		return out[i].Credits > out[j].Credits
	})
	return out, nil
}

// stageOrder is a stage's position in domain.Stages (workflow order),
// or a large sentinel for an unknown stage so it sorts last.
func stageOrder(st domain.Stage) int {
	for i, s := range domain.Stages {
		if s == st {
			return i
		}
	}
	return len(domain.Stages)
}

// CheckResult is one gummi-check outcome in a feature's baseline: how
// the command fared on the fresh worktree before any feature changes.
type CheckResult struct {
	Name     string
	Cmd      string
	OK       bool
	ExitCode int
	Output   string
	RanAt    time.Time
}

// SetCheckBaseline replaces the feature's whole check baseline in one
// transaction (delete + insert), so a re-baseline never leaves stale
// rows behind renamed or removed checks.
func (s *Store) SetCheckBaseline(ctx context.Context, id domain.FeatureID, results []CheckResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("baselining checks for %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM check_baseline WHERE feature_id = ?`, string(id)); err != nil {
		return fmt.Errorf("baselining checks for %s: %w", id, err)
	}
	for _, r := range results {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO check_baseline (feature_id, name, cmd, ok, exit_code, output, ran_at)
			VALUES (?,?,?,?,?,?,?)`,
			string(id), r.Name, r.Cmd, r.OK, r.ExitCode, r.Output,
			r.RanAt.UTC().Format(timeFmt)); err != nil {
			return fmt.Errorf("baselining check %s for %s: %w", r.Name, id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baselining checks for %s: %w", id, err)
	}
	return nil
}

// CheckBaseline returns the feature's check baseline, empty when none
// was ever taken (older features, guarded mode) — callers degrade to
// treating every failure as live.
func (s *Store) CheckBaseline(ctx context.Context, id domain.FeatureID) ([]CheckResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, cmd, ok, exit_code, output, ran_at
		FROM check_baseline WHERE feature_id = ? ORDER BY name`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		var r CheckResult
		var ranAt string
		if err := rows.Scan(&r.Name, &r.Cmd, &r.OK, &r.ExitCode, &r.Output, &ranAt); err != nil {
			return nil, err
		}
		if r.RanAt, err = time.Parse(timeFmt, ranAt); err != nil {
			return nil, fmt.Errorf("corrupt check_baseline timestamp %q: %w", ranAt, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

// FeatureByExternalRef returns the feature carrying the given external
// reference (e.g. a GitHub issue URL), or ErrNotFound. Bug ingestion uses
// it to skip sources already imported, so re-ingesting a repo never mints
// duplicate bugs. An empty ref never matches (manual items share "").
func (s *Store) FeatureByExternalRef(ctx context.Context, ref string) (domain.Feature, error) {
	if ref == "" {
		return domain.Feature{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE external_ref = ? ORDER BY num LIMIT 1`, ref)
	f, err := scanFeature(row)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("external ref %q: %w", ref, ErrNotFound)
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
			skip_brainstorm=?, skip_plan=?, skip_triage=?, skip_diagnose=?, profile=?,
			budget_envelope=?, budget_spent=?, updated_at=?
		WHERE id=?`,
		f.Title, f.OneLiner, f.Slug,
		f.Skip.Brainstorm, f.Skip.Plan, f.Skip.Triage, f.Skip.Diagnose, f.Profile,
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
	if err := workflow.CanTransition(f.Kind, f.Stage, to, f.Skip); err != nil {
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
