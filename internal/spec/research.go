package spec

import (
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// Research prompts: the %% guidance a blank research document carries in
// each section. Only Brief and Questions can be seeded (from the
// ResearchSeed); the other eight sections stay open for the investigate
// and shape stages to fill — Findings (what was learned), Constraints,
// Options, Direction, Slices, Out of scope, Open risks, and Review.
const (
	promptResearchBrief     = "%% @gummi: the ask, in the requester's own words — what to investigate and why"
	promptResearchQuestions = "%% @gummi: the open questions this research must answer to be useful"
	promptResearchFindings  = "%% @gummi: what the investigation learned — prose with inline `path:line` / `path:start-end` citations"
	promptResearchCons      = "%% @gummi: the constraints the investigation is bound by (time, scope, invariants)"
	promptResearchOptions   = "%% @gummi: the candidate directions considered, with tradeoffs"
	promptResearchDirection = "%% @gummi: the recommended direction, and why it wins"
	promptResearchSlices    = "%% @gummi: the proposed follow-on work, one row per slice — see the scaffold below"
	promptResearchOutScope  = "%% @gummi: what this research deliberately won't cover — each line is `- key: prose`"
	promptResearchRisks     = "%% @gummi: open risks and what would de-risk each"
	promptResearchReview    = "%% @gummi: reviewer findings land here; the researcher resolves each one"
)

// slicesScaffold is the `## Slices` example row that ships with the blank
// document so authors never improvise the fenced-YAML shape the decompose
// pass parses. Its comment line doubles as the field guide. The `id` slot is
// blank here — it is back-annotated with the minted FD.
const slicesScaffold = "```yaml\n" +
	"# title / one-liner / depends-on / requirements (keys) / id (back-annotated)\n" +
	"- title: example slice\n" +
	"  one-liner: what it mints\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```"

// ResearchTemplate renders the initial (blank) research document.
func ResearchTemplate(f *domain.Feature) string {
	return renderResearch(f, domain.ResearchSeed{}, domain.DraftProvenance{})
}

// SeededResearchTemplate renders a research document pre-populated from a
// creation form: Brief verbatim (marker-neutralized) and one %% thread per
// question. The other eight sections keep their %% prompts — they are the
// design and build stages' work, never the form's.
func SeededResearchTemplate(f *domain.Feature, seed domain.ResearchSeed, prov domain.DraftProvenance) string {
	return renderResearch(f, seed, prov)
}

// renderResearch mirrors renderDraft: `# ID: Title` header, one-liner,
// provenance, then the ten sections in contract order. Brief and Questions
// come from the seed; Slices renders its scaffold beside the prompt; the
// other seven are prompt-only.
func renderResearch(f *domain.Feature, seed domain.ResearchSeed, prov domain.DraftProvenance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	renderProvenance(&b, prov)

	section(&b, "Brief", neutralizeMarkers(seed.Brief), promptResearchBrief)

	// Each question is its own content line (a bullet) with a %% thread
	// under it, so each is an independent checklist thread: resolving one
	// never closes the others (adjacent %% lines with no content between
	// them would collapse into one anchor and one thread).
	section(&b, "Questions", "", promptResearchQuestions)
	emitted := 0
	for _, q := range seed.Questions {
		if q = oneLine(q); q != "" {
			fmt.Fprintf(&b, "- %s\n%%%% @gummi: open question from the brief\n", q)
			emitted++
		}
	}
	if emitted > 0 {
		b.WriteString("\n")
	}

	section(&b, "Findings", "", promptResearchFindings)
	section(&b, "Constraints", "", promptResearchCons)
	section(&b, "Options", "", promptResearchOptions)
	section(&b, "Direction", "", promptResearchDirection)
	section(&b, "Slices", promptResearchSlices+"\n\n"+slicesScaffold, "")
	section(&b, "Out of scope", "", promptResearchOutScope)
	section(&b, "Open risks", "", promptResearchRisks)
	section(&b, "Review", "", promptResearchReview)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Diagnosis prompts: the %% guidance a blank diagnosis document carries.
// Only Symptom is seeded (from the ResearchSeed's brief — what the
// requester saw go wrong); the other nine sections are the stages' work.
const (
	promptDiagSymptom  = "%% @gummi: what goes wrong, in the requester's own words — what was seen, where, and how often"
	promptDiagRepro    = "%% @gummi: how to see it happen; when it cannot be reproduced on demand, say so and name what the evidence is instead"
	promptDiagCons     = "%% @gummi: the bounds of the investigation (time, scope, what must not be touched)"
	promptDiagEvidence = "%% @gummi: what the investigation observed — prose with inline `path:line` / `path:start-end` citations"
	promptDiagRuledOut = "%% @gummi: candidate causes eliminated, and what eliminated each — this is what stops the fix cards re-walking dead ends"
	promptDiagCauses   = "%% @gummi: the causes found — one bullet each, saying why it produces the symptom; a symptom with two causes gets two bullets"
	promptDiagSlices   = "%% @gummi: the follow-on work, one row per cause — see the scaffold below"
	promptDiagOutScope = "%% @gummi: what this diagnosis deliberately won't explain or fix — each line is `- key: prose`"
	promptDiagRisks    = "%% @gummi: open risks and what would de-risk each"
	promptDiagReview   = "%% @gummi: reviewer findings land here; the investigator resolves each one"
)

// Diagnosis section titles. Three of them are read by machinery rather
// than only by people — Symptom seeds, Evidence carries the citations
// verifydoc resolves, Causes is the checklist the slices must answer —
// and Layout is where that mapping is declared once.
const (
	DiagSectionSymptom  = "Symptom"
	DiagSectionRepro    = "Reproduction"
	DiagSectionCons     = "Constraints"
	DiagSectionEvidence = "Evidence"
	DiagSectionRuledOut = "Ruled out"
	DiagSectionCauses   = "Causes"
)

// diagSlicesScaffold is the diagnosis document's `## Slices` example row.
// It differs from the research one by a single field — `kind` — and that
// field is the whole reason a diagnosis is not a survey wearing different
// headings: a survey's slices are features to build, a diagnosis's are
// fixes. The `requirements` line is where a row names the cause it
// settles, which is what the coverage check reconciles.
const diagSlicesScaffold = "```yaml\n" +
	"# title / one-liner / kind (bug, feature) / depends-on / requirements (the causes this settles) / id (back-annotated)\n" +
	"- title: example fix\n" +
	"  one-liner: what it changes\n" +
	"  kind: bug\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```"

// DiagnosisTemplate renders the initial (blank) diagnosis document.
func DiagnosisTemplate(f *domain.Feature) string {
	return renderDiagnosis(f, domain.ResearchSeed{}, domain.DraftProvenance{})
}

// SeededDiagnosisTemplate renders a diagnosis document pre-populated from
// a creation form: the symptom verbatim, marker-neutralized. Nothing else
// is seeded — a diagnosis that arrives with its causes already filled in
// is not a diagnosis.
func SeededDiagnosisTemplate(f *domain.Feature, seed domain.ResearchSeed, prov domain.DraftProvenance) string {
	return renderDiagnosis(f, seed, prov)
}

// renderDiagnosis mirrors renderResearch: the same header, provenance and
// section machinery, a different contract in the sections.
func renderDiagnosis(f *domain.Feature, seed domain.ResearchSeed, prov domain.DraftProvenance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	renderProvenance(&b, prov)

	section(&b, DiagSectionSymptom, neutralizeMarkers(seed.Brief), promptDiagSymptom)

	// The form's questions, when it collected any, are what the requester
	// already wants to know. They ride under Reproduction rather than a
	// section of their own: the diagnosis's checklist is Causes, which is
	// discovered rather than asked, and a second seeded checklist would
	// be a coverage contract nothing reconciles.
	section(&b, DiagSectionRepro, "", promptDiagRepro)
	emitted := 0
	for _, q := range seed.Questions {
		if q = oneLine(q); q != "" {
			fmt.Fprintf(&b, "- %s\n%%%% @gummi: asked at creation\n", q)
			emitted++
		}
	}
	if emitted > 0 {
		b.WriteString("\n")
	}

	section(&b, DiagSectionCons, "", promptDiagCons)
	section(&b, DiagSectionEvidence, "", promptDiagEvidence)
	section(&b, DiagSectionRuledOut, "", promptDiagRuledOut)
	section(&b, DiagSectionCauses, "", promptDiagCauses)
	section(&b, "Slices", promptDiagSlices+"\n\n"+diagSlicesScaffold, "")
	section(&b, "Out of scope", "", promptDiagOutScope)
	section(&b, "Open risks", "", promptDiagRisks)
	section(&b, "Review", "", promptDiagReview)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Layout names the sections a research card's machinery reads, for one
// mode. The two documents are read by the same three readers — the
// citation check, the coverage check, and the gates — and this is the one
// place that says which heading each of them looks under, so adding a
// mode is a Layout rather than a switch in every reader.
type Layout struct {
	// Seeded is the section the creation form's text lands in.
	Seeded string
	// Evidence carries the inline `path:line` citations verifydoc resolves.
	Evidence string
	// Coverage is the bullet checklist every slice's `requirements` (or an
	// explicit Out of scope line) must answer.
	Coverage string
	// DefaultSliceKind is what a `## Slices` row mints when it names no
	// kind of its own.
	DefaultSliceKind domain.Kind
}

// LayoutFor returns the section mapping for a research mode.
func LayoutFor(mode domain.ResearchMode) Layout {
	if mode == domain.ModeDiagnosis {
		return Layout{
			Seeded:           DiagSectionSymptom,
			Evidence:         DiagSectionEvidence,
			Coverage:         DiagSectionCauses,
			DefaultSliceKind: domain.KindBug,
		}
	}
	return Layout{
		Seeded:           "Brief",
		Evidence:         "Findings",
		Coverage:         "Questions",
		DefaultSliceKind: domain.KindFeature,
	}
}

// LayoutOf returns the layout for a card: the research mode's when it is
// a research card, the survey's otherwise (nothing else has a document
// these readers read, and a zero Layout would make every caller check).
func LayoutOf(f *domain.Feature) Layout {
	if f.Kind == domain.KindResearch {
		return LayoutFor(f.Mode)
	}
	return LayoutFor(domain.ModeSurvey)
}
