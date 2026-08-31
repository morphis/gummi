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
	// Kind selects the card's workflow route and artifact home:
	// KindFeature/KindBug share the draft-seeded shape; KindResearch mints
	// an RS card and seeds its `## Brief` directly (research has no draft
	// step, so Full below is ignored for it — its route is always the
	// full one).
	Kind domain.Kind
	// Description is the free-form input text. For KindResearch it is
	// split into title/one-liner only (domain.SplitDescription); for
	// everything else it is split into title/one-liner/problem-seed
	// (domain.SplitFreeform), and the seed pre-fills the draft's Problem
	// section when non-empty.
	Description string
	// Profile is the model-role profile the card runs under (empty means
	// the workspace default).
	Profile string
	// Envelope is the card's credit budget ceiling.
	Envelope int
	// Full opts a feature/bug into the brainstorm+plan route instead of
	// the default quick route (domain.QuickRoute). Ignored for
	// KindResearch, which has no quick route at all.
	Full bool
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
	// ExternalRef is an optional external correlation id (e.g. a GitHub
	// issue reference), persisted as Feature.ExternalRef and echoed by
	// callers that track it.
	ExternalRef string
	// Acceptance is optional acceptance-criteria text seeded into the
	// draft's Verification plan section alongside the description's
	// overflow. Ignored for KindResearch.
	Acceptance string
	// GateApproval selects who crosses this card's design gates:
	// domain.GateGates (auto-crosses) or domain.GateOff (checkpoints
	// each for a human). Empty reads as domain.GateGates, matching
	// Feature.GateApproval's own empty-reads-as-auto contract.
	GateApproval string
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
	var title, oneLiner, seed string
	if in.Kind == domain.KindResearch {
		title, oneLiner = domain.SplitDescription(in.Description)
	} else {
		title, oneLiner, seed = domain.SplitFreeform(in.Description)
	}
	slug, err := domain.Slugify(title)
	if err != nil {
		return domain.Feature{}, err
	}
	if err := requireRepo(in.RequireRepo, in.Repo); err != nil {
		return domain.Feature{}, err
	}
	num, err := store.MintFeatureNum(ctx, ws.SeqFile())
	if err != nil {
		return domain.Feature{}, err
	}
	id, err := domain.NewID(in.Kind, num)
	if err != nil {
		return domain.Feature{}, err
	}
	skip := domain.QuickRoute()
	if in.Full || in.Kind == domain.KindResearch {
		skip = domain.SkipFlags{}
	}
	gate := in.GateApproval
	if gate == "" {
		gate = domain.GateGates
	}
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: in.Kind, Title: title, OneLiner: oneLiner,
		Slug: slug, Stage: workflow.Initial(in.Kind), Skip: skip,
		Profile: in.Profile, Budget: domain.Budget{Envelope: in.Envelope},
		GateApproval: gate,
		ExternalRef:  in.ExternalRef, Repo: in.Repo, CreatedAt: now, UpdatedAt: now,
	}
	if in.Kind == domain.KindResearch {
		artifact := filepath.Join(ws.Root, f.ArtifactPath())
		content := spec.SeededResearchTemplate(&f, domain.ResearchSeed{Brief: in.Description}, domain.DraftProvenance{})
		if err := os.MkdirAll(filepath.Dir(artifact), 0o750); err != nil {
			return domain.Feature{}, err
		}
		if err := atomicfile.Write(artifact, []byte(content), 0o600); err != nil {
			return domain.Feature{}, err
		}
	} else if seed != "" || in.Acceptance != "" {
		// seed the draft before persisting: the description's overflow fills
		// the Problem section (a title-sized description seeds nothing
		// there), and Acceptance fills the Verification plan (D10). Either
		// input alone is enough to warrant a draft; both are just a pre-fill
		// the spec agent still owns and approves.
		draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
		content := spec.SeededTemplate(&f, domain.DraftSeed{Problem: seed, Acceptance: in.Acceptance}, domain.DraftProvenance{})
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
