package domain

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Kind distinguishes the two units of work gummi tracks. Both share the
// store, engine, worktree, and board; they differ only in which workflow
// governs them (see internal/workflow) and which artifact template seeds
// them (see internal/spec). An empty Kind reads as a feature, so features
// created or scanned before bugs existed need no backfill.
type Kind string

const (
	// KindFeature is design-driven work: brainstorm → spec → plan → …
	KindFeature Kind = "feature"
	// KindBug is diagnosis-driven work: triage → diagnose → fix → …
	KindBug Kind = "bug"
)

// prefix is the ID prefix for a kind: FD for features, BG for bugs.
func (k Kind) prefix() string {
	if k == KindBug {
		return "BG"
	}
	return "FD" // KindFeature and the empty default
}

// Valid reports whether k is a recognized kind (empty is not — callers
// that accept a default normalize it before validating).
func (k Kind) Valid() bool { return k == KindFeature || k == KindBug }

// FeatureID is a work item's identifier, e.g. "FD-042" (feature) or
// "BG-007" (bug). IDs are minted from the monotonic counter in
// .gummi/seq — shared across kinds, so numbers never collide — and
// zero-padded to three digits.
type FeatureID string

var featureIDRe = regexp.MustCompile(`^(FD|BG)-[0-9]{3,}$`)

// Kind reports the work kind an ID's prefix encodes.
func (id FeatureID) Kind() Kind {
	if strings.HasPrefix(string(id), "BG-") {
		return KindBug
	}
	return KindFeature
}

// NewID builds the canonical ID for kind and sequence number n (n >= 1).
func NewID(kind Kind, n int) (FeatureID, error) {
	if n < 1 {
		return "", fmt.Errorf("work item number must be >= 1, got %d", n)
	}
	return FeatureID(fmt.Sprintf("%s-%03d", kind.prefix(), n)), nil
}

// NewFeatureID builds the canonical feature ID for sequence number n.
func NewFeatureID(n int) (FeatureID, error) { return NewID(KindFeature, n) }

// ParseFeatureID validates s as a canonical work-item ID (feature or bug).
func ParseFeatureID(s string) (FeatureID, error) {
	if !featureIDRe.MatchString(s) {
		return "", fmt.Errorf("invalid work item ID %q (want FD-NNN or BG-NNN)", s)
	}
	return FeatureID(s), nil
}

// Budget is a work item's spend envelope in Copilot credits
// (1 credit = $0.01): one pool every stage draws from until it runs dry
// and a human gate offers a top-up. See Remaining and RaisedEnvelope
// (plan.go) for the budget math.
type Budget struct {
	Envelope int // credits allotted for the whole feature; 0 = no cap
	Spent    int // credits consumed so far
}

// Spend is a feature's metered cost, accumulated across every stage's
// agent sessions. Credits meter Copilot-hosted usage; tokens meter BYOK
// (each convertible to display dollars in a later milestone).
// EstimatedCredits is the portion of Credits that was derived from token
// counts (a usage event that carried no provider-reported cost) rather
// than metered by the provider — displays label such figures as estimates
// instead of presenting them as real cost.
type Spend struct {
	Credits          float64
	EstimatedCredits float64 // token-derived subset of Credits
	InputTokens      int64
	OutputTokens     int64
}

// Add accumulates another usage sample.
func (s *Spend) Add(credits float64, in, out int64) {
	s.Credits += credits
	s.InputTokens += in
	s.OutputTokens += out
}

// Estimated reports whether any of the credit figure is token-derived
// rather than provider-metered.
func (s Spend) Estimated() bool { return s.EstimatedCredits > 0 }

// Zero reports whether nothing has been metered.
func (s Spend) Zero() bool {
	return s.Credits == 0 && s.InputTokens == 0 && s.OutputTokens == 0
}

// Feature is one unit of work: the kanban card, its workflow position,
// and everything needed to derive its branch, worktree, and spec paths.
type Feature struct {
	ID       FeatureID
	Num      int    // numeric part of ID, unique
	Kind     Kind   // feature (default) or bug; selects workflow + template
	Title    string // human title, free text
	OneLiner string // short description from the creation form
	Slug     string // allowlist-sanitized, used in branch and file names
	Stage    Stage
	Skip     SkipFlags
	Profile  string // profile name mapping roles to agent configs
	Budget   Budget
	Spend    Spend // metered cost across all stages
	// ExternalRef ties a bug back to its source (e.g. a GitHub issue URL),
	// so re-ingesting the same source skips items already imported. Empty
	// for manually created features and bugs.
	ExternalRef string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Kind returns the feature's kind, treating the empty default as a
// feature so items predating bugs read correctly.
func (f *Feature) kind() Kind {
	if f.Kind == KindBug {
		return KindBug
	}
	return KindFeature
}

// BranchName is the feature's git branch: gummi/FD-042-slug.
func (f *Feature) BranchName() string {
	return "gummi/" + string(f.ID) + "-" + f.Slug
}

// WorktreePath is the feature's worktree directory relative to the
// repo root: .gummi/worktrees/FD-042.
func (f *Feature) WorktreePath() string {
	return path.Join(".gummi", "worktrees", string(f.ID))
}

// SpecPath is the feature's spec file relative to the repo root:
// .gummi/specs/FD-042-slug.md.
func (f *Feature) SpecPath() string {
	return path.Join(".gummi", "specs", string(f.ID)+"-"+f.Slug+".md")
}

// ArtifactPath is the item's durable design artifact relative to the
// repo root: a feature's spec (.gummi/specs/…) or a bug's report
// (.gummi/bugs/…). Both live in the main checkout's gummi workspace —
// never in the worktree, never committed — and are what the stage
// agents read and write.
func (f *Feature) ArtifactPath() string {
	if f.kind() == KindBug {
		return path.Join(".gummi", "bugs", string(f.ID)+"-"+f.Slug+".md")
	}
	return f.SpecPath()
}

// Validate checks the invariants every stored feature must satisfy.
func (f *Feature) Validate() error {
	if _, err := ParseFeatureID(string(f.ID)); err != nil {
		return err
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return fmt.Errorf("feature %s: unknown kind %q", f.ID, f.Kind)
	}
	want, err := NewID(f.kind(), f.Num)
	if err != nil {
		return fmt.Errorf("feature %s: %w", f.ID, err)
	}
	if want != f.ID {
		return fmt.Errorf("feature %s: ID %s does not match kind %s / number %d", f.ID, f.ID, f.kind(), f.Num)
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("feature %s: title is empty", f.ID)
	}
	if err := ValidateSlug(f.Slug); err != nil {
		return fmt.Errorf("feature %s: %w", f.ID, err)
	}
	if !f.Stage.Valid() {
		return fmt.Errorf("feature %s: unknown stage %q", f.ID, f.Stage)
	}
	if f.Budget.Envelope < 0 || f.Budget.Spent < 0 {
		return fmt.Errorf("feature %s: negative budget", f.ID)
	}
	return nil
}

const maxSlugLen = 40

// maxTitleLen bounds a derived card title so a long description doesn't
// become the whole title (the full text is kept in OneLiner).
const maxTitleLen = 60

// DeriveTitle reduces a free-text description to a concise card title:
// its first sentence, or the first maxTitleLen characters on a word
// boundary if that runs long, whichever is shorter. The full description
// is preserved separately (a feature's OneLiner). A short description is
// returned unchanged, so it is its own title with no OneLiner needed
// (SplitDescription reports when the two differ).
func DeriveTitle(desc string) string {
	desc = strings.TrimSpace(strings.Join(strings.Fields(desc), " "))
	if desc == "" {
		return ""
	}
	// first sentence: cut at the first ., !, or ? followed by space or end
	if i := firstSentenceEnd(desc); i > 0 && i < len(desc) {
		desc = strings.TrimRight(strings.TrimSpace(desc[:i]), ".!?")
	}
	if len(desc) <= maxTitleLen {
		return desc
	}
	// too long: truncate on a word boundary within the budget, adding an
	// ellipsis so the cut is visible
	cut := desc[:maxTitleLen]
	if sp := strings.LastIndexByte(cut, ' '); sp > maxTitleLen/2 {
		cut = cut[:sp]
	}
	return strings.TrimRight(cut, " ,;:-") + "…"
}

// SplitDescription derives a card title and reports the full description
// as a one-liner only when it carries more than the title does — so a
// short, single-sentence description isn't stored twice.
func SplitDescription(desc string) (title, oneLiner string) {
	desc = strings.TrimSpace(strings.Join(strings.Fields(desc), " "))
	title = DeriveTitle(desc)
	if title != desc {
		oneLiner = desc
	}
	return title, oneLiner
}

// firstSentenceEnd returns the index just past the first sentence
// terminator (., !, ?) that is followed by whitespace or the string end,
// or -1 when there is none. A terminator glued to the next character
// (a decimal, a version, "e.g.") does not end the sentence.
func firstSentenceEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '!', '?':
			if i+1 == len(s) || s[i+1] == ' ' {
				return i + 1
			}
		}
	}
	return -1
}

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	nonSlugRune = regexp.MustCompile(`[^a-z0-9]+`)
)

// Slugify derives a branch- and filename-safe slug from a feature
// title: lowercase, [a-z0-9-] only, single dashes, max 40 chars.
// Titles that yield an empty slug (e.g. all punctuation) are an error —
// slugs flow into git branch names and paths, so gummi refuses to
// invent one silently.
func Slugify(title string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(title))
	s = nonSlugRune.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		return "", fmt.Errorf("title %q yields an empty slug; use at least one ASCII letter or digit", title)
	}
	return s, nil
}

// ValidateSlug enforces the slug allowlist ([a-z0-9] with single
// dashes). Everything that reaches a branch name, worktree path, or
// spec filename must pass this.
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("slug is empty")
	}
	if len(s) > maxSlugLen {
		return fmt.Errorf("slug %q exceeds %d chars", s, maxSlugLen)
	}
	if !slugRe.MatchString(s) {
		return fmt.Errorf("slug %q contains characters outside [a-z0-9-]", s)
	}
	return nil
}
