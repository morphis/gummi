package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// gitShim installs a PATH-fronted `git` that logs each invocation's joined
// args to a file (one line per invocation) and execs the real git, so a
// test can count exactly how many git subprocesses a board reload spawns.
// The real git is resolved before PATH is swapped so the shim never
// recurses on itself.
func gitShim(t *testing.T) (logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "git.log")
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// gitLogLines returns the logged invocations since the shim was installed.
func gitLogLines(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil // nothing spawned git yet
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestReloadGitCount proves the Landed gate: a board reload walks the
// expensive squash/merge-tree path only for cards whose branch has commits
// of its own ahead of the fork. Worktree-less and fresh (no-commit) cards
// contribute no merge-tree invocation at all, and the total spawn count per
// reload is far below what it was when every worktree card walked Landed.
func TestReloadGitCount(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	now := fixedTime
	mkFeat := func(num int, title string) *domain.Feature {
		id, _ := domain.NewFeatureID(num)
		slug, _ := domain.Slugify(title)
		return &domain.Feature{
			ID: id, Num: num, Title: title, Slug: slug,
			Stage: domain.StageTodo, CreatedAt: now, UpdatedAt: now,
		}
	}

	// FD-001: no worktree (never left spec).
	if err := m.store.CreateFeature(ctx, mkFeat(1, "No worktree")); err != nil {
		t.Fatal(err)
	}
	// FD-002/FD-003: fresh worktrees at main HEAD, no commits of their own.
	for _, n := range []int{2, 3} {
		f := mkFeat(n, "Fresh")
		if err := m.store.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
		if _, err := m.wt.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	// FD-004: a worktree with a commit of its own (branch ahead, unmerged) —
	// the only card that can possibly be landed, so the only one that may
	// walk the squash path.
	committed := mkFeat(4, "Committed")
	if err := m.store.CreateFeature(ctx, committed); err != nil {
		t.Fatal(err)
	}
	wt4, err := m.wt.Create(ctx, committed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt4, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt4, "add", ".")
	git(t, wt4, "commit", "-qm", "committed work")

	// from here, every git spawn goes through the counting shim
	logPath := gitShim(t)
	m = pump(t, m, m.loadRows)
	if len(m.rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(m.rows))
	}

	lines := gitLogLines(t, logPath)
	// the squash path (merge-tree) runs exactly once — only the committed
	// card; the two fresh and the worktree-less cards contribute none.
	mergeTree := 0
	for _, l := range lines {
		if strings.Contains(l, "merge-tree") {
			mergeTree++
		}
	}
	if mergeTree != 1 {
		t.Errorf("merge-tree invocations = %d, want 1 (only the committed card)", mergeTree)
	}
	// a fresh card now costs only its cheap BranchAhead pair, not the full
	// Landed walk: the total stays well under the pre-change figure, where
	// every worktree card (three here) ran the whole Landed path.
	if len(lines) >= 20 {
		t.Errorf("git spawns per loadRows = %d, want well under the pre-change walk", len(lines))
	}
}
