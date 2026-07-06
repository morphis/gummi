package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// rebaseFeatureFixture creates a feature with a worktree branched from
// main, and returns the shell, repo root, and worktree path.
func rebaseFeatureFixture(t *testing.T) (*Shell, string, string) {
	t.Helper()
	m, root := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Rebase me", Slug: "rebase-me",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	wt, err := m.wt.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.Init())
	return m, root, wt
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRebaseCleanSucceeds(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	// advance main with a non-conflicting change
	if err := os.WriteFile(filepath.Join(root, "NEW.md"), []byte("main advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main advance")
	// a feature commit that doesn't touch NEW.md
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature work")

	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if m.notice.isErr || !strings.Contains(m.notice.text, "rebased onto main") {
		t.Fatalf("clean rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
}

func TestRebaseDirtyRefused(t *testing.T) {
	m, _, wt := rebaseFeatureFixture(t)
	// leave an uncommitted change in the worktree
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if !m.notice.isErr || !strings.Contains(m.notice.text, "uncommitted") {
		t.Fatalf("dirty rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
}

func TestRebaseConflictNamesFile(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	// conflicting edits to README.md on both main and the feature branch
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("feature version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature edit")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main edit")

	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if !m.notice.isErr || !strings.Contains(m.notice.text, "README.md") {
		t.Fatalf("conflict rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	// and the worktree is left clean (self-aborted)
	if dirty, err := m.wt.Dirty(context.Background(), &f); dirty || err != nil {
		t.Errorf("worktree dirty after aborted rebase: %v %v", dirty, err)
	}
}
