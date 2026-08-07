package domain

import "strings"

// Bugs are the second kind of work item (KindBug). They share the store,
// engine, worktree, and board with features but run a diagnosis-driven
// workflow (triage → diagnose → fix → review → verify) and carry a bug
// report instead of a spec. The types here are the structured input a
// bug source (GitHub, manual, …) produces — proposals, not yet minted
// items — mirroring FeatureProposal / DraftSeed for the ingest pipeline.

// Severity ranks a bug's impact. It seeds the report header and, later,
// can order the backlog. Empty means the source did not classify it.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// NormalizeSeverity maps a free-form label ("P0", "sev1", "blocker", …)
// onto a canonical severity, returning "" when nothing recognizable is
// present so an unclassified bug reads as unset rather than mis-ranked.
func NormalizeSeverity(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "crit", "sev0", "sev1", "s1", "p0", "blocker", "urgent":
		return SeverityCritical
	case "high", "sev2", "s2", "p1", "major":
		return SeverityHigh
	case "medium", "med", "sev3", "s3", "p2", "moderate", "normal":
		return SeverityMedium
	case "low", "sev4", "s4", "p3", "minor", "trivial":
		return SeverityLow
	default:
		return ""
	}
}

// BugReport is the content seeded into a bug's report sections — the
// symptoms a source can supply. The *root cause* and *fix* sections are
// never seeded: converging on why and how is diagnose/fix work, just as
// the spec's chosen-approach is brainstorm's (DESIGN §11.2's principle,
// applied to bugs). Empty fields keep the template's %% prompt.
type BugReport struct {
	Description   string   // → ## Summary
	Reproduction  string   // → ## Reproduction
	Expected      string   // → ## Expected vs actual (expected)
	Actual        string   // → ## Expected vs actual (actual)
	Environment   string   // → ## Environment
	Discussion    string   // → ## Discussion (issue comments; rendered only when non-empty)
	OpenQuestions []string // → %% markers under Summary (triage's checklist)
}

// BugProvenance records where a bug came from, rendered into the report
// header: the source name and the external reference (e.g. a GitHub
// issue URL). Kept separate from BugReport (section content) so the
// template renders the two distinctly, mirroring DraftProvenance.
type BugProvenance struct {
	Source      string // "github", "manual", …
	ExternalRef string // e.g. https://github.com/o/r/issues/42
}

// Empty reports whether there is no provenance to render.
func (p BugProvenance) Empty() bool { return p.Source == "" && p.ExternalRef == "" }

// BugProposal is one candidate bug a source emits — everything needed to
// mint a bug and seed its report. It parallels FeatureProposal; there is
// no coverage map because sources yield discrete, already-separated bugs
// rather than one document to decompose.
type BugProposal struct {
	Title       string    // → Feature.Title (and, slugified, its ID's slug)
	OneLiner    string    // → Feature.OneLiner
	Source      string    // source name, e.g. "github" / "manual"
	ExternalRef string    // → Feature.ExternalRef; dedup key + provenance
	Severity    Severity  // impact, seeded into the report header
	Skip        SkipFlags // suggested skip flags (Triage/Diagnose)
	Report      BugReport
}

// Slug derives the proposal's branch/filename slug from its title, with
// the same rules feature creation uses. A proposal whose title yields no
// slug can't be materialized.
func (p BugProposal) Slug() (string, error) { return Slugify(p.Title) }

// Provenance is the header metadata seeded into the bug's report.
func (p BugProposal) Provenance() BugProvenance {
	return BugProvenance{Source: p.Source, ExternalRef: p.ExternalRef}
}
