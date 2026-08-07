package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// commentsJSON renders the `gh issue view --json comments` output for a
// list of (login, createdAt, body) comment tuples.
func commentsJSON(comments [][3]string) string {
	entries := make([]string, len(comments))
	for i, c := range comments {
		entries[i] = fmt.Sprintf(`{"author":{"login":%q},"createdAt":%q,"body":%q}`, c[0], c[1], c[2])
	}
	return `{"comments":[` + strings.Join(entries, ",") + `]}`
}

const issueWithHeadings = `[{"number":42,"title":"Login loops","body":"Some prose.\n\n## Steps to reproduce\n1. log in\n2. click\n## Expected behavior\ndashboard\n## Actual behavior\nbounce back","url":"https://github.com/o/r/issues/42","state":"open","labels":[],"author":{"login":"a"}}]`

// fakeGHSeq returns a run func that serves different canned JSON per gh
// subcommand: "list" yields issues, "view" yields comments, and records
// every invocation's args.
func fakeGHSeq(t *testing.T, issuesJSON, commentsJSON string, gotArgs *[][]string) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if gotArgs != nil {
			*gotArgs = append(*gotArgs, args)
		}
		switch args[1] {
		case "view":
			return []byte(commentsJSON), nil
		default:
			return []byte(issuesJSON), nil
		}
	}
}

func TestGitHubSourceFetch_WithComments(t *testing.T) {
	const body42 = "first comment on #42"
	const body7 = "second comment"
	const twoHeadingIssues = `[
  {"number":42,"title":"Login loops","body":"Some prose.\n\n## Steps to reproduce\n1. log in\n2. click\n## Expected behavior\ndashboard\n## Actual behavior\nbounce back","url":"https://github.com/o/r/issues/42","state":"open","labels":[],"author":{"login":"a"}},
  {"number":7,"title":"Crash on nil","body":"## Environment\nLinux","url":"https://github.com/o/r/issues/7","state":"open","labels":[],"author":{"login":"b"}}
]`
	var calls [][]string
	src := GitHubSource{
		Repo: "o/r", FetchComments: true,
		run: fakeGHSeq(t, twoHeadingIssues, commentsJSON([][3]string{
			{"alice", "2024-01-01T10:00:00Z", body42},
			{"bob", "2024-01-02T10:00:00Z", body7},
			{"carol", "2024-01-03T10:00:00Z", "third"},
			{"dave", "2024-01-04T10:00:00Z", "fourth"},
			{"erin", "2024-01-05T10:00:00Z", "fifth"},
		}), &calls),
	}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("got %d proposals, want 2", len(props))
	}
	r := props[0].Report
	if r.Description == "" || !strings.Contains(r.Description, "Some prose.") {
		t.Errorf("body parsing should still run with comments on; got Description %q", r.Description)
	}
	if r.Reproduction != "1. log in\n2. click" {
		t.Errorf("Reproduction = %q", r.Reproduction)
	}
	want := "**alice:** " + body42 + "\n\n**bob:** " + body7 + "\n\n**carol:** third\n\n**dave:** fourth\n\n**erin:** fifth"
	if r.Discussion != want {
		t.Errorf("Discussion = %q\nwant %q", r.Discussion, want)
	}
	// each issue fetched its comments after the batch list, with the repo
	// carried through to `gh issue view --repo`.
	var viewCalls int
	for _, args := range calls {
		if args[1] == "view" {
			viewCalls++
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--repo o/r") || !strings.Contains(joined, "--json comments") {
				t.Errorf("view args missing repo/json: %v", args)
			}
		}
	}
	if viewCalls != 2 {
		t.Errorf("expected 2 gh issue view calls, got %d\ncalls: %v", viewCalls, calls)
	}
}

func TestGitHubSourceFetch_CommentsCap(t *testing.T) {
	comments := make([][3]string, 8)
	for i := range comments {
		comments[i] = [3]string{fmt.Sprintf("u%d", i), fmt.Sprintf("2024-01-01T%02d:00:00Z", i+1), fmt.Sprintf("comment %d", i)}
	}
	src := GitHubSource{FetchComments: true, run: fakeGHSeq(t, twoIssues, commentsJSON(comments), nil)}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := props[0].Report
	// first 5 by creation time, then the cap drops the rest.
	want := strings.Join([]string{
		"**u0:** comment 0",
		"**u1:** comment 1",
		"**u2:** comment 2",
		"**u3:** comment 3",
		"**u4:** comment 4",
	}, "\n\n")
	if r.Discussion != want {
		t.Errorf("Discussion = %q\nwant %q", r.Discussion, want)
	}
}

func TestGitHubSourceFetch_CommentTruncation(t *testing.T) {
	body := strings.Repeat("a", 3000)
	src := GitHubSource{FetchComments: true, run: fakeGHSeq(t, twoIssues, commentsJSON([][3]string{
		{"alice", "2024-01-01T10:00:00Z", body},
	}), nil)}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := props[0].Report.Discussion
	want := "**alice:** " + strings.Repeat("a", 2000) + "… [truncated 1000 chars]"
	if got != want {
		t.Errorf("truncated comment mismatch\n got %d chars\nwant %d chars", len(got), len(want))
	}
}

func TestGitHubSourceFetch_CommentFetchFailure(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[1] == "view" {
			return nil, errors.New("gh: not authenticated")
		}
		return []byte(issueWithHeadings), nil
	}
	src := GitHubSource{Repo: "o/r", FetchComments: true, run: run}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a failed comment fetch must not fail the import: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("got %d proposals, want 1", len(props))
	}
	r := props[0].Report
	if r.Reproduction != "1. log in\n2. click" {
		t.Errorf("body should still be parsed on comment failure, got Reproduction %q", r.Reproduction)
	}
	if r.Discussion != "" {
		t.Errorf("Discussion should be empty on comment fetch failure, got %q", r.Discussion)
	}
}

func TestGitHubSourceFetch_CommentsDisabled(t *testing.T) {
	var calls [][]string
	src := GitHubSource{FetchComments: false, run: fakeGHSeq(t, issueWithHeadings, "", &calls)}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 {
		t.Fatalf("got %d proposals, want 1", len(props))
	}
	r := props[0].Report
	if r.Discussion != "" {
		t.Errorf("Discussion should be empty when comments are disabled, got %q", r.Discussion)
	}
	if r.Reproduction != "1. log in\n2. click" {
		t.Errorf("body should still be heading-parsed with comments off, got %q", r.Reproduction)
	}
	// the only gh call is the batch list — no per-issue view calls.
	if len(calls) != 1 || calls[0][0] != "issue" || calls[0][1] != "list" {
		t.Errorf("expected a single `gh issue list` call, got %v", calls)
	}
}
