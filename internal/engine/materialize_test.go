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
				SourceRefs: []string{"Payments"}, Skip: domain.SkipFlags{Brainstorm: true},
				Draft: domain.DraftSeed{Problem: "We miss async state.", Acceptance: "Signed event flips order.", OpenQuestions: []string{"which providers?"}},
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
	e := New(Config{Agent: agent.NewFake("x"), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
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
	if f0.Stage != domain.StageTodo || f0.Profile != "thrifty" || f0.Budget.Envelope != 200 || !f0.Skip.Brainstorm {
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

func TestMaterializeRejectsUnslugifiableTitle(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: agent.NewFake("x"), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
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
