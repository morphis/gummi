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

func sampleResult() domain.IngestResult {
	return domain.IngestResult{
		SourcePath: filepath.Join(".gummi", "ingest", "prd.md"),
		Proposals: []domain.FeatureProposal{
			{
				Title: "Payment webhooks", OneLiner: "receive callbacks",
				SourceRefs: []string{"Payments"},
				Draft:      domain.DraftSeed{Problem: "We miss async state.", Acceptance: "Signed event flips order.", OpenQuestions: []string{"which providers?"}},
			},
			{
				Title: "Webhook retries", OneLiner: "retry failed",
				DependsOn: []string{"Payment webhooks", "External thing"},
				Draft:     domain.DraftSeed{Problem: "Deliveries dropped."},
			},
		},
	}
}

func TestMaterializeCreatesFeaturesAndDrafts(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	created, err := e.Materialize(ctx, sampleResult(), MaterializeOpts{Profile: "thrifty", Envelope: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created %d features, want 2", len(created))
	}

	// features persisted into the todo backlog with the chosen profile,
	// envelope, and per-proposal skip flags.
	all, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("store has %d features, want 2", len(all))
	}
	f0 := created[0]
	if f0.Stage != domain.StageTodo || f0.Profile != "thrifty" || f0.Budget.Envelope != 200 {
		t.Errorf("feature[0] fields wrong: %+v", f0)
	}

	// seeded draft exists with provenance, seed content, resolved and
	// unresolved dependency labels, and the open question as a %% marker.
	d1 := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&created[0]))
	body0, err := os.ReadFile(d1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ingested from `.gummi/ingest/prd.md`", "We miss async state.", "- which providers?"} {
		if !strings.Contains(string(body0), want) {
			t.Errorf("draft[0] missing %q", want)
		}
	}

	body1, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&created[1])))
	if err != nil {
		t.Fatal(err)
	}
	// in-batch dependency resolves to the minted FD-ID; the external one
	// stays as its raw title.
	if !strings.Contains(string(body1), string(created[0].ID)+" payment-webhooks") {
		t.Errorf("draft[1] dependency not resolved to FD-ID:\n%s", body1)
	}
	if !strings.Contains(string(body1), "External thing") {
		t.Errorf("draft[1] should keep an unmatched dependency verbatim:\n%s", body1)
	}
}

// TestMaterializeWritesDependencyEdges: a depends_on title that names
// another proposal in the batch is persisted as a first-class feature_deps
// edge (what `deps list` and the coding-stage gate read), not only the
// draft's provenance prose; a title outside the batch stays prose only and
// adds no edge.
func TestMaterializeWritesDependencyEdges(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	created, err := e.Materialize(ctx, sampleResult(), MaterializeOpts{Profile: "thrifty", Envelope: 200})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	deps, err := store.ListDependencies(ctx, created[1].ID)
	if err != nil {
		t.Fatalf("ListDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0] != created[0].ID {
		t.Fatalf("ingest should persist the in-batch dependency edge; got %v, want [%s]", deps, created[0].ID)
	}
	if deps, _ := store.ListDependencies(ctx, created[0].ID); len(deps) != 0 {
		t.Errorf("feature[0] declares no dependencies, got %v", deps)
	}
}

// TestMaterializeWiresForwardDependency: a proposal may name a later
// proposal in the batch; the edge is wired once every ID is minted, so
// document order is no constraint.
func TestMaterializeWiresForwardDependency(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	res := domain.IngestResult{
		SourcePath: filepath.Join(".gummi", "ingest", "prd.md"),
		Proposals: []domain.FeatureProposal{
			{Title: "Webhook retries", OneLiner: "retry failed", DependsOn: []string{"Payment webhooks"}},
			{Title: "Payment webhooks", OneLiner: "receive callbacks"},
		},
	}
	ctx := context.Background()
	created, err := e.Materialize(ctx, res, MaterializeOpts{Profile: "thrifty", Envelope: 200})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	deps, err := store.ListDependencies(ctx, created[0].ID)
	if err != nil {
		t.Fatalf("ListDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0] != created[1].ID {
		t.Fatalf("forward reference should persist as an edge; got %v, want [%s]", deps, created[1].ID)
	}
}

func TestMaterializeRejectsUnslugifiableTitle(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	res := domain.IngestResult{Proposals: []domain.FeatureProposal{{Title: "!!!"}}}
	if _, err := e.Materialize(context.Background(), res, MaterializeOpts{}); err == nil {
		t.Error("expected an unslugifiable title to fail materialization")
	}
	// nothing should have been created.
	all, _ := store.ListFeatures(context.Background())
	if len(all) != 0 {
		t.Errorf("no features should exist after a rejected batch, got %d", len(all))
	}
}

// TestMaterializeNamesRepo: features minted with a repo target persist the
// repository name; every card in the batch shares the same repo.
func TestMaterializeNamesRepo(t *testing.T) {
	e := multiRepoEngine(t)
	ctx := context.Background()
	created, err := e.Materialize(ctx, sampleResult(), MaterializeOpts{Repo: "b"})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created %d, want 2", len(created))
	}
	for i, f := range created {
		if f.Repo != "b" {
			t.Errorf("feature[%d] repo = %q, want b", i, f.Repo)
		}
		got, err := e.cfg.Store.GetFeature(ctx, f.ID)
		if err != nil || got.Repo != "b" {
			t.Errorf("feature[%d] persisted repo = %q (err=%v), want b", i, got.Repo, err)
		}
	}
}

// TestMaterializeUnknownRepo: an unconfigured repository name fails the
// whole batch before any feature number is consumed, with the same
// repos:-pointing message MaterializeBugs uses.
func TestMaterializeUnknownRepo(t *testing.T) {
	e := multiRepoEngine(t)
	ctx := context.Background()
	_, err := e.Materialize(ctx, sampleResult(), MaterializeOpts{Repo: "nope"})
	if err == nil {
		t.Fatal("expected an error materializing into an unconfigured repo")
	}
	if !strings.Contains(err.Error(), `repository "nope" is not configured`) ||
		!strings.Contains(err.Error(), "repos:") {
		t.Errorf("error should name the repo and point at `repos:` in config, got: %v", err)
	}
	// the repo check is Materialize's first pre-flight, before the mint
	// loop, so the whole batch fails atomically with nothing created.
	all, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("no features should exist after a rejected batch, got %d", len(all))
	}
}

// TestMaterializeMintsBugProposalsAsBugs: the mint honours each
// proposal's kind, so a diagnosis's slices arrive on the board as BG
// cards carrying a bug report — not FD cards carrying a spec.
func TestMaterializeMintsBugProposalsAsBugs(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	res := domain.IngestResult{
		SourcePath: "RS-009 picker-drops-answers",
		Proposals: []domain.FeatureProposal{
			{
				Title: "Reject unlabelled options", OneLiner: "refuse the malformed call",
				Kind:  domain.KindBug,
				Draft: domain.DraftSeed{Problem: "An option with no label renders a blank row.\n\n## Steps to reproduce\n\nemit an option without a label"},
			},
			{Title: "Explain the refusal", OneLiner: "say why", Draft: domain.DraftSeed{Problem: "The keystroke vanishes."}},
		},
	}
	created, err := e.Materialize(context.Background(), res, MaterializeOpts{Envelope: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created %d, want 2", len(created))
	}
	if created[0].Kind != domain.KindBug || !strings.HasPrefix(string(created[0].ID), "BG-") {
		t.Errorf("proposal[0] minted as %s %s, want a BG bug", created[0].Kind, created[0].ID)
	}
	if created[1].Kind != domain.KindFeature || !strings.HasPrefix(string(created[1].ID), "FD-") {
		t.Errorf("proposal[1] minted as %s %s, want an FD feature", created[1].Kind, created[1].ID)
	}
	// the bug's draft is a bug report, and the seed's headings routed into
	// its sections the same way a typed or imported bug's do
	draft, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&created[0])))
	if err != nil {
		t.Fatal(err)
	}
	body := string(draft)
	for _, want := range []string{"## Summary", "## Reproduction", "## Root cause"} {
		if !strings.Contains(body, want) {
			t.Errorf("the bug's draft has no %s:\n%s", want, body)
		}
	}
	if repro, ok := spec.ViewSection(body, "Reproduction"); !ok || !strings.Contains(repro, "emit an option without a label") {
		t.Errorf("the seed's reproduction heading did not route into the report: %q", repro)
	}
	// Root cause stays the prompt: the diagnosis knows the cause, but the
	// fix card's own design stage is what establishes it against the code.
	if got := spec.UndraftedSections(body, []string{"Root cause"}); len(got) != 1 {
		t.Error("Root cause should still be the %% prompt on a minted fix card")
	}
}

// TestMaterializeRefusesAnUnmintableProposalKind: before any number is
// consumed, matching the slug pre-flight beside it.
func TestMaterializeRefusesAnUnmintableProposalKind(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	res := domain.IngestResult{Proposals: []domain.FeatureProposal{{Title: "a goal", Kind: domain.KindGoal}}}
	if _, err := e.Materialize(context.Background(), res, MaterializeOpts{Envelope: 200}); err == nil {
		t.Fatal("a goal proposal should be refused")
	}
	all, err := store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("a refused batch minted %d cards", len(all))
	}
}
