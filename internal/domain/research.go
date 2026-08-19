package domain

// Research cards are the third kind of work item (KindResearch). They run
// an investigation-driven workflow (investigate → shape) and deliver an
// approved research document instead of a branch. The types here are the
// structured input a creation source produces, mirroring DraftSeed /
// BugReport for the ingest pipeline.

// ResearchSeed is the content a creation form can seed into a research
// document — the two sections that belong to the request itself: the
// brief (the ask, verbatim) and the questions it raises. Everything else
// (Findings, Constraints, Options, Direction, Slices, Out of scope, Open
// risks, Review) is left open for the investigate/shape stages to
// converge on; the seed never pre-empts that work. Empty fields keep the
// template's %% prompt.
type ResearchSeed struct {
	Brief     string   // → ## Brief, verbatim (marker-neutralized)
	Questions []string // → one bullet + %% thread per item under ## Questions
}
