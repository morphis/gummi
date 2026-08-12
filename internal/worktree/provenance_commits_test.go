package worktree

import (
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
