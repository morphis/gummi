package domain

import (
	"crypto/sha1" // #nosec G505 -- names a slug, secures nothing
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/morphis/gummi/internal/gummidir"
)

// Gate approval modes: who crossed a workflow card's gates — a person at
// each one (GateAttended, also the empty default) or nobody until the
// branch was verified (GateAutopilot). Workflow cards are retired; the
// spellings survive because a legacy card's log still records its mode
// changes, and the thread renders them (threadfold.ModeLine).
const (
	GateAttended  = "attended"
	GateAutopilot = "autopilot"
)

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
// here; the caller decides what empty means for it.
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
	// KindFreeform is the one kind that is not in the workflow at all: a
	// coding-agent conversation that happens to be a card (DESIGN §19).
	// It gets everything structural — an id, a branch, a worktree, an
	// envelope, a thread, the diff surface — and none of the graph: no
	// stages, no gates, no critique, no verify. It holds StageOpen from
	// mint until it closes, and what it may do instead of advancing is
	// land (see MayLand), hand off, or be discarded.
	//
	// The trade that makes it legitimate rather than a hole in the
	// quality floor is stated once, in MayLand: a card in the workflow
	// lands on a verified branch, a freeform card lands on a human's
	// read of its diff, and gummi records which floor each commit
	// crossed.
	KindFreeform Kind = "freeform"
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
	case KindFreeform:
		return "FF"
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
	// BranchSchemeAdopted means the card did not get a spelling at all:
	// it was minted onto a branch that already existed (DESIGN §10 D22),
	// and Branch holds that ref verbatim. It is the one scheme whose name
	// is stored rather than derived, because there is nothing to derive it
	// from — the branch was named by whoever cut it, which was not gummi.
	BranchSchemeAdopted = "adopted"
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
	case KindFreeform:
		return "ff"
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
	case KindFreeform:
		// The one kind with no artifact: a freeform card's record is its
		// thread, so there is no document for a surface to name or for a
		// stage to be told to go and read. Empty is the answer, not an
		// oversight — see TestArtifactNounNamesEveryKind, which pins the
		// exception rather than letting a new kind silently inherit
		// "spec".
		return ""
	case KindGoal:
		return "goal doc"
	}
	return "spec" // KindFeature and the empty default
}

// Valid reports whether k is a recognized kind (empty is not — callers
// that accept a default normalize it before validating).
func (k Kind) Valid() bool {
	return k == KindFeature || k == KindBug || k == KindResearch ||
		k == KindGoal || k == KindFreeform
}

// FeatureID is a work item's identifier, e.g. "FD-042" (feature),
// "BG-007" (bug), "RS-003" (research), "GL-004" (goal), or "FF-012"
// (freeform). IDs are minted from the
// monotonic counter in .gummi/seq — shared across kinds, so numbers
// never collide — and zero-padded to three digits.
type FeatureID string

var featureIDRe = regexp.MustCompile(`^(FD|BG|RS|GL|FF)-[0-9]{3,}$`)

// Kind reports the work kind an ID's prefix encodes.
func (id FeatureID) Kind() Kind {
	switch {
	case strings.HasPrefix(string(id), "BG-"):
		return KindBug
	case strings.HasPrefix(string(id), "RS-"):
		return KindResearch
	case strings.HasPrefix(string(id), "GL-"):
		return KindGoal
	case strings.HasPrefix(string(id), "FF-"):
		return KindFreeform
	default:
		return KindFeature
	}
}

// Num is the sequence number an ID carries, 0 when it carries none. It is
// the whole of a card's identity to a person: numbers are minted from one
// counter across kinds, so no two cards share one.
func (id FeatureID) Num() int {
	_, digits, ok := strings.Cut(string(id), "-")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// Ref is how a card is named where a person reads it: its number alone,
// "#178". What kind of card it is belongs beside its title (TypeName), not
// in a code in front of its number. An ID with no number reads as itself.
//
// Never write a Ref into anything bound for a forge — a commit message, a
// pull request — where "#178" names somebody else's issue.
func (id FeatureID) Ref() string {
	n := id.Num()
	if n == 0 {
		return string(id)
	}
	return "#" + strconv.Itoa(n)
}

// ParseRef reads a card's number as a person types it: "178" or "#178".
// It is the typed half of Ref; a canonical ID still goes through
// ParseFeatureID.
func ParseRef(s string) (int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1
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
		return "", fmt.Errorf("invalid work item ID %q (want FD-NNN, BG-NNN, RS-NNN, GL-NNN, or FF-NNN)", s)
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

	// Decompose* are the part of Credits/InputTokens/OutputTokens a
	// research card's decompose pass spent, read back from rows written
	// before decomposition was retired; nothing books them any more.
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
	// SessionBackend and SessionModel are the agent and model a freeform
	// card's session runs on, chosen by the person rather than read from
	// a profile (DESIGN §19.8). Both empty means the profile's
	// implementer, which is what every freeform card did before a session
	// could pick its own. A card in the workflow never carries either:
	// its stages take their agents from its profile, and Validate refuses
	// a stage card that names one, so "stages come from the profile" is a
	// fact of the data and not a convention of the readers.
	SessionBackend string
	SessionModel   string
	// MainCheckout marks a freeform card whose session runs in the
	// repository's main checkout instead of a worktree of its own (DESIGN
	// §19): no branch is cut, no worktree is created, and its turns write
	// straight into the checkout the person works in. It is mint-time-only
	// and freeform-only — Validate refuses it on every other kind, and on
	// a freeform card that carries a base, a branch or a stack position,
	// all of which name a branch such a card does not have. Landing is
	// not a thing it does: there is no branch to squash, its work stays
	// loose in the checkout, and the person commits what they keep.
	MainCheckout bool
	// Home marks an agent: a freeform card that works in no repository of
	// the person's at all, but in a home of its own (DESIGN §19.3d) — a
	// directory under .gummi/homes that outlives its conversations, where
	// it keeps what it builds for itself. gummi makes that directory a
	// git repository and the card runs in its checkout, so a home card is
	// a main-checkout card whose repository is its home: Validate demands
	// MainCheckout with it and refuses a repository name, and everything
	// a main-checkout card does without — a branch, a landing — an agent
	// does without for the same reason.
	Home bool
	// ContinuedAs names the feature a freeform card's work went on as,
	// once a spec was written from it (DESIGN §19.8). That feature's
	// branch is cut from this card's, so it carries every commit here and
	// lands them on a verified branch; this card landing them too would
	// put the same work on main a second way, past the floor the person
	// just sent it to. MayLandAfterAll refuses it. Freeform-only. Legacy:
	// "write a spec" is retired, so only a session continued before then
	// carries one.
	ContinuedAs FeatureID
	// ParentID is the session (a freeform card) this card is a hidden
	// child of (Child), empty for every other card. A child resolves to the
	// manager rooted at the session's worktree (worktree.Pool.ManagerFor),
	// so its work reaches main only through the person's read of the
	// session's diff. A legacy card a session once created under its
	// delegation carries one too.
	ParentID FeatureID
	// Child marks a session's hidden child: a workflow step's card, or an
	// old crew's (ChildLink). Zero on every card a person works.
	Child  ChildLink
	Budget Budget
	Spend  Spend // metered cost across all stages
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
	// TreeRepo is set only on the view AttachedView builds of a card for
	// a repository attached to it (DESIGN §27): the worktree manager names
	// that tree <ID>@<repo>. Never stored, never serialised.
	TreeRepo string `json:"-"`
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
	// Branch is the ref an ADOPTED card was minted onto, and is empty for
	// every other card. It is the one branch name gummi stores rather than
	// derives, for the reason BranchSchemeAdopted gives: an adopted branch
	// was named by somebody else, so there is no (scheme, id, slug) to
	// recover it from and the ref itself is the only record.
	//
	// It is also the field that makes "gummi did not cut this" a fact the
	// code can ask about rather than a convention — Adopted() reads it, and
	// the rules that keep custody from becoming ownership (never delete it,
	// never rewrite it unasked) hang off that one question.
	Branch    string
	CreatedAt time.Time
	UpdatedAt time.Time
	// VerifiedAt is stamped when the item's verify gate passes and its
	// branch becomes ready to land — the headless driver's stop-at-verified
	// terminal state (DESIGN §12). Zero until then. Distinct from reaching
	// StageDone, which a merge sets: a verified branch is ready to land, not
	// yet landed.
	VerifiedAt time.Time
	// VerifiedRev is the branch tip the verify pass behind VerifiedAt ran
	// on: what was verified, as opposed to when. A commit added after it —
	// a hand edit, a later agent turn, a landing's own final checkpoint —
	// is work no verify has seen, and MayLandAt refuses to land it until a
	// verify runs on the new tip. Empty on an unstamped card, and on one
	// stamped before the revision was recorded (see MayLandAt for what
	// that reads as).
	VerifiedRev string
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
	// FoundBy names the CARD this one was filed from — provenance, not
	// dependency: nothing about it blocks, schedules or orders work.
	//
	// A finished card's follow-up is filed from it, and it means exactly
	// that — this came out of that.
	FoundBy FeatureID
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
	case KindFreeform:
		return KindFreeform
	default:
		return KindFeature
	}
}

// IsFreeform reports whether the card is a freeform card: one that is not
// in the workflow at all (KindFreeform). It is the question every graph
// reader asks before assuming a card has a stage to advance, and every
// artifact reader asks before assuming there is a document.
func (f *Feature) IsFreeform() bool { return f.kind() == KindFreeform }

// TypeName is what kind of card this is in a person's words, said beside
// its title on every face: Feature, Bug, Research, Goal, Session or Agent.
// It is not Kind: a session and an agent are one kind (KindFreeform), told
// apart by Home, and neither is called "freeform" where a person reads.
func (f *Feature) TypeName() string {
	switch f.kind() {
	case KindBug:
		return "Bug"
	case KindResearch:
		return "Research"
	case KindGoal:
		return "Goal"
	case KindFreeform:
		if f.Home {
			return "Agent"
		}
		return "Session"
	}
	return "Feature"
}

// nameWidth is how much of a title a sentence about a card carries.
const nameWidth = 40

// Name is how a sentence names a card where a person reads it: its title
// in quotes, cut to nameWidth. A person recognises a card by what it is
// about; its number (Ref) is for typing one and for the card's own head,
// and is only what Name falls back to for a card with no title.
func (f *Feature) Name() string {
	t := strings.Join(strings.Fields(f.Title), " ")
	if t == "" {
		return f.ID.Ref()
	}
	if r := []rune(t); len(r) > nameWidth {
		t = strings.TrimRight(string(r[:nameWidth-1]), " ") + "…"
	}
	return "“" + t + "”"
}

// CheckoutName is what a card that holds no branch works in, in the words
// a sentence about it uses: an agent's home, or the main checkout.
func (f *Feature) CheckoutName() string {
	if f.Home {
		return "its home"
	}
	return "the main checkout"
}

// MayLand reports whether this card may become a commit on its base,
// and says why not when it may not.
//
// There is one floor, and it is not here: a card lands on a person's read
// of its diff (DESIGN §19.1, decision 27). Unresolved diff comments hold
// the landing, enforced by the same open-comment check every landing
// crosses, and an open workflow run holds its session's (Engine.RunHolds). What
// this refuses is the card that has no landing of its own: a session
// whose work went on as a spec, which that spec lands; a legacy goal,
// which never landed; and a legacy research card, which holds no branch.
//
// A handed-off card asks the same question: hand-off closes a card with
// its branch left unlanded, and landing it after all stays available for
// as long as the branch is there.
func (f *Feature) MayLand() error {
	if f.ContinuedAs != "" {
		return fmt.Errorf("%w — its work continues as %s, which lands it", ErrContinued, f.ContinuedAs)
	}
	switch f.kind() {
	case KindGoal:
		return errors.New("a goal no longer lands")
	case KindResearch:
		return errors.New("a research card holds no branch to land")
	}
	return nil
}

// ShortRev is a commit id cut to the length a sentence quotes it at.
func ShortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// HandedOff reports whether the card was ended by hand-off — closed with
// its branch deliberately left unlanded. It is the predicate every
// surface asks (the board badge, the clean-up refusal, Advance's fourth
// skip), phrased once so none of them tests the timestamp by hand.
func (f *Feature) HandedOff() bool { return !f.HandedOffAt.IsZero() }

// ErrContinued refuses landing a freeform card whose work went on as a
// spec: that spec lands it (Feature.ContinuedAs).
var ErrContinued = errors.New("continued as a spec")

// Ending names how a card left gummi. It is the one word every surface
// uses for that — the board badge, `gummi status`, `status --json`, the
// card's own closing block — so that a reader never has to combine flags
// to name one fact.
//
// It is derived, never stored: each ending already has its own record
// (LandedSHA, HandedOffAt), and a second field naming what
// those two already say is a field that can disagree with them.
type Ending string

const (
	// EndingLanded: the branch reached the base branch — gummi's own
	// squash merge, or any other route that put the work there.
	EndingLanded Ending = "landed"
	// EndingHandedOff: closed with the branch deliberately kept.
	EndingHandedOff Ending = "handed_off"
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
// checkouts this process cannot see. New cards get the per-kind spelling,
// `bug/token-parser` — the kind of work and the label, and nothing else.
//
// The card id is deliberately absent from the new spelling. It was doing
// two jobs there: naming the card, which the slug already does in the
// words a reviewer reads, and guaranteeing uniqueness, which it did for
// free. Dropping it gives up the second, so uniqueness is now checked
// rather than structural: BranchTaken is asked at mint, before a card
// exists that could never cut a branch, and worktree.Create's
// already-exists refusal remains the backstop for a name git acquired by
// some other route. The worktree path is still keyed by id
// (.gummi/worktrees/FD-042), so two cards may safely share a slug as
// long as they are in different repositories.
// An adopted card is the exception to every sentence above: its name was
// not constructed here and is not derivable, so BranchName hands back the
// ref it was minted onto (see BranchSchemeAdopted).
func (f *Feature) BranchName() string {
	if f.BranchScheme == BranchSchemeAdopted {
		return f.Branch
	}
	if f.IsChild() {
		// a step works in its session's tree, and keeps no branch; an old
		// crew's branches are left in the repository as they were
		return ""
	}
	if f.BranchScheme == BranchSchemeKind {
		return f.kind().branchPrefix() + "/" + f.Slug
	}
	return "gummi/" + string(f.ID) + "-" + f.Slug
}

// Adopted reports whether this card was minted onto a branch gummi did
// not cut (DESIGN §10 D22). It is the question the custody rules ask:
// cleanup must not delete this branch, and a rebase of it rewrites
// history someone may already have pushed.
func (f *Feature) Adopted() bool {
	return f.BranchScheme == BranchSchemeAdopted
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
	case KindFreeform:
		// No artifact, so no path: a freeform card's record is its thread
		// (ArtifactNoun says the same). Callers that would read or write
		// one must ask IsFreeform first rather than joining this onto a
		// root and landing on the directory itself.
		return ""
	default:
		return f.SpecPath()
	}
}

// ArtifactFile resolves the card's artifact under root, reporting false
// when the card has none.
//
// EVERY caller that would read, write or REMOVE the artifact must go
// through this rather than joining ArtifactPath onto a root itself. A
// freeform card's ArtifactPath is empty (its record is its thread), and
// filepath.Join(root, "") is root — so the bare join turns "this card has
// no document" into "operate on the whole workspace". A pty drive of a
// freeform card found it the expensive way: deleting one ran
// os.RemoveAll over the repository.
func (f *Feature) ArtifactFile(root string) (string, bool) {
	p := f.ArtifactPath()
	if p == "" {
		return "", false
	}
	return gummidir.FromRel(root, p), true
}

// Validate checks the invariants every stored feature must satisfy.
func (f *Feature) Validate() error {
	if _, err := ParseFeatureID(string(f.ID)); err != nil {
		return err
	}
	if err := f.validateTreeRepo(); err != nil {
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
	// StageOpen and KindFreeform imply each other, and storing one
	// without the other is what a card half-way between the graph and
	// outside it would look like. A freeform card holds StageOpen until
	// it closes at StageDone; nothing else may hold StageOpen at all,
	// because a workflow stage with no outgoing edge would park a card in
	// the graph with nothing able to move it.
	if f.kind() == KindFreeform && f.Stage != StageOpen && f.Stage != StageDone {
		return fmt.Errorf("feature %s: a freeform card is at %s or %s, never %q", f.ID, StageOpen, StageDone, f.Stage)
	}
	if f.Stage == StageOpen && f.kind() != KindFreeform {
		return fmt.Errorf("feature %s: stage %s is a freeform card's, not a %s's", f.ID, StageOpen, f.kind())
	}
	if (f.SessionBackend != "" || f.SessionModel != "") && f.kind() != KindFreeform {
		return fmt.Errorf("feature %s: a %s takes its agents from its profile, not a session model", f.ID, f.kind())
	}
	if f.ContinuedAs != "" && f.kind() != KindFreeform {
		return fmt.Errorf("feature %s: only a freeform card is continued as a spec, not a %s", f.ID, f.kind())
	}
	// MainCheckout is a freeform-only property, and a main-checkout card
	// has no branch to name: a base, a carried branch or a stack position
	// on one is a mint that lost track of what it was making, and a reader
	// of the row should not have to know to ignore any of the three.
	if f.MainCheckout {
		if f.kind() != KindFreeform {
			return fmt.Errorf("feature %s: main checkout is a freeform card's, not a %s's", f.ID, f.kind())
		}
		if f.Base != "" {
			return fmt.Errorf("feature %s: a main-checkout card forks from no branch, but names base %q", f.ID, f.Base)
		}
		if f.Branch != "" {
			return fmt.Errorf("feature %s: a main-checkout card holds no branch, but carries %q", f.ID, f.Branch)
		}
		if f.StackID != "" {
			return fmt.Errorf("feature %s: a main-checkout card has no branch to stack", f.ID)
		}
	}
	if f.Home {
		if !f.MainCheckout {
			return fmt.Errorf("feature %s: an agent runs in its home's own checkout, but is not marked main-checkout", f.ID)
		}
		if f.Repo != "" {
			return fmt.Errorf("feature %s: an agent is in no repository, but names %q", f.ID, f.Repo)
		}
		if f.ParentID != "" {
			return fmt.Errorf("feature %s: an agent is nobody's child card", f.ID)
		}
	}
	if f.Budget.Envelope < 0 {
		return fmt.Errorf("feature %s: negative budget", f.ID)
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
	if err := f.validateParent(); err != nil {
		return err
	}
	if f.FoundBy != "" {
		// Any card id: the field means "this came out of that" (its doc
		// comment above).
		if _, err := ParseFeatureID(string(f.FoundBy)); err != nil {
			return fmt.Errorf("feature %s: found-by %q is not a card id", f.ID, f.FoundBy)
		}
		// Filed from itself is a mint that lost track of its parent, and
		// it would read as a card that is its own provenance.
		if f.FoundBy == f.ID {
			return fmt.Errorf("feature %s: found-by names the card itself", f.ID)
		}
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
	switch f.BranchScheme {
	case BranchSchemeGummi, BranchSchemeKind:
		if f.Branch != "" {
			return fmt.Errorf("feature %s: carries a stored branch %q but its name is derived", f.ID, f.Branch)
		}
	case BranchSchemeAdopted:
		if err := ValidateAdoptedBranch(f.Branch); err != nil {
			return fmt.Errorf("feature %s: %w", f.ID, err)
		}
		// Adopting the branch the card lands on would put the implementer's
		// commits straight onto it, with no diff to review and nothing to
		// merge. It is the one adoption that is never a mistake worth
		// tolerating, so it is refused at the domain edge rather than left
		// to git.
		if f.Base != "" && f.Branch == f.Base {
			return fmt.Errorf("feature %s: cannot adopt %q — it is the branch this card lands on", f.ID, f.Branch)
		}
		// A research card runs in a detached scratch tree and never cuts or
		// holds a branch, so there is nothing for it to adopt onto.
		if f.kind() == KindResearch {
			return fmt.Errorf("feature %s: a research card has no branch, so it cannot adopt one", f.ID)
		}
	default:
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
//
// Latin letters with marks fold to their plain letters ("Café résumé" is
// cafe-resume, not caf-r-sum). A title written in a script with no such
// folding (Cyrillic, CJK, …) still names a card: its slug is "card-"
// and a short hash of the title, which the card's head shows as its
// branch. Only a title with no letter or digit in any script is an error
// — slugs flow into git branch names and paths, and there is nothing in
// "!!!" to name one after.
func Slugify(title string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(title))
	s = foldMarks(s)
	s = nonSlugRune.ReplaceAllString(s, "-")
	s = cutSlug(strings.Trim(s, "-"), maxSlugLen)
	if s == "" {
		if !strings.ContainsFunc(title, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return "", fmt.Errorf("title %q yields an empty slug; use at least one letter or digit", title)
		}
		sum := sha1.Sum([]byte(strings.TrimSpace(title))) // #nosec G401 -- a name, not a secret
		return "card-" + hex.EncodeToString(sum[:4]), nil
	}
	return s, nil
}

// cutSlug cuts s to max bytes, at the last dash when the cut would land
// inside a word and that dash keeps at least half of s, so a branch does
// not end on half a word ("…-the-sentenc").
func cutSlug(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if s[max] != '-' {
		if i := strings.LastIndexByte(cut, '-'); i > max/2 {
			cut = cut[:i]
		}
	}
	return strings.Trim(cut, "-")
}

// maxBranchWords bounds how many of a title's words a card's branch keeps.
const maxBranchWords = 6

// branchFiller are the words a sentence carries and a branch name does
// without: articles, pronouns, auxiliaries, question words, politeness
// and the small prepositions. A negation is never filler.
var branchFiller = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an the this that these those
		i im ive id me my we our us you your they them their it its
		is are was were be been being am do does did can could would should will shall may might must
		please how what why when where which who whether there here so just also really some any
		let lets want wanted wanna need needs like to of as and or but if then than into at by with on in for`) {
		m[w] = true
	}
	return m
}()

// BranchSlug is the slug a session's branch is named by: its title's
// words without branchFiller, at most maxBranchWords of them, cut on a
// word boundary. "Can we make the board load faster?" is
// make-board-load-faster. A title that is nothing but filler, or has no
// Latin letters, slugs as Slugify has it.
func BranchSlug(title string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(title))
	s = foldMarks(strings.NewReplacer("'", "", "’", "").Replace(s))
	var kept []string
	for _, w := range nonSlugRune.Split(s, -1) {
		if w == "" || branchFiller[w] {
			continue
		}
		if kept = append(kept, w); len(kept) == maxBranchWords {
			break
		}
	}
	if len(kept) == 0 {
		return Slugify(title)
	}
	return cutSlug(strings.Join(kept, "-"), maxSlugLen), nil
}

// markFolds are the Latin letters with marks (and the ligatures) a title
// is likely to carry, as the plain letters a slug keeps.
var markFolds = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"ç", "c", "ć", "c", "č", "c", "ĉ", "c", "ċ", "c",
	"ď", "d", "đ", "d", "ð", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ę", "e", "ě", "e",
	"ğ", "g", "ģ", "g", "ĝ", "g",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i", "į", "i", "ı", "i",
	"ķ", "k", "ł", "l", "ľ", "l", "ļ", "l", "ĺ", "l",
	"ñ", "n", "ń", "n", "ň", "n", "ņ", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ő", "o",
	"ŕ", "r", "ř", "r", "ś", "s", "š", "s", "ş", "s", "ș", "s", "ß", "ss",
	"ť", "t", "ţ", "t", "ț", "t", "þ", "th",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u", "ų", "u",
	"ý", "y", "ÿ", "y", "ź", "z", "ż", "z", "ž", "z",
	"æ", "ae", "œ", "oe",
)

func foldMarks(s string) string { return markFolds.Replace(s) }

// SlugVariant is the nth alternative to a slug, for when the branch a
// slug wants is already taken.
//
// It cannot be built by slugifying "<title> 2": a slug is capped at
// maxSlugLen, and a title already at the cap truncates straight back to
// the same string — so every "alternative" is the original, and a caller
// looking for a free one concludes that all of them are taken. The room
// for the suffix has to be made before the cap, not after.
func SlugVariant(slug string, n int) (string, error) {
	suffix := "-" + strconv.Itoa(n)
	base := slug
	base = cutSlug(base, maxSlugLen-len(suffix))
	out := base + suffix
	if err := ValidateSlug(out); err != nil {
		return "", err
	}
	return out, nil
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
	return validateRefName("base branch", b)
}

// ValidateAdoptedBranch is ValidateBaseBranch's counterpart for the ref an
// adopted card was minted onto. The rules are identical — the value is
// handed to git as a revision either way — but empty is a failure rather
// than a default: "whatever the checkout has out" is a sensible base and
// a meaningless thing to adopt.
func ValidateAdoptedBranch(b string) error {
	if b == "" {
		return fmt.Errorf("adopted branch is empty")
	}
	return validateRefName("adopted branch", b)
}

// validateRefName rejects the values that must never reach git as a
// revision: a leading dash (git reads it as an option), whitespace,
// refspec and revision syntax, and the characters git itself refuses in a
// ref. label names the field so the error says which one was wrong.
func validateRefName(label, b string) error {
	if strings.TrimSpace(b) != b {
		return fmt.Errorf("%s %q has surrounding whitespace", label, b)
	}
	if strings.HasPrefix(b, "-") {
		return fmt.Errorf("%s %q starts with a dash", label, b)
	}
	if strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") {
		return fmt.Errorf("%s %q is not a valid ref name", label, b)
	}
	if strings.Contains(b, "..") || strings.Contains(b, "@{") {
		return fmt.Errorf("%s %q contains revision syntax", label, b)
	}
	if strings.ContainsAny(b, " \t\n\\~^:?*[") {
		return fmt.Errorf("%s %q contains characters git refuses in a ref", label, b)
	}
	return nil
}
