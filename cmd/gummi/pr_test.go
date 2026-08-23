package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// prFixture builds a temp repo with one todo card (FD-001), chdirs the test
// into the repo root where the runPR* commands expect to run, and returns
// the store so tests can assert the persisted ref directly.
func prFixture(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := domain.Feature{
		ID: "FD-001", Num: 1, Kind: domain.KindFeature, Title: "Add a thing", Slug: "add-a-thing",
		Stage: domain.StageTodo, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return store
}

// fakePRTestGH writes an executable shell shim that returns viewOut for any
// "pr view" invocation and listOut for any "pr list" invocation — the same
// no-network, no-real-gh seam internal/pr's own tests use, wired here via
// GUMMI_GH_CMD so the CLI layer is exercised end to end.
func fakePRTestGH(t *testing.T, viewOut, listOut string) string {
	t.Helper()
	dir := t.TempDir()
	viewFile := filepath.Join(dir, "view.json")
	listFile := filepath.Join(dir, "list.json")
	if err := os.WriteFile(viewFile, []byte(viewOut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listFile, []byte(listOut), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"pr view\"*) cat \"" + viewFile + "\" ;;\n" +
		"  *\"pr list\"*) cat \"" + listFile + "\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

const testViewJSON = `{"number":42,"url":"https://github.com/o/r/pull/42","headRefOid":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}`

func setFakeGHEnv(t *testing.T, bin string) {
	t.Helper()
	t.Setenv("GUMMI_GH_CMD", bin)
	t.Setenv("GH_TOKEN", "x")
	t.Setenv("GITHUB_TOKEN", "")
}

// pr link persists a resolved ref (round-tripped via a re-open of the
// store) and reports it; a second link on the same card refuses.
func TestPRLinkPersistsAndRefusesDoubleLink(t *testing.T) {
	store := prFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)

	out := captureStdout(t, func() {
		if err := runPRLink([]string{"FD-001", "https://github.com/o/r/pull/42"}); err != nil {
			t.Fatalf("runPRLink: %v", err)
		}
	})
	if !strings.Contains(out, "o/r#42") {
		t.Errorf("link output missing repo#number:\n%s", out)
	}

	got, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if got.PullRequest != want {
		t.Fatalf("persisted PullRequest = %+v, want %+v", got.PullRequest, want)
	}

	if err := runPRLink([]string{"FD-001", "https://github.com/o/r/pull/99"}); err == nil {
		t.Fatal("double-link should be refused")
	} else if !strings.Contains(err.Error(), "already linked") {
		t.Errorf("error = %q, want it to name the existing link", err)
	}
}

// pr link --auto resolves via the card's branch, with no positional spec.
func TestPRLinkAuto(t *testing.T) {
	store := prFixture(t)
	bin := fakePRTestGH(t, "{}", "["+testViewJSON+"]")
	setFakeGHEnv(t, bin)

	if err := runPRLink([]string{"FD-001", "--auto"}); err != nil {
		t.Fatalf("runPRLink --auto: %v", err)
	}
	got, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest.Number != 42 {
		t.Fatalf("PullRequest after --auto link = %+v, want number 42", got.PullRequest)
	}
}

// pr unlink clears a linked ref and refuses when nothing is linked.
func TestPRUnlinkClearsAndRefusesWhenUnlinked(t *testing.T) {
	store := prFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)

	if err := runPRLink([]string{"FD-001", "42"}); err != nil {
		t.Fatalf("runPRLink: %v", err)
	}
	if err := runPRUnlink([]string{"FD-001"}); err != nil {
		t.Fatalf("runPRUnlink: %v", err)
	}
	got, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if !got.PullRequest.Empty() {
		t.Fatalf("PullRequest after unlink = %+v, want Empty()", got.PullRequest)
	}
	if err := runPRUnlink([]string{"FD-001"}); err == nil {
		t.Fatal("unlink on an already-unlinked card should refuse")
	}
}

// pr status renders both the text and --json forms from a live query.
func TestPRStatusRendersTextAndJSON(t *testing.T) {
	prFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)

	if err := runPRLink([]string{"FD-001", "https://github.com/o/r/pull/42"}); err != nil {
		t.Fatalf("runPRLink: %v", err)
	}

	// re-point gh at a shim whose "pr view" returns live status fields.
	bin2 := fakePRTestGH(t, `{"state":"OPEN","comments":[{},{}]}`, "[]")
	t.Setenv("GUMMI_GH_CMD", bin2)

	out := captureStdout(t, func() {
		if err := runPRStatus([]string{"FD-001"}); err != nil {
			t.Fatalf("runPRStatus: %v", err)
		}
	})
	if !strings.Contains(out, "open") || !strings.Contains(out, "2") {
		t.Errorf("status text output missing state/comment count:\n%s", out)
	}

	jsonOut := captureStdout(t, func() {
		if err := runPRStatus([]string{"FD-001", "--json"}); err != nil {
			t.Fatalf("runPRStatus --json: %v", err)
		}
	})
	if !strings.Contains(jsonOut, `"state": "OPEN"`) || !strings.Contains(jsonOut, `"comments": 2`) {
		t.Errorf("status --json output missing expected fields:\n%s", jsonOut)
	}
}

// Each verb fails deterministically, naming exactly what is absent, when gh
// itself is missing — never silently. Authentication is gh's own concern
// (GH_TOKEN, GITHUB_TOKEN, or a `gh auth login` config), so a verb run with
// gh present but no token env vars set must not be gated here.
func TestPRVerbsFailWithoutGH(t *testing.T) {
	prFixture(t)
	t.Setenv("GUMMI_GH_CMD", filepath.Join(t.TempDir(), "nonexistent-gh"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if err := runPRLink([]string{"FD-001", "42"}); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("pr link with missing gh = %v, want an error naming gh", err)
	}
	if err := runPRStatus([]string{"FD-001"}); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("pr status with missing gh = %v, want an error naming gh", err)
	}
	if err := runPRUnlink([]string{"FD-001"}); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("pr unlink with missing gh = %v, want an error naming gh", err)
	}

	bin := fakePRTestGH(t, testViewJSON, "[]")
	t.Setenv("GUMMI_GH_CMD", bin)
	if err := runPRLink([]string{"FD-001", "42"}); err != nil {
		t.Errorf("pr link with gh present but no token env vars = %v, want nil (auth is gh's concern)", err)
	}
}

// pr comments on an unlinked card refuses, naming `pr link` (the refusal
// message deliberately diverges from pr unlink/status's, which name no
// remedy).
func TestPRCommentsRefusesUnlinkedCard(t *testing.T) {
	prFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)

	err := runPRComments([]string{"FD-001"})
	if err == nil {
		t.Fatal("pr comments on an unlinked card should refuse")
	}
	if !strings.Contains(err.Error(), "pr link") {
		t.Errorf("error = %q, want it to name `pr link`", err)
	}
}

// fakePRCommentsGH extends fakePRTestGH's "pr view"/"pr list" shim with an
// "api graphql" case returning graphqlOut verbatim — standing in for what a
// real `gh api graphql --paginate` call streams back.
func fakePRCommentsGH(t *testing.T, viewOut, graphqlOut string) string {
	t.Helper()
	dir := t.TempDir()
	viewFile := filepath.Join(dir, "view.json")
	graphqlFile := filepath.Join(dir, "graphql.json")
	if err := os.WriteFile(viewFile, []byte(viewOut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphqlFile, []byte(graphqlOut), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"api graphql\"*) cat \"" + graphqlFile + "\" ;;\n" +
		"  *\"pr view\"*) cat \"" + viewFile + "\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// prCommentsFixture builds on prFixture with a real feature worktree
// carrying one committed file (foo.go, 7 lines, added whole so every line
// is "+"-marked in the diff — the shape wt.Diff will actually produce), and
// returns the store and feature so ingest tests can assert against both the
// persisted rows and the worktree diff foo.go anchors into.
func prCommentsFixture(t *testing.T) (*state.Store, domain.Feature) {
	t.Helper()
	store := prFixture(t)
	ctx := context.Background()
	f, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wt, err := worktree.NewManager(ctx, root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	p, err := wt.Create(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	content := "ctx1\nctx2\nctx3\nnew\nafter1\nafter2\nafter3\n"
	if err := os.WriteFile(filepath.Join(p, "foo.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, p, "add", ".")
	cliGit(t, p, "commit", "-q", "-m", "add foo.go")
	return store, f
}

// reviewThreadsFixture carries T1 (unresolved, anchors cleanly to foo.go's
// "new" line, root+reply) and T2 (unresolved, outdated, whose diffHunk
// targets text absent from the worktree diff — an orphan), plus one
// top-level PR-body comment. A resolved thread is included to prove it
// never reaches list/ingest output.
const reviewThreadsFixture = `{"data":{"repository":{"pullRequest":{
 "reviewThreads":{"nodes":[
   {"id":"T1","path":"foo.go","isResolved":false,"isOutdated":false,"comments":{"nodes":[
     {"id":"C1","author":{"login":"alice"},"body":"please fix","diffHunk":"@@ -0,0 +1,7 @@\n+ctx2\n+ctx3\n+new"},
     {"id":"C2","author":{"login":"bob"},"body":"still an issue","diffHunk":"@@ -0,0 +1,7 @@\n+ctx2\n+ctx3\n+new"}
   ]}},
   {"id":"T2","path":"foo.go","isResolved":false,"isOutdated":true,"comments":{"nodes":[
     {"id":"C3","author":{"login":"dave"},"body":"stale comment","diffHunk":"@@ -5,3 +5,3 @@\n+doesnotexist\n+neither\n+thisone"}
   ]}},
   {"id":"T3","path":"foo.go","isResolved":true,"isOutdated":false,"comments":{"nodes":[
     {"id":"C4","author":{"login":"carol"},"body":"already resolved","diffHunk":"@@ -0,0 +1,7 @@\n+ctx1\n+ctx2\n+ctx3"}
   ]}}
 ],"pageInfo":{"hasNextPage":false,"endCursor":""}},
 "comments":{"nodes":[{"author":{"login":"erin"},"body":"nice work overall"}]},
 "reviews":{"nodes":[]}
}}}}`

// pr comments (no --ingest) lists every unresolved thread with an outdated
// flag on T2, hides the resolved T3, and prints the top-level comment under
// its own heading. It writes nothing.
func TestPRCommentsListMode(t *testing.T) {
	store, f := prCommentsFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)
	if err := runPRLink([]string{"FD-001", "https://github.com/o/r/pull/42"}); err != nil {
		t.Fatalf("runPRLink: %v", err)
	}

	bin2 := fakePRCommentsGH(t, testViewJSON, reviewThreadsFixture)
	t.Setenv("GUMMI_GH_CMD", bin2)

	out := captureStdout(t, func() {
		if err := runPRComments([]string{"FD-001"}); err != nil {
			t.Fatalf("runPRComments: %v", err)
		}
	})
	if !strings.Contains(out, "foo.go — @alice: please fix") {
		t.Errorf("list output missing T1 line:\n%s", out)
	}
	if !strings.Contains(out, "[outdated]") {
		t.Errorf("list output missing [outdated] flag on T2:\n%s", out)
	}
	if strings.Contains(out, "already resolved") {
		t.Errorf("list output should hide the resolved thread T3:\n%s", out)
	}
	if !strings.Contains(out, "Top-level comments:") || !strings.Contains(out, "@erin: nice work overall") {
		t.Errorf("list output missing the top-level comments heading/body:\n%s", out)
	}

	anns, err := store.ListDiffAnnotations(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 0 {
		t.Fatalf("list mode wrote %d annotations, want 0", len(anns))
	}
}

// pr comments --ingest writes one annotation per unresolved thread (T1
// anchored, T2 orphaned since its target text never appears in the
// worktree diff), skips the top-level comment and the resolved thread, and
// a second immediate run is a no-op — proving both the summary's
// wrote/existing split and FD-094's (feature_id, source_ref) idempotency.
func TestPRCommentsIngestWritesAndIsIdempotent(t *testing.T) {
	store, f := prCommentsFixture(t)
	bin := fakePRTestGH(t, testViewJSON, "[]")
	setFakeGHEnv(t, bin)
	if err := runPRLink([]string{"FD-001", "https://github.com/o/r/pull/42"}); err != nil {
		t.Fatalf("runPRLink: %v", err)
	}

	bin2 := fakePRCommentsGH(t, testViewJSON, reviewThreadsFixture)
	t.Setenv("GUMMI_GH_CMD", bin2)

	firstOut := captureStdout(t, func() {
		if err := runPRComments([]string{"FD-001", "--ingest"}); err != nil {
			t.Fatalf("runPRComments --ingest (first run): %v", err)
		}
	})
	if !strings.Contains(firstOut, "wrote 2 (existing 0, top-level 1, orphaned 1)") {
		t.Errorf("first-run summary = %q, want \"wrote 2 (existing 0, top-level 1, orphaned 1)\"", firstOut)
	}

	anns, err := store.ListDiffAnnotations(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 2 {
		t.Fatalf("wrote %d annotations, want 2", len(anns))
	}
	sourceRefs := map[string]bool{}
	var hitAnn *domain.DiffAnnotation
	for i := range anns {
		sourceRefs[anns[i].SourceRef] = true
		if anns[i].SourceRef == "T1" {
			hitAnn = &anns[i]
		}
	}
	if !sourceRefs["T1"] || !sourceRefs["T2"] {
		t.Fatalf("source_ref set = %v, want {T1, T2}", sourceRefs)
	}
	if hitAnn == nil || hitAnn.Anchor == "" {
		t.Fatalf("T1's row = %+v, want a non-empty (located) Anchor", hitAnn)
	}

	secondOut := captureStdout(t, func() {
		if err := runPRComments([]string{"FD-001", "--ingest"}); err != nil {
			t.Fatalf("runPRComments --ingest (second run): %v", err)
		}
	})
	if !strings.Contains(secondOut, "wrote 0 (existing 2, top-level 1, orphaned 1)") {
		t.Errorf("second-run summary = %q, want \"wrote 0 (existing 2, top-level 1, orphaned 1)\"", secondOut)
	}
	anns2, err := store.ListDiffAnnotations(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns2) != 2 {
		t.Fatalf("second run left %d annotations, want still 2 (no duplicate rows)", len(anns2))
	}

	jsonOut := captureStdout(t, func() {
		if err := runPRComments([]string{"FD-001", "--ingest", "--json"}); err != nil {
			t.Fatalf("runPRComments --ingest --json: %v", err)
		}
	})
	for _, want := range []string{`"written": 0`, `"existing": 2`, `"top_level_skipped": 1`, `"orphaned": 1`} {
		if !strings.Contains(jsonOut, want) {
			t.Errorf("--json summary missing %q:\n%s", want, jsonOut)
		}
	}
}
