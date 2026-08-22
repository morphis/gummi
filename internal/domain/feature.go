package domain

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Gate approval modes: who crosses a feature's design gates. GateAuto
// auto-crosses them; GateCaller checkpoints each for the caller. The mode
// is chosen at `run` and persisted on the card so an unattended `resume`
// keeps it instead of silently reverting to auto. Empty reads as GateAuto.
// These are the canonical values; internal/driver aliases them.
const (
	GateAuto   = "auto"
	GateCaller = "caller"
)

// ValidGateApproval reports whether s is a storable gate-approval mode
// (empty, "auto", or "caller"). Empty is valid and reads as GateAuto.
func ValidGateApproval(s string) bool {
	return s == "" || s == GateAuto || s == GateCaller
}

// Kind distinguishes the units of work gummi tracks. They share the
// store, engine, worktree, and board; they differ in which workflow
// governs them (see internal/workflow) and which artifact template seeds
// them (see internal/spec). An empty Kind reads as a feature, so features
// created or scanned before bugs existed need no backfill.
type Kind string

const (
	// KindFeature is design-driven work: brainstorm → spec → plan → …
	KindFeature Kind = "feature"
	// KindBug is diagnosis-driven work: triage → diagnose → fix → …
	KindBug Kind = "bug"
	// KindResearch is investigation-driven work: investigate → shape →
	// review → verify. It is a third kind with its own workflow, a
	// dedicated artifact home, and no quick one-pass route.
	KindResearch Kind = "research"
)

// prefix is the ID prefix for a kind: FD for features, BG for bugs,
// RS for research.
func (k Kind) prefix() string {
	switch k {
	case KindBug:
		return "BG"
	case KindResearch:
		return "RS"
	}
	return "FD" // KindFeature and the empty default
}

// Valid reports whether k is a recognized kind (empty is not — callers
// that accept a default normalize it before validating).
func (k Kind) Valid() bool { return k == KindFeature || k == KindBug || k == KindResearch }

// FeatureID is a work item's identifier, e.g. "FD-042" (feature),
// "BG-007" (bug), or "RS-003" (research). IDs are minted from the
// monotonic counter in .gummi/seq — shared across kinds, so numbers
// never collide — and zero-padded to three digits.
type FeatureID string

var featureIDRe = regexp.MustCompile(`^(FD|BG|RS)-[0-9]{3,}$`)

// Kind reports the work kind an ID's prefix encodes.
func (id FeatureID) Kind() Kind {
	switch {
	case strings.HasPrefix(string(id), "BG-"):
		return KindBug
	case strings.HasPrefix(string(id), "RS-"):
		return KindResearch
	default:
		return KindFeature
	}
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

// ParseFeatureID validates s as a canonical work-item ID (feature, bug,
// or research).
func ParseFeatureID(s string) (FeatureID, error) {
	if !featureIDRe.MatchString(s) {
		return "", fmt.Errorf("invalid work item ID %q (want FD-NNN, BG-NNN, or RS-NNN)", s)
	}
	return FeatureID(s), nil
}

// Budget is a work item's spend envelope in Copilot credits
// (1 credit = $0.01): one pool every stage draws from until it runs dry
// and a human gate offers a top-up. See Remaining and RaisedEnvelope
// (plan.go) for the budget math.
type Budget struct {
	Envelope int // credits allotted for the whole feature; 0 = no cap
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

	// Decompose* mirror Credits/InputTokens/OutputTokens but count only
	// the FD-081 decompose pass's own spend, so it stays distinguishable
	// in reporting. Store.AddDecomposeSpend increments the pair (overall,
	// decompose) together, so DecomposeCreditEquivalentAt(rate) can never
	// exceed CreditEquivalentAt(rate) at any rate.
	DecomposeCredits      float64
	DecomposeInputTokens  int64
	DecomposeOutputTokens int64
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
	Kind     Kind   // feature (default), bug, or research; selects workflow + template
	Title    string // human title, free text
	OneLiner string // short description from the creation form
	Slug     string // allowlist-sanitized, used in branch and file names
	Stage    Stage
	Skip     SkipFlags
	Profile  string // profile name mapping roles to agent configs
	// GateApproval is who crosses this feature's design gates on an
	// unattended resume: GateAuto (default) or GateCaller. Persisted at
	// creation so a `resume` that doesn't re-pass --gate-approval inherits
	// the run's choice rather than reverting to auto. Empty reads as auto.
	GateApproval string
	Budget       Budget
	Spend        Spend // metered cost across all stages
	// ExternalRef ties a bug back to its source (e.g. a GitHub issue URL),
	// so re-ingesting the same source skips items already imported. Empty
	// for manually created features and bugs.
	ExternalRef string
	// Severity is the bug's impact level; empty for features and
	// unclassified bugs. It is a bug-only field (features created through
	// normal channels never carry one) but lives on the card so the board
	// can badge and sort by it without a join.
	Severity Severity
	// Repo is the configured name of the git repository this card belongs
	// to, chosen at creation and immutable thereafter. Empty names the
	// workspace's default repo (the `repo:` key when set, else the
	// workspace root), so every pre-existing row needs no migration value.
	// It is metadata for routing git operations; it never feeds a branch,
	// worktree, or spec path.
	Repo      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// VerifiedAt is stamped when the item's verify gate passes and its
	// branch becomes ready to land — the headless driver's stop-at-verified
	// terminal state (DESIGN §12). Zero until then. Distinct from reaching
	// StageDone, which a merge sets: a verified branch is ready to land, not
	// yet landed.
	VerifiedAt time.Time
	// ForkPoint is the commit SHA that was `git merge-base main <branch>`
	// when the item's worktree was created — the anchor used to detect
	// fork-point drift (main rewound past the recorded fork). Empty until
	// the worktree is created (or, for pre-existing worktrees, until the
	// first diff-based access lazily backfills it).
	ForkPoint string
	// CommitDraftFail is the durable reason a squash-merge scribe pass last
	// failed to produce a draft (a backend/config fault, a guard rejection,
	// or a timeout), persisted so the failure survives the dialog and later
	// inspection still sees it. Empty when the last pass drafted cleanly.
	CommitDraftFail string
	// PullRequest is the outbound PR this card is linked to — the mirror of
	// ExternalRef (which ties a bug back to its inbound source). Set by
	// `gummi pr link`, cleared by `gummi pr unlink`. A linked card refuses a
	// local squash-merge (see the driver/UI landing guards): a card lands
	// either via its PR or locally, never both. Empty() until linked.
	PullRequest PullRequestRef
}

// PullRequestRef records an outbound pull request a card is linked to: a
// point-in-time snapshot taken at link time, not a live view. Repo is the
// PR's own repo ("owner/repo") — which, in a fork workflow, is the upstream
// repo the PR was opened against, not necessarily the card's own configured
// Repo. HeadSHA is the PR's head commit as of link time; nothing here is
// refreshed automatically.
type PullRequestRef struct {
	Repo    string // "owner/repo"
	Number  int
	URL     string
	HeadSHA string
}

// Empty reports whether the ref carries no linked PR (all four fields at
// their zero value) — the "unlinked" state every pre-existing row reads as.
func (r PullRequestRef) Empty() bool {
	return r.Repo == "" && r.Number == 0 && r.URL == "" && r.HeadSHA == ""
}

var (
	prRefRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	prRefSHARe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Validate checks a non-empty ref's shape: Repo must be "owner/repo" (never
// a bare name), Number must be a real PR number, URL must be a github.com
// link, and HeadSHA, when set, must be a 40-hex-char SHA. An Empty ref is
// always legal and is never passed here by callers that check first.
func (r PullRequestRef) Validate() error {
	if !prRefRepoRe.MatchString(r.Repo) {
		return fmt.Errorf("pull request repo %q is not in owner/repo form", r.Repo)
	}
	if r.Number < 1 {
		return fmt.Errorf("pull request number must be >= 1, got %d", r.Number)
	}
	if !strings.HasPrefix(r.URL, "https://github.com/") {
		return fmt.Errorf("pull request URL %q does not start with https://github.com/", r.URL)
	}
	if r.HeadSHA != "" && !prRefSHARe.MatchString(r.HeadSHA) {
		return fmt.Errorf("pull request head SHA %q is not a 40-hex-char SHA", r.HeadSHA)
	}
	return nil
}

// kind returns the feature's kind, treating the empty default as a
// feature so items predating bugs read correctly.
func (f *Feature) kind() Kind {
	switch f.Kind {
	case KindBug:
		return KindBug
	case KindResearch:
		return KindResearch
	default:
		return KindFeature
	}
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
// repo root: a feature's spec (.gummi/specs/…), a bug's report
// (.gummi/bugs/…), or a research item's artifact (.gummi/research/…).
// All live in the main checkout's gummi workspace — never in the
// worktree, never committed — and are what the stage agents read and
// write.
func (f *Feature) ArtifactPath() string {
	switch f.kind() {
	case KindBug:
		return path.Join(".gummi", "bugs", string(f.ID)+"-"+f.Slug+".md")
	case KindResearch:
		return path.Join(".gummi", "research", string(f.ID)+"-"+f.Slug+".md")
	default:
		return f.SpecPath()
	}
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
	if f.Budget.Envelope < 0 {
		return fmt.Errorf("feature %s: negative budget", f.ID)
	}
	if !ValidGateApproval(f.GateApproval) {
		return fmt.Errorf("feature %s: unknown gate-approval mode %q", f.ID, f.GateApproval)
	}
	// Repo, when set, must be a plain configured name: no whitespace and no
	// path separators, so it can never be mistaken for a path and can never
	// smuggle a repo root outside the configured set. Empty is always legal
	// (it names the workspace default).
	if strings.TrimSpace(f.Repo) != f.Repo || strings.Contains(f.Repo, "/") || strings.Contains(f.Repo, "\\") {
		return fmt.Errorf("feature %s: repo name %q is not a plain configured name", f.ID, f.Repo)
	}
	if !f.PullRequest.Empty() {
		if err := f.PullRequest.Validate(); err != nil {
			return fmt.Errorf("feature %s: %w", f.ID, err)
		}
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

// SplitFreeform splits a free-form, possibly multi-line description
// into a card title, a card one-liner, and a draft seed. The title and
// one-liner derive from the first non-blank line exactly as
// SplitDescription derives them from a single-line description. When
// the description carries anything beyond that line, the full text —
// verbatim, newlines intact — is returned as seed so creation can
// pre-fill the draft's Problem section: the card stores only
// line-sized text, the paragraphs live in the artifact.
func SplitFreeform(desc string) (title, oneLiner, seed string) {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	desc = strings.ReplaceAll(desc, "\r", "\n")
	desc = strings.TrimSpace(desc)
	first, rest, _ := strings.Cut(desc, "\n")
	title, oneLiner = SplitDescription(first)
	if strings.TrimSpace(rest) != "" {
		seed = desc
	}
	return title, oneLiner, seed
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
