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
