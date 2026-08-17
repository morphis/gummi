package state

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	_ "modernc.org/sqlite"
)

func gitRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInitRequiresGitRepo(t *testing.T) {
	if _, err := Init(t.TempDir()); err == nil {
		t.Fatal("Init outside a git repo: want error, got nil")
	}
}

func TestInitCreatesSkeleton(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{w.GummiDir(), w.StateDir(), w.DraftsDir(), w.SpecsDir(), w.WorktreesDir()} {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			t.Errorf("missing dir %s (err=%v)", dir, err)
		}
	}
	if fi, err := os.Stat(w.StateDir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir perms = %v, want 0700 (err=%v)", fi.Mode().Perm(), err)
	}
	ignore, err := os.ReadFile(filepath.Join(w.GummiDir(), ".gitignore"))
	if err != nil {
		t.Fatalf("no .gummi/.gitignore: %v", err)
	}
	for _, want := range []string{"/worktrees/", "/state/"} {
		if !strings.Contains(string(ignore), want) {
			t.Errorf(".gummi/.gitignore missing %q", want)
		}
	}
	seq, err := os.ReadFile(w.SeqFile())
	if err != nil || string(seq) != "0\n" {
		t.Errorf("seq file = %q, err=%v; want \"0\\n\"", seq, err)
	}
}

func TestInitIdempotent(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.SeqFile(), []byte("41\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	seq, _ := os.ReadFile(w.SeqFile())
	if string(seq) != "41\n" {
		t.Errorf("re-init clobbered seq: %q", seq)
	}
}

func TestInitRefusesSymlinkedState(t *testing.T) {
	root := gitRoot(t)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gummi"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".gummi", "state")); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root); err == nil {
		t.Fatal("Init accepted a symlinked state dir")
	}
}

// nestedLayout synthesizes a parent workspace P with a git repo and a
// managed worktree entry FD-042 (a worktree root carrying a .git
// gitdir-pointer file), returning P. It mirrors the failure mode where an
// agent runs inside its own feature worktree.
func nestedLayout(t *testing.T) string {
	t.Helper()
	p := gitRoot(t)
	if err := os.MkdirAll(filepath.Join(p, ".gummi", "worktrees", "FD-042"), 0o750); err != nil {
		t.Fatal(err)
	}
	// A nested worktree has a .git gitdir-pointer FILE, not a directory.
	if err := os.WriteFile(filepath.Join(p, ".gummi", "worktrees", "FD-042", ".git"), []byte("gitdir: ../.git/worktrees/FD-042\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertNestedRefusal(t *testing.T, p, root string) {
	t.Helper()
	_, err := Init(root)
	if !errors.Is(err, ErrNestedInit) {
		t.Fatalf("Init(%s) = %v, want ErrNestedInit", root, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, p) || !strings.Contains(msg, "FD-042") {
		t.Errorf("error %q does not name enclosing root %s and worktree FD-042", msg, p)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".gummi")); !os.IsNotExist(statErr) {
		t.Errorf("nested .gummi at %s created after refusal: %v", root, statErr)
	}
}

func TestInitRefusesNestedWorkspace(t *testing.T) {
	p := nestedLayout(t)
	assertNestedRefusal(t, p, filepath.Join(p, ".gummi", "worktrees", "FD-042"))
}

func TestInitRefusesFromWorktreeSubdir(t *testing.T) {
	p := nestedLayout(t)
	sub := filepath.Join(p, ".gummi", "worktrees", "FD-042", "pkg", "foo")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	assertNestedRefusal(t, p, sub)
}

func TestInitSucceedsInUnrelatedRepo(t *testing.T) {
	q := gitRoot(t)
	w, err := Init(q)
	if err != nil {
		t.Fatalf("Init in unrelated repo: %v", err)
	}
	if fi, statErr := os.Stat(w.GummiDir()); statErr != nil || !fi.IsDir() {
		t.Errorf("unrelated repo .gummi not created: %v", statErr)
	}
}

func TestInitAtEnclosingWorkspaceRoot(t *testing.T) {
	p := nestedLayout(t)
	if _, err := Init(p); err != nil {
		t.Fatalf("Init at enclosing workspace root %s should proceed: %v", p, err)
	}
}

func TestInitNestingBeatsGitCheck(t *testing.T) {
	p := nestedLayout(t)
	root := filepath.Join(p, ".gummi", "worktrees", "FD-042")
	_, err := Init(root)
	if !errors.Is(err, ErrNestedInit) {
		t.Fatalf("Init at nested worktree with .git pointer = %v, want ErrNestedInit", err)
	}
}

func TestInitIgnoresSymlinkedAncestorGummi(t *testing.T) {
	parent := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(parent, ".gummi")); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(q, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	w, err := Init(q)
	if err != nil {
		t.Fatalf("Init with symlinked ancestor .gummi: %v", err)
	}
	if fi, statErr := os.Stat(w.GummiDir()); statErr != nil || !fi.IsDir() {
		t.Errorf("repo .gummi not created across symlinked ancestor: %v", statErr)
	}
}

func TestInitNestingHasNoOverride(t *testing.T) {
	t.Setenv("GUMMI_ALLOW_NESTED", "1")
	p := nestedLayout(t)
	assertNestedRefusal(t, p, filepath.Join(p, ".gummi", "worktrees", "FD-042"))
}

func TestOpenRequiresInit(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open without init: want error")
	}
	root := gitRoot(t)
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatalf("Open after init: %v", err)
	}
}

func TestNextSeqSequential(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		got, err := NextSeq(w.SeqFile())
		if err != nil || got != want {
			t.Fatalf("NextSeq = %d, %v; want %d", got, err, want)
		}
	}
}

func TestNextSeqConcurrent(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := NextSeq(w.SeqFile())
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[v] {
				errs <- errors.New("duplicate seq value")
				return
			}
			seen[v] = true
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(seen) != n {
		t.Fatalf("got %d unique values, want %d", len(seen), n)
	}
}

func TestNextSeqCorruptCounter(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.SeqFile(), []byte("not-a-number"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NextSeq(w.SeqFile()); err == nil {
		t.Fatal("corrupt counter accepted")
	}
}

func TestNextSeqStaleLock(t *testing.T) {
	root := gitRoot(t)
	w, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.SeqFile()+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = NextSeq(w.SeqFile())
	if err == nil {
		t.Fatal("want deterministic error while lock is held")
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	w, err := Init(gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func feat(num int, title string) *domain.Feature {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now().UTC()
	return &domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug,
		Stage: domain.StageTodo, Profile: "thrifty",
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestStoreCRUD(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Dark mode")
	f.OneLiner = "a toggle"
	f.Budget = domain.Budget{Envelope: 300}
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, f); err == nil {
		t.Fatal("duplicate ID accepted")
	}

	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Dark mode" || got.OneLiner != "a toggle" || got.Budget.Envelope != 300 ||
		got.Stage != domain.StageTodo || got.Profile != "thrifty" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || !got.CreatedAt.Equal(f.CreatedAt) {
		t.Errorf("created_at mismatch: %v vs %v", got.CreatedAt, f.CreatedAt)
	}

	got.Title = "Dark mode toggle"
	if err := s.UpdateFeature(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetFeature(ctx, f.ID)
	if err != nil || got2.Title != "Dark mode toggle" {
		t.Fatalf("update lost: %+v err=%v", got2, err)
	}

	// stage changes must be rejected outside Transition
	got2.Stage = domain.StageImplement
	if err := s.UpdateFeature(ctx, &got2); err == nil {
		t.Fatal("UpdateFeature accepted a stage change")
	}

	if err := s.CreateFeature(ctx, feat(2, "CSV export")); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListFeatures(ctx)
	if err != nil || len(list) != 2 || list[0].Num != 1 || list[1].Num != 2 {
		t.Fatalf("list = %+v, err=%v", list, err)
	}

	if err := s.DeleteFeature(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFeature(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err=%v, want ErrNotFound", err)
	}
	if err := s.DeleteFeature(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: err=%v, want ErrNotFound", err)
	}
}

func TestStoreTransition(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Auth fix")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := s.Transition(ctx, f.ID, domain.StageBrainstorm, "user")
	if err != nil || got.Stage != domain.StageBrainstorm {
		t.Fatalf("transition: %+v, %v", got.Stage, err)
	}

	// illegal jump: brainstorm → implement
	if _, err := s.Transition(ctx, f.ID, domain.StageImplement, "user"); err == nil {
		t.Fatal("illegal transition accepted")
	}
	// stage unchanged after rejection
	cur, _ := s.GetFeature(ctx, f.ID)
	if cur.Stage != domain.StageBrainstorm {
		t.Fatalf("stage moved despite rejection: %s", cur.Stage)
	}

	// skip flag honored
	g := feat(2, "Tiny tweak")
	g.Skip = domain.SkipFlags{Brainstorm: true, Plan: true}
	if err := s.CreateFeature(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, g.ID, domain.StageSpec, "user"); err != nil {
		t.Fatalf("skip-brainstorm transition rejected: %v", err)
	}

	// unknown feature
	if _, err := s.Transition(ctx, "FD-999", domain.StageBrainstorm, "user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}

	hist, err := s.History(ctx, f.ID)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %+v, err=%v", hist, err)
	}
	h := hist[0]
	if h.From != domain.StageTodo || h.To != domain.StageBrainstorm || h.Actor != "user" || h.At.IsZero() {
		t.Errorf("bad history record: %+v", h)
	}
}

func TestMintFeatureNumSkipsUsedNumbers(t *testing.T) {
	w, err := Init(gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// mint normally
	n, err := s.MintFeatureNum(ctx, w.SeqFile())
	if err != nil || n != 1 {
		t.Fatalf("first mint = %d, %v; want 1", n, err)
	}
	if err := s.CreateFeature(ctx, feat(1, "First")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, feat(2, "Second")); err != nil {
		t.Fatal(err)
	}

	// simulate a merge rewinding the committed counter below used nums
	if err := os.WriteFile(w.SeqFile(), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err = s.MintFeatureNum(ctx, w.SeqFile())
	if err != nil || n != 3 {
		t.Fatalf("mint after rewind = %d, %v; want 3 (skipping used 1 and 2)", n, err)
	}
}

func TestStoreRejectsCorruptRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.CreateFeature(ctx, feat(1, "Fine")); err != nil {
		t.Fatal(err)
	}
	// corrupt the slug behind the store's back
	if _, err := s.db.ExecContext(ctx,
		`UPDATE features SET slug='../../etc' WHERE num=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFeature(ctx, "FD-001"); err == nil {
		t.Fatal("corrupt slug row loaded without error")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	w, err := Init(gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateFeature(ctx, feat(1, "Persist me")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, err := s2.ListFeatures(ctx)
	if err != nil || len(list) != 1 || list[0].Title != "Persist me" {
		t.Fatalf("reopen lost data: %+v, err=%v", list, err)
	}
}

// TestMigrations verifies the additive severity column exists, survives
// a reopen (severity persisted through a round trip), and that opening
// the store twice is idempotent (the ALTER TABLE duplicate-column error
// is swallowed).
func TestMigrations(t *testing.T) {
	w, err := Init(gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	// Open twice up front to exercise the migration path twice — the
	// second open must not choke on the already-present column.
	s1, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	defer s2.Close()

	// The severity column exists in the schema after migration.
	db, err := sql.Open("sqlite", w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('features') WHERE name='severity'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("severity column missing from features schema (count=%d)", n)
	}

	ctx := context.Background()
	id, _ := domain.NewID(domain.KindBug, 1)
	f := feat(1, "Severe bug")
	f.ID = id
	f.Kind = domain.KindBug
	f.Severity = domain.SeverityCritical
	if err := s1.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	// Severity survives a fresh open.
	s3, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	got, err := s3.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != domain.SeverityCritical {
		t.Errorf("severity = %q, want %q after reopen", got.Severity, domain.SeverityCritical)
	}

	// The sessions flavor column exists after migration and round-trips a
	// session's pass identity through a fresh open.
	var fn int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='flavor'`,
	).Scan(&fn); err != nil {
		t.Fatal(err)
	}
	if fn != 1 {
		t.Fatalf("flavor column missing from sessions schema (count=%d)", fn)
	}
	if err := s3.SaveSession(ctx, SessionSnapshot{
		Feature: f.ID, Stage: f.Stage, Role: "implementer",
		Flavor: "rebase", State: "done",
	}); err != nil {
		t.Fatal(err)
	}
	snaps, err := s3.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Flavor != "rebase" {
		t.Errorf("restored sessions = %+v, want one rebase-flavored session", snaps)
	}
}

// TestFreshSchemaCoversWrites locks in the invariant that a fresh
// database's features table contains every column the store reads back.
// If a future column is added to featureCols but not to CREATE TABLE (or
// vice versa), the written set and the real schema diverge and this test
// fails.
func TestFreshSchemaCoversWrites(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	written := writtenFeatureColumns()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('features')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var missing []string
	for name := range written {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Errorf("fresh features schema missing %d written column(s): %v", len(missing), missing)
	}
}

// TestOldSchemaStillOpens hand-builds a database using the
// pre-reconciliation features table (no severity/plan_rounds/review_rounds,
// but with budget_spent), then opens the store over it. It must open
// cleanly (the ALTERs backfill the three new columns with their defaults),
// create and round-trip a feature, and leave budget_spent at its 0
// default — proving the Budget.Spent removal broke nothing and old
// databases are unharmed.
func TestOldSchemaStillOpens(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE features (
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
		skip_diagnose   INTEGER NOT NULL DEFAULT 0,
		quick           INTEGER NOT NULL DEFAULT 0,
		verified_at     TEXT NOT NULL DEFAULT '',
		gate_approval   TEXT NOT NULL DEFAULT '',
		fork_point      TEXT NOT NULL DEFAULT ''
	);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open over old schema: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	f := feat(1, "Old DB feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Old DB feature" {
		t.Errorf("round-trip title = %q", got.Title)
	}

	var severity, budgetSpent string
	var planRounds, reviewRounds int
	if err := s.db.QueryRowContext(ctx,
		`SELECT severity, plan_rounds, review_rounds, budget_spent FROM features WHERE id = ?`,
		string(f.ID)).Scan(&severity, &planRounds, &reviewRounds, &budgetSpent); err != nil {
		t.Fatal(err)
	}
	if severity != "" || planRounds != 0 || reviewRounds != 0 {
		t.Errorf("backfilled defaults = severity %q, plan_rounds %d, review_rounds %d; want empty/0/0",
			severity, planRounds, reviewRounds)
	}
	if budgetSpent != "0" {
		t.Errorf("budget_spent = %q, want 0", budgetSpent)
	}
}
