package pr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
)

// fakeGraphQL writes an executable shell shim that ignores its args and
// prints out verbatim to stdout for any "api graphql" invocation — the
// no-network, no-real-gh seam the other internal/pr tests use, standing in
// for what a real `gh api graphql --paginate` call would have streamed back
// (one JSON document per page, concatenated).
func fakeGraphQL(t *testing.T, out string) string {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.json")
	if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"api graphql\"*) cat \"" + outFile + "\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

const singlePageFixture = `{"data":{"repository":{"pullRequest":{
 "reviewThreads":{"nodes":[
   {"id":"T1","path":"foo.go","isResolved":false,"isOutdated":false,"comments":{"nodes":[
     {"id":"C1","author":{"login":"alice"},"body":"please fix","diffHunk":"@@ -1,3 +1,3 @@\n ctx\n-old\n+new"},
     {"id":"C2","author":{"login":"bob"},"body":"done","diffHunk":"@@ -1,3 +1,3 @@\n ctx\n-old\n+different"}
   ]}},
   {"id":"T2","path":"bar.go","isResolved":true,"isOutdated":false,"comments":{"nodes":[
     {"id":"C3","author":{"login":"carol"},"body":"resolved thread","diffHunk":"@@ -1,1 +1,1 @@\n+x"}
   ]}},
   {"id":"T3","path":"baz.go","isResolved":false,"isOutdated":true,"comments":{"nodes":[
     {"id":"C4","author":{"login":"dave"},"body":"outdated one","diffHunk":"@@ -1,1 +1,1 @@\n+y"}
   ]}}
 ],"pageInfo":{"hasNextPage":false,"endCursor":""}},
 "comments":{"nodes":[{"author":{"login":"erin"},"body":"top level comment"}]},
 "reviews":{"nodes":[
   {"author":{"login":"frank"},"body":"LGTM overall","state":"APPROVED"},
   {"author":{"login":"grace"},"body":"","state":"COMMENTED"}
 ]}
}}}}`

// TestFetchReviewThreadsSinglePage covers the resolved filter (T2 dropped),
// the outdated flag surviving (T3 kept with IsOutdated), DiffHunk coming
// from the root comment only (T1.DiffHunk is C1's, not C2's differing
// per-comment diffHunk), and top-level folding (erin's comment and frank's
// non-empty review body, not grace's empty one).
func TestFetchReviewThreadsSinglePage(t *testing.T) {
	bin := fakeGraphQL(t, singlePageFixture)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 1, URL: "https://github.com/o/r/pull/1"}
	threads, topLevel, err := FetchReviewThreads(context.Background(), bin, ref)
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %d, want 2 (resolved T2 dropped)", len(threads))
	}
	if threads[0].Id != "T1" || threads[0].DiffHunk != "@@ -1,3 +1,3 @@\n ctx\n-old\n+new" {
		t.Errorf("T1 = %+v, want DiffHunk from root comment C1", threads[0])
	}
	if threads[1].Id != "T3" || !threads[1].IsOutdated {
		t.Errorf("T3 = %+v, want IsOutdated=true and present", threads[1])
	}
	if len(topLevel) != 2 || topLevel[0].AuthorLogin != "erin" || topLevel[1].AuthorLogin != "frank" {
		t.Fatalf("topLevel = %+v, want [erin's comment, frank's review] (grace's empty-body review dropped)", topLevel)
	}
}

const twoPageFixtureA = `{"data":{"repository":{"pullRequest":{
 "reviewThreads":{"nodes":[{"id":"TA","path":"a.go","isResolved":false,"isOutdated":false,"comments":{"nodes":[
   {"id":"CA","author":{"login":"a"},"body":"a","diffHunk":"@@ -1,1 +1,1 @@\n+a"}
 ]}}],"pageInfo":{"hasNextPage":true,"endCursor":"CUR1"}},
 "comments":{"nodes":[{"author":{"login":"top"},"body":"top comment"}]},
 "reviews":{"nodes":[]}
}}}}`

const twoPageFixtureB = `{"data":{"repository":{"pullRequest":{
 "reviewThreads":{"nodes":[{"id":"TB","path":"b.go","isResolved":false,"isOutdated":false,"comments":{"nodes":[
   {"id":"CB","author":{"login":"b"},"body":"b","diffHunk":"@@ -1,1 +1,1 @@\n+b"}
 ]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}},
 "comments":{"nodes":[{"author":{"login":"top"},"body":"top comment"}]},
 "reviews":{"nodes":[]}
}}}}`

// TestFetchReviewThreadsFoldsAcrossPages asserts reviewThreads accumulate
// across both decoded pages while TopLevelComment folds from the first page
// only — page B repeats the same top-level comment verbatim (as a real
// --paginate response would, since comments/reviews are non-paginated
// siblings of reviewThreads), and that must not double the count.
func TestFetchReviewThreadsFoldsAcrossPages(t *testing.T) {
	bin := fakeGraphQL(t, twoPageFixtureA+"\n"+twoPageFixtureB)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 1, URL: "https://github.com/o/r/pull/1"}
	threads, topLevel, err := FetchReviewThreads(context.Background(), bin, ref)
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if len(threads) != 2 || threads[0].Id != "TA" || threads[1].Id != "TB" {
		t.Fatalf("threads = %+v, want [TA, TB] accumulated across both pages", threads)
	}
	if len(topLevel) != 1 {
		t.Fatalf("topLevel = %+v, want exactly 1 (first-page-only fold, not doubled)", topLevel)
	}
}

func TestFormatBody(t *testing.T) {
	cases := []struct {
		name string
		t    ReviewThread
		want string
	}{
		{
			name: "single comment",
			t:    ReviewThread{Comments: []ThreadComment{{AuthorLogin: "alice", Body: "please fix"}}},
			want: "@alice: please fix",
		},
		{
			name: "root plus reply",
			t: ReviewThread{Comments: []ThreadComment{
				{AuthorLogin: "alice", Body: "please fix"},
				{AuthorLogin: "bob", Body: "done"},
			}},
			want: "@alice: please fix\n\n@bob: done",
		},
		{
			name: "outdated",
			t:    ReviewThread{IsOutdated: true, Comments: []ThreadComment{{AuthorLogin: "alice", Body: "please fix"}}},
			want: "[outdated]\n\n@alice: please fix",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatBody(c.t); got != c.want {
				t.Errorf("formatBody = %q, want %q", got, c.want)
			}
		})
	}
}

// diffFixtureWithContext builds a worktree-diff fixture for path with
// before/after context lines around a target "-old"/"+new" pair, returning
// the lines and the target ("+new") line's index.
func diffFixtureWithContext(path string) (lines []string, targetIdx int) {
	lines = []string{
		"diff --git a/" + path + " b/" + path,
		"index 111..222 100644",
		"--- a/" + path,
		"+++ b/" + path,
		"@@ -1,9 +1,9 @@",
		" ctx1",
		" ctx2",
		" ctx3",
		"-old",
		"+new",
		" after1",
		" after2",
		" after3",
	}
	return lines, 9
}

func TestAnnotationForHitWithAfterContext(t *testing.T) {
	worktreeLines, targetIdx := diffFixtureWithContext("foo.go")
	th := ReviewThread{
		Id: "T1", Path: "foo.go",
		DiffHunk: "@@ -1,4 +1,4 @@\n ctx2\n ctx3\n-old\n+new",
		Comments: []ThreadComment{{AuthorLogin: "alice", Body: "fix this"}},
	}
	ann := AnnotationFor("FD-001", th, worktreeLines)
	if ann.Anchor == "" {
		t.Fatalf("AnnotationFor got empty Anchor, want a hit at %d", targetIdx)
	}
	if want := diffannot.Anchor(worktreeLines, targetIdx); ann.Anchor != want {
		t.Errorf("Anchor = %q, want %q (diffannot.Anchor at targetIdx)", ann.Anchor, want)
	}
	if idx := diffannot.Locate(worktreeLines, ann.Anchor); idx != targetIdx {
		t.Errorf("Locate(worktreeLines, ann.Anchor) = %d, want %d", idx, targetIdx)
	}
	if ann.SourceRef != "T1" {
		t.Errorf("SourceRef = %q, want T1", ann.SourceRef)
	}
}

// TestAnnotationForShortHunkStripsHeader guards locateHunkTail's
// hunkLines[1:] split: a hunk whose header + payload is exactly 4 lines
// (the tail window size) must not let the header participate in matching.
func TestAnnotationForShortHunkStripsHeader(t *testing.T) {
	worktreeLines := []string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -10,3 +10,3 @@",
		" ctx1",
		"-old",
		"+new",
		" after1",
		" after2",
		" after3",
	}
	targetIdx := 6
	th := ReviewThread{
		Id: "T2", Path: "foo.go",
		DiffHunk: "@@ -10,3 +10,3 @@\n ctx1\n-old\n+new",
	}
	ann := AnnotationFor("FD-001", th, worktreeLines)
	if ann.Anchor == "" {
		t.Fatal("AnnotationFor got empty Anchor, want a hit (header must not participate in matching)")
	}
	if idx := diffannot.Locate(worktreeLines, ann.Anchor); idx != targetIdx {
		t.Errorf("Locate = %d, want %d", idx, targetIdx)
	}
}

func TestAnnotationForOrphanWhenTargetAbsent(t *testing.T) {
	worktreeLines, _ := diffFixtureWithContext("foo.go")
	// delete the target "+new" line so the hunk's tail no longer appears.
	worktreeLines = append(worktreeLines[:9], worktreeLines[10:]...)
	th := ReviewThread{
		Id: "T3", Path: "foo.go",
		DiffHunk: "@@ -1,4 +1,4 @@\n ctx2\n ctx3\n-old\n+new",
	}
	ann := AnnotationFor("FD-001", th, worktreeLines)
	if ann.Anchor != "" {
		t.Errorf("Anchor = %q, want \"\" (target line absent from worktree)", ann.Anchor)
	}
	if idx := diffannot.Locate(worktreeLines, ann.Anchor); idx != -1 {
		t.Errorf("Locate = %d, want -1", idx)
	}
}

func TestAnnotationForOrphanOnAmbiguity(t *testing.T) {
	worktreeLines := []string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,4 +1,4 @@",
		" ctx2",
		" ctx3",
		"-old",
		"+new",
		" after1",
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -20,4 +20,4 @@",
		" ctx2",
		" ctx3",
		"-old",
		"+new",
		" after1",
	}
	th := ReviewThread{
		Id: "T4", Path: "foo.go",
		DiffHunk: "@@ -1,4 +1,4 @@\n ctx2\n ctx3\n-old\n+new",
	}
	ann := AnnotationFor("FD-001", th, worktreeLines)
	if ann.Anchor != "" {
		t.Errorf("Anchor = %q, want \"\" (two matching sections should demote to orphan)", ann.Anchor)
	}
}

func TestAnnotationForOrphanOnEmptyPayload(t *testing.T) {
	worktreeLines, _ := diffFixtureWithContext("foo.go")
	th := ReviewThread{Id: "T5", Path: "foo.go", DiffHunk: "@@ -1,0 +1,0 @@"}
	ann := AnnotationFor("FD-001", th, worktreeLines)
	if ann.Anchor != "" {
		t.Errorf("Anchor = %q, want \"\" (header-only diffHunk has no payload)", ann.Anchor)
	}
}
