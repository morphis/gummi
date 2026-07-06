package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// fakeGH returns a run func that serves canned issue JSON and records the
// args it was called with.
func fakeGH(t *testing.T, json string, gotArgs *[]string) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if gotArgs != nil {
			*gotArgs = args
		}
		return []byte(json), nil
	}
}

const twoIssues = `[
  {"number":42,"title":"Login loops","body":"SSO users bounce back","url":"https://github.com/o/r/issues/42","state":"open","labels":[{"name":"bug"},{"name":"P1"}],"author":{"login":"a"}},
  {"number":7,"title":"Crash on nil","body":"panic","url":"https://github.com/o/r/issues/7","state":"open","labels":[{"name":"bug"}],"author":{"login":"b"}}
]`

func TestGitHubSourceFetchMapsIssues(t *testing.T) {
	var args []string
	src := GitHubSource{Repo: "o/r", Label: "bug", run: fakeGH(t, twoIssues, &args)}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("got %d proposals, want 2", len(props))
	}
	p := props[0]
	if p.Title != "Login loops" || p.Source != "github" || p.ExternalRef != "https://github.com/o/r/issues/42" {
		t.Errorf("proposal[0] fields wrong: %+v", p)
	}
	if p.Severity != domain.SeverityHigh { // P1 → high
		t.Errorf("severity = %q, want high (from P1 label)", p.Severity)
	}
	if p.Report.Description != "SSO users bounce back" {
		t.Errorf("body should seed Description, got %q", p.Report.Description)
	}
	// the target repo and label reached gh.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--repo o/r") || !strings.Contains(joined, "--label bug") || !strings.Contains(joined, "--state open") {
		t.Errorf("gh args missing repo/label/state: %v", args)
	}
}

func TestGitHubSourceDropsUnusableIssues(t *testing.T) {
	json := `[{"number":1,"title":"","body":"x","url":"https://x/1"},{"number":2,"title":"ok","body":"y","url":""}]`
	src := GitHubSource{run: fakeGH(t, json, nil)}
	props, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 0 {
		t.Errorf("issues with no title or no url should be dropped, got %d", len(props))
	}
}

func TestIngestBugsDedupesAgainstBoard(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: agent.NewFake("x"), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	// import both issues once.
	src := GitHubSource{run: fakeGH(t, twoIssues, nil)}
	first, err := e.IngestBugs(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Proposals) != 2 || len(first.Skipped) != 0 {
		t.Fatalf("first ingest: %d fresh / %d skipped, want 2/0", len(first.Proposals), len(first.Skipped))
	}
	if _, err := e.MaterializeBugs(ctx, first.Proposals, MaterializeOpts{Profile: "thrifty"}); err != nil {
		t.Fatal(err)
	}

	// re-ingest the same source: both already on the board → all skipped.
	second, err := e.IngestBugs(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Proposals) != 0 || len(second.Skipped) != 2 {
		t.Errorf("re-ingest: %d fresh / %d skipped, want 0/2", len(second.Proposals), len(second.Skipped))
	}
}

func TestMaterializeBugsCreatesSeededBugs(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: agent.NewFake("x"), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	props := []domain.BugProposal{{
		Title: "Login loops", Source: "github", ExternalRef: "https://x/42",
		Severity: domain.SeverityHigh,
		Skip:     domain.SkipFlags{Triage: true},
		Report:   domain.BugReport{Description: "SSO bounce", Reproduction: "1. log in"},
	}}
	created, err := e.MaterializeBugs(ctx, props, MaterializeOpts{Profile: "thrifty", Envelope: 150})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created %d, want 1", len(created))
	}
	b := created[0]
	if b.Kind != domain.KindBug || !strings.HasPrefix(string(b.ID), "BG-") {
		t.Errorf("want a BG-* bug, got %s / kind %s", b.ID, b.Kind)
	}
	if b.Stage != domain.StageTodo || b.Profile != "thrifty" || b.Budget.Envelope != 150 || !b.Skip.Triage || b.ExternalRef != "https://x/42" {
		t.Errorf("bug fields wrong: %+v", b)
	}

	// the seeded bug report draft exists and carries the symptoms + severity.
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&b))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("no seeded draft: %v", err)
	}
	for _, want := range []string{"# " + string(b.ID) + ": Login loops", "SSO bounce", "1. log in", "Severity: high", "Reported via github"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("seeded report missing %q\n---\n%s", want, raw)
		}
	}
}

func TestManualSourceFetch(t *testing.T) {
	src := ManualSource{Bug: domain.BugProposal{Title: "Typo in footer", Report: domain.BugReport{Description: "says 2019"}}}
	props, err := src.Fetch(context.Background())
	if err != nil || len(props) != 1 {
		t.Fatalf("Fetch = %d props, %v", len(props), err)
	}
	if props[0].Source != "manual" {
		t.Errorf("manual source should stamp Source=manual, got %q", props[0].Source)
	}
	if _, err := (ManualSource{Bug: domain.BugProposal{Title: "  "}}).Fetch(context.Background()); err == nil {
		t.Error("a titleless manual bug should error")
	}
}
