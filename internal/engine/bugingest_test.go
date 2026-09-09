package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
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
	if p.Number != 42 {
		t.Errorf("proposal[0].Number = %d, want 42", p.Number)
	}
	if props[1].Number != 7 {
		t.Errorf("proposal[1].Number = %d, want 7", props[1].Number)
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
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
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
	if first.Proposals[0].Number != 42 || first.Proposals[1].Number != 7 {
		t.Errorf("IngestBugs should pass Number through untouched: %+v", first.Proposals)
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
	for _, s := range second.Skipped {
		if s.LocalID == "" {
			t.Errorf("skipped bug %q has empty LocalID", s.Proposal.Title)
		}
	}
}

func TestMaterializeBugsCreatesSeededBugs(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	props := []domain.BugProposal{{
		Title: "Login loops", Source: "github", ExternalRef: "https://x/42",
		Severity: domain.SeverityHigh,
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
	if b.Stage != domain.StageTodo || b.Profile != "thrifty" || b.Budget.Envelope != 150 || b.ExternalRef != "https://x/42" {
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

// multiRepoEngine builds an engine over a pool whose default repo is the
// workspace root plus a nested named repo "b", so bug ingestion can target a
// non-default repository.
func multiRepoEngine(t *testing.T) *Engine {
	t.Helper()
	wsRoot := t.TempDir()
	wsRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	git := func(root string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	init := func(root string) {
		git(root, "init", "-q", "-b", "main")
		git(root, "config", "user.name", "t")
		git(root, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(root, "add", ".")
		git(root, "commit", "-q", "-m", "init")
	}
	init(wsRoot)
	repoB := filepath.Join(wsRoot, "git", "b")
	if err := os.MkdirAll(repoB, 0o750); err != nil {
		t.Fatal(err)
	}
	init(repoB)

	ws, err := state.Init(wsRoot, wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), ws.Root, ws.Root,
		[]worktree.NamedRepo{{Name: "b", Root: repoB}}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Pool: pool, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e
}

// TestMaterializeBugsNamesRepo: bugs minted with a --repo target persist the
// repo name on the feature; an unknown repo fails the whole batch before any
// bug is created.
func TestMaterializeBugsNamesRepo(t *testing.T) {
	e := multiRepoEngine(t)
	ctx := context.Background()
	props := []domain.BugProposal{{
		Title: "Crash on nil", Source: "manual", ExternalRef: "https://x/7",
		Severity: domain.SeverityHigh,
		Report:   domain.BugReport{Description: "panic"},
	}}

	created, err := e.MaterializeBugs(ctx, props, MaterializeOpts{Repo: "b"})
	if err != nil {
		t.Fatalf("MaterializeBugs: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created %d, want 1", len(created))
	}
	if created[0].Repo != "b" {
		t.Errorf("created repo = %q, want b", created[0].Repo)
	}
	got, err := e.cfg.Store.GetFeature(ctx, created[0].ID)
	if err != nil || got.Repo != "b" {
		t.Errorf("persisted repo = %q (err=%v), want b", got.Repo, err)
	}

	// an unconfigured repo fails the batch before anything is minted.
	if _, err := e.MaterializeBugs(ctx, props, MaterializeOpts{Repo: "nope"}); err == nil {
		t.Fatal("expected an error materializing into an unconfigured repo")
	}
}

// reposOnlyEngine builds an engine over a pool with no default repo: a
// repos:-only workspace whose root is NOT a git repository (the natural
// multi-repo parent layout). Card creation must target a named repo.
func reposOnlyEngine(t *testing.T) *Engine {
	t.Helper()
	wsRoot := t.TempDir()
	wsRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	git := func(root string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	init := func(root string) {
		git(root, "init", "-q", "-b", "main")
		git(root, "config", "user.name", "t")
		git(root, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(root, "add", ".")
		git(root, "commit", "-q", "-m", "init")
	}
	repoA := filepath.Join(wsRoot, "git", "a")
	repoB := filepath.Join(wsRoot, "git", "b")
	if err := os.MkdirAll(repoA, 0o750); err != nil {
		t.Fatal(err)
	}
	init(repoA)
	if err := os.MkdirAll(repoB, 0o750); err != nil {
		t.Fatal(err)
	}
	init(repoB)

	// wsRoot itself is deliberately left a non-git parent.
	ws, err := state.Init(wsRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), ws.Root, "",
		[]worktree.NamedRepo{{Name: "a", Root: repoA}, {Name: "b", Root: repoB}}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Pool: pool, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e
}

// TestMaterializeBugsReposOnlyNamedRepo: in a repos:-only workspace (no
// default, non-git root) `bugs new --repo a` succeeds and persists the card
// in repo a, without ever resolving a default. This is the bug's call site:
// the eagerly-resolved default used to poison the command at pool build.
func TestMaterializeBugsReposOnlyNamedRepo(t *testing.T) {
	e := reposOnlyEngine(t)
	ctx := context.Background()
	props := []domain.BugProposal{{
		Title: "Multi-repo crash", Source: "manual", ExternalRef: "https://x/9",
		Severity: domain.SeverityHigh,
		Report:   domain.BugReport{Description: "explodes"},
	}}

	created, err := e.MaterializeBugs(ctx, props, MaterializeOpts{Repo: "a"})
	if err != nil {
		t.Fatalf("MaterializeBugs with a named repo in a repos:-only workspace: %v", err)
	}
	if len(created) != 1 || created[0].Repo != "a" {
		t.Fatalf("created = %+v, want one bug in repo a", created)
	}
	if got, err := e.cfg.Store.GetFeature(ctx, created[0].ID); err != nil || got.Repo != "a" {
		t.Errorf("persisted repo = %q (err=%v), want a", got.Repo, err)
	}

	// materializing with no repo fails at the point the default is needed.
	if _, err := e.MaterializeBugs(ctx, props, MaterializeOpts{}); err == nil {
		t.Fatal("expected a no-default error materializing without --repo")
	} else if !strings.Contains(err.Error(), "no default repository configured") {
		t.Errorf("unexpected no-default error: %v", err)
	}
}

// TestMaterializeBugsStoresAGateMode: a bug minted through the ingest
// path must store its gate mode, not leave the field empty. This is the
// mint half of the empty-gate defect — MaterializeBugs' domain.Feature
// literal carried no GateApproval at all, so every bug `bugs new` and the
// GitHub import created was persisted with the empty string, and any
// reader comparing that string rather than resolving it (lanePoolFor,
// StopForQuit) read the card as autopilot work. cardmint.Mint resolves
// empty to the same value, so both mint paths now write identical rows.
func TestMaterializeBugsStoresAGateMode(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	props := []domain.BugProposal{{Title: "Login loops", Report: domain.BugReport{Description: "SSO bounce"}}}
	created, err := e.MaterializeBugs(ctx, props, MaterializeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created %d, want 1", len(created))
	}
	if created[0].GateApproval != domain.GateAttended {
		t.Errorf("minted bug GateApproval = %q, want %q", created[0].GateApproval, domain.GateAttended)
	}
	// and it survives the round trip through the store, which is where
	// every later reader picks it up.
	got, err := store.GetFeature(ctx, created[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAttended {
		t.Errorf("stored GateApproval = %q, want %q", got.GateApproval, domain.GateAttended)
	}
	if lanePoolFor(got) != poolAttended {
		t.Error("an ingested bug must compete in the attended pool, not autopilot")
	}
}

// TestGitHubSourceFetchIssue: a single issue comes back with its body
// split, its severity read from the labels, its state and label names
// carried for display, and its comments joined into Discussion — all
// from one `gh issue view` call in the source's directory.
func TestGitHubSourceFetchIssue(t *testing.T) {
	const one = `{"number":42,"title":"Login loops","body":"SSO users bounce back\n## Steps to reproduce\n1. log in","url":"https://github.com/o/r/issues/42","state":"OPEN","labels":[{"name":"bug"},{"name":"P1"}],"author":{"login":"a"},"comments":[{"author":{"login":"c"},"body":"me too","createdAt":"2026-01-02T00:00:00Z"},{"author":{"login":"b"},"body":"same here","createdAt":"2026-01-01T00:00:00Z"}]}`
	var args []string
	src := GitHubSource{Repo: "o/r", run: fakeGH(t, one, &args)}
	p, err := src.FetchIssue(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "issue view 42 --repo o/r --json ") || !strings.Contains(joined, "comments") {
		t.Errorf("gh args = %v", args)
	}
	if p.Title != "Login loops" || p.Number != 42 || p.ExternalRef != "https://github.com/o/r/issues/42" {
		t.Errorf("proposal = %+v", p)
	}
	if p.Severity != domain.SeverityHigh {
		t.Errorf("severity from P1 label = %q", p.Severity)
	}
	if p.State != "open" || strings.Join(p.Labels, ",") != "bug,P1" {
		t.Errorf("state/labels = %q %v", p.State, p.Labels)
	}
	if p.Report.Reproduction != "1. log in" {
		t.Errorf("body not split: %+v", p.Report)
	}
	if p.Report.Discussion != "**b:** same here\n\n**c:** me too" {
		t.Errorf("discussion = %q", p.Report.Discussion)
	}

	// an issue with no title cannot be imported
	if _, err := (GitHubSource{run: fakeGH(t, `{"number":1,"title":"","url":"https://x/1"}`, nil)}).FetchIssue(context.Background(), 1); err == nil {
		t.Error("untitled issue imported")
	}
}
