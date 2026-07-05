package domain

// Ingestion turns an existing document (a PRD, a design doc) into a set
// of pre-seeded features (DESIGN §11). An architect pass decomposes the
// source into PR-sized slices; gummi reviews and materializes them. The
// types here are the pass's structured output — proposals, not yet minted
// features — plus a coverage map proving nothing was dropped.

// DraftSeed is the content extracted for one proposed feature, mapped
// onto the spec template's sections (DESIGN §11.2). Empty fields fall
// back to the template's %% prompt, so a partial extraction still yields
// a valid draft. The considered/chosen approach sections are never
// seeded — converging on an approach is brainstorm's job, not ingestion's.
type DraftSeed struct {
	Problem       string   // → ## Problem
	Constraints   string   // → ## Implementation notes
	Acceptance    string   // → ## Verification plan
	OpenQuestions []string // → %% markers under Problem (the checklist)
}

// FeatureProposal is one candidate FD the ingest pass emits — a slice of
// the source with everything needed to mint a feature and seed its draft.
type FeatureProposal struct {
	Title      string    // → Feature.Title (and, slugified, its ID's slug)
	OneLiner   string    // → Feature.OneLiner
	SourceRefs []string  // section headings / ranges this slice came from
	DependsOn  []string  // titles of other proposals this one needs (prose in v1)
	Skip       SkipFlags // the pass's suggested skip flags for this slice
	Draft      DraftSeed
}

// Slug derives the proposal's branch/filename slug from its title, with
// the same rules feature creation uses. A proposal whose title yields no
// slug can't be materialized.
func (p FeatureProposal) Slug() (string, error) { return Slugify(p.Title) }

// CoverageStatus classifies how a source requirement was handled.
type CoverageStatus string

const (
	// CoverageMapped: the requirement is covered by a proposed feature.
	CoverageMapped CoverageStatus = "mapped"
	// CoverageOutOfScope: deliberately excluded (the pass says why in Note).
	CoverageOutOfScope CoverageStatus = "out-of-scope"
	// CoverageUnmapped: the pass could not place it — surfaced loudly so a
	// requirement never falls through the decomposition silently.
	CoverageUnmapped CoverageStatus = "unmapped"
)

// CoverageEntry maps one source requirement to its disposition.
type CoverageEntry struct {
	Requirement string // the source requirement or section
	Feature     string // title of the proposal covering it (Mapped only)
	Status      CoverageStatus
	Note        string // rationale, esp. for out-of-scope / unmapped
}

// IngestResult is the full output of one ingest pass: the source it read,
// the proposed features, and the coverage map over the source.
type IngestResult struct {
	SourcePath string // where the source doc was copied (.gummi/ingest/…)
	Proposals  []FeatureProposal
	Coverage   []CoverageEntry
}

// Unmapped returns the coverage entries the pass could not place — the
// review gate flags these loudly (DESIGN §11.2).
func (r IngestResult) Unmapped() []CoverageEntry {
	var out []CoverageEntry
	for _, c := range r.Coverage {
		if c.Status == CoverageUnmapped {
			out = append(out, c)
		}
	}
	return out
}

// DraftProvenance is the ingestion metadata rendered into a seeded draft's
// header: where it came from and what it depends on. Kept separate from
// DraftSeed (which is section content) so the template can render the two
// distinctly.
type DraftProvenance struct {
	Source    string   // .gummi/ingest/<name>.md, relative to the repo root
	Refs      []string // source section headings / ranges
	DependsOn []string // resolved dependency labels, e.g. "FD-002 payment-webhooks"
}

// Empty reports whether there is no provenance to render.
func (p DraftProvenance) Empty() bool {
	return p.Source == "" && len(p.Refs) == 0 && len(p.DependsOn) == 0
}
