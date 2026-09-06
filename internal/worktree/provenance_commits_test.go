package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBranchCommits extracts the branch's own commits in git-log order,
// splitting hash from body on the \x1f separator.
func TestBranchCommits(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)

	writeFile(t, p, "one.txt", "1\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "first commit line\n\nbody of first")

	writeFile(t, p, "two.txt", "2\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "second commit line")

	commits, err := m.BranchCommits(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// committedFeature pre-seeds one "feature commit", then our two: three total.
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	// git-log order: newest first
	if !strings.Contains(commits[0].Body, "second commit line") {
		t.Errorf("commits[0] body = %q, want the second commit", commits[0].Body)
	}
	if !strings.Contains(commits[1].Body, "first commit line") {
		t.Errorf("commits[1] body = %q, want the first commit", commits[1].Body)
	}
	if !strings.Contains(commits[2].Body, "feature commit") {
		t.Errorf("commits[2] body = %q, want the feature commit", commits[2].Body)
	}
	for i, c := range commits {
		if c.Hash == "" {
			t.Errorf("commits[%d] hash empty", i)
		}
	}

	// the merge-base boundary: a branch with no commits of its own (the
	// base commit) must not appear.
	root2 := newRepo(t)
	m2 := newManager(t, root2)
	f2 := feature(11, "Empty branch")
	if _, err := m2.Create(ctx, f2); err != nil {
		t.Fatal(err)
	}
	empty, err := m2.BranchCommits(ctx, f2)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("branch with no commits returned %d, want 0", len(empty))
	}
}

// TestBranchDraftFeedCaps proves a long branch is truncated and the
// diffstat is bounded before it reaches the scribe.
func TestBranchDraftFeedCaps(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)
	for i := 0; i < draftCommitCap+20; i++ {
		name := filepath.Join(p, "f"+string(rune('a'+i%26))+"e"+string(rune('0'+i/26))+".txt")
		if err := os.WriteFile(name, []byte("content "+string(rune('a'+i))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, p, "add", ".")
		mustGit(t, p, "commit", "-q", "-m", "commit "+string(rune('a'+i)))
	}
	feed, err := m.BranchDraftFeed(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Commits) != draftCommitCap {
		t.Errorf("feed commits = %d, want cap %d", len(feed.Commits), draftCommitCap)
	}
	if feed.Diffstat == "" {
		t.Error("diffstat empty for a branched feature")
	}
	if len(feed.Diffstat) > draftDiffstatMax {
		t.Errorf("diffstat length %d exceeds max %d", len(feed.Diffstat), draftDiffstatMax)
	}
}

// TestBranchDraftFeedStyleSubjects proves the style examples are main's
// own landing subjects (never the branch's commits), newest-first, and
// count- and byte-capped, and that a sparse history stays safe.
func TestBranchDraftFeedStyleSubjects(t *testing.T) {
	commitMain := func(t *testing.T, root, name, subject string) {
		t.Helper()
		writeFile(t, root, name, name+"\n")
		mustGit(t, root, "add", ".")
		mustGit(t, root, "commit", "-q", "-m", subject)
	}

	t.Run("main-sourced", func(t *testing.T) {
		root := newRepo(t)
		m, f, _ := committedFeature(t, root)
		// main advances past the branch point; the branch's own "feature
		// commit" must never appear as a style example.
		commitMain(t, root, "m1.txt", "feat(engine,ui): main landing one")
		commitMain(t, root, "m2.txt", "fix(driver): main landing two")
		feed, err := m.BranchDraftFeed(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		want := "fix(driver): main landing two|feat(engine,ui): main landing one|initial"
		if got := strings.Join(feed.StyleSubjects, "|"); got != want {
			t.Errorf("StyleSubjects = %q, want %q (main's, newest first)", got, want)
		}
	})

	t.Run("count-cap", func(t *testing.T) {
		root := newRepo(t)
		m, f, _ := committedFeature(t, root)
		for i := 0; i < styleSubjectCap+5; i++ {
			commitMain(t, root, fmt.Sprintf("c%02d.txt", i), fmt.Sprintf("chore: land number %02d", i))
		}
		feed, err := m.BranchDraftFeed(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.StyleSubjects) != styleSubjectCap {
			t.Fatalf("StyleSubjects = %d subjects, want cap %d", len(feed.StyleSubjects), styleSubjectCap)
		}
		// newest first: the last commit made leads the list.
		if got := feed.StyleSubjects[0]; got != fmt.Sprintf("chore: land number %02d", styleSubjectCap+4) {
			t.Errorf("StyleSubjects[0] = %q, want the newest commit's subject", got)
		}
	})

	t.Run("byte-cap", func(t *testing.T) {
		root := newRepo(t)
		m, f, _ := committedFeature(t, root)
		long := "feat(engine,ui): landing number %02d with a fairly long rationale subject"
		for i := 0; i < styleSubjectCap+5; i++ {
			commitMain(t, root, fmt.Sprintf("c%02d.txt", i), fmt.Sprintf(long, i))
		}
		feed, err := m.BranchDraftFeed(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, s := range feed.StyleSubjects {
			total += len(s) + 1
		}
		if total > styleSubjectBytesMax {
			t.Errorf("style subjects total %d bytes, want <= %d", total, styleSubjectBytesMax)
		}
		if len(feed.StyleSubjects) >= styleSubjectCap+5 {
			t.Errorf("byte cap did not bite: %d subjects taken", len(feed.StyleSubjects))
		}
	})

	t.Run("sparse", func(t *testing.T) {
		root := newRepo(t)
		m := newManager(t, root)
		f := feature(3, "Fresh")
		if _, err := m.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		feed, err := m.BranchDraftFeed(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.StyleSubjects) != 1 || feed.StyleSubjects[0] != "initial" {
			t.Errorf("StyleSubjects = %v, want the repo's single seed subject", feed.StyleSubjects)
		}
	})
}
