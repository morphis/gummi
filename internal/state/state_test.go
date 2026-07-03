package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphia/gummi/internal/domain"
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
	got.Budget.Spent = 12
	if err := s.UpdateFeature(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetFeature(ctx, f.ID)
	if err != nil || got2.Title != "Dark mode toggle" || got2.Budget.Spent != 12 {
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
