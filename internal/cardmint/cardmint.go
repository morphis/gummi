// Package cardmint owns the one recipe for minting a new card (feature,
// bug, or research item) from a free-form description: split the
// description, slugify the title, validate the target repo, mint the
// next sequence number, build the domain.Feature, and seed whatever draft
// artifact its kind wants before persisting it.
//
// That recipe used to live twice — once as (*driver.Driver).createFeature
// for headless `gummi run`/`bugs new`, and a second time, differently
// shaped, inside Engine.Materialize's bulk-ingest path. This package
// exists to let a *third* caller mint a single card — the workspace MCP
// endpoint's card_new tool, invoked by an agent hosted inside gummi's own
// TUI — without adding a fourth copy or reaching across a package
// boundary that would cycle: internal/driver already imports
// internal/engine (Driver embeds *engine.Engine), so internal/engine
// cannot import internal/driver to reuse createFeature, and neither can
// import the other's package for this. cardmint sits below both — it
// imports only internal/domain, internal/state, internal/spec, and
// internal/workflow, none of which import driver or engine — so both
// driver and engine import cardmint instead of each other.
//
// cardmint knows nothing about either caller. It does not know what a
// Driver's Options or an Engine's tool arguments look like; it takes an
// Input describing exactly the decisions a mint needs (kind, description,
// profile, envelope, repo, external ref, route, gate approval) and a
// RequireRepo callback, because "can this card have this repository" is
// answered differently by each caller (a driver asks its *engine.Engine;
// the workspace endpoint asks its own *Engine directly) and cardmint has
// no engine of its own to ask.
package cardmint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
)

// Input describes one card to mint. It is deliberately shaped around what
// the mint recipe itself needs to make its decisions, not around either
// caller's own option struct — driver.Options carries run-loop concerns
// (timeouts, autonomy, verbosity) that have nothing to do with minting,
// and a future MCP-only concern should land here, not leak the other way.
//
// Every field is a plain value or a single callback; cardmint holds no
// reference to an *engine.Engine or a *driver.Driver; wiring those in
// would immediately create the import cycle this package exists to avoid.
type Input struct {
	// Kind selects the card's artifact home and its stages' contracts —
	// not a route, since there is one graph: KindFeature/KindBug share the
	// draft-seeded shape, and KindResearch mints an RS card and seeds its
	// `## Brief` directly (research has no draft step).
	Kind domain.Kind
	// Mode refines KindResearch into the survey (empty) or the diagnosis
	// contract, choosing which document template seeds the card. Ignored
	// for every other kind, and refused by Feature.Validate if it reaches
	// one.
	Mode domain.ResearchMode
	// Description is the free-form input text: its first line is the
	// title (domain.SplitFreeform). For KindResearch the whole text is the
	// Brief; for everything else the overflow past the first line seeds
	// the draft (the Problem section, or a bug report's sections).
	Description string
	// Profile is the model-role profile the card runs under (empty means
	// the workspace default).
	Profile string
	// Envelope is the card's credit budget ceiling.
	Envelope int
	// Repo is the managed repository the card belongs to: a configured
	// `repos:` name, or "" for the workspace default.
	Repo string
	// RequireRepo validates Repo, returning the error to fail the mint
	// with. It is asked about EVERY card, the unnamed one included: a
	// workspace configured with `repos:` has no default repository, so a
	// card that names none is exactly as unmintable as one that names a
	// repo that isn't there, and both must be refused before a sequence
	// number is spent. Only the caller can tell the two apart — it owns
	// the pool that knows whether the empty name resolves — which is why
	// this is an error-returning callback rather than a bool: the message
	// belongs to whoever knows what the workspace looks like.
	//
	// A nil RequireRepo with a non-empty Repo fails closed, since
	// silently minting against an unvalidated repo name is exactly the
	// bug this check exists to catch. A nil one with an empty Repo mints
	// the card: a caller with no repo pool at all (cardmint's own tests)
	// has a single implicit repository and nothing to choose between.
	RequireRepo func(repo string) error
	// Base is the git branch the card's work forks from and lands on: a
	// local branch name, or "" for whatever the managed checkout has out
	// (which is what every card did before bases were selectable). A
	// stacked card above the bottom ignores it — its base is the branch
	// of the card below it.
	Base string
	// Adopt is an existing branch to mint this card ONTO rather than
	// cutting one for it (DESIGN §10 D22): the card's branch becomes this
	// ref verbatim, and its first stage opens onto the work already there.
	// Empty mints an ordinary card, which is nearly all of them.
	Adopt string
	// InspectAdopted answers, for Adopt, the two questions cardmint cannot:
	// is that branch really there, and what is on it. It is a callback for
	// the same reason RequireRepo is — cardmint imports no git and holds no
	// worktree manager, and the caller that does is the one able to say.
	//
	// It returns the work the card is inheriting, which is seeded into the
	// draft so the architect reads the branch before planning against it.
	// An error refuses the mint, before a sequence number is spent: a card
	// minted onto a branch that is not there could never cut one either.
	//
	// A nil callback with a non-empty Adopt fails closed, exactly as
	// RequireRepo does, since minting onto an unverified ref is the bug the
	// check exists to catch.
	InspectAdopted func(branch string) (domain.AdoptedWork, error)
	// ExternalRef is an optional external correlation id (e.g. a GitHub
	// issue reference), persisted as Feature.ExternalRef and echoed by
	// callers that track it.
	ExternalRef string
	// Acceptance is optional acceptance-criteria text seeded into the
	// draft's Verification plan section alongside the description's
	// overflow. Ignored for KindResearch.
	Acceptance string
	// Severity is a bug's impact, persisted on the card and seeded into
	// its report header. Ignored for every other kind.
	Severity domain.Severity
	// Source names where the description came from ("manual", "github"),
	// rendered into the artifact's provenance header beside ExternalRef.
	// Empty renders no provenance.
	Source string
	// Discussion is an imported issue's comment thread, seeded into a bug
	// report's Discussion section. It is the one part of an import that is
	// not in the description text a person can edit, so it rides beside
	// it. Ignored for every other kind.
	Discussion string
	// GateApproval selects who crosses this card's gates on an unattended
	// resume: domain.GateAutopilot (crosses them all) or
	// domain.GateAttended (checkpoints each one for a human). Empty reads
	// as domain.GateAttended — the stricter of the two — matching
	// domain.(*Feature).GateMode, which is how every reader resolves the
	// stored field. Mint writes the resolved value rather than the empty
	// string so nothing downstream has to resolve it again.
	GateApproval string
	// Goal mints the card into that goal: its branch will fork from the
	// goal branch and its budget is carved out of the goal's. Empty mints
	// an open-board card. A goal cannot be minted into a goal.
	Goal domain.FeatureID
	// FoundBy records the card that filed this one: a goal that found the
	// work along the way, or a finished card whose follow-up this is.
	// Provenance only — it blocks and schedules nothing. Ignored when
	// Goal is set.
	FoundBy domain.FeatureID
	// GoalDoc, for a goal, is a complete goal doc to start from (headless
	// --plan-file) instead of the template seeded with the description.
	// Ignored for every other kind.
	GoalDoc string
}

// Mint validates in, mints the next sequence number, builds the
// domain.Feature, seeds its draft or research artifact where the kind and
// input call for one, and persists it. It does not drive the card —
// callers that want it running still own that decision, the same way
// (*driver.Driver).Create does today.
//
// Order matters and is deliberate: the repo check runs before
// MintFeatureNum so a bad --repo never burns a sequence number, matching
// both callers' pre-existing behavior (createFeature's own comment, and
// Engine.Materialize's requireRepo pre-flight).

// freeSlug returns a slug whose branch nobody holds.
//
// Telling a person to retitle a card is the right answer when a person is
// naming one: the fix is a word in the title and nothing else. It is the
// wrong answer to a goal minting its own cards, because there is nobody
// there to retitle anything — the collision surfaces after the plan gate,
// with the plan's spend already gone, and a resume runs into it again
// unless the architect happens to invent a different title. A goal that
// runs the same shape of work twice in one workspace (the second tier of
// a programme, a re-run after a wrap-up) will collide by nature: the good
// title for the work is the title the last one used.
//
// So a goal's card takes the next free variant and says nothing; a
// person's card still gets told.
func freeSlug(ctx context.Context, store *state.Store, in Input, slug string) (string, error) {
	branchFor := func(s string) string {
		f := domain.Feature{Kind: in.Kind, Slug: s, BranchScheme: domain.DefaultBranchScheme}
		return f.BranchName()
	}
	owner, taken, err := store.BranchTaken(ctx, in.Repo, branchFor(slug), "")
	if err != nil {
		return "", err
	}
	if !taken {
		return slug, nil
	}
	if in.Goal == "" {
		return "", fmt.Errorf(
			"%s already uses the branch %s — retitle this card so it gets a different one", owner, branchFor(slug))
	}
	for n := 2; n <= 50; n++ {
		cand, serr := domain.SlugVariant(slug, n)
		if serr != nil {
			return "", serr
		}
		if _, dup, berr := store.BranchTaken(ctx, in.Repo, branchFor(cand), ""); berr != nil {
			return "", berr
		} else if !dup {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s is taken, and so is every variant of it up to 50", branchFor(slug))
}

// adoptedWork validates an adoption and returns what the card inherits.
//
// Two checks, and they are the inverse of the ones an ordinary mint runs.
// freeSlug asks whether a derived branch name is free and refuses when it
// is taken; adoption asks whether the named ref EXISTS (the caller's
// business, via InspectAdopted) and whether any card already holds it
// (gummi's business, via the same BranchTaken). Two cards on one branch
// would be two cards committing over each other with no way to tell whose
// work a diff belonged to — a worse failure than a naming collision,
// since nothing about it looks wrong until the work is lost.
func adoptedWork(ctx context.Context, store *state.Store, in Input) (domain.AdoptedWork, error) {
	if err := domain.ValidateAdoptedBranch(in.Adopt); err != nil {
		return domain.AdoptedWork{}, err
	}
	if in.Kind == domain.KindResearch {
		return domain.AdoptedWork{}, fmt.Errorf("a research card runs in a scratch tree and has no branch, so it cannot adopt %s", in.Adopt)
	}
	if owner, taken, err := store.BranchTaken(ctx, in.Repo, in.Adopt, ""); err != nil {
		return domain.AdoptedWork{}, err
	} else if taken {
		return domain.AdoptedWork{}, fmt.Errorf("%s already has %s — one branch, one card", owner, in.Adopt)
	}
	if in.InspectAdopted == nil {
		return domain.AdoptedWork{}, fmt.Errorf("branch %q cannot be verified: no branch inspector was wired", in.Adopt)
	}
	w, err := in.InspectAdopted(in.Adopt)
	if err != nil {
		return domain.AdoptedWork{}, err
	}
	if w.Branch == "" {
		w.Branch = in.Adopt
	}
	return w, nil
}

// adoptedFor is the inherited work as a template argument: a pointer for
// an adopted card, nil for every other, so the draft renderers can keep
// treating "no inherited work" as the absence of a value rather than as a
// zero struct they must each know how to recognise.
func adoptedFor(in Input, w domain.AdoptedWork) *domain.AdoptedWork {
	if in.Adopt == "" {
		return nil
	}
	return &w
}

func Mint(ctx context.Context, store *state.Store, ws state.Workspace, in Input) (domain.Feature, error) {
	// the first line is the title for every kind — a multi-line research
	// brief names itself on its first line exactly as a feature does; the
	// full brief goes into the artifact below. Research just keeps no
	// separate seed: the whole description is the Brief.
	title, oneLiner, seed := domain.SplitFreeform(in.Description)
	slug, err := domain.Slugify(title)
	if err != nil {
		return domain.Feature{}, err
	}
	if err := requireRepo(in.RequireRepo, in.Repo); err != nil {
		return domain.Feature{}, err
	}
	// The branch name a card gets no longer carries its id, so two cards
	// whose titles slugify the same would want the same ref. Refuse here,
	// before a sequence number is spent and before a card exists that
	// could never cut its branch — and say what to do about it, since the
	// fix is a word in the title and nothing else.
	//
	// Research cards are exempt: they never cut a branch. So are adopted
	// cards, for the opposite reason — their branch name does not come
	// from the slug at all, so two of them may slugify alike without ever
	// wanting the same ref. What they need instead is the same uniqueness
	// question asked about the ref itself, which adoptedWork does.
	var inherited domain.AdoptedWork
	switch {
	case in.Adopt != "":
		w, aerr := adoptedWork(ctx, store, in)
		if aerr != nil {
			return domain.Feature{}, aerr
		}
		inherited = w
	case in.Kind != domain.KindResearch:
		free, terr := freeSlug(ctx, store, in, slug)
		if terr != nil {
			return domain.Feature{}, terr
		}
		slug = free
	}
	num, err := store.MintFeatureNum(ctx, ws.SeqFile())
	if err != nil {
		return domain.Feature{}, err
	}
	id, err := domain.NewID(in.Kind, num)
	if err != nil {
		return domain.Feature{}, err
	}
	gate := in.GateApproval
	if gate == "" {
		gate = domain.GateAttended
	}
	// Only a research card carries a mode; dropping it here rather than
	// refusing keeps a caller that presets the dialog's type row (where
	// diagnosis sits beside research) from failing when the person then
	// moves the row to bug.
	mode := in.Mode
	if in.Kind != domain.KindResearch {
		mode = domain.ModeSurvey
	}
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: in.Kind, Mode: mode, Title: title, OneLiner: oneLiner,
		Slug: slug, Stage: workflow.Initial(),
		Profile: in.Profile, Budget: domain.Budget{Envelope: in.Envelope},
		GateApproval: gate,
		ExternalRef:  in.ExternalRef, Repo: in.Repo, CreatedAt: now, UpdatedAt: now,
		Base: in.Base,
		// Every newly minted card gets the current branch-name scheme.
		// Stored rather than read from the default at render time, so a
		// later change to the default never renames this card's branch —
		// which by then exists in checkouts this process cannot see.
		BranchScheme: domain.DefaultBranchScheme,
	}
	if in.Adopt != "" {
		// The adopted card is the one case where the branch is not derived
		// at all: it has a name already, given by whoever cut it.
		f.BranchScheme, f.Branch = domain.BranchSchemeAdopted, in.Adopt
	}
	if in.Kind == domain.KindBug {
		f.Severity = in.Severity
	}
	if in.Goal != "" {
		f.GoalID = in.Goal
	} else {
		f.FoundBy = in.FoundBy
	}
	if in.Kind == domain.KindGoal {
		// A goal's objective is the whole description, and the plan
		// conversation starts from it — so a goal always has a draft, even
		// from a one-line description, exactly as a research card always
		// has its document. A caller-supplied goal doc replaces the
		// template outright; the plan gate checks it like any other.
		content := in.GoalDoc
		if strings.TrimSpace(content) == "" {
			content = spec.SeededGoalTemplate(&f, spec.GoalSeed{Objective: in.Description})
		}
		if err := f.Validate(); err != nil {
			return domain.Feature{}, err
		}
		draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
		if err := os.MkdirAll(ws.DraftsDir(), 0o750); err != nil {
			return domain.Feature{}, err
		}
		if err := atomicfile.Write(draft, []byte(content), 0o600); err != nil {
			return domain.Feature{}, err
		}
		// The draft is what the plan conversation edits, so it stops being
		// what the owner wrote the moment the architect touches it. Keep
		// what they submitted, unchanged, so the gate can say what it did
		// differently — a plan approved unattended is otherwise agreed by
		// nobody who can see what changed.
		if strings.TrimSpace(in.GoalDoc) != "" {
			submitted := filepath.Join(ws.DraftsDir(), string(f.ID)+".submitted.md")
			if err := atomicfile.Write(submitted, []byte(in.GoalDoc), 0o600); err != nil {
				return domain.Feature{}, err
			}
		}
	} else if in.Kind == domain.KindResearch {
		artifact := filepath.Join(ws.Root, f.ArtifactPath())
		seed := domain.ResearchSeed{Brief: in.Description}
		prov := domain.DraftProvenance{Source: in.Source}
		content := spec.SeededResearchTemplate(&f, seed, prov)
		if mode == domain.ModeDiagnosis {
			content = spec.SeededDiagnosisTemplate(&f, seed, prov)
		}
		if err := os.MkdirAll(filepath.Dir(artifact), 0o750); err != nil {
			return domain.Feature{}, err
		}
		if err := atomicfile.Write(artifact, []byte(content), 0o600); err != nil {
			return domain.Feature{}, err
		}
	} else if seed != "" || in.Acceptance != "" || in.Adopt != "" || (in.Kind == domain.KindBug && in.Discussion != "") {
		// seed the draft before persisting: the description's overflow fills
		// the Problem section (a title-sized description seeds nothing
		// there), and Acceptance fills the Verification plan (D10). Either
		// input alone is enough to warrant a draft; both are just a pre-fill
		// the spec agent still owns and approves.
		//
		// KindBug gets the bug report shape instead, and the overflow goes
		// through domain.ParseBugBody: the same headings the GitHub import
		// recognises in an issue body (Steps to reproduce, Expected,
		// Actual, Environment) route typed text into the report's sections,
		// and everything else lands in Summary. Acceptance has no
		// destination for a bug (SeededBugTemplate never pre-seeds Root
		// cause/Fix/Verification) and is silently unused here, matching
		// that template's existing contract.
		//
		// A feature keeps its overflow verbatim in Problem, with exactly one
		// heading recognised: an `## Acceptance` section is cut out and
		// seeds the Verification plan, the same place the --acceptance flag
		// writes. An explicit Acceptance wins over one found in the text.
		draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
		var content string
		if in.Kind == domain.KindBug {
			report := domain.ParseBugBody(seed)
			report.Discussion = in.Discussion
			content = spec.SeededBugTemplate(&f, report, domain.BugProvenance{Source: in.Source, ExternalRef: in.ExternalRef, Adopted: adoptedFor(in, inherited)}, in.Severity)
		} else {
			problem, acceptance := domain.SplitAcceptance(seed)
			if in.Acceptance != "" {
				acceptance = in.Acceptance
			}
			content = spec.SeededTemplate(&f, domain.DraftSeed{Problem: problem, Acceptance: acceptance}, domain.DraftProvenance{Source: in.Source, Adopted: adoptedFor(in, inherited)})
		}
		if err := os.MkdirAll(ws.DraftsDir(), 0o750); err != nil {
			return domain.Feature{}, err
		}
		if err := atomicfile.Write(draft, []byte(content), 0o600); err != nil {
			return domain.Feature{}, err
		}
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		return domain.Feature{}, err
	}
	return f, nil
}

// requireRepo defers to check when the caller wired one — for every repo,
// named or not — and otherwise fails closed on a named repo. See
// Input.RequireRepo's doc comment for why "no checker" must never read as
// "any repo is fine".
func requireRepo(check func(string) error, repo string) error {
	if check != nil {
		return check(repo)
	}
	if repo != "" {
		return fmt.Errorf("repository %q cannot be validated: no repository checker was wired", repo)
	}
	return nil
}
