package pr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// defaultReposJSON is what `gh api repos/<repo>` answers by default in the
// fake shim: every merge method allowed, so tests that don't care about
// RepoAllowsSquashMerge (Resolve/LiveStatus) are unaffected by its addition.
const defaultReposJSON = `{"allow_squash_merge":true,"allow_merge_commit":true,"allow_rebase_merge":true}`

// apiReposErrorSentinel, when written as reposOut, makes the shim's "api
// repos" case exit non-zero instead of printing a body — standing in for a
// failed or unauthorized settings read.
const apiReposErrorSentinel = "__api_repos_error__"

// fakeGH writes an executable shell shim to a temp dir that logs its argv
// (one space-joined line per invocation) to argvLog and prints viewOut for
// any "pr view" call or listOut for any "pr list" call, answering "api
// repos" calls with defaultReposJSON. No network, no real gh CLI — the
// deterministic offline seam the plan calls for.
func fakeGH(t *testing.T, viewOut, listOut string) (binPath, argvLog string) {
	t.Helper()
	return fakeGHWithRepoJSON(t, viewOut, listOut, defaultReposJSON)
}

// fakeGHWithRepoJSON is fakeGH plus a caller-chosen reposOut for "api repos"
// calls, letting a test force allow_squash_merge:false or (via
// apiReposErrorSentinel) a non-zero exit on that path.
func fakeGHWithRepoJSON(t *testing.T, viewOut, listOut, reposOut string) (binPath, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv.log")
	viewFile := filepath.Join(dir, "view.json")
	listFile := filepath.Join(dir, "list.json")
	reposFile := filepath.Join(dir, "repos.json")
	if err := os.WriteFile(viewFile, []byte(viewOut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listFile, []byte(listOut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reposFile, []byte(reposOut), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + argvLog + "\"\n" +
		"case \"$*\" in\n" +
		"  *\"api repos\"*) if [ \"$(cat \"" + reposFile + "\")\" = \"" + apiReposErrorSentinel + "\" ]; then echo \"repos settings unavailable\" >&2; exit 1; fi; cat \"" + reposFile + "\" ;;\n" +
		"  *\"pr view\"*) cat \"" + viewFile + "\" ;;\n" +
		"  *\"pr list\"*) cat \"" + listFile + "\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	binPath = filepath.Join(dir, "gh")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argvLog
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePullRefURL(t *testing.T) {
	repo, number, err := ParsePullRefURL("https://github.com/o/r/pull/42")
	if err != nil {
		t.Fatalf("ParsePullRefURL: %v", err)
	}
	if repo != "o/r" || number != 42 {
		t.Errorf("ParsePullRefURL = (%q, %d), want (\"o/r\", 42)", repo, number)
	}
	for _, bad := range []string{"", "https://example.com/o/r/pull/42", "https://github.com/o/r/issues/42", "not a url"} {
		if _, _, err := ParsePullRefURL(bad); err == nil {
			t.Errorf("ParsePullRefURL(%q) = nil error, want rejection", bad)
		}
	}
}

func TestAvailableMissingGH(t *testing.T) {
	t.Setenv("GH_TOKEN", "x")
	if err := Available(filepath.Join(t.TempDir(), "nonexistent-gh-binary")); err == nil {
		t.Fatal("Available with a missing binary should error")
	} else if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("error %q should name gh as the missing piece", err)
	}
}

// TestAvailableAcceptsGhAuthLoginUser stands in for the `gh auth login`
// state: gh is on PATH and authenticated, but neither GH_TOKEN nor
// GITHUB_TOKEN is set (gh keeps that credential in its own keyring/config,
// not the environment). Available must not reject this — auth is gh's own
// concern, surfaced via its own error on the first real call if it's
// genuinely missing.
func TestAvailableAcceptsGhAuthLoginUser(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nif [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then exit 0; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if err := Available(bin); err != nil {
		t.Fatalf("Available with gh authed via `gh auth login` (no env token) = %v, want nil", err)
	}
}

const viewJSON = `{"number":42,"url":"https://github.com/o/r/pull/42","headRefOid":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}`

func TestResolveURL(t *testing.T) {
	bin, log := fakeGH(t, viewJSON, "[]")
	ref, err := Resolve(context.Background(), bin, "https://github.com/o/r/pull/42", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if ref != want {
		t.Errorf("Resolve(url) = %+v, want %+v", ref, want)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr view 42") || !strings.Contains(argv, "--repo o/r") {
		t.Errorf("argv %q missing expected pr view + --repo o/r", argv)
	}
}

func TestResolveBareNumber(t *testing.T) {
	bin, log := fakeGH(t, viewJSON, "[]")
	ref, err := Resolve(context.Background(), bin, "42", t.TempDir(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Number != 42 || ref.Repo != "o/r" {
		t.Errorf("Resolve(number) = %+v, want number 42 repo o/r", ref)
	}
	argv := readLog(t, log)
	if strings.Contains(argv, "--repo") {
		t.Errorf("bare-number form must not pass --repo (relies on gh auto-detecting from dir): argv %q", argv)
	}
}

func TestResolveAuto(t *testing.T) {
	bin, log := fakeGH(t, "{}", "["+viewJSON+"]")
	ref, err := Resolve(context.Background(), bin, "", t.TempDir(), "gummi/FD-090-add-pr-ref")
	if err != nil {
		t.Fatalf("Resolve(--auto): %v", err)
	}
	if ref.Number != 42 {
		t.Errorf("Resolve(--auto) = %+v, want number 42", ref)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr list") || !strings.Contains(argv, "--head gummi/FD-090-add-pr-ref") {
		t.Errorf("argv %q missing expected pr list --head", argv)
	}
}

func TestResolveAutoRequiresExactlyOneMatch(t *testing.T) {
	bin, _ := fakeGH(t, "{}", "[]")
	if _, err := Resolve(context.Background(), bin, "", t.TempDir(), "some-branch"); err == nil {
		t.Fatal("--auto with zero matches should error")
	}
	bin2, _ := fakeGH(t, "{}", "["+viewJSON+","+viewJSON+"]")
	if _, err := Resolve(context.Background(), bin2, "", t.TempDir(), "some-branch"); err == nil {
		t.Fatal("--auto with multiple matches should error")
	}
}

func TestResolveRejectsGarbageSpec(t *testing.T) {
	// A garbage spec is rejected before gh is ever invoked, so no fake
	// binary or valid directory is needed.
	if _, err := Resolve(context.Background(), "/nonexistent/gh", "not-a-url-or-number", "/nonexistent/dir", ""); err == nil {
		t.Fatal("a spec that is neither URL nor number should error")
	}
}

// TestFetchReviewThreadsSplitsOwnerRepo guards the owner/repo derivation:
// domain.PullRequestRef.Repo is a single "owner/repo" string, but the
// GraphQL query takes owner and repo as two separately-named -F variables,
// so FetchReviewThreads must split it rather than passing it through
// combined (the way gh pr view/list's --repo flag does elsewhere in this
// package).
func TestFetchReviewThreadsSplitsOwnerRepo(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.json")
	argvLog := filepath.Join(dir, "argv.log")
	if err := os.WriteFile(outFile, []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},"comments":{"nodes":[]},"reviews":{"nodes":[]}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> \"" + argvLog + "\"\ncat \"" + outFile + "\"\n"
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	if _, _, _, err := FetchReviewThreads(context.Background(), bin, ref); err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	argv := readLog(t, argvLog)
	if !strings.Contains(argv, "owner=o") || !strings.Contains(argv, "repo=r") || strings.Contains(argv, "owner=o/r") {
		t.Errorf("argv %q missing split -F owner=o -F repo=r", argv)
	}
}

// TestFetchReviewThreadsRefusesMalformedRepo asserts a Repo not in
// "owner/repo" form is rejected before gh is ever invoked, rather than
// sending a half-empty GraphQL variable pair.
func TestFetchReviewThreadsRefusesMalformedRepo(t *testing.T) {
	ref := domain.PullRequestRef{Repo: "nosplit", Number: 1, URL: "https://github.com/nosplit/pull/1"}
	if _, _, _, err := FetchReviewThreads(context.Background(), "/nonexistent/gh", ref); err == nil {
		t.Fatal("malformed repo ref should be refused before gh is invoked")
	}
}

func TestRepoAllowsSquashMergeTrue(t *testing.T) {
	bin, log := fakeGH(t, "{}", "[]")
	ok, err := RepoAllowsSquashMerge(context.Background(), bin, "o/r")
	if err != nil {
		t.Fatalf("RepoAllowsSquashMerge: %v", err)
	}
	if !ok {
		t.Errorf("RepoAllowsSquashMerge = false, want true under default repo settings")
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "api repos/o/r") {
		t.Errorf("argv %q missing expected api repos/o/r", argv)
	}
}

func TestRepoAllowsSquashMergeFalse(t *testing.T) {
	bin, _ := fakeGHWithRepoJSON(t, "{}", "[]", `{"allow_squash_merge":false,"allow_merge_commit":true,"allow_rebase_merge":true}`)
	ok, err := RepoAllowsSquashMerge(context.Background(), bin, "o/r")
	if err != nil {
		t.Fatalf("RepoAllowsSquashMerge: %v", err)
	}
	if ok {
		t.Errorf("RepoAllowsSquashMerge = true, want false when the repo disallows squash merging")
	}
}

func TestRepoAllowsSquashMergeErrorPropagates(t *testing.T) {
	bin, _ := fakeGHWithRepoJSON(t, "{}", "[]", apiReposErrorSentinel)
	if _, err := RepoAllowsSquashMerge(context.Background(), bin, "o/r"); err == nil {
		t.Fatal("RepoAllowsSquashMerge with a failing api call should error")
	}
}

func TestLiveStatus(t *testing.T) {
	bin, log := fakeGH(t, `{"state":"OPEN","comments":[{},{},{}],"headRefOid":"cafebabecafebabecafebabecafebabecafebabe"}`, "[]")
	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	state, comments, headSHA, err := LiveStatus(context.Background(), bin, ref)
	if err != nil {
		t.Fatalf("LiveStatus: %v", err)
	}
	if state != "OPEN" || comments != 3 || headSHA != "cafebabecafebabecafebabecafebabecafebabe" {
		t.Errorf("LiveStatus = (%q, %d, %q), want (\"OPEN\", 3, \"cafebabecafebabecafebabecafebabecafebabe\")", state, comments, headSHA)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr view 42") || !strings.Contains(argv, "--repo o/r") {
		t.Errorf("argv %q missing expected pr view 42 --repo o/r", argv)
	}
}
