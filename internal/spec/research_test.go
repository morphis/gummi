package spec

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/morphis/gummi/internal/domain"
	"gopkg.in/yaml.v3"
)

func researchCard() *domain.Feature {
	return &domain.Feature{
		ID: "RS-007", Num: 7, Kind: domain.KindResearch, Title: "Widget perf",
		OneLiner: "study", Slug: "widget-perf", Stage: domain.StagePlan,
	}
}

func researchSeed() domain.ResearchSeed {
	return domain.ResearchSeed{
		Brief:     "How does widget rendering scale under load?",
		Questions: []string{"max concurrency?", "  ", "cache invalidation model?"},
	}
}

func researchProv() domain.DraftProvenance {
	return domain.DraftProvenance{
		Source:    ".gummi/ingest/perf.md",
		Refs:      []string{"Performance"},
		DependsOn: []string{"FD-074 research-kind"},
	}
}

func TestResearchTemplateGolden(t *testing.T) {
	golden.RequireEqual(t, []byte(ResearchTemplate(researchCard())))
}

func TestSeededResearchTemplateGolden(t *testing.T) {
	golden.RequireEqual(t, []byte(SeededResearchTemplate(researchCard(), researchSeed(), researchProv())))
}

// TestSeededResearchThreadsIndependent locks the per-question checklist
// guarantee: each seed question is its own thread, so resolving one leaves
// the others open.
func TestSeededResearchThreadsIndependent(t *testing.T) {
	out := SeededResearchTemplate(researchCard(), researchSeed(), domain.DraftProvenance{})
	before := len(Parse(out).OpenQuestions())
	line, ok := FindAnchor(out, "- max concurrency?")
	if !ok {
		t.Fatal("could not anchor the first question bullet")
	}
	resolved, err := AddComment(out, line, "gummi", "2026-08-19", "resolved — 16")
	if err != nil {
		t.Fatal(err)
	}
	after := len(Parse(resolved).OpenQuestions())
	if after != before-1 {
		t.Errorf("resolving one question changed open count %d → %d, want a drop of exactly 1", before, after)
	}
}

// TestSeededResearchNeutralizesBriefMarker locks the %% channel: a Brief
// line that would parse as a marker is defused, never forged into a thread.
func TestSeededResearchNeutralizesBriefMarker(t *testing.T) {
	seed := domain.ResearchSeed{Brief: "ask\n%% resolved by the team"}
	out := SeededResearchTemplate(researchCard(), seed, domain.DraftProvenance{})
	for _, m := range Parse(out).Markers {
		if strings.Contains(m.Text, "resolved by the team") {
			t.Errorf("ingested Brief line was parsed as a %%%% marker: %+v", m)
		}
	}
	if !strings.Contains(out, "% % resolved by the team") {
		t.Error("Brief marker should be neutralized to % %")
	}
}

// TestBlankTemplateRoutesResearch locks kind routing: a research card's
// blank artifact is a research document, not a feature spec.
func TestBlankTemplateRoutesResearch(t *testing.T) {
	out := blankTemplate(researchCard())
	if !strings.Contains(out, "## Brief") {
		t.Errorf("research blank template missing ## Brief\n---\n%s", out)
	}
	if strings.Contains(out, "## Problem") || strings.Contains(out, "## Chosen approach") {
		t.Error("research artifact must not contain feature spec sections")
	}
}

// TestResearchSlicesScaffoldParses locks the `## Slices` contract: the
// blank template's fenced YAML parses and carries the documented row schema
// (title/one-liner/depends-on/requirements/id) with a commented example row.
func TestResearchSlicesScaffoldParses(t *testing.T) {
	out := ResearchTemplate(researchCard())
	fence := slicesFence(out)
	if fence == "" {
		t.Fatalf("no ```yaml fence in Slices\n---\n%s", out)
	}
	var rows []map[string]interface{}
	if err := yaml.Unmarshal([]byte(fence), &rows); err != nil {
		t.Fatalf("Slices fence does not parse: %v\n%s", err, fence)
	}
	if len(rows) != 1 {
		t.Fatalf("Slices scaffold parsed %d rows, want 1\n%s", len(rows), fence)
	}
	r := rows[0]
	for _, key := range []string{"title", "one-liner", "depends-on", "requirements", "id"} {
		if _, ok := r[key]; !ok {
			t.Errorf("Slices row missing key %q in %v", key, r)
		}
	}
	if r["title"] != "example slice" {
		t.Errorf("Slices example row title = %v, want example slice", r["title"])
	}
	if !strings.Contains(fence, "# title / one-liner / depends-on / requirements (keys) / id") {
		t.Error("Slices scaffold should carry the commented field guide")
	}
}

// slicesFence extracts the fenced YAML body under ## Slices.
func slicesFence(doc string) string {
	var in []string
	inSlices := false
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## Slices") {
			inSlices = true
			continue
		}
		if !inSlices {
			continue
		}
		if strings.HasPrefix(line, "```") {
			if inFence {
				return strings.Join(in, "\n")
			}
			inFence = true
			continue
		}
		if inFence {
			in = append(in, line)
		}
	}
	return ""
}

func diagnosisCard() *domain.Feature {
	return &domain.Feature{
		ID: "RS-009", Num: 9, Kind: domain.KindResearch, Mode: domain.ModeDiagnosis,
		Title: "Picker drops answers", OneLiner: "answers vanish and the card parks",
		Slug: "picker-drops-answers", Stage: domain.StagePlan,
	}
}

func TestDiagnosisTemplateGolden(t *testing.T) {
	golden.RequireEqual(t, []byte(DiagnosisTemplate(diagnosisCard())))
}

func TestSeededDiagnosisTemplateGolden(t *testing.T) {
	seed := domain.ResearchSeed{
		Brief:     "The ask_user picker silently drops answers and parks the card.",
		Questions: []string{"does it depend on window size?"},
	}
	golden.RequireEqual(t, []byte(SeededDiagnosisTemplate(diagnosisCard(), seed, researchProv())))
}

// TestDiagnosisTemplateCarriesItsOwnSections: the diagnosis document is
// not the research one with different prompts — it has the sections the
// diagnosis contract names, and none of the survey's converging ones.
func TestDiagnosisTemplateCarriesItsOwnSections(t *testing.T) {
	out := DiagnosisTemplate(diagnosisCard())
	for _, want := range []string{
		DiagSectionSymptom, DiagSectionRepro, DiagSectionCons, DiagSectionEvidence,
		DiagSectionRuledOut, DiagSectionCauses, "Slices", "Out of scope", "Open risks", "Review",
	} {
		if _, ok := ViewSection(out, want); !ok {
			t.Errorf("diagnosis template has no `## %s`", want)
		}
	}
	// the survey's own three: a diagnosis converges on a cause, not on a
	// direction between options, and its checklist is discovered rather
	// than asked.
	for _, unwanted := range []string{"Questions", "Findings", "Options", "Direction"} {
		if _, ok := ViewSection(out, unwanted); ok {
			t.Errorf("diagnosis template should not carry `## %s`", unwanted)
		}
	}
}

// TestDiagnosisSlicesScaffoldMintsFixes: the scaffold's one structural
// difference from the survey's is the field that makes its rows fixes.
func TestDiagnosisSlicesScaffoldMintsFixes(t *testing.T) {
	body, ok := ViewSection(DiagnosisTemplate(diagnosisCard()), "Slices")
	if !ok {
		t.Fatal("no Slices section")
	}
	var rows []struct {
		Title string `yaml:"title"`
		Kind  string `yaml:"kind"`
	}
	fence := strings.Index(body, "```yaml")
	if fence < 0 {
		t.Fatal("the Slices scaffold is not a fenced yaml block")
	}
	rest := body[fence+len("```yaml"):]
	if err := yaml.Unmarshal([]byte(rest[:strings.Index(rest, "```")]), &rows); err != nil {
		t.Fatalf("the scaffold does not parse as the decompose pass reads it: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != "bug" {
		t.Fatalf("scaffold rows = %+v, want one row with kind: bug", rows)
	}
}

// TestLayoutPerMode: the two modes are one set of readers and two section
// maps, and nothing else decides where a citation or a checklist lives.
func TestLayoutPerMode(t *testing.T) {
	survey := LayoutFor(domain.ModeSurvey)
	if survey.Evidence != "Findings" || survey.Coverage != "Questions" || survey.DefaultSliceKind != domain.KindFeature {
		t.Errorf("survey layout = %+v", survey)
	}
	diag := LayoutFor(domain.ModeDiagnosis)
	if diag.Evidence != DiagSectionEvidence || diag.Coverage != DiagSectionCauses || diag.DefaultSliceKind != domain.KindBug {
		t.Errorf("diagnosis layout = %+v", diag)
	}
	// a card with no mode, and every non-research card, read as the survey
	if got := LayoutOf(researchCard()); got != survey {
		t.Errorf("an RS card with no mode should read as the survey, got %+v", got)
	}
	if got := LayoutOf(diagnosisCard()); got != diag {
		t.Errorf("a diagnosis card should read as the diagnosis layout, got %+v", got)
	}
	if got := LayoutOf(&domain.Feature{ID: "FD-001", Kind: domain.KindFeature}); got != survey {
		t.Errorf("a feature should read as the survey layout, got %+v", got)
	}
}
