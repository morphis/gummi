package worktree

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

// rewindMainBackward rewinds the main checkout to an unrelated lineage that
// does not descend from the recorded fork — the shape of the FD-002 incident
// where main loses commits that had merged past a feature's fork, so the
// live merge-base would slide and re-introduce them as the feature's own.
func rewindMainBackward(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "rewound.ts", "rewound\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "checkout", "-q", "--orphan", "tmp-rewound")
	mustGit(t, root, "commit", "-q", "-m", "rewound main")
	mustGit(t, root, "branch", "-M", "tmp-rewound", "main")
}

// Create stamps the fork-point — merge-base(main, branch) at creation —
// into the store, so diff-based stages know what drift to detect against.
func TestCreateRecordsForkPoint(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Record fork")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	want := mustGit(t, root, "merge-base", "HEAD", f.BranchName())
	got, err := fs.ForkPoint(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("recorded fork = %s, want merge-base %s", got, want)
	}
}

// A forward advance of main leaves the recorded fork an ancestor, so the
// guard adds no false positives.
func TestAssertNoForkDrift_ForwardOnlyPasses(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Forward")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "advance.txt", "advance\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main advance")
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatalf("forward advance misflagged as drift: %v", err)
	}
}

// A rewind of main backward past the recorded fork is drift: the guard
// returns a ForkDriftError naming the stored SHA and main's new HEAD.
func TestAssertNoForkDrift_DriftRefuses(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Drift")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	recorded, err := fs.ForkPoint(ctx, f.ID)
	if err != nil || recorded == "" {
		t.Fatalf("no recorded fork: %q, %v", recorded, err)
	}

	rewindMainBackward(t, root)

	err = m.AssertNoForkDrift(ctx, f)
	if err == nil {
		t.Fatal("drift not detected")
	}
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *ForkDriftError, got %T: %v", err, err)
	}
	if fe.Recorded != recorded {
		t.Errorf("Recorded = %s, want %s", fe.Recorded, recorded)
	}
	if fe.MainHead != mustGit(t, root, "rev-parse", "HEAD") {
		t.Errorf("MainHead = %s, want main HEAD", fe.MainHead)
	}
}

// A branch rebase (the stored SHA no longer an ancestor of the *branch* HEAD
// but still an ancestor of main HEAD) is a legitimate feature-owner action,
// not drift.
func TestAssertNoForkDrift_BranchRebaseNotDrift(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Rebase")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "feat.txt", "feat\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feat")
	writeFile(t, root, "main.txt", "main\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main advance")

	if err := m.RebaseOnMain(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatalf("branch rebase misflagged as drift: %v", err)
	}
}

// A worktree predating drift detection (empty recorded fork) is lazily
// anchored to the current merge-base on first access, logging the one-line
// note exactly once.
func TestAssertNoForkDrift_LazyBackfill(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Backfill")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	// simulate a pre-existing worktree: no recorded fork.
	delete(fs.m, f.ID)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer func() { log.SetOutput(os.Stderr) }()

	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatal(err)
	}
	want := mustGit(t, root, "merge-base", "HEAD", f.BranchName())
	got, err := fs.ForkPoint(ctx, f.ID)
	if err != nil || got != want {
		t.Fatalf("backfilled fork = %s, %v; want %s", got, err, want)
	}
	if n := strings.Count(buf.String(), "drift detection"); n != 1 {
		t.Fatalf("backfill note logged %d times, want 1: %q", n, buf.String())
	}

	// a second access no-ops the backfill (stamped once) and re-logs nothing.
	buf.Reset()
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "drift detection"); n != 0 {
		t.Fatalf("note re-logged on second access: %d, %q", n, buf.String())
	}
}

// Diff refuses on drift, returning a ForkDriftError with an empty diff and
// never shelling out to `git diff` (a dirty edit that would otherwise appear
// in the output stays out).
func TestDiffRefusesOnDrift(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Diff guard")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "README.md", "uncommitted edit that would show in a real diff\n")
	rewindMainBackward(t, root)

	diff, err := m.Diff(ctx, f)
	if err == nil {
		t.Fatal("Diff succeeded despite drift")
	}
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *ForkDriftError, got %T: %v", err, err)
	}
	if diff != "" {
		t.Fatalf("Diff produced content despite refusal: %q", diff)
	}
}

// SquashMerge refuses on drift before merging — main's HEAD is byte-identical
// before and after (nothing lands).
func TestSquashMergeRefusesOnDrift(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Squash guard")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "sq.txt", "feature work\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature commit")
	rewindMainBackward(t, root)

	before := mustGit(t, root, "rev-parse", "HEAD")
	err = m.SquashMerge(ctx, f, "land "+string(f.ID))
	if err == nil {
		t.Fatal("SquashMerge succeeded despite drift")
	}
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *ForkDriftError, got %T: %v", err, err)
	}
	if after := mustGit(t, root, "rev-parse", "HEAD"); after != before {
		t.Fatalf("main moved despite refusal: %s → %s", before, after)
	}
}

// The remedy ForkDriftError points at must actually work: after a rewind
// trips the guard, Remove + DeleteBranch clear the stale fork and a recreate
// re-anchors it to main's current head, so the guard passes again. Without
// the clear, the recreated worktree would keep the original (now-stale) SHA
// and refuse forever.
func TestRecreateClearsDrift(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Recreate")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	rewindMainBackward(t, root)
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("expected drift after rewind")
	}

	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteBranch(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ForkPoint(ctx, f.ID); err != nil || got != "" {
		t.Fatalf("fork not cleared after remove+delete: %q, %v", got, err)
	}

	if _, err := m.Create(ctx, f); err != nil {
		t.Fatalf("recreate failed: %v", err)
	}
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatalf("recreated worktree still refuses: %v", err)
	}
}
