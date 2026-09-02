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

// ErrDependedOn is returned by DeleteFeature when the card still has
// dependents. The store pre-checks before deleting so the raw SQLite FK
// constraint error never surfaces and no edge is silently dropped.
var ErrDependedOn = errors.New("feature has dependents")

// ErrForkPointStamped is returned by SetForkPoint when the feature row
// already records a fork-point SHA. It is the "stamped once" refusal that
// keeps Create and the lazy backfill from racing a stored SHA into being
// overwritten; callers distinguish it from a genuine store error with
// errors.Is.
var ErrForkPointStamped = errors.New("fork point already stamped")

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
	spend_decompose_credits REAL NOT NULL DEFAULT 0,
	spend_decompose_in      INTEGER NOT NULL DEFAULT 0,
	spend_decompose_out     INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	kind            TEXT NOT NULL DEFAULT 'feature',
	external_ref    TEXT NOT NULL DEFAULT '',
	skip_triage     INTEGER NOT NULL DEFAULT 0,
	skip_diagnose   INTEGER NOT NULL DEFAULT 0,
	quick           INTEGER NOT NULL DEFAULT 0,
	verified_at     TEXT NOT NULL DEFAULT '',
	gate_approval   TEXT NOT NULL DEFAULT '',
	severity        TEXT NOT NULL DEFAULT '',
	fork_point      TEXT NOT NULL DEFAULT '',
	landed_sha      TEXT NOT NULL DEFAULT '',
	commit_draft_fail TEXT NOT NULL DEFAULT '',
	repo            TEXT NOT NULL DEFAULT '',
	pr_repo         TEXT NOT NULL DEFAULT '',
	pr_number       INTEGER NOT NULL DEFAULT 0,
	pr_url          TEXT NOT NULL DEFAULT '',
	pr_head_sha     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS features_external_ref ON features(external_ref);

-- One row per (feature, round kind) live loop counter: the collapsed
-- seam behind internal/rounds, replacing the former plan_rounds and
-- review_rounds columns above. A missing row reads as 0 (no cycle
-- started); IncrementRounds is a single atomic UPDATE, never a
-- read-modify-write.
CREATE TABLE IF NOT EXISTS rounds (
	feature_id TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	round_kind TEXT NOT NULL,
	count      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (feature_id, round_kind)
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
	flavor        TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL,
	agent_session TEXT NOT NULL DEFAULT '',
	spend_credits REAL NOT NULL DEFAULT 0,
	spend_in      INTEGER NOT NULL DEFAULT 0,
	spend_out     INTEGER NOT NULL DEFAULT 0,
	spend_model   TEXT NOT NULL DEFAULT '',
	activity      TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	verdict       TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL,
	started_at    TEXT NOT NULL DEFAULT ''
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
	created_at TEXT NOT NULL,
	source_ref TEXT NOT NULL DEFAULT ''
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

-- One directed edge per dependency: feature_id depends on depends_on_id.
-- feature_id cascades on delete (a deleted card's outgoing edges go with
-- it); depends_on_id does not, so SQLite FK enforcement refuses deleting
-- a card others still depend on. The store pre-checks dependents before
-- delete (ErrDependedOn) so the raw constraint error never surfaces.
CREATE TABLE IF NOT EXISTS feature_deps (
	feature_id    TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	depends_on_id TEXT NOT NULL REFERENCES features(id),
	PRIMARY KEY (feature_id, depends_on_id)
);
CREATE INDEX IF NOT EXISTS feature_deps_depends_on ON feature_deps(depends_on_id);

-- The event log: one row per notable thing that happened on a card's
-- stage session — a message, a tool call, a stage boundary, a gate
-- crossing. It is the durable record a card's whole history is read
-- from, unlike the ephemeral sessions/session_messages rows, which hold
-- only the live stage and are rewritten wholesale on every save.
-- Append-only; seq is the total order. dedupe
-- carries a caller-chosen idempotency key so a mirrored write survives a
-- retried save without doubling up (see the partial unique index below);
-- '' means "always append" for events with no natural key. output holds
-- raw tool output and is the one column the retention sweep prunes
-- (PruneStageOutput) once a stage is no longer live and its tool calls
-- didn't fail — payload and every other column are kept forever.
CREATE TABLE IF NOT EXISTS card_events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
	stage      TEXT    NOT NULL DEFAULT '',
	kind       TEXT    NOT NULL,
	status     TEXT    NOT NULL DEFAULT '',
	at         TEXT    NOT NULL,
	payload    TEXT    NOT NULL DEFAULT '',
	output     TEXT    NOT NULL DEFAULT '',
	dedupe     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS card_events_feature ON card_events(feature_id, seq);
CREATE INDEX IF NOT EXISTS card_events_kind ON card_events(kind, feature_id);
CREATE UNIQUE INDEX IF NOT EXISTS card_events_dedupe
	ON card_events(feature_id, dedupe) WHERE dedupe <> '';

-- The high-water mark of a card's event log that this machine's viewer
-- has already read: the card_events.seq up through which everything has
-- been seen. One row per card that has ever been marked seen; a card
-- with no row (every card, before the first mark) reads as 0, which is
-- also below card_events' lowest real seq (an AUTOINCREMENT PK starts
-- at 1) — so "no row" and "seen through seq 0" agree on the same
-- meaning: never seen.
--
-- This is deliberately per-viewer, not part of a card's shared meaning:
-- gummi has no notion of separate users, so "seen" means "seen on this
-- machine's database," nothing more, and it is stored well away from
-- the features table for that reason.
CREATE TABLE IF NOT EXISTS card_last_seen (
	feature_id TEXT    PRIMARY KEY REFERENCES features(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL DEFAULT 0
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
	if err := rebuildRoundsKeyed(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating state db: %w", err)
	}
	return &Store{db: db}, nil
}

// rebuildRoundsKeyed migrates the two live per-feature round columns
// (plan_rounds, review_rounds) into the keyed rounds table, then drops
// them from features — the two columns the collapsed internal/rounds
// seam replaces. Idempotent gate: it runs only when features still
// carries a plan_rounds column (SQLite cannot drop a column that was
// never added, and a fresh DB's base schema never adds one), so a
// fresh DB and a re-opened already-migrated DB are both no-ops. When it
// does run, one transaction creates the rounds table (if not already
// present), copies each feature's nonzero plan_rounds/review_rounds
// into a row, then rebuilds features without the two columns — the
// same copy+rename shape as rebuildStageSpendPK.
func rebuildRoundsKeyed(db *sql.DB) error {
	ctx := context.Background()
	// A single-row aggregate scan (rather than Query+Next+Close) needs no
	// explicit Close: db has a single pooled connection (SetMaxOpenConns(1)),
	// and QueryRowContext releases it as soon as Scan returns, before the
	// PRAGMA/transaction calls further down this same function reuse it.
	var hasColumn bool
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) > 0 FROM pragma_table_info('features') WHERE name = 'plan_rounds'`,
	).Scan(&hasColumn); err != nil {
		return err
	}
	if !hasColumn {
		return nil
	}

	// features is the parent of several FK-referencing tables (transitions,
	// sessions, diff_annotations, stage_spend, check_baseline, feature_deps,
	// and now rounds); recreating it must run with FK enforcement off, per
	// SQLite's documented recipe for schema changes ALTER TABLE cannot
	// express. PRAGMA foreign_keys cannot be toggled inside a transaction,
	// so it brackets the transaction rather than living inside it.
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer db.ExecContext(ctx, `PRAGMA foreign_keys=ON`) //nolint:errcheck // best-effort restore

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS rounds (
			feature_id TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
			round_kind TEXT NOT NULL,
			count      INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (feature_id, round_kind)
		)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rounds (feature_id, round_kind, count)
		SELECT id, 'plan', plan_rounds FROM features WHERE plan_rounds > 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rounds (feature_id, round_kind, count)
		SELECT id, 'review', review_rounds FROM features WHERE review_rounds > 0`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE TABLE features_new (
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
			spend_decompose_credits REAL NOT NULL DEFAULT 0,
			spend_decompose_in      INTEGER NOT NULL DEFAULT 0,
			spend_decompose_out     INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			kind            TEXT NOT NULL DEFAULT 'feature',
			external_ref    TEXT NOT NULL DEFAULT '',
			skip_triage     INTEGER NOT NULL DEFAULT 0,
			skip_diagnose   INTEGER NOT NULL DEFAULT 0,
			quick           INTEGER NOT NULL DEFAULT 0,
			verified_at     TEXT NOT NULL DEFAULT '',
			gate_approval   TEXT NOT NULL DEFAULT '',
			severity        TEXT NOT NULL DEFAULT '',
			fork_point      TEXT NOT NULL DEFAULT '',
			landed_sha      TEXT NOT NULL DEFAULT '',
			commit_draft_fail TEXT NOT NULL DEFAULT '',
			repo            TEXT NOT NULL DEFAULT '',
			pr_repo         TEXT NOT NULL DEFAULT '',
			pr_number       INTEGER NOT NULL DEFAULT 0,
			pr_url          TEXT NOT NULL DEFAULT '',
			pr_head_sha     TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO features_new
			(id, num, title, one_liner, slug, stage,
			 skip_brainstorm, skip_plan, profile,
			 budget_envelope, budget_spent, spend_credits, spend_est, spend_in, spend_out,
			 spend_decompose_credits, spend_decompose_in, spend_decompose_out,
			 created_at, updated_at,
			 kind, external_ref, skip_triage, skip_diagnose, quick, verified_at, gate_approval,
			 severity, fork_point, landed_sha, commit_draft_fail, repo, pr_repo, pr_number, pr_url, pr_head_sha)
		 SELECT id, num, title, one_liner, slug, stage,
			 skip_brainstorm, skip_plan, profile,
			 budget_envelope, budget_spent, spend_credits, spend_est, spend_in, spend_out,
			 spend_decompose_credits, spend_decompose_in, spend_decompose_out,
			 created_at, updated_at,
			 kind, external_ref, skip_triage, skip_diagnose, quick, verified_at, gate_approval,
			 severity, fork_point, landed_sha, commit_draft_fail, repo, pr_repo, pr_number, pr_url, pr_head_sha
		 FROM features`,
		`DROP TABLE features`,
		`ALTER TABLE features_new RENAME TO features`,
		`CREATE INDEX IF NOT EXISTS features_external_ref ON features(external_ref)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	`ALTER TABLE features ADD COLUMN quick INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN verified_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN gate_approval TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN severity TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN fork_point TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN flavor TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN verdict TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN commit_draft_fail TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN repo TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN spend_decompose_credits REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_decompose_in INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN spend_decompose_out INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN pr_repo TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE features ADD COLUMN pr_url TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE features ADD COLUMN pr_head_sha TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE diff_annotations ADD COLUMN source_ref TEXT NOT NULL DEFAULT ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS diff_annotations_source_ref ON diff_annotations(feature_id, source_ref) WHERE source_ref != ''`,
	`ALTER TABLE sessions ADD COLUMN started_at TEXT NOT NULL DEFAULT ''`,
	// gate_approval vocabulary rename: rewrite the old stored spellings to
	// their new canonical form (domain.GateGates/domain.GateOff). Both
	// UPDATEs are idempotent by construction (a row not matching the WHERE
	// clause is left untouched, so re-running on an already-migrated or
	// fresh DB is a no-op) and, unlike the ADD COLUMN statements above,
	// never error — they just affect zero rows when there is nothing to do.
	`UPDATE features SET gate_approval = 'gates' WHERE gate_approval = 'auto'`,
	`UPDATE features SET gate_approval = 'off'   WHERE gate_approval = 'caller'`,
	`ALTER TABLE features ADD COLUMN landed_sha TEXT NOT NULL DEFAULT ''`,
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
			budget_envelope, created_at, updated_at,
			kind, external_ref, skip_triage, skip_diagnose, quick, gate_approval, severity, fork_point, landed_sha, commit_draft_fail, repo,
			pr_repo, pr_number, pr_url, pr_head_sha)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(f.ID), f.Num, f.Title, f.OneLiner, f.Slug, string(f.Stage),
		f.Skip.Brainstorm, f.Skip.Plan, f.Profile,
		f.Budget.Envelope,
		f.CreatedAt.UTC().Format(timeFmt), f.UpdatedAt.UTC().Format(timeFmt),
		string(kind), f.ExternalRef, f.Skip.Triage, f.Skip.Diagnose, f.Skip.Quick, f.GateApproval, string(f.Severity), f.ForkPoint, f.LandedSHA, f.CommitDraftFail, f.Repo,
		f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, f.PullRequest.HeadSHA)
	if err != nil {
		return fmt.Errorf("creating %s: %w", f.ID, err)
	}
	return nil
}

const featureCols = `id, num, title, one_liner, slug, stage,
	skip_brainstorm, skip_plan, profile,
	budget_envelope, spend_credits, spend_est, spend_in, spend_out,
	spend_decompose_credits, spend_decompose_in, spend_decompose_out,
	created_at, updated_at,
	kind, external_ref, skip_triage, skip_diagnose, quick, verified_at, gate_approval, severity, fork_point, landed_sha, commit_draft_fail, repo,
	pr_repo, pr_number, pr_url, pr_head_sha`

// writtenFeatureColumns returns the set of feature columns the store
// reads back (the SELECT list of featureCols), keyed by name. It is the
// anti-drift guard for the schema: a fresh database's CREATE TABLE must
// cover every column here, or a column the code writes would be missing
// from the base schema.
func writtenFeatureColumns() map[string]bool {
	cols := map[string]bool{}
	for _, c := range strings.Split(strings.ReplaceAll(featureCols, "\n", " "), ",") {
		name := strings.TrimSpace(c)
		if name != "" {
			cols[name] = true
		}
	}
	return cols
}

type rowScanner interface{ Scan(dest ...any) error }

func scanFeature(r rowScanner) (domain.Feature, error) {
	var f domain.Feature
	var id, stage, created, updated, kind, verified, severity string
	err := r.Scan(&id, &f.Num, &f.Title, &f.OneLiner, &f.Slug, &stage,
		&f.Skip.Brainstorm, &f.Skip.Plan, &f.Profile,
		&f.Budget.Envelope,
		&f.Spend.Credits, &f.Spend.EstimatedCredits, &f.Spend.InputTokens, &f.Spend.OutputTokens,
		&f.Spend.DecomposeCredits, &f.Spend.DecomposeInputTokens, &f.Spend.DecomposeOutputTokens,
		&created, &updated,
		&kind, &f.ExternalRef, &f.Skip.Triage, &f.Skip.Diagnose, &f.Skip.Quick, &verified, &f.GateApproval, &severity, &f.ForkPoint, &f.LandedSHA, &f.CommitDraftFail, &f.Repo,
		&f.PullRequest.Repo, &f.PullRequest.Number, &f.PullRequest.URL, &f.PullRequest.HeadSHA)
	if err != nil {
		return f, err
	}
	f.Severity = domain.Severity(severity)
	f.ID = domain.FeatureID(id)
	f.Kind = domain.Kind(kind)
	f.Stage = domain.Stage(stage)
	if f.CreatedAt, err = time.Parse(timeFmt, created); err != nil {
		return f, fmt.Errorf("feature %s: corrupt created_at %q: %w", id, created, err)
	}
	if f.UpdatedAt, err = time.Parse(timeFmt, updated); err != nil {
		return f, fmt.Errorf("feature %s: corrupt updated_at %q: %w", id, updated, err)
	}
	// verified_at is empty until the verify gate stamps it; only a
	// non-empty value is parsed, so an un-verified feature reads as zero.
	if verified != "" {
		if f.VerifiedAt, err = time.Parse(timeFmt, verified); err != nil {
			return f, fmt.Errorf("feature %s: corrupt verified_at %q: %w", id, verified, err)
		}
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

// AddDecomposeSpend accumulates a decompose-pass usage sample (FD-081)
// onto both a feature's overall running total and its decompose-only
// bucket, in one UPDATE — so DecomposeCreditEquivalentAt(rate) can never
// exceed CreditEquivalentAt(rate) at any rate, hosted or BYOK, and spend
// reporting can distinguish what decomposition cost from the rest.
func (s *Store) AddDecomposeSpend(ctx context.Context, id domain.FeatureID, credits float64, in, out int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE features SET
			spend_credits = spend_credits + ?,
			spend_in = spend_in + ?,
			spend_out = spend_out + ?,
			spend_decompose_credits = spend_decompose_credits + ?,
			spend_decompose_in = spend_decompose_in + ?,
			spend_decompose_out = spend_decompose_out + ?
		WHERE id = ?`, credits, in, out, credits, in, out, string(id))
	if err != nil {
		return fmt.Errorf("metering decompose spend for %s: %w", id, err)
	}
	return nil
}

// SetVerifiedAt stamps when a feature's verify gate passed and its branch
// became ready to land — the marker behind status's `verified` field and the
// headless driver's stop-at-verified terminal state. Like AddSpend it is a
// side-channel (it neither touches updated_at nor moves the stage, which
// stays "verify" through and after the run), so the engine can record
// "verified" the moment the gate is reached without a full-feature write.
func (s *Store) SetVerifiedAt(ctx context.Context, id domain.FeatureID, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET verified_at = ? WHERE id = ?`,
		t.UTC().Format(timeFmt), string(id))
	if err != nil {
		return fmt.Errorf("marking %s verified: %w", id, err)
	}
	return nil
}

// SetGateApproval persists a feature's gate-approval mode (who crosses its
// design gates on an unattended resume). Like SetVerifiedAt it is a
// side-channel write — it neither touches updated_at nor moves the stage —
// so `run` records the chosen mode at creation and a later `resume` that
// re-passes --gate-approval can override it without a full-feature write.
// An empty mode reads as domain.GateGates.
func (s *Store) SetGateApproval(ctx context.Context, id domain.FeatureID, mode string) error {
	if !domain.ValidGateApproval(mode) {
		return fmt.Errorf("setting gate-approval for %s: unknown mode %q", id, mode)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET gate_approval = ? WHERE id = ?`, mode, string(id))
	if err != nil {
		return fmt.Errorf("setting gate-approval for %s: %w", id, err)
	}
	s.appendAutopilotEvent(ctx, id, mode)
	return nil
}

// SetCommitDraftFail durably records why a squash-merge scribe pass last
// failed to produce a draft (a backend/config fault, a guard rejection, or
// a timeout). It is a side-channel write (it neither touches updated_at nor
// moves the stage), so the merge dialog can persist the reason without
// disturbing the row's audit trail; a successful draft clears it with the
// empty string.
func (s *Store) SetCommitDraftFail(ctx context.Context, id domain.FeatureID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET commit_draft_fail = ? WHERE id = ?`, reason, string(id))
	if err != nil {
		return fmt.Errorf("setting commit-draft failure for %s: %w", id, err)
	}
	return nil
}

// SetForkPoint stamps a feature's recorded fork-point SHA. It is a
// side-channel write (it neither touches updated_at nor moves the stage) —
// the worktree manager records it at worktree-creation time so diff-based
// stages can detect later fork drift. It is stamped exactly once: the empty
// string is the sentinel for "recorded at creation or lazily backfilled",
// so an UPDATE is refused unless the row currently holds the "" sentinel,
// guaranteeing Create and the lazy backfill can never race a stored SHA
// into being overwritten. sha must be non-empty and not already recorded.
func (s *Store) SetForkPoint(ctx context.Context, id domain.FeatureID, sha string) error {
	if sha == "" {
		return fmt.Errorf("setting fork-point for %s: refusing an empty SHA", id)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE features SET fork_point = ? WHERE id = ? AND fork_point = ''`,
		sha, string(id))
	if err != nil {
		return fmt.Errorf("setting fork-point for %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting fork-point for %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("setting fork-point for %s: %w", id, ErrForkPointStamped)
	}
	return nil
}

// ForkPoint reads a feature's recorded fork-point SHA — the empty string
// when the worktree predates drift detection (the lazy-backfill sentinel).
// A feature without a row reads the same way: it has no fork to guard
// against, so the guard treats it as needing an initial anchor rather than
// hard-failing.
func (s *Store) ForkPoint(ctx context.Context, id domain.FeatureID) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx,
		`SELECT fork_point FROM features WHERE id = ?`, string(id)).Scan(&sha)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("reading fork-point for %s: %w", id, err)
	}
	return sha, nil
}

// ReanchorForkPoint overwrites a feature's recorded fork-point SHA
// unconditionally — the one explicit, auditable exception to SetForkPoint's
// stamped-once rule. It is only reachable through the worktree manager's
// re-anchor operation, which has already verified the new SHA is sound
// (main's HEAD is an ancestor of the branch). SetForkPoint and the lazy
// backfill keep their protection: Create still cannot clobber a recorded
// fork. sha must be non-empty.
func (s *Store) ReanchorForkPoint(ctx context.Context, id domain.FeatureID, sha string) error {
	if sha == "" {
		return fmt.Errorf("re-anchoring fork-point for %s: refusing an empty SHA", id)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE features SET fork_point = ? WHERE id = ?`, sha, string(id)); err != nil {
		return fmt.Errorf("re-anchoring fork-point for %s: %w", id, err)
	}
	return nil
}

// ClearForkPoint resets a feature's recorded fork-point SHA to the empty
// backfill sentinel. It is called when a feature's worktree is removed or
// its branch deleted, so a later recreate re-anchors the fork to main as it
// reads then instead of keeping a now-stale SHA. It never fails when the
// row exists, regardless of whether a fork was previously recorded.
func (s *Store) ClearForkPoint(ctx context.Context, id domain.FeatureID) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE features SET fork_point = '' WHERE id = ?`, string(id)); err != nil {
		return fmt.Errorf("clearing fork-point for %s: %w", id, err)
	}
	return nil
}

// SetLandedSHA stamps the commit SquashMerge created when a feature's
// branch actually landed on main — the lineage record the worktree
// manager's squash-landed test checks ancestry against, instead of the
// tree-content-equality guess it used before. SquashMerge calls this
// itself right after creating the commit, so every caller gets the
// record for free rather than having to remember to persist the sha it
// returns. Like SetCommitDraftFail it is a side-channel write: no
// updated_at touch, no stage move.
func (s *Store) SetLandedSHA(ctx context.Context, id domain.FeatureID, sha string) error {
	if sha == "" {
		return fmt.Errorf("setting landed sha for %s: refusing an empty SHA", id)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET landed_sha = ? WHERE id = ?`, sha, string(id))
	if err != nil {
		return fmt.Errorf("setting landed sha for %s: %w", id, err)
	}
	return nil
}

// LandedSHA reads a feature's recorded landed-commit SHA — the empty
// string when gummi has never squash-merged this feature's branch. A
// feature without a row reads the same way.
func (s *Store) LandedSHA(ctx context.Context, id domain.FeatureID) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx,
		`SELECT landed_sha FROM features WHERE id = ?`, string(id)).Scan(&sha)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("reading landed sha for %s: %w", id, err)
	}
	return sha, nil
}

// SetPullRequest persists a feature's linked outbound PR (or clears it, when
// ref is Empty()) — the side-channel `gummi pr link`/`unlink` write onto the
// four pr_* columns. Like SetCommitDraftFail it neither touches updated_at
// nor moves the stage: linking is metadata about how the card will land, not
// a stage transition. ref must be Empty() or pass Validate().
func (s *Store) SetPullRequest(ctx context.Context, id domain.FeatureID, ref domain.PullRequestRef) error {
	if !ref.Empty() {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("setting pull request for %s: %w", id, err)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET pr_repo = ?, pr_number = ?, pr_url = ?, pr_head_sha = ? WHERE id = ?`,
		ref.Repo, ref.Number, ref.URL, ref.HeadSHA, string(id))
	if err != nil {
		return fmt.Errorf("setting pull request for %s: %w", id, err)
	}
	return nil
}

// Rounds reads a feature's live round count for the given kind — the
// per-cycle counter the engine reads back on resume to size the
// remaining round budget (plan-critique or review→fix). A feature with
// no row for (id, kind) reads 0 (it has not started that cycle).
func (s *Store) Rounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT count FROM rounds WHERE feature_id = ? AND round_kind = ?`, string(id), string(kind)).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s-rounds for %s: %w", kind, id, err)
	}
	return count, nil
}

// IncrementRounds bumps a feature's round count for the given kind by one
// in a single atomic upsert — never a read-modify-write — so concurrent
// processes can't lose an increment across a resume. It is unbounded: no
// cap lives here (see verdict.MaxRounds).
func (s *Store) IncrementRounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rounds (feature_id, round_kind, count) VALUES (?, ?, 1)
		ON CONFLICT (feature_id, round_kind) DO UPDATE SET count = count + 1`,
		string(id), string(kind)); err != nil {
		return fmt.Errorf("incrementing %s-rounds for %s: %w", kind, id, err)
	}
	return nil
}

// ClearRounds resets a feature's round count for the given kind to 0 —
// the reset that happens when a cycle completes or escalates. A
// live-cycle counter, so clearing a row that doesn't exist yet is
// harmless.
func (s *Store) ClearRounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rounds (feature_id, round_kind, count) VALUES (?, ?, 0)
		ON CONFLICT (feature_id, round_kind) DO UPDATE SET count = 0`,
		string(id), string(kind)); err != nil {
		return fmt.Errorf("clearing %s-rounds for %s: %w", kind, id, err)
	}
	return nil
}

// SetPlanRounds sets a feature's plan-critique round count by value — a
// test-only side-channel write routed to the keyed rounds storage. No cap
// is enforced here; that stays verdict.MaxRounds.
func (s *Store) SetPlanRounds(ctx context.Context, id domain.FeatureID, rounds int) error {
	return s.setRounds(ctx, id, domain.RoundKindPlan, rounds)
}

// SetReviewRounds sets a feature's review→fix round count by value — a
// test-only side-channel write routed to the keyed rounds storage,
// matching SetPlanRounds. No cap is enforced here; that stays
// verdict.MaxRounds.
func (s *Store) SetReviewRounds(ctx context.Context, id domain.FeatureID, rounds int) error {
	return s.setRounds(ctx, id, domain.RoundKindReview, rounds)
}

func (s *Store) setRounds(ctx context.Context, id domain.FeatureID, kind domain.RoundKind, rounds int) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rounds (feature_id, round_kind, count) VALUES (?, ?, ?)
		ON CONFLICT (feature_id, round_kind) DO UPDATE SET count = ?`,
		string(id), string(kind), rounds, rounds); err != nil {
		return fmt.Errorf("setting %s-rounds for %s: %w", kind, id, err)
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
			skip_brainstorm=?, skip_plan=?, skip_triage=?, skip_diagnose=?, quick=?, profile=?,
			budget_envelope=?, repo=?, updated_at=?
		WHERE id=?`,
		f.Title, f.OneLiner, f.Slug,
		f.Skip.Brainstorm, f.Skip.Plan, f.Skip.Triage, f.Skip.Diagnose, f.Skip.Quick, f.Profile,
		f.Budget.Envelope, f.Repo,
		now.Format(timeFmt), string(f.ID))
	if err != nil {
		return fmt.Errorf("updating %s: %w", f.ID, err)
	}
	return nil
}

// DeleteFeature removes a feature and (via cascade) its history. A card
// that others still depend on is refused: dependents are checked first so
// the raw SQLite FK constraint never surfaces and no edge is dropped.
func (s *Store) DeleteFeature(ctx context.Context, id domain.FeatureID) error {
	deps, err := s.ListDependents(ctx, id)
	if err != nil {
		return err
	}
	if len(deps) > 0 {
		return fmt.Errorf("deleting %s: %w", id, ErrDependedOn)
	}
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
	// The gate event goes in the same transaction as the transition it
	// describes, so a card's history cannot record a crossing the features
	// table never made, or miss one it did. Every caller reaches a
	// crossing through here — the engine's own Advance, the review loop's
	// automatic steps, the headless driver — so this is the one place that
	// sees all of them. The crossing answers the newest still-open gate
	// decision, looked up in this same transaction so the crossing and its
	// answer commit together.
	if err := appendGateEventTx(ctx, tx, id, f.Stage, to, actor, now, s.newestOpenGateDecisionTx(ctx, tx, id)); err != nil {
		return f, err
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
