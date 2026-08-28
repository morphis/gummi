package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

func TestRenderBugProposals(t *testing.T) {
	res := engine.BugIngestResult{
		Source: "github",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", OneLiner: "SSO bounce", Severity: domain.SeverityHigh, ExternalRef: "https://x/42"},
			{Title: "Typo in footer"},
		},
		Skipped: []engine.SkippedBug{{Proposal: domain.BugProposal{Title: "old one", ExternalRef: "https://x/1"}, LocalID: "BG-041"}},
	}
	var b strings.Builder
	renderBugProposals(&b, res)
	out := b.String()
	for _, want := range []string{
		"Proposed 2 bug(s)",
		"1. Login loops",
		"SSO bounce",
		"severity high",
		"https://x/42",
		"2. Typo in footer",
		"Skipped 1 already on the board:",
		"→ BG-041  old one",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
}

// selectIssueFixture builds the golden fixture from the spec: Proposals
// numbered {40, 41, 42} and one Skipped entry numbered 7.
func selectIssueFixture() engine.BugIngestResult {
	return engine.BugIngestResult{
		Proposals: []domain.BugProposal{
			{Title: "issue 40", Number: 40},
			{Title: "issue 41", Number: 41},
			{Title: "issue 42", Number: 42},
		},
		Skipped: []engine.SkippedBug{
			{Proposal: domain.BugProposal{Title: "issue 7", Number: 7}, LocalID: "BG-007"},
		},
	}
}

func TestSelectIssueFindsPresentIssue(t *testing.T) {
	res := selectIssueFixture()
	p, err := selectIssue(res, 42, "o/r", "bug", "open")
	if err != nil {
		t.Fatalf("selectIssue(42): %v", err)
	}
	if p.Title != "issue 42" {
		t.Errorf("selectIssue(42).Title = %q, want %q", p.Title, "issue 42")
	}
}

func TestSelectIssueReportsAlreadySkipped(t *testing.T) {
	res := selectIssueFixture()
	_, err := selectIssue(res, 7, "o/r", "bug", "open")
	if err == nil || !strings.Contains(err.Error(), "issue 7 already on the board as BG-007") {
		t.Errorf("selectIssue(7) error = %v, want mention of already on the board as BG-007", err)
	}
}

func TestSelectIssueReportsFiltersNamedNotFound(t *testing.T) {
	res := selectIssueFixture()
	_, err := selectIssue(res, 99, "o/r", "bug", "open")
	if err == nil || !strings.Contains(err.Error(), "issue 99 not in the fetched set: repo=o/r label=bug state=open") {
		t.Errorf("selectIssue(99) error = %v, want the filters-named not-found message", err)
	}
}

// fakeGHOnPath drops an executable named "gh" that prints json to stdout
// regardless of its arguments, and prepends its directory onto PATH, so
// runBugIngest's real `gh issue list` call resolves hermetically.
func fakeGHOnPath(t *testing.T, json string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'GHEOF'\n" + json + "\nGHEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// bugIngestRepoFixture builds a bare git repo with a `.gummi` workspace,
// chdirs the test into it (where openBugEnv expects to run), and returns
// the store so the test can inspect what runBugIngest actually created.
func bugIngestRepoFixture(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
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
	t.Chdir(root)
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestRunBugIngestIssueMaterializesExactlyOne proves --issue N materializes
// only that one bug out of a multi-issue fetched batch, leaving the rest
// uncreated — the CLI parity half of the single-select picker.
const twoGHIssues = `[
  {"number":42,"title":"Login loops","body":"SSO users bounce back","url":"https://github.com/o/r/issues/42","state":"open","labels":[{"name":"bug"}],"author":{"login":"a"}},
  {"number":7,"title":"Crash on nil","body":"panic","url":"https://github.com/o/r/issues/7","state":"open","labels":[{"name":"bug"}],"author":{"login":"b"}}
]`

func TestRunBugIngestIssueMaterializesExactlyOne(t *testing.T) {
	clearDoctorEnv(t)
	store := bugIngestRepoFixture(t)
	fakeGHOnPath(t, twoGHIssues)

	if err := runBugIngest([]string{"--issue", "42", "--yes"}); err != nil {
		t.Fatalf("runBugIngest --issue 42: %v", err)
	}

	created, err := store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var bugs []domain.Feature
	for _, f := range created {
		if f.Kind == domain.KindBug {
			bugs = append(bugs, f)
		}
	}
	if len(bugs) != 1 {
		t.Fatalf("bugs created = %d, want exactly 1 (rest of the fetched batch must stay uncreated): %+v", len(bugs), bugs)
	}
	if bugs[0].ExternalRef != "https://github.com/o/r/issues/42" {
		t.Errorf("materialized bug ExternalRef = %q, want issue 42's", bugs[0].ExternalRef)
	}
}

func TestBugIngest_CommentsFlag(t *testing.T) {
	withComments := ingestGitHubSource("o/r", "bug", "open", true, "/tmp/repo")
	if !withComments.FetchComments {
		t.Error("--comments=true should set FetchComments")
	}
	withoutComments := ingestGitHubSource("o/r", "bug", "open", false, "/tmp/repo")
	if withoutComments.FetchComments {
		t.Error("--comments absent should leave FetchComments false")
	}
	// the other fields are threaded through unchanged.
	if withComments.Repo != "o/r" || withComments.Label != "bug" || withComments.State != "open" || withComments.Dir != "/tmp/repo" {
		t.Errorf("source fields wrong: %+v", withComments)
	}
}
