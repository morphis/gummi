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
// investigate/shape stages' work, never the form's.
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
