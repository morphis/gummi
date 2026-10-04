package worktree

import (
	"strings"
	"testing"
)

// A file the agent created and never staged is part of what the next
// commit of the worktree takes along — a checkpoint, or the landing's
// own — so the diff a reviewer reads before landing shows it as new. An
// ignored file is not, and the real index is left exactly as it was:
// reading a diff stages nothing.
func TestDiffShowsUntrackedFilesWithoutStagingThem(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gitignore", "*.log\n")
	mustGit(t, root, "add", ".gitignore")
	mustGit(t, root, "commit", "-q", "-m", "ignore logs")
	m := newManager(t, root)
	f := feature(1, "Untracked")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "hello.go", "package hello\n")
	writeFile(t, p, "odd [name].go", "package odd\n")
	writeFile(t, p, "build.log", "noise\n")

	diff, err := m.Diff(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+++ b/hello.go", "+package hello", "+package odd"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff is missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "build.log") {
		t.Errorf("an ignored file showed in the diff:\n%s", diff)
	}
	if staged := mustGit(t, p, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("reading the diff staged %q", staged)
	}
	if st := mustGit(t, p, "status", "--porcelain", "--", "hello.go"); !strings.HasPrefix(st, "??") {
		t.Errorf("hello.go is %q after the diff, want still untracked", st)
	}

	head := mustGit(t, p, "rev-parse", "HEAD")
	since, err := m.DiffSince(ctx, f, head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(since, "+package hello") {
		t.Errorf("DiffSince is missing the new file:\n%s", since)
	}
}
