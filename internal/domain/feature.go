package domain

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// FeatureID is a feature's identifier, e.g. "FD-042". IDs are minted
// from the monotonic counter in .gummi/seq and zero-padded to three
// digits.
type FeatureID string

var featureIDRe = regexp.MustCompile(`^FD-[0-9]{3,}$`)

// NewFeatureID builds the canonical ID for sequence number n (n >= 1).
func NewFeatureID(n int) (FeatureID, error) {
	if n < 1 {
		return "", fmt.Errorf("feature number must be >= 1, got %d", n)
	}
	return FeatureID(fmt.Sprintf("FD-%03d", n)), nil
}

// ParseFeatureID validates s as a canonical feature ID.
func ParseFeatureID(s string) (FeatureID, error) {
	if !featureIDRe.MatchString(s) {
		return "", fmt.Errorf("invalid feature ID %q (want FD-NNN)", s)
	}
	return FeatureID(s), nil
}

// Budget is a feature's spend envelope in Copilot credits
// (1 credit = $0.01). Per-stage allocation and metering land in M3;
// M0 records the envelope and a running total.
type Budget struct {
	Envelope int // credits allotted for the whole feature; 0 = no cap
	Spent    int // credits consumed so far
}

// Feature is one unit of work: the kanban card, its workflow position,
// and everything needed to derive its branch, worktree, and spec paths.
type Feature struct {
	ID        FeatureID
	Num       int    // numeric part of ID, unique
	Title     string // human title, free text
	OneLiner  string // short description from the creation form
	Slug      string // allowlist-sanitized, used in branch and file names
	Stage     Stage
	Skip      SkipFlags
	Profile   string // profile name mapping roles to agent configs
	Budget    Budget
	CreatedAt time.Time
	UpdatedAt time.Time
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

// Validate checks the invariants every stored feature must satisfy.
func (f *Feature) Validate() error {
	if _, err := ParseFeatureID(string(f.ID)); err != nil {
		return err
	}
	want, err := NewFeatureID(f.Num)
	if err != nil {
		return fmt.Errorf("feature %s: %w", f.ID, err)
	}
	if want != f.ID {
		return fmt.Errorf("feature %s: ID does not match number %d", f.ID, f.Num)
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
