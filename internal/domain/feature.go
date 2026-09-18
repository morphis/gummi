package domain

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gate approval modes: who crosses a feature's gates on an unattended
// resume. There are two, and the axis is "how much do I trust this card"
// rather than a per-gate policy:
//
//   - GateAttended stops at every gate. The stage asks whether to
//     advance; you review, comment, and answer. This is the default when
//     the field is empty.
//   - GateAutopilot stops at none. It runs all the way to a verified
//     branch on its own — gates, critique bounces and conflict handoffs
//     all auto-cross — and stops there: it never lands on main by itself.
//
// This replaces three modes with two, and the default gets STRICTER in
// the process. The retired middle mode ("gates") auto-crossed design
// gates while still stopping for a mid-stage question, and it was the
// default; attended stops at all of them. The trade is that each stop is
// a question in the conversation rather than a control to go find.
//
// The mode is chosen at `run` and persisted on the card so an unattended
// `resume` keeps it instead of silently reverting to the default. Empty
// reads as GateAttended. These are the canonical, stored values — the
// only ones ValidGateApproval accepts. The spellings that came before
// them ("off", "gates", "caller", "auto" → attended; "full" →
// autopilot) are still accepted as INPUT through NormalizeGateApproval,
// so existing scripts keep working and old rows migrate, but they are
// never stored and nothing branches on them.
const (
	GateAttended  = "attended"
	GateAutopilot = "autopilot"
)

// ValidGateApproval reports whether s is a storable gate-approval mode:
// empty, "attended", or "autopilot". Empty is valid and reads as
// GateAttended. It does not accept the retired input spellings — those
// are resolved to their canonical form by NormalizeGateApproval before
// anything reaches storage or Validate.
func ValidGateApproval(s string) bool {
	return s == "" || s == GateAttended || s == GateAutopilot
}

// NormalizeGateApproval resolves a gate-approval mode as given by a
// caller (CLI flag, MCP tool argument, a persisted row written by an
// older version) to its canonical stored form, reporting false when s is
// not a recognized mode or alias. It is the ONE place a retired spelling
// is resolved, which is what lets the three-mode era migrate without a
// separate migration: every old value has an unambiguous new meaning.
//
// "off" and "caller" were "stop at every gate", which is attended.
// "gates" and "auto" were "cross design gates but stop for a question";
// they map to attended too, and that is the one place this collapse
// loses something — a card stored at "gates" will now stop where it used
// to walk. That is the deliberate default change, not an accident of the
// mapping. "full" was "stop at nothing until the branch is verified",
// which is autopilot exactly.
//
// "" passes through unchanged — the empty string carries no default
// here; the caller decides what empty means for it (persisted rows and
// Feature.GateApproval read it as GateAttended).
func NormalizeGateApproval(s string) (string, bool) {
	switch s {
	case "":
		return "", true
	case "off", "caller", "gates", "auto", GateAttended:
		return GateAttended, true
	case "full", GateAutopilot:
		return GateAutopilot, true
	default:
		return "", false
	}
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
	// KindResearch is question-driven work on the shared graph: the
	// design stage shapes the research question and direction, the build
	// stage surveys and writes the document up. It is a third kind with
	// a dedicated artifact home and no quick one-pass route.
	KindResearch Kind = "research"
	// KindGoal is outcome-driven work: a card whose work is other cards.
	// The design stage agrees the objective and what "done" means; the
	// build stage is conducted rather than written — the goal's cards run
	// on a shared goal branch and land there one commit each — and verify
	// checks the combined branch. It walks the same graph as every kind.
	KindGoal Kind = "goal"
)

// prefix is the ID prefix for a kind: FD for features, BG for bugs,
// RS for research.
func (k Kind) prefix() string {
	switch k {
	case KindBug:
		return "BG"
	case KindResearch:
		return "RS"
	case KindGoal:
		return "GL"
	}
	return "FD" // KindFeature and the empty default
}

// Branch-name schemes. A card stores which one it was minted under, so
// changing the default never renames an existing branch.
const (
	// BranchSchemeGummi is the original `gummi/<ID>-<slug>` spelling. It
	// is the empty value so every card minted before schemes existed
	// reads as this one with no backfill.
	BranchSchemeGummi = ""
	// BranchSchemeKind spells the branch with a prefix naming the kind of
	// work — `feat/`, `bug/`, `goal/` — which is the convention most
	// repos, changelog tools and PR templates already expect.
	BranchSchemeKind = "kind"
)

// DefaultBranchScheme is what a newly minted card gets.
const DefaultBranchScheme = BranchSchemeKind

// branchPrefix is the branch-name prefix for a kind under
// BranchSchemeKind. Research has no branch at all (it runs in a detached
// scratch tree), so its value is never spelled onto a ref; it is defined
// only so the function is total.
func (k Kind) branchPrefix() string {
	switch k {
	case KindBug:
		return "bug"
	case KindResearch:
		return "research"
	case KindGoal:
		return "goal"
	}
	return "feat" // KindFeature and the empty default
}

// ArtifactNoun names the design artifact this kind of work item carries,
// in the word the rest of that kind uses for it: a bug's report, a
// research card's research document, a feature's spec. It lives here,
// rather than in whichever package needs it, because both the agent's
// stage kickoff and every surface the reader sees must call the document
// the same thing — a document renamed between the instruction sent to
// the agent and the line the reader opens it from is the same defect in
// two places (BG-079, BG-081).
//
// The default is deliberately the feature wording rather than a generic
// one: "spec" is what a feature card's own template, gates and hints
// say, and a neutral word here would make every one of those surfaces
// disagree with the artifact itself.
func (k Kind) ArtifactNoun() string {
	switch k {
	case KindBug:
		return "bug report"
	case KindResearch:
		return "research document"
	case KindGoal:
		return "goal doc"
	}
	return "spec" // KindFeature and the empty default
}

// Valid reports whether k is a recognized kind (empty is not — callers
// that accept a default normalize it before validating).
func (k Kind) Valid() bool {
	return k == KindFeature || k == KindBug || k == KindResearch || k == KindGoal
}

// FeatureID is a work item's identifier, e.g. "FD-042" (feature),
// "BG-007" (bug), "RS-003" (research), or "GL-004" (goal). IDs are minted from the
// monotonic counter in .gummi/seq — shared across kinds, so numbers
// never collide — and zero-padded to three digits.
type FeatureID string

var featureIDRe = regexp.MustCompile(`^(FD|BG|RS|GL)-[0-9]{3,}$`)

// Kind reports the work kind an ID's prefix encodes.
func (id FeatureID) Kind() Kind {
	switch {
	case strings.HasPrefix(string(id), "BG-"):
		return KindBug
	case strings.HasPrefix(string(id), "RS-"):
		return KindResearch
	case strings.HasPrefix(string(id), "GL-"):
		return KindGoal
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
// research, or goal).
func ParseFeatureID(s string) (FeatureID, error) {
	if !featureIDRe.MatchString(s) {
		return "", fmt.Errorf("invalid work item ID %q (want FD-NNN, BG-NNN, RS-NNN, or GL-NNN)", s)
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
	ID   FeatureID
	Num  int  // numeric part of ID, unique
	Kind Kind // feature (default), bug, research, or goal; selects the stage contracts + template
	// Mode refines KindResearch into the survey (empty) or the diagnosis
	// contract — see ResearchMode. Always empty for every other kind:
	// nothing else has a second contract to choose between, and a mode
	// stored on a feature would be a value readers have to ignore.
	Mode     ResearchMode
	Title    string // human title, free text
	OneLiner string // short description from the creation form
	Slug     string // allowlist-sanitized, used in branch and file names
	Stage    Stage
	Profile  string // profile name mapping roles to agent configs
	// GateApproval is who crosses this card's gates on an unattended
	// resume: GateAttended (default) or GateAutopilot.
	// Persisted at creation so a `resume` that doesn't re-pass
	// --gate-approval inherits the run's choice rather than reverting to
	// the default. Empty reads as GateAttended.
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
	// to, chosen at creation and changeable until the card cuts a worktree.
	// Empty names the workspace's default repo (the `repo:` key when set,
	// else the workspace root), so every pre-existing row needs no migration
	// value. It is metadata for routing git operations; it never feeds a
	// branch, worktree, or spec path.
	Repo string
	// Base is the git branch this card's work forks from and lands on,
	// chosen at creation and changeable until the card cuts a worktree.
	//
	// Empty means "whatever the managed checkout has out" — which is what
	// every card did before bases were selectable, so no pre-existing row
	// needs a migration value and an empty base reproduces the old
	// behavior exactly. A stacked card at a position above the bottom
	// ignores this field: its base is the branch of the card below it,
	// resolved live (see BranchScheme's note on why the scheme is stored
	// but the branch name is not).
	Base string
	// BranchScheme is how this card's branch name was spelled when the
	// card was minted. Empty is the original `gummi/<ID>-<slug>` scheme;
	// BranchSchemeKind is the per-kind spelling (`feat/`, `bug/`, `goal/`).
	//
	// It is the scheme and not the rendered name because the name is
	// derivable from (scheme, ID, slug) and a derivable fact gets a method
	// — but the scheme in force at minting is NOT recoverable afterwards,
	// since it changes with the default. Storing it is what lets the
	// spelling change for new cards without renaming a branch that already
	// exists in someone's checkout.
	BranchScheme string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// VerifiedAt is stamped when the item's verify gate passes and its
	// branch becomes ready to land — the headless driver's stop-at-verified
	// terminal state (DESIGN §12). Zero until then. Distinct from reaching
	// StageDone, which a merge sets: a verified branch is ready to land, not
	// yet landed.
	VerifiedAt time.Time
	// HandedOffAt is stamped when someone ends a card WITHOUT landing it:
	// the card moves to done and its branch stays where it is, theirs to
	// push, PR, cherry-pick or sit on. Zero on every other card.
	//
	// It is one stored fact rather than an ending enum because the other
	// endings already have their own records: a local landing is
	// LandedSHA, a PR landing is PullRequest plus whatever main says, and
	// a card that was simply dropped carries none of the three. Together
	// they answer "how did this end" for any done card, which is the
	// question a done card could not answer at all while Done and "the
	// squash merge happened" were the same event.
	HandedOffAt time.Time
	// ForkPoint is the commit SHA that was `git merge-base main <branch>`
	// when the item's worktree was created — the anchor used to detect
	// fork-point drift (main rewound past the recorded fork). Empty until
	// the worktree is created (or, for pre-existing worktrees, until the
	// first diff-based access lazily backfills it).
	ForkPoint string
	// LandedSHA is the squash commit SquashMerge created when this
	// feature's branch actually landed on main, persisted at merge time.
	// Empty until gummi performs that merge. It is the lineage record
	// Landed's squash route tests ancestry against, so a branch whose
	// content merely coincides with main by some other route (a sibling
	// card, a cherry-pick, an independently identical fix) is never
	// misread as landed.
	LandedSHA string
	// CommitDraftFail is the durable reason a squash-merge scribe pass last
	// failed to produce a draft (a backend/config fault, a guard rejection,
	// or a timeout), persisted so the failure survives the dialog and later
	// inspection still sees it. Empty when the last pass drafted cleanly.
	CommitDraftFail string
	// CommitDraft is a landing commit message composed ahead of the
	// landing itself — at the moment verify passed and the card parked at
	// its landing gate, where nobody is waiting on the answer. The merge
	// dialog opens on it instead of on an empty box behind a ~60s pass.
	// It is a DRAFT in the same sense as the one the dialog used to make
	// at the keypress: the human still reviews, edits and approves it, and
	// nothing lands except on an explicit ctrl+s.
	CommitDraft string
	// CommitDraftSHA is the branch tip CommitDraft was composed against.
	// It is the staleness guard, and the reason the draft can be trusted
	// at all: a branch that moved after the draft was written (a rebase, a
	// post-verify fix, the merge flow's own final checkpoint) describes
	// work the draft never saw, so the dialog ignores the stored draft and
	// runs the live pass exactly as it did before. Empty with CommitDraft.
	CommitDraftSHA string
	// PullRequest is the outbound PR this card is linked to — the mirror of
	// ExternalRef (which ties a bug back to its inbound source). Set by
	// `gummi pr link`, cleared by `gummi pr unlink`. A linked card refuses a
	// local squash-merge (see the driver/UI landing guards): a card lands
	// either via its PR or locally, never both. Empty() until linked.
	PullRequest PullRequestRef
	// GoalID is the goal this card belongs to, empty for a card on the open
	// board. A goal card's branch forks from the goal's branch and lands
	// back on it; its budget is carved out of the goal's. Goals do not
	// nest, so a goal never carries one.
	GoalID FeatureID
	// GoalAttached marks a card that existed before its goal and was handed
	// to it, rather than created by it. Dropping an attached card returns
	// it to the board with its work kept; dropping one the goal created
	// closes it inside the goal.
	GoalAttached bool
	// GoalDroppedAt is stamped when a goal drops this card: it stays where
	// it stopped, spends nothing more, and no longer counts toward the
	// goal. Zero on every card a goal has not dropped.
	GoalDroppedAt time.Time
	// FoundBy names the CARD this one was filed from — provenance, not
	// dependency: nothing about it blocks, schedules or orders work.
	//
	// It was written only by a goal at first ("found along the way": a
	// goal filing something real but outside its objective, as an
	// open-board card it never works), because a goal was the only thing
	// that ever filed a card. A finished card's follow-up is the second,
	// and it means exactly the same thing — this came out of that.
	FoundBy FeatureID
	// Goal holds the settings only a goal card carries. Zero on every
	// other kind.
	Goal GoalSettings
	// StackID is the stack this card sits in, empty for a card that is not
	// stacked. A stack is TOPOLOGY, not scheduling: it says this card's
	// branch forks from the branch of the card below it, and it never
	// blocks the card from running. Every member of a stack shares one
	// repo, because a branch cannot fork from a branch in another one.
	//
	// This is deliberately not a dependency edge. A dependency is met only
	// at StageDone, so a position that implied one would stop every card
	// above the bottom from entering its coding stage until the card below
	// had landed — serializing exactly the parallel work a stack is for.
	// Dependencies remain available alongside a stack for the rare card
	// that genuinely cannot start yet.
	StackID StackID
	// StackPos is this card's place in its stack, 0 at the bottom.
	// Contiguous within a stack and meaningless when StackID is empty.
	// The card at position 0 forks from its own Base; the card at N forks
	// from the branch of the card at N-1, so "exactly one predecessor" is
	// structural rather than a rule that could be violated.
	StackPos int
}

// GoalSettings are the goal-only fields of a card: how many of its cards
// may run at once, the reserve the lead holds back, and how it is ending.
type GoalSettings struct {
	// Lanes is how many of the goal's cards may run at the same time. 0
	// reads as DefaultGoalLanes.
	Lanes int
	// Reserve is the lead's current estimate, in credits, of what finishing
	// cleanly costs. 0 means the lead has not estimated yet and the default
	// formula (DefaultGoalReserve) applies.
	Reserve int
	// WrapUpAt is stamped when the goal must finish now — you stopped it,
	// its budget reached the reserve, or its lead kept failing. Nothing new
	// starts after it; verified work lands and the rest is dropped.
	WrapUpAt time.Time
	// Partial is why a finished goal is partial, empty while it is whole.
	Partial string
}

// DefaultGoalLanes is the lane count a goal gets when its plan names none.
const DefaultGoalLanes = 2

// LaneCount returns the goal's lanes with the default resolved.
func (g GoalSettings) LaneCount() int {
	if g.Lanes <= 0 {
		return DefaultGoalLanes
	}
	return g.Lanes
}

// WrappingUp reports whether the goal has been told to finish now.
func (g GoalSettings) WrappingUp() bool { return !g.WrapUpAt.IsZero() }

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

// Badge is the compact board marker for a linked PR: "PR#42". Empty for an
// unlinked ref, so callers can append it unconditionally.
func (r PullRequestRef) Badge() string {
	if r.Empty() {
		return ""
	}
	return "PR#" + strconv.Itoa(r.Number)
}

// PlainLine is the "owner/repo#N" rendering used by the plain-text `gummi
// status` line and any other owner/repo#N render — the single source for
// that shape, so it never drifts between call sites. Empty for an unlinked
// ref.
func (r PullRequestRef) PlainLine() string {
	if r.Empty() {
		return ""
	}
	return r.Repo + "#" + strconv.Itoa(r.Number)
}

// NextStepsHint is the nextsteps line for a verified, linked card: "PR #42 —
// merge on GitHub, then pull main". Empty when the ref is unlinked or the
// card hasn't verified yet, so the caller falls back to its existing
// wording.
func (r PullRequestRef) NextStepsHint(verified bool) string {
	if r.Empty() || !verified {
		return ""
	}
	return "PR #" + strconv.Itoa(r.Number) + " — merge on GitHub, then pull main"
}

// pullRequestPayload mirrors PullRequestRef for the status --json and driver
// done-event wire shapes. Field order is declaration order (encoding/json
// serializes in that order), pinned here to repo, number, url, head_sha.
type pullRequestPayload struct {
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	URL     string `json:"url"`
	HeadSHA string `json:"head_sha"`
}

// StatusPayload is the wire shape for a linked ref — used verbatim by both
// `status --json`'s `pull_request` object and the driver's `done` event, so
// the two surfaces stay identical by construction. nil for an unlinked ref,
// so an `any`-typed `omitempty` field drops it off the wire.
func (r PullRequestRef) StatusPayload() any {
	if r.Empty() {
		return nil
	}
	return &pullRequestPayload{Repo: r.Repo, Number: r.Number, URL: r.URL, HeadSHA: r.HeadSHA}
}

// kind returns the feature's kind, treating the empty default as a
// feature so items predating bugs read correctly.
func (f *Feature) kind() Kind {
	switch f.Kind {
	case KindBug:
		return KindBug
	case KindResearch:
		return KindResearch
	case KindGoal:
		return KindGoal
	default:
		return KindFeature
	}
}

// IsGoal reports whether the card is a goal.
func (f *Feature) IsGoal() bool { return f.kind() == KindGoal }

// InGoal reports whether the card belongs to a goal.
func (f *Feature) InGoal() bool { return f.GoalID != "" }

// GoalDropped reports whether the card's goal dropped it.
func (f *Feature) GoalDropped() bool { return !f.GoalDroppedAt.IsZero() }

// GateMode returns the feature's gate-approval mode with the empty
// default resolved, the same job kind() does for Kind. Every read of
// GateApproval that branches on the mode must go through this: the field
// is documented as "empty reads as GateAttended" and ValidGateApproval
// accepts empty as storable, so a bare `f.GateApproval == GateAttended`
// silently classifies an unset card as autopilot. That is exactly what
// engine.lanePoolFor did — every card minted by `bugs new` stores the
// empty string, so every bug card competed in the autopilot lane pool
// while its own card page read "autopilot: off".
func (f *Feature) GateMode() string {
	if f.GateApproval == GateAutopilot {
		return GateAutopilot
	}
	return GateAttended
}

// HandedOff reports whether the card was ended by hand-off — closed with
// its branch deliberately left unlanded. It is the predicate every
// surface asks (the board badge, the clean-up refusal, Advance's fourth
// skip), phrased once so none of them tests the timestamp by hand.
func (f *Feature) HandedOff() bool { return !f.HandedOffAt.IsZero() }

// Ending names how a card left gummi. It is the one word every surface
// uses for that — the board badge, `gummi status`, `status --json`, the
// card's own closing block — so that a reader never has to combine flags
// to name one fact.
//
// It is derived, never stored: each ending already has its own record
// (LandedSHA, GoalDroppedAt, HandedOffAt), and a second field naming what
// those three already say is a field that can disagree with them.
type Ending string

const (
	// EndingLanded: the branch reached the base branch — gummi's own
	// squash merge, or any other route that put the work there.
	EndingLanded Ending = "landed"
	// EndingHandedOff: closed with the branch deliberately kept.
	EndingHandedOff Ending = "handed_off"
	// EndingDropped: a goal gave up on the card. Nobody chose this one,
	// which is exactly why it needs a name of its own — it used to borrow
	// the hand-off stamp to get through the landing floor, and reported
	// itself as handed off ever after.
	EndingDropped Ending = "dropped"
	// EndingNone: the card has not ended. Every open card, and a done card
	// whose ending predates the records above.
	EndingNone Ending = ""
)

// Ending reports how the card ended.
//
// landedOnBase is the one fact the record cannot hold on its own: a
// branch that reached the base branch by a route gummi did not perform —
// a merged PR, a hand merge, a cherry-pick — leaves no LandedSHA, because
// nothing here did the merging. Callers holding that answer (the board
// row, `status`) pass it; callers that only have the record pass false
// and get the ending the card can prove.
//
// The order is the board's own badge order, so no two surfaces resolve a
// card carrying more than one stamp differently.
func (f *Feature) Ending(landedOnBase bool) Ending {
	switch {
	case landedOnBase || f.LandedSHA != "":
		return EndingLanded
	case f.GoalDropped():
		return EndingDropped
	case f.HandedOff():
		return EndingHandedOff
	}
	return EndingNone
}

// BranchName is the feature's git branch — the ONE place a branch name is
// constructed, and nothing anywhere parses one back. Worktrees are found
// by path (worktree.Manager.List filters .gummi/worktrees/), branches by
// this derived name, and drifted cards by walking store rows, so the
// spelling is free to change without a migration.
//
// Which spelling a card gets is its stored BranchScheme, not the current
// default: a card minted under the original scheme keeps
// `gummi/FD-042-slug` for life, because its branch already exists in
// checkouts this process cannot see. New cards get the per-kind spelling.
func (f *Feature) BranchName() string {
	if f.BranchScheme == BranchSchemeKind {
		return f.kind().branchPrefix() + "/" + string(f.ID) + "-" + f.Slug
	}
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
	case KindGoal:
		return path.Join(".gummi", "goals", string(f.ID)+"-"+f.Slug+".md")
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
	if !f.Mode.Valid() {
		return fmt.Errorf("feature %s: unknown research mode %q", f.ID, f.Mode)
	}
	// A mode on anything but a research card is a mint that lost track of
	// what it was making. Refuse it here rather than storing a field the
	// reader of a feature or a bug has to know to ignore.
	if f.Mode != ModeSurvey && f.kind() != KindResearch {
		return fmt.Errorf("feature %s: research mode %q on a %s card", f.ID, f.Mode, f.kind())
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
	if f.GoalID != "" {
		if f.kind() == KindGoal {
			return fmt.Errorf("feature %s: goals do not nest (belongs to %s)", f.ID, f.GoalID)
		}
		if _, err := ParseFeatureID(string(f.GoalID)); err != nil || f.GoalID.Kind() != KindGoal {
			return fmt.Errorf("feature %s: goal %q is not a goal id", f.ID, f.GoalID)
		}
	}
	if f.FoundBy != "" {
		if _, err := ParseFeatureID(string(f.FoundBy)); err != nil || f.FoundBy.Kind() != KindGoal {
			return fmt.Errorf("feature %s: found-by %q is not a goal id", f.ID, f.FoundBy)
		}
	}
	if f.Goal.Lanes < 0 || f.Goal.Reserve < 0 {
		return fmt.Errorf("feature %s: negative goal lanes or reserve", f.ID)
	}
	// Base, when set, must be a plain branch name. The check is the same
	// shape as Repo's and for the same reason: this value is handed to git
	// as a revision, so a leading dash (an option), whitespace, or a refspec
	// separator must never reach it. Empty is always legal — it means "the
	// managed checkout's HEAD", which is what every card did before bases
	// were selectable.
	if err := ValidateBaseBranch(f.Base); err != nil {
		return fmt.Errorf("feature %s: %w", f.ID, err)
	}
	if f.BranchScheme != BranchSchemeGummi && f.BranchScheme != BranchSchemeKind {
		return fmt.Errorf("feature %s: unknown branch scheme %q", f.ID, f.BranchScheme)
	}
	if f.StackID != "" {
		if !stackIDRe.MatchString(string(f.StackID)) {
			return fmt.Errorf("feature %s: invalid stack id %q", f.ID, f.StackID)
		}
		if f.kind() == KindGoal {
			return fmt.Errorf("feature %s: a goal card is not stackable — its cards share one goal branch", f.ID)
		}
		// A research card never cuts a branch, so it has no base to chain
		// and nothing for a successor to fork from.
		if f.kind() == KindResearch {
			return fmt.Errorf("feature %s: a research card has no branch to stack", f.ID)
		}
		if f.StackPos < 0 {
			return fmt.Errorf("feature %s: negative stack position %d", f.ID, f.StackPos)
		}
	} else if f.StackPos != 0 {
		return fmt.Errorf("feature %s: stack position %d without a stack", f.ID, f.StackPos)
	}
	return nil
}

const maxSlugLen = 40

// maxTitleLen bounds a derived card title so a long description doesn't
// become the whole title (the full text is kept in OneLiner).
//
// It was 60, which is under a git subject line and well under what people
// actually type. The new-card dialog says "Describe it. The first line is
// the title", and round 3 typed a perfectly ordinary 64-character bug
// title under that promise — "ledger sum -category food reports nothing
// when the file says Food" — and got it cut, ellipsis and all, into the
// features row, the board, the card header, the notices, and the H1 of the
// committed bug report, which then quoted the full title two lines below
// its own mangled heading. The cut is permanent and there is no way to
// decline it.
//
// 100 covers a hand-written one-liner (git's own soft subject limit is 50,
// GitHub issue titles routinely run past 80) while still bounding someone
// who pastes a paragraph into the first line. The BRANCH name is not
// affected either way: maxSlugLen caps that separately at 40.
const maxTitleLen = 100

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

// ValidateBaseBranch checks a card's chosen base branch. Empty is legal
// and means the managed checkout's HEAD.
//
// This value reaches git as a revision, so the rules are about what git
// would do with it rather than about tidiness: a leading dash would be
// read as an option, whitespace and the ref-format exclusions would make
// it an invalid ref, and `..` / `@{` are revision syntax that would
// resolve to something other than the branch the reader picked. The
// allowlist is deliberately narrower than git's own: a base branch is
// chosen from a list of local branches, never typed freehand, so nothing
// legitimate is excluded.
func ValidateBaseBranch(b string) error {
	if b == "" {
		return nil
	}
	if strings.TrimSpace(b) != b {
		return fmt.Errorf("base branch %q has surrounding whitespace", b)
	}
	if strings.HasPrefix(b, "-") {
		return fmt.Errorf("base branch %q starts with a dash", b)
	}
	if strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") {
		return fmt.Errorf("base branch %q is not a valid ref name", b)
	}
	if strings.Contains(b, "..") || strings.Contains(b, "@{") {
		return fmt.Errorf("base branch %q contains revision syntax", b)
	}
	if strings.ContainsAny(b, " \t\n\\~^:?*[") {
		return fmt.Errorf("base branch %q contains characters git refuses in a ref", b)
	}
	return nil
}
