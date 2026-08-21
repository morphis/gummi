package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestSeededTemplateFillsSectionsAndParses(t *testing.T) {
	f := &domain.Feature{ID: "FD-012", Num: 12, Title: "Webhook retries", OneLiner: "retry failed webhooks", Slug: "webhook-retries", Stage: domain.StageTodo}
	seed := domain.DraftSeed{
		Problem:       "Failed webhook deliveries are dropped, losing events.",
		Constraints:   "Must not double-deliver; idempotency keys required.",
		Acceptance:    "A forced 500 is retried with backoff and eventually delivered.",
		OpenQuestions: []string{"max retry count?", "  ", "dead-letter after N?"},
	}
	prov := domain.DraftProvenance{
		Source:    ".gummi/ingest/platform-prd.md",
		Refs:      []string{"Reliability", "Webhooks"},
		DependsOn: []string{"FD-011 event-bus"},
	}
	out := SeededTemplate(f, seed, prov)

	for _, want := range []string{
		"# FD-012: Webhook retries",
		"> retry failed webhooks",
		"Ingested from `.gummi/ingest/platform-prd.md`",
		"§Reliability, Webhooks",
		"Depends on: FD-011 event-bus",
		seed.Problem,
		seed.Constraints,
		seed.Acceptance,
		"- max retry count?",
		"- dead-letter after N?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("seeded draft missing %q\n---\n%s", want, out)
		}
	}

	// the seeded content must replace the section prompts it fills, but the
	// unfilled approach sections keep theirs.
	if strings.Contains(out, promptProblem) {
		t.Error("Problem prompt should have been replaced by seed content")
	}
	if !strings.Contains(out, promptConsidered) || !strings.Contains(out, promptChosen) {
		t.Error("approach sections should keep their prompts (brainstorm's job)")
	}

	// the two non-blank open questions surface as INDEPENDENT checklist
	// threads (each anchored to its own bullet), so resolving one leaves
	// the other open.
	d := Parse(out)
	var qThreads int
	for _, thread := range d.OpenQuestions() {
		for _, m := range thread.Markers {
			if strings.Contains(m.Text, "open question from the ingested spec") {
				qThreads++
				break
			}
		}
	}
	if qThreads != 2 {
		t.Errorf("got %d independent open-question threads, want 2", qThreads)
	}
}

func TestSeededTemplateResolvingOneQuestionLeavesOthers(t *testing.T) {
	f := &domain.Feature{ID: "FD-014", Num: 14, Title: "Y", Slug: "y", Stage: domain.StageTodo}
	seed := domain.DraftSeed{Problem: "P.", OpenQuestions: []string{"first?", "second?"}}
	out := SeededTemplate(f, seed, domain.DraftProvenance{})

	before := len(Parse(out).OpenQuestions())
	// resolve just the first question's thread by threading a resolution
	// under its bullet.
	line, ok := FindAnchor(out, "- first?")
	if !ok {
		t.Fatal("could not anchor the first question bullet")
	}
	resolved, err := AddComment(out, line, "gummi", "2026-07-05", "resolved — done")
	if err != nil {
		t.Fatal(err)
	}
	after := len(Parse(resolved).OpenQuestions())
	if after != before-1 {
		t.Errorf("resolving one question changed open count %d → %d, want a drop of exactly 1", before, after)
	}
}

func TestSeededTemplateNeutralizesMarkerInBody(t *testing.T) {
	f := &domain.Feature{ID: "FD-015", Num: 15, Title: "Z", Slug: "z", Stage: domain.StageTodo}
	// a constraints body whose line would otherwise parse as a resolving
	// marker must not become a real marker in the draft.
	seed := domain.DraftSeed{Problem: "P.", Constraints: "note\n%% resolved by the team"}
	out := SeededTemplate(f, seed, domain.DraftProvenance{})
	for _, m := range Parse(out).Markers {
		if strings.Contains(m.Text, "resolved by the team") {
			t.Errorf("ingested body line was parsed as a %%%% marker: %+v", m)
		}
	}
}

func TestSeededTemplateNoProvenanceMatchesShape(t *testing.T) {
	f := &domain.Feature{ID: "FD-013", Num: 13, Title: "X", OneLiner: "", Slug: "x", Stage: domain.StageTodo}
	out := SeededTemplate(f, domain.DraftSeed{}, domain.DraftProvenance{})
	// an empty seed with no provenance is exactly the blank template.
	if out != Template(f) {
		t.Errorf("empty seed should equal Template\n--- got ---\n%s\n--- want ---\n%s", out, Template(f))
	}
	if strings.Contains(out, "Ingested") {
		t.Error("no provenance should render no Ingested header")
	}
}

// TestSeededTemplateResearchProvenance covers renderProvenance's two
// branches: a source naming an RS card renders without backticks, and the
// pre-existing PRD/stashed-path source keeps its backticked form.
func TestSeededTemplateResearchProvenance(t *testing.T) {
	f := &domain.Feature{ID: "FD-014", Num: 14, Title: "Y", Slug: "y", Stage: domain.StageTodo}

	rs := SeededTemplate(f, domain.DraftSeed{}, domain.DraftProvenance{Source: "RS-042 topic-slug"})
	if !strings.Contains(rs, "> _Ingested from RS-042 topic-slug_") {
		t.Errorf("RS source should render verbatim without backticks\n---\n%s", rs)
	}
	if strings.Contains(rs, "`RS-042 topic-slug`") {
		t.Errorf("RS source should not be backticked\n---\n%s", rs)
	}

	prd := SeededTemplate(f, domain.DraftSeed{}, domain.DraftProvenance{Source: ".gummi/ingest/foo.md"})
	if !strings.Contains(prd, "> _Ingested from `.gummi/ingest/foo.md`_") {
		t.Errorf("PRD source should keep its backticked form\n---\n%s", prd)
	}
}
