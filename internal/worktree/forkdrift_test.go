package worktree

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
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
	_, err = m.SquashMerge(ctx, f, "land "+string(f.ID))
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

// Landed refuses on drift: the is-ancestor vs squash-landed disagreement
// from the Problem statement must surface as a typed ForkDriftError, never
// as an ambiguous bool.
func TestLandedRefusesOnDrift(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Landed drift")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	recorded, err := fs.ForkPoint(ctx, f.ID)
	if err != nil || recorded == "" {
		t.Fatalf("no recorded fork: %q, %v", recorded, err)
	}

	rewindMainBackward(t, root)

	landed, err := m.Landed(ctx, f)
	if err == nil {
		t.Fatalf("Landed returned (%v, nil) despite drift", landed)
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

// A forward advance of main leaves the recorded fork an ancestor, so
// Landed keeps its normal boolean-returning behavior — no drift refusal.
func TestLandedForwardAdvancePasses(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Landed forward")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "feat.txt", "feature\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature work")
	writeFile(t, root, "main.txt", "main\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main advance")

	landed, err := m.Landed(ctx, f)
	if err != nil {
		t.Fatalf("Landed refused on forward advance: %v", err)
	}
	if landed {
		t.Fatal("branch with unmerged work read as landed")
	}
}

// CommitAll refuses on drift before staging: the branch tip is
// byte-identical before and after (no add, no commit).
func TestCommitAllRefusesOnDrift(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "CommitAll drift")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "dirty.txt", "uncommitted\n")
	branch := "refs/heads/" + f.BranchName()
	before := mustGit(t, root, "rev-parse", branch)

	rewindMainBackward(t, root)

	done, err := m.CommitAll(ctx, f, "checkpoint")
	if err == nil {
		t.Fatalf("CommitAll returned (%v, nil) despite drift", done)
	}
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *ForkDriftError, got %T: %v", err, err)
	}
	if after := mustGit(t, root, "rev-parse", branch); after != before {
		t.Fatalf("branch tip moved despite refusal: %s → %s", before, after)
	}
}

// A forward advance of main lets CommitAll proceed normally: the dirty
// work is committed and the branch tip moves.
func TestCommitAllForwardAdvancePasses(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "CommitAll forward")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "dirty.txt", "uncommitted\n")
	branch := "refs/heads/" + f.BranchName()
	before := mustGit(t, root, "rev-parse", branch)

	writeFile(t, root, "main.txt", "main\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main advance")

	done, err := m.CommitAll(ctx, f, "checkpoint")
	if err != nil {
		t.Fatalf("CommitAll refused on forward advance: %v", err)
	}
	if !done {
		t.Fatal("CommitAll made no commit for a dirty worktree")
	}
	if after := mustGit(t, root, "rev-parse", branch); after == before {
		t.Fatal("branch tip did not move after CommitAll")
	}
}

// The cleanup escape hatches must not be blocked by drift: an operator
// facing a ForkDriftError has to be able to tear the affected worktree
// down. Remove on a clean worktree succeeds without force and clears the
// recorded fork.
func TestRemoveSurvivesDrift(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Remove drift")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	rewindMainBackward(t, root)
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("expected drift after rewind")
	}

	if err := m.Remove(ctx, f, false); err != nil {
		t.Fatalf("Remove refused on drift: %v", err)
	}
	if got, err := fs.ForkPoint(ctx, f.ID); err != nil || got != "" {
		t.Fatalf("fork not cleared after Remove: %q, %v", got, err)
	}
}

// DeleteBranch is another cleanup hatch: force=true is the operator
// semantic under drift, since the branch sits on the pre-rewind lineage
// and git's -d ancestry check would refuse it as "not fully merged".
func TestDeleteBranchSurvivesDrift(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "DeleteBranch drift")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	rewindMainBackward(t, root)
	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if err := m.DeleteBranch(ctx, f, true); err != nil {
		t.Fatalf("DeleteBranch refused on drift: %v", err)
	}
	if got, err := fs.ForkPoint(ctx, f.ID); err != nil || got != "" {
		t.Fatalf("fork not cleared after DeleteBranch: %q, %v", got, err)
	}
}

// DeleteLandedBranch goes through the private squashLanded, never the
// guarded public Landed, so drift must never block the teardown — whatever
// git's own -d verdict is, it is not a ForkDriftError.
func TestDeleteLandedBranchBypassesGuard(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Landed bypass")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "sq.txt", "feature work\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature commit")

	rewindMainBackward(t, root)
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("expected drift after rewind")
	}

	err = m.DeleteLandedBranch(ctx, f)
	var fe *ForkDriftError
	if errors.As(err, &fe) {
		t.Fatalf("DeleteLandedBranch surfaced ForkDriftError: %v", err)
	}
}

// TestReanchorOnMain proves the full re-anchor recovery: after main is
// rewritten under a drifted worktree, rebasing the branch onto main and then
// re-anchoring the fork clears drift, re-stamps the fork to main's HEAD, and
// leaves the branch's checkpoint commit intact.
func TestReanchorOnMain(t *testing.T) {
	root := newRepo(t)
	fs := &memForkStore{}
	m, err := NewManager(ctx, root, fs)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(1, "Reanchor")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// a checkpoint commit of the feature's own
	writeFile(t, p, "check.txt", "checkpoint\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "checkpoint")

	// rewrite main under the worktree — drift
	rewindMainBackward(t, root)
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("expected drift after rewind")
	}

	// the recovery gesture: rebase then re-anchor
	if err := m.RebaseOnMain(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.ReanchorOnMain(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatalf("still drifted after re-anchor: %v", err)
	}
	// the fork is re-stamped to main's HEAD
	want := mustGit(t, root, "rev-parse", "HEAD")
	if got, _ := fs.ForkPoint(ctx, f.ID); got != want {
		t.Fatalf("re-anchored fork = %s, want main HEAD %s", got, want)
	}
	// the checkpoint commit survives the rebase
	logMsg := mustGit(t, root, "log", "--oneline", f.BranchName())
	if !strings.Contains(logMsg, "checkpoint") {
		t.Fatalf("checkpoint commit lost after re-anchor:\n%s", logMsg)
	}
}

// Re-anchoring refuses when main's HEAD is not in the branch's history: the
// soundness guard fails, the feature is still drifted, and the operator
// keeps a ForkDriftError naming the remedies.
func TestReanchorOnMain_RefusesWhenNotRebased(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Refuse reanchor")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "chk.txt", "x\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "chk")
	rewindMainBackward(t, root)

	err = m.ReanchorOnMain(ctx, f)
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("ReanchorOnMain without a rebase: want *ForkDriftError, got %T: %v", err, err)
	}
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("drift cleared without a rebase")
	}
}

// The rewritten ForkDriftError names the work item, its branch, both full
// SHAs, the likely causes, and both remedies — and errors.As matching still
// works for existing callers.
func TestForkDriftErrorMessage(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Message")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "m.txt", "m\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "m")
	rewindMainBackward(t, root)

	recorded, _ := m.forkStore.ForkPoint(ctx, f.ID)
	mainHead := mustGit(t, root, "rev-parse", "HEAD")
	err = m.AssertNoForkDrift(ctx, f)
	var fe *ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *ForkDriftError, got %T: %v", err, err)
	}
	msg := fe.Error()
	for _, want := range []string{
		string(f.ID), f.BranchName(), recorded, mainHead,
		"fork drift", "press r", "reflog",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
}

// A drifted worktree with an uncommitted edit recovers in one gesture: the
// --autostash rebase carries the edit across, and after re-anchoring the
// drift clears with the uncommitted edit restored — never silently dropped.
func TestRebaseOnMainAutostashCarriesDirty(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Autostash")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// an uncommitted edit to a tracked file
	writeFile(t, p, "README.md", "uncommitted edit\n")
	rewindMainBackward(t, root)
	if err := m.AssertNoForkDrift(ctx, f); err == nil {
		t.Fatal("expected drift")
	}

	if err := m.RebaseOnMainAutostash(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.ReanchorOnMain(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		t.Fatalf("drift not cleared: %v", err)
	}
	// the uncommitted edit survived the stash/unstash
	if content, err := os.ReadFile(filepath.Join(p, "README.md")); err != nil || string(content) != "uncommitted edit\n" {
		t.Fatalf("uncommitted edit lost after autostash: %q, %v", content, err)
	}
}
