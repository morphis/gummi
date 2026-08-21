package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// researchCard builds an RS-kind work item the same way feature() does for
// FD cards, so IngestResearch tests can target a card with a real
// ArtifactPath (.gummi/research/…).
func researchCard(num int, title string) domain.Feature {
	id, _ := domain.NewID(domain.KindResearch, num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindResearch, Title: title, Slug: slug,
		Stage: domain.StageShape, CreatedAt: now, UpdatedAt: now,
	}
}

// writeResearchArtifact writes body at rsCard's ArtifactPath under the
// repo root — the first (and, in these tests, only) candidate
// e.artifactFile checks.
func writeResearchArtifact(t *testing.T, root string, rsCard domain.Feature, body string) {
	t.Helper()
	path := filepath.Join(root, rsCard.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ingestResearchEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e, wt.RepoRoot()
}

const slicesHappyPath = "# RS-001: research topic\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: First slice\n" +
	"  one-liner: does the first thing\n" +
	"  depends-on: []\n" +
	"  requirements: [req-a, req-b]\n" +
	"  id: \"\"\n" +
	"- title: Second slice\n" +
	"  one-liner: does the second thing\n" +
	"  depends-on: [First slice]\n" +
	"  requirements: [req-c]\n" +
	"  id: \"\"\n" +
	"```\n\n" +
	"## Out of scope\n\n" +
	"- req-d: not doing this now\n"

func TestIngestResearchHappyPath(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	writeResearchArtifact(t, root, rsCard, slicesHappyPath)

	res, err := e.IngestResearch(context.Background(), rsCard)
	if err != nil {
		t.Fatalf("IngestResearch: %v", err)
	}
	if len(res.Proposals) != 2 {
		t.Fatalf("proposals = %d, want 2", len(res.Proposals))
	}
	if res.Proposals[0].Title != "First slice" || res.Proposals[1].Title != "Second slice" {
		t.Errorf("proposals out of row order: %+v", res.Proposals)
	}
	if res.Proposals[1].OneLiner != "does the second thing" {
		t.Errorf("one-liner not carried through: %+v", res.Proposals[1])
	}
	if len(res.Proposals[1].DependsOn) != 1 || res.Proposals[1].DependsOn[0] != "First slice" {
		t.Errorf("depends-on not carried through: %+v", res.Proposals[1].DependsOn)
	}
	want := string(rsCard.ID) + " " + rsCard.Slug
	if res.SourcePath != want {
		t.Errorf("SourcePath = %q, want %q", res.SourcePath, want)
	}
}

const slicesScaffoldOnly = "# RS-002: blank topic\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: example slice\n" +
	"  one-liner: what it mints\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```\n"

const slicesScaffoldPlusReal = "# RS-003: partial topic\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: example slice\n" +
	"  one-liner: what it mints\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"- title: Real slice\n" +
	"  one-liner: mints something real\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```\n"

func TestIngestResearchDropsScaffoldRow(t *testing.T) {
	t.Run("scaffold-only", func(t *testing.T) {
		e, root := ingestResearchEngine(t)
		rsCard := researchCard(2, "blank topic")
		writeResearchArtifact(t, root, rsCard, slicesScaffoldOnly)

		_, err := e.IngestResearch(context.Background(), rsCard)
		if err == nil {
			t.Fatal("expected an error for a scaffold-only Slices table")
		}
		if !strings.Contains(err.Error(), "no usable proposals") {
			t.Errorf("error = %v, want it to mention no usable proposals", err)
		}
	})

	t.Run("scaffold-plus-real-row", func(t *testing.T) {
		e, root := ingestResearchEngine(t)
		rsCard := researchCard(3, "partial topic")
		writeResearchArtifact(t, root, rsCard, slicesScaffoldPlusReal)

		res, err := e.IngestResearch(context.Background(), rsCard)
		if err != nil {
			t.Fatalf("IngestResearch: %v", err)
		}
		if len(res.Proposals) != 1 {
			t.Fatalf("proposals = %d, want 1 (scaffold dropped)", len(res.Proposals))
		}
		if res.Proposals[0].Title != "Real slice" {
			t.Errorf("proposals[0].Title = %q, want %q", res.Proposals[0].Title, "Real slice")
		}
	})
}

func TestIngestResearchLoudFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "blank title row",
			body: "# RS-004: t\n\n## Slices\n\n```yaml\n- title: \"\"\n  one-liner: x\n  depends-on: []\n  requirements: []\n  id: \"\"\n```\n",
		},
		{
			name: "unparseable yaml",
			body: "# RS-004: t\n\n## Slices\n\n```yaml\n- title: [unterminated\n```\n",
		},
		{
			name: "missing Slices heading",
			body: "# RS-004: t\n\n## Findings\n\nnothing here.\n",
		},
		{
			name: "empty fenced block",
			body: "# RS-004: t\n\n## Slices\n\n```yaml\n```\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, root := ingestResearchEngine(t)
			rsCard := researchCard(4, "t")
			writeResearchArtifact(t, root, rsCard, c.body)

			res, err := e.IngestResearch(context.Background(), rsCard)
			if err == nil {
				t.Fatal("expected a loud error")
			}
			if len(res.Proposals) != 0 || len(res.Coverage) != 0 || res.SourcePath != "" {
				t.Errorf("expected an empty IngestResult on failure, got %+v", res)
			}
		})
	}
}

const slicesCoverageFixture = "# RS-005: t\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: Slice one\n" +
	"  one-liner: x\n" +
	"  depends-on: []\n" +
	"  requirements: [alpha, beta]\n" +
	"  id: \"\"\n" +
	"- title: Slice two\n" +
	"  one-liner: y\n" +
	"  depends-on: []\n" +
	"  requirements: [gamma]\n" +
	"  id: \"\"\n" +
	"```\n"

func TestIngestResearchCoverageMapped(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(5, "t")
	writeResearchArtifact(t, root, rsCard, slicesCoverageFixture)

	res, err := e.IngestResearch(context.Background(), rsCard)
	if err != nil {
		t.Fatalf("IngestResearch: %v", err)
	}
	wantReqs := []string{"alpha", "beta", "gamma"}
	if len(res.Coverage) != len(wantReqs) {
		t.Fatalf("coverage entries = %d, want %d: %+v", len(res.Coverage), len(wantReqs), res.Coverage)
	}
	for i, want := range wantReqs {
		c := res.Coverage[i]
		if c.Requirement != want || c.Status != domain.CoverageMapped {
			t.Errorf("coverage[%d] = %+v, want Requirement=%q Status=Mapped", i, c, want)
		}
	}
	if res.Coverage[0].Feature != "Slice one" || res.Coverage[2].Feature != "Slice two" {
		t.Errorf("coverage Feature not tied to its row: %+v", res.Coverage)
	}
	if len(res.Unmapped()) != 0 {
		t.Errorf("expected no Unmapped entries, got %+v", res.Unmapped())
	}
}

const slicesOutOfScopeFixture = "# RS-006: t\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: Slice one\n" +
	"  one-liner: x\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```\n\n" +
	"## Out of scope\n\n" +
	"%% @gummi: what this research deliberately won't cover — each line is `- key: prose`\n\n" +
	"- delta: not now\n" +
	"- epsilon: someone else's problem\n"

func TestIngestResearchCoverageOutOfScope(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(6, "t")
	writeResearchArtifact(t, root, rsCard, slicesOutOfScopeFixture)

	res, err := e.IngestResearch(context.Background(), rsCard)
	if err != nil {
		t.Fatalf("IngestResearch: %v", err)
	}
	var oos []domain.CoverageEntry
	for _, c := range res.Coverage {
		if c.Status == domain.CoverageOutOfScope {
			oos = append(oos, c)
		}
	}
	if len(oos) != 2 {
		t.Fatalf("out-of-scope entries = %d, want 2: %+v", len(oos), res.Coverage)
	}
	if oos[0].Requirement != "delta" || oos[0].Note != "not now" {
		t.Errorf("oos[0] = %+v", oos[0])
	}
	if oos[1].Requirement != "epsilon" || oos[1].Note != "someone else's problem" {
		t.Errorf("oos[1] = %+v", oos[1])
	}
}

func TestIngestResearchThroughMaterialize(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(42, "topic slug")
	body := "# RS-042: topic slug\n\n" +
		"## Slices\n\n" +
		"```yaml\n" +
		"- title: First slice\n" +
		"  one-liner: does the first thing\n" +
		"  depends-on: []\n" +
		"  requirements: []\n" +
		"  id: \"\"\n" +
		"- title: Second slice\n" +
		"  one-liner: does the second thing\n" +
		"  depends-on: [First slice]\n" +
		"  requirements: []\n" +
		"  id: \"\"\n" +
		"```\n"
	writeResearchArtifact(t, root, rsCard, body)

	ctx := context.Background()
	res, err := e.IngestResearch(ctx, rsCard)
	if err != nil {
		t.Fatalf("IngestResearch: %v", err)
	}
	if res.SourcePath != "RS-042 topic-slug" {
		t.Fatalf("SourcePath = %q, want %q", res.SourcePath, "RS-042 topic-slug")
	}

	created, err := e.Materialize(ctx, res, MaterializeOpts{Profile: "thrifty"})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(created) != 2 || created[0].Title != "First slice" || created[1].Title != "Second slice" {
		t.Fatalf("materialize did not preserve row order: %+v", created)
	}

	ws := e.cfg.Workspace
	draft0, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&created[0])))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draft0), "> _Ingested from RS-042 topic-slug_") {
		t.Errorf("draft[0] missing RS provenance header:\n%s", draft0)
	}

	draft1, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&created[1])))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draft1), string(created[0].ID)+" first-slice") {
		t.Errorf("draft[1] in-batch dependency not resolved to FD-ID:\n%s", draft1)
	}
}
