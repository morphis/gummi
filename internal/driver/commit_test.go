package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitStatusPorcelain returns `git status --porcelain` output for dir,
// trimmed, failing the test on error.
func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestDriverCommitCommitsDirtyWorktree proves the happy path: a dirty
// worktree's changes land on the card's own branch under the caller's
// message, and the worktree is clean afterward.
func TestDriverCommitCommitsDirtyWorktree(t *testing.T) {
	h, d, id := driveVerified(t)
	f, err := h.store.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	wtPath, err := h.wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "stray.txt"), []byte("stray\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := d.Commit(context.Background(), id, "fix(export): commit stray worktree changes")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}

	ev := lastEvent(h, "committed")
	if ev == nil {
		t.Fatalf("no committed event in stream; got %v", h.eventKinds())
	}
	if ev["branch"] != f.BranchName() {
		t.Fatalf("committed.branch = %v, want %s", ev["branch"], f.BranchName())
	}
	sha, _ := ev["commit"].(string)
	if sha == "" || sha == before {
		t.Fatalf("committed.commit = %q, want a fresh sha != pre-commit HEAD %q", sha, before)
	}
	if got := gitHead(t, wtPath); got != sha {
		t.Fatalf("committed.commit %q != worktree HEAD %q", sha, got)
	}
	if out := gitStatusPorcelain(t, wtPath); out != "" {
		t.Errorf("worktree not clean after Commit:\n%s", out)
	}
}

// TestDriverCommitNoopOnCleanWorktree proves a clean worktree is a no-op:
// StatusDone, no `committed` event, and the branch tip unchanged.
func TestDriverCommitNoopOnCleanWorktree(t *testing.T) {
	h, d, id := driveVerified(t)
	f, err := h.store.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	wtPath, err := h.wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, wtPath)

	out, err := d.Commit(context.Background(), id, "fix(export): commit stray worktree changes")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if lastEvent(h, "committed") != nil {
		t.Fatal("no-op commit still emitted a committed event")
	}
	if got := gitHead(t, wtPath); got != before {
		t.Fatalf("branch tip moved on a no-op commit: %s -> %s", before, got)
	}
}

// TestDriverCommitInvalidMessageRefused refuses a non-Conventional-Commits
// message before any git mutation, leaving the worktree still dirty.
func TestDriverCommitInvalidMessageRefused(t *testing.T) {
	h, d, id := driveVerified(t)
	f, err := h.store.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	wtPath, err := h.wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "stray.txt"), []byte("stray\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, wtPath)

	out, err := d.Commit(context.Background(), id, "not a conventional commit subject")
	if err == nil {
		t.Fatal("Commit accepted an invalid commit message")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, wtPath); got != before {
		t.Fatalf("worktree HEAD moved by invalid-message commit: %s -> %s", before, got)
	}
	if status := gitStatusPorcelain(t, wtPath); !strings.Contains(status, "stray.txt") {
		t.Errorf("worktree no longer dirty after refused commit:\n%s", status)
	}
	if lastEvent(h, "committed") != nil {
		t.Fatal("invalid-message commit still emitted a committed event")
	}
}
