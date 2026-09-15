package domain

import "strings"

// Research cards are the third kind of work item (KindResearch). They
// run the one shared workflow — the design stage shapes the research
// question and direction, the build stage surveys and writes the
// findings up — and deliver an approved research document instead of a
// branch. The types here are the structured input a creation source
// produces, mirroring DraftSeed / BugReport for the ingest pipeline.

// ResearchSeed is the content a creation form can seed into a research
// document — the two sections that belong to the request itself: the
// brief (the ask, verbatim) and the questions it raises. Everything else
// (Findings, Constraints, Options, Direction, Slices, Out of scope, Open
// risks, Review) is left open for the design and build stages to
// converge on; the seed never pre-empts that work. Empty fields keep the
// template's %% prompt.
type ResearchSeed struct {
	Brief     string   // → ## Brief, verbatim (marker-neutralized)
	Questions []string // → one bullet + %% thread per item under ## Questions
}

// ResearchMode selects which contract a research card runs under. It is a
// mode rather than a fifth Kind because everything structural about the
// two is identical — no branch, no worktree, the scratch tree, the
// read-only build stage, the agent-free document verify, the decompose
// gate — and a kind buys only an ID prefix at the cost of a
// `== KindResearch` site in every one of those places. What differs is
// the contract: which sections the document carries, which of them the
// gates demand, what the stages are told to do, and what the slices mint.
//
// The empty mode is the survey — the original research card — so every
// row written before diagnosis existed reads correctly with no backfill.
type ResearchMode string

const (
	// ModeSurvey is question-driven: an open ask, grounded into findings
	// and a recommended direction, decomposing into features to build.
	ModeSurvey ResearchMode = ""
	// ModeDiagnosis is symptom-driven: something already behaves wrong,
	// and the work is to explain WHY before anything is fixed. It differs
	// from a bug card in the two ways that matter — it never writes a
	// fix, and it may find more than one cause — and from a survey in
	// that its evidence is a reproduction rather than a reading, and its
	// slices mint bugs rather than features.
	ModeDiagnosis ResearchMode = "diagnosis"
)

// Valid reports whether m is a storable research mode.
func (m ResearchMode) Valid() bool { return m == ModeSurvey || m == ModeDiagnosis }

// String names the mode for a reader. The survey's name is "research",
// not "survey": that is what every existing surface, template and CLI
// verb already calls it, and renaming the default to make the pair
// symmetric would rename the thing people already use.
func (m ResearchMode) String() string {
	if m == ModeDiagnosis {
		return "diagnosis"
	}
	return "research"
}

// CardType is what a creation surface offers: a kind, plus the research
// mode when the kind is research. Kinds and modes are one choice to the
// person at the dialog and two facts to the store, and this is the seam
// between those views — every surface that asks "what are you making?"
// asks it in these terms, and everything past the mint speaks Kind and
// ResearchMode.
type CardType struct {
	Kind Kind
	Mode ResearchMode
}

// CardTypes are the choices a creation surface offers, in the order they
// are shown. Diagnosis sits beside research because that is where a
// person deciding between the two will look for it.
var CardTypes = []CardType{
	{Kind: KindFeature},
	{Kind: KindBug},
	{Kind: KindResearch},
	{Kind: KindResearch, Mode: ModeDiagnosis},
	{Kind: KindGoal},
}

// Name is the one word a surface shows for this type, and the one word
// every input spelling resolves to.
func (c CardType) Name() string {
	if c.Kind == KindResearch {
		return c.Mode.String()
	}
	return string(c.Kind)
}

// Valid reports whether c names one of the offered types. It is
// membership in CardTypes, not two independent field checks: a mode on a
// kind that has none is invalid even though both halves are, and that is
// exactly the combination a surface that presets a kind row and then
// moves it can produce.
func (c CardType) Valid() bool {
	for _, t := range CardTypes {
		if t == c {
			return true
		}
	}
	return false
}

// Prefix is the ID prefix cards of this type are minted with. Diagnosis
// shares research's RS: it is the same kind, and a second prefix would
// have to be parsed, migrated and routed everywhere RS already is.
func (c CardType) Prefix() string { return c.Kind.prefix() }

// ParseCardType resolves a card type as a caller spells it — a CLI flag,
// an MCP tool argument, a goal's `gummi-cards` row. "diagnosis" is the
// one name that is not a Kind; everything else is the kind's own word.
func ParseCardType(s string) (CardType, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, c := range CardTypes {
		if c.Name() == s {
			return c, true
		}
	}
	return CardType{}, false
}

// CardTypeOf reads the type back off a card.
func CardTypeOf(f *Feature) CardType {
	if f.Kind == KindResearch {
		return CardType{Kind: KindResearch, Mode: f.Mode}
	}
	return CardType{Kind: f.kind()}
}

// IsDiagnosis reports whether f is a research card running the diagnosis
// contract. Every caller that only cares "is this read-only, branchless
// research?" keeps asking about the Kind; this is for the ones that
// choose a contract.
func (f *Feature) IsDiagnosis() bool {
	return f.kind() == KindResearch && f.Mode == ModeDiagnosis
}
