package engine

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// rewindMain rewinds the main checkout to an unrelated lineage that does not
// descend from a feature's recorded fork — the FD-002 drift shape.
func rewindMain(t *testing.T, root string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "rewound.ts"), []byte("rewound\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("checkout", "-q", "--orphan", "tmp-rewound")
	git("commit", "-q", "-m", "rewound main")
	git("branch", "-M", "tmp-rewound", "main")
}

// addWorktreeRaw creates a feature's worktree through git directly, without
// the manager's Create — so fork_point stays "" (a pre-existing worktree).
func addWorktreeRaw(t *testing.T, root string, f domain.Feature) {
	t.Helper()
	dir := filepath.Join(root, f.WorktreePath())
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("worktree", "add", "-b", f.BranchName(), "--", dir)
}

func TestAdvanceReviewRefusesOnDrift(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "review drift", domain.StageImplement)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)

	rewindMain(t, ws.Root)

	res, err := e.Advance(context.Background(), f.ID, "test")
	if err == nil {
		t.Fatal("Advance into review succeeded despite drift")
	}
	var fe *worktree.ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *worktree.ForkDriftError, got %T: %v", err, err)
	}
	if res.Feature.Stage != domain.StageImplement {
		t.Errorf("feature moved to %s despite refusal, want StageImplement", res.Feature.Stage)
	}
	stored, gerr := store.GetFeature(context.Background(), f.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if stored.Stage != domain.StageImplement {
		t.Errorf("stored stage = %s, want StageImplement (row must not move)", stored.Stage)
	}
	if s := e.Get(f.ID); s != nil {
		t.Error("review session was created despite refusal")
	}
}

func TestAdvanceReviewBackfillsForkPoint(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "review backfill", domain.StageImplement)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// a pre-existing worktree: never went through manager.Create, so the
	// recorded fork is empty.
	addWorktreeRaw(t, ws.Root, f)
	if got, err := store.ForkPoint(context.Background(), f.ID); err != nil || got != "" {
		t.Fatalf("setup: fork_point = %q, %v; want empty", got, err)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer func() { log.SetOutput(os.Stderr) }()

	res, err := e.Advance(context.Background(), f.ID, "test")
	if err != nil {
		t.Fatalf("Advance with backfill failed: %v", err)
	}
	if res.Status != StatusAdvanced || res.Feature.Stage != domain.StageReview {
		t.Fatalf("Advance status = %v stage = %s, want Advanced/StageReview", res.Status, res.Feature.Stage)
	}
	want := mustGitsha(t, ws.Root, "merge-base", "HEAD", f.BranchName())
	stored, gerr := store.GetFeature(context.Background(), f.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if stored.ForkPoint != want {
		t.Errorf("backfilled fork = %s, want merge-base %s", stored.ForkPoint, want)
	}
	if !strings.Contains(buf.String(), "drift detection") {
		t.Errorf("backfill note missing from log: %q", buf.String())
	}
}

func mustGitsha(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
