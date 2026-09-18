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
	// FoundBy records the goal that filed this open-board card as found
	// along the way. Ignored when Goal is set.
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
	// Research cards are exempt: they never cut a branch.
	if in.Kind != domain.KindResearch {
		probe := domain.Feature{Kind: in.Kind, Slug: slug, BranchScheme: domain.DefaultBranchScheme}
		branch := probe.BranchName()
		if owner, taken, terr := store.BranchTaken(ctx, in.Repo, branch, ""); terr != nil {
			return domain.Feature{}, terr
		} else if taken {
			return domain.Feature{}, fmt.Errorf(
				"%s already uses the branch %s — retitle this card so it gets a different one", owner, branch)
		}
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
	} else if seed != "" || in.Acceptance != "" || (in.Kind == domain.KindBug && in.Discussion != "") {
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
			content = spec.SeededBugTemplate(&f, report, domain.BugProvenance{Source: in.Source, ExternalRef: in.ExternalRef}, in.Severity)
		} else {
			problem, acceptance := domain.SplitAcceptance(seed)
			if in.Acceptance != "" {
				acceptance = in.Acceptance
			}
			content = spec.SeededTemplate(&f, domain.DraftSeed{Problem: problem, Acceptance: acceptance}, domain.DraftProvenance{Source: in.Source})
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
