package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/verifydoc"
	"github.com/morphis/gummi/internal/workflow"
)

// AdvanceStatus classifies what Advance did (or why it could not move the
// item forward), so every driver — the TUI and the headless run loop —
// routes the outcome without re-deriving the state machine. Genuine
// infrastructure failures (store, git, promotion) are returned as errors
// instead; a Status describes a normal-flow gate outcome.
type AdvanceStatus int

const (
	// StatusAdvanced: the item transitioned to Result.To.
	StatusAdvanced AdvanceStatus = iota
	// StatusNoop: the item is terminal — nothing to advance.
	StatusNoop
	// StatusBlockedQuestions: unresolved user %% threads block the gate.
	StatusBlockedQuestions
	// StatusBlockedDiff: unresolved diff annotations block the gate.
	StatusBlockedDiff
	// StatusBlockedDependency: the item cannot enter its coding stage while
	// one of its direct dependencies is still short of Done — its work is
	// not yet available to build on.
	StatusBlockedDependency
	// StatusBlockedDocument: a research card's Verify→done gate, where the
	// document fails the deterministic citation/coverage floor
	// (internal/verifydoc). The feature stays at verify; DocumentReport
	// carries the broken citations and unmapped questions.
	StatusBlockedDocument
	// StatusNeedsMerge: the verify→done gate, where the branch is ahead of
	// main and awaits a squash-merge decision. Advance never merges — the
	// caller lands the branch (the TUI collects a commit message; the
	// headless driver stops at the verified branch, DESIGN §10.6/§12).
	StatusNeedsMerge
	// StatusBlockedOmission: bug-only omission gate fired at the
	// verify→done edge — a bug with a clean-present env prerequisite, zero
	// [env:] live checks, and no human waiver cannot finalize.
	StatusBlockedOmission
	// StatusBlockedUndrafted: the stage the item is leaving wrote nothing
	// into the one section that gate expects as its output — a stage that
	// ran and produced no work must not be waved through just because
	// nothing is technically wrong with it (measured: 4/9 spec sessions
	// drafted nothing, auto-approved, and implement started from a stub).
	// Undrafted names the sections; the stage stays put.
	StatusBlockedUndrafted
)

// BlockingDep names a single outstanding dependency blocking a card's
// entry into its coding stage: the dependency's ID and the stage it is
// currently at (anything short of Done counts as unmet).
type BlockingDep struct {
	ID    domain.FeatureID `json:"id"`
	Stage domain.Stage     `json:"stage"`
}

// String formats a blocking dependency for a status notice, e.g.
// "FD-003@implement".
func (b BlockingDep) String() string { return fmt.Sprintf("%s@%s", b.ID, b.Stage) }

// AdvanceResult reports the outcome of one Advance call. Only the fields
// relevant to Status are populated.
type AdvanceResult struct {
	// Feature is the item after the call: transitioned on StatusAdvanced,
	// otherwise the loaded (unchanged) record.
	Feature domain.Feature
	From    domain.Stage // the stage the item started in
	To      domain.Stage // the target stage (the intended forward edge)
	Status  AdvanceStatus
	// Blockers is the open-thread / open-comment count for the Blocked*
	// statuses.
	Blockers int
	// BlockingDeps names each direct dependency still short of Done when
	// Status is StatusBlockedDependency — its ID and the stage it is
	// stuck at.
	BlockingDeps []BlockingDep
	// DocumentReport carries the broken citations and unmapped coverage
	// items when Status is StatusBlockedDocument.
	DocumentReport verifydoc.Report
	// Reason is populated only for StatusBlockedOmission; it mirrors the
	// human-facing reason produced by the shared omission-gate predicate.
	Reason string
	// Undrafted names the required section(s) the departing stage left
	// blank when Status is StatusBlockedUndrafted (requiredSections' entry
	// for this edge, filtered to the ones spec.UndraftedSections found
	// empty).
	Undrafted []string
	// EnteredWorktree reports that this call created the item's worktree
	// (the design→work approval gate), so the caller can kick off the
	// one-shot check-discovery + baseline passes over the fresh branch.
	EnteredWorktree bool
	// EstimatedCredits / EstimateSamples record the historical-median
	// envelope estimate applied at spec approval (0 when none was). The
	// engine leaves the follow-on scribe estimate — an agent pass — to the
	// caller's policy: whether to run it (the TUI gates it on its default
	// envelope) is not a floor concern, so From is enough to key it on.
	EstimatedCredits int
	EstimateSamples  int
}

// Advance moves a feature along its primary forward edge — the engine-level
// quality floor both drivers share (DESIGN §8, the extraction of the TUI's
// former advanceStage). It owns the gate mechanics: blocker checks
// (unresolved %% threads and diff annotations block every human gate,
// §6.1), worktree creation and artifact promotion at the design→work
// approval gate (§10.11), plan-time envelope estimation at spec approval
// (§5.1), and the recorded store.Transition under actor. It never merges:
// the verify→done gate reports StatusNeedsMerge for the caller to land.
//
// Infrastructure failures surface as errors; a blocked/terminal/merge gate
// is a Status with no error, so a driver can style a notice or map a typed
// exit from the same return.
func (e *Engine) Advance(ctx context.Context, id domain.FeatureID, actor string) (AdvanceResult, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return AdvanceResult{}, err
	}
	res := AdvanceResult{Feature: f, From: f.Stage}

	next := e.nextStage(f)
	if next == f.Stage {
		// no forward edge — a terminal item has nothing to advance.
		res.Status = StatusNoop
		return res, nil
	}
	res.To = next

	// Leaving todo is not a gate. Nothing has been produced to review, no
	// agent has run, and the artifact holds nothing but its template — so
	// the blocker checks below have nothing to be about, and running them
	// here wedges the card outright: a comment written before the first
	// stage (the natural way to add context for the agent you are about
	// to start) blocked the only action the card had, and the refusal's
	// own advice — request changes instead — is refused too, because todo
	// has no agent to send them to. A comment written at todo is INPUT to
	// the stage about to run, not an objection to work already done; it
	// travels into the plan session in the artifact, which is where the
	// architect reads it.
	if f.Stage != domain.StageTodo {
		// unresolved user %% annotations block every human gate, not just
		// spec approval — the gate re-opens only once they resolve
		// (DESIGN §6.1).
		if n := e.openQuestionsBlockingGate(f); n > 0 {
			res.Status, res.Blockers = StatusBlockedQuestions, n
			return res, nil
		}
		// a stage that ran and wrote nothing into its one required section
		// is not a clean crossing — auto-approval must not wave a stub
		// through to the next stage (the measured failure this gate exists
		// to catch).
		if names := e.undraftedBlockingGate(f); len(names) > 0 {
			res.Status, res.Undrafted = StatusBlockedUndrafted, names
			return res, nil
		}
		// so do unresolved diff annotations, the gate's other backend.
		if n := e.openDiffCommentsBlockingGate(ctx, id); n > 0 {
			res.Status, res.Blockers = StatusBlockedDiff, n
			return res, nil
		}
	}

	// A research card's Verify→done gate additionally runs the
	// deterministic citation/coverage floor (internal/verifydoc): a broken
	// citation or an unmapped brief question bounces the card back to
	// investigate the same way an open thread does, before any worktree or
	// merge bookkeeping runs.
	if next == domain.StageDone && f.Kind == domain.KindResearch {
		report, err := e.documentReport(ctx, &f)
		if err != nil {
			return res, err
		}
		if !report.Pass() {
			res.Status, res.DocumentReport = StatusBlockedDocument, report
			return res, nil
		}
	}

	// A card cannot enter its coding stage while one of its direct
	// dependencies is still short of Done — its work is not yet available
	// to build on. The gate fires only at coding-stage entry (the resolved
	// forward edge's target is the item's coding stage), and only on this
	// call, read-on-Advance like every other blocker. The stored stage is
	// left unchanged.
	if e.nextStage(f) == domain.StageImplement {
		if deps, err := e.unmetDeps(ctx, id); err != nil {
			return res, err
		} else if len(deps) > 0 {
			res.Status, res.BlockingDeps = StatusBlockedDependency, deps
			return res, nil
		}
	}

	// Advancing out of Verify is the "this feature is done" decision: the
	// branch lands on main as one squash commit before the record moves to
	// Done. Advance never merges — it reports that a landing is owed. A
	// branch that already landed, is already gone, or never got any commits
	// of its own (nothing to land — the artifact lives in the workspace,
	// not on the branch) skips straight to the transition.
	if next == domain.StageDone {
		// Bug-only omission gate: a bug with a clean-present env
		// prerequisite, zero [env:] live checks, and no human waiver cannot
		// finalize. This runs before any worktree/git calls in this branch,
		// and mirrors gateVerifyVerdict's skip-on-error behavior.
		if reason, blocked := e.omissionGateBlocksAdvance(ctx, f); blocked {
			res.Status = StatusBlockedOmission
			res.Reason = reason
			return res, nil
		}

		wt, err := e.mgr(ctx, &f)
		if err != nil {
			return res, err
		}
		if exists, err := wt.BranchExists(ctx, &f); err != nil {
			return res, err
		} else if exists {
			if landed, err := wt.Landed(ctx, &f); err != nil {
				return res, err
			} else if !landed {
				if ahead, err := wt.BranchAhead(ctx, &f); err != nil {
					return res, err
				} else if ahead {
					res.Status = StatusNeedsMerge
					// The verify gate has passed and the branch is ready to
					// land: stamp the verified marker (once, keeping the first
					// pass's time stable) so status can report `verified` at
					// this terminal state without moving the stage off verify.
					if f.VerifiedAt.IsZero() {
						now := time.Now().UTC()
						if err := e.cfg.Store.SetVerifiedAt(ctx, id, now); err != nil {
							return res, err
						}
						res.Feature.VerifiedAt = now
					}
					return res, nil
				}
			}
		}
	}

	// Crossing out of the design phase is still the approval gate, but it
	// no longer creates or destroys anything: the card has run in its own
	// worktree since its first stage, and its artifact has been at its
	// workspace home since then too. What is still owed here is the
	// one-shot work that belongs to "work is starting", not to "a tree
	// exists" — check discovery, the baseline pass, and the plan-time
	// envelope estimate.
	//
	// The crossing is identified by its shape rather than by a side
	// effect: leaving an interactive stage for a non-interactive one
	// happens exactly once per card in the forward direction. A bounce
	// (review/verify → work) leaves a non-interactive stage, so it never
	// re-triggers discovery the way a worktree-existence test would have
	// had to be taught not to.
	if f.Kind != domain.KindResearch && f.Stage == domain.StagePlan && next == domain.StageImplement {
		res.EnteredWorktree = true
		// Ensure, not Create: the card has almost certainly been running in
		// its worktree since its first design stage, in which case this is
		// a no-op. It still runs, because a card advanced without ever
		// running a stage (a skip-walked card, a board driven by hand) has
		// no tree yet, and everything past this gate — the diff at the
		// gate, check discovery, the landing — assumes one exists.
		wt, werr := e.mgr(ctx, &f)
		if werr != nil {
			return res, werr
		}
		if _, werr := wt.Ensure(ctx, &f); werr != nil {
			return res, werr
		}
		// Likewise idempotent: locate promotes the artifact on the card's
		// first stage run, so this is a no-op for any card that has
		// actually been worked on. It runs for the same reason Ensure
		// does — a card walked forward without a stage ever running still
		// has to arrive past this gate with its artifact at home.
		if werr := e.promoteDraft(&f); werr != nil {
			return res, werr
		}
		// plan-time estimation: size the spend-plan envelope from what
		// completed cards cost, before budgeted autonomous work begins
		// (DESIGN §5.1). This is the design→work crossing, which is the
		// only place it was ever owed.
		res.EstimatedCredits, res.EstimateSamples = e.estimateEnvelope(ctx, &f)
	}

	// Leaving the work stage hands the diff to something that judges it —
	// verify's checks for a feature or bug, research's Review stage. The
	// agent shells out to git itself rather than routing through
	// Manager.Diff(), so this transition is the last place the engine can
	// fail cleanly before a poisoned diff is judged: refuse to cross if
	// main was rewound past the recorded fork.
	//
	// The guard used to key on entering Review. Review stopped being a
	// stage for features and bugs, so the equivalent crossing is the one
	// into Verify; research still has its Review stage and keeps it here.
	if next == domain.StageVerify && f.Kind != domain.KindResearch {
		wt, err := e.mgr(ctx, &f)
		if err != nil {
			return res, err
		}
		if err := wt.AssertNoForkDrift(ctx, &f); err != nil {
			return res, err
		}
	}
	if _, err := e.cfg.Store.Transition(ctx, id, next, actor); err != nil {
		return res, err
	}
	e.Drop(id) // the old stage's session is stale now
	res.Feature = f
	res.Feature.Stage = next
	res.Status = StatusAdvanced
	return res, nil
}

// nextStage resolves a feature's forward target — the single edge every
// forward path into the coding stage resolves through. It prefers the skip
// edge when the flag opts the item out of the intermediate stage, otherwise
// the primary edge; out of Review/Verify it takes the forward edge (never
// the rerun, which is a bounce Advance doesn't make). Returns the stage
// unchanged when the item is terminal.
func (e *Engine) nextStage(f domain.Feature) domain.Stage {
	nexts := workflow.Next(f.Stage)
	if len(nexts) == 0 {
		return f.Stage
	}
	// nexts[0], always: the graph lists a stage's forward edge first and
	// its rerun edges after it, and Advance only ever goes forward.
	//
	// This used to take the LAST entry, because a skip edge was appended
	// after the forward one and a skipped stage meant the later edge was
	// the one to take. Skip edges went with SkipFlags, so the last entry
	// is now the rerun bounce — implement → plan, verify → implement —
	// and taking it would walk the card backwards forever.
	return nexts[0]
}

// unmetDeps returns each direct dependency of id still short of Done — its
// ID and current stage. Empty when there are no dependencies or all are
// done. Only direct edges are read: transitivity holds transitively,
// because a card reaches Done only by passing its own coding gate.
func (e *Engine) unmetDeps(ctx context.Context, id domain.FeatureID) ([]BlockingDep, error) {
	ids, err := e.cfg.Store.ListDependencies(ctx, id)
	if err != nil {
		return nil, err
	}
	var deps []BlockingDep
	for _, depID := range ids {
		dep, err := e.cfg.Store.GetFeature(ctx, depID)
		if err != nil {
			return nil, err
		}
		if dep.Stage != domain.StageDone {
			deps = append(deps, BlockingDep{ID: depID, Stage: dep.Stage})
		}
	}
	return deps, nil
}

// GateBlockers reports the open %%-thread and diff-annotation counts, and
// the outstanding dependencies, that would block advancing id's current
// gate, without moving it — the read-only view a caller-approval
// checkpoint needs before offering to cross a gate (the headless driver's
// --gate-approval=caller path). It reuses the exact floor checks Advance
// applies — deps under the same coding-stage condition — so the pre-check
// and the gate can never disagree.
func (e *Engine) GateBlockers(ctx context.Context, id domain.FeatureID) (specOpen, diffOpen int, deps []BlockingDep, err error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return 0, 0, nil, err
	}
	specOpen = e.openQuestionsBlockingGate(f)
	diffOpen = e.openDiffCommentsBlockingGate(ctx, id)
	if e.nextStage(f) == domain.StageImplement {
		if deps, err = e.unmetDeps(ctx, id); err != nil {
			return 0, 0, nil, err
		}
	}
	return specOpen, diffOpen, deps, nil
}

// DependencyBlockers reports the direct dependencies of id still short of
// Done, but only when the card's next forward step is entering its coding
// stage — exactly the dependency half of the Advance gate, factored out so
// the board badge can share one definition with the gate without re-running
// the thread/comment IO GateBlockers also does. Returns nil when the next
// forward step is not the coding stage (a card in brainstorm/spec never
// shows a badge even with an unmet dependency listed), or when every direct
// dependency is Done. The stored stage is left unchanged — this is a
// read-only check, never a transition.
func (e *Engine) DependencyBlockers(ctx context.Context, id domain.FeatureID) ([]BlockingDep, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.nextStage(f) != domain.StageImplement {
		return nil, nil
	}
	return e.unmetDeps(ctx, id)
}

// estimateEnvelope sizes a feature's spend-plan envelope from the median
// spend of previously completed features and persists it, returning the
// applied estimate and the number of metered samples behind it. It only
// fills an *unset* envelope (0), so an explicit envelope a user chose is
// respected, not silently replaced. Returns (0, 0) when the envelope is
// already set, when there's no history to learn from, or on any error —
// estimation is best-effort and never blocks the transition.
func (e *Engine) estimateEnvelope(ctx context.Context, f *domain.Feature) (credits, samples int) {
	if f.Budget.Envelope != 0 {
		return 0, 0 // an explicit envelope wins over estimation
	}
	feats, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		return 0, 0
	}
	var hist []domain.Spend
	for _, x := range feats {
		if x.ID != f.ID && x.Stage == domain.StageDone {
			hist = append(hist, x.Spend)
		}
	}
	env, n := domain.EstimateEnvelope(hist)
	if n == 0 || env <= 0 {
		return 0, 0
	}
	f.Budget.Envelope = int(env)
	if err := e.cfg.Store.UpdateFeature(ctx, f); err != nil {
		return 0, 0
	}
	// n is the number of past features that metered spend (zero-spend
	// completions are not samples).
	return int(env), n
}

// EstimateNotice formats the human-readable suffix a driver appends to the
// spec-approval transition notice when a historical estimate was applied.
// Empty when none was, so it composes cleanly onto any notice.
func (r AdvanceResult) EstimateNotice() string {
	if r.EstimatedCredits <= 0 {
		return ""
	}
	// "card(s)" was the last of round 2's §6 plural sweep to survive, on a
	// line the user reads at every spec approval.
	plural := "s"
	if r.EstimateSamples == 1 {
		plural = ""
	}
	return fmt.Sprintf(" · budget estimated at %d credits from %d metered card%s",
		r.EstimatedCredits, r.EstimateSamples, plural)
}

// artifactFile resolves where an item's design artifact lives right now:
// its workspace home once promoted, the draft before then, or the worktree
// copy of an item mid-flight from the committed-artifact era. Empty when
// none exists yet.
func (e *Engine) artifactFile(f *domain.Feature) string {
	root := e.pool.Root()
	return spec.LocateArtifact(
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
	)
}

// documentReport runs the deterministic verifydoc floor against a research
// card's artifact: the file map covers only the paths its Findings
// citations name, read from the card's managed repo checkout, so the
// report never leaks existence or line counts of any other file.
func (e *Engine) documentReport(ctx context.Context, f *domain.Feature) (verifydoc.Report, error) {
	path := e.artifactFile(f)
	if path == "" {
		return verifydoc.Report{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return verifydoc.Report{}, nil
	}
	artifact := string(raw)
	wt, err := e.mgr(ctx, f)
	if err != nil {
		return verifydoc.Report{}, err
	}
	files := fileMap(wt.RepoRoot(), verifydoc.CitedPaths(artifact))
	return verifydoc.Check(artifact, files), nil
}

// fileMap reads each cited path's lines from root, keyed by the path as
// cited. A path that would resolve outside root is skipped and never
// read — containment is enforced once at citation-parse time too
// (verifydoc.Check), but fileMap asserts it independently so the file set
// handed to the report can never extend past the checkout.
func fileMap(root string, paths []string) map[string][]string {
	out := make(map[string][]string, len(paths))
	for _, p := range paths {
		full := filepath.Join(root, p)
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		data, err := os.ReadFile(full) //nolint:gosec // rel is checked above to stay under root
		if err != nil {
			continue // missing file: verifydoc.Check reports it as a citation issue
		}
		out[p] = strings.Split(string(data), "\n")
	}
	return out
}

// requiredSections names the section(s) a gate expects the departing
// stage to have actually written, keyed on the edge Advance is crossing.
// Deliberately narrow — a stage's own output, not a completeness
// checklist over the template: the gate is checking that the stage did
// its own job (spec chose an approach, diagnose found a root cause, plan
// wrote the notes implement needs, and the coding stages wrote their
// verification story), not that every template section got filled in.
//
// Research used to fall through to nil on every edge, on the theory that
// its verify→done edge already had a floor of its own (verifydoc). It
// does not: verifydoc checks that the citations in Findings resolve and
// that the brief's questions are answered, and a document where nothing
// was ever written has no citations and no questions — so it passes
// vacuously. That is how a research card walked todo→done in four
// keypresses, spending nothing, with every section of its document still
// holding the `%% @gummi:` prompt the template shipped. The rows below
// give each research edge the same treatment the other kinds get.
func requiredSections(kind domain.Kind, from, to domain.Stage) []string {
	switch {
	// The design gate. Its row grew from one section to two when the
	// GATES shrank from two to one: a feature used to cross spec→plan
	// owing "Chosen approach" and plan→implement owing "Implementation
	// notes", and merging those stages merged their gates. This is the
	// same coverage collapsed, not the completeness checklist 0a warned
	// against — each section is still one stage's own output, there is
	// just one stage now where there were three.
	case kind == domain.KindFeature && from == domain.StagePlan && to == domain.StageImplement:
		return []string{"Chosen approach", "Implementation notes"}
	case kind == domain.KindBug && from == domain.StagePlan && to == domain.StageImplement:
		return []string{"Root cause"}
	case kind == domain.KindFeature && to == domain.StageDone:
		return []string{"Verification plan"}
	case kind == domain.KindBug && to == domain.StageDone:
		return []string{"Verification"}

	// The research design gate. The row is the shape contract's own stop
	// condition, quoted from shapeHint (hints.go): "stop when the
	// question, its constraints, and the direction are set". Three
	// sections rather than one because that stage settles three separate
	// decisions the survey then runs on — not a completeness sweep of the
	// ten-section template: Brief is the requester's own words, and the
	// remaining six are later stages' work.
	case kind == domain.KindResearch && from == domain.StagePlan && to == domain.StageImplement:
		return []string{"Questions", "Constraints", "Direction"}

	// The research build gate. investigateHint sends the build stage to
	// survey the question read-only and "record your findings … in the
	// research document as you go": Findings is that survey, and it is the
	// section every later check reads — verifydoc resolves its citations,
	// and with nothing in it there is nothing to resolve.
	case kind == domain.KindResearch && from == domain.StageImplement && to == domain.StageVerify:
		return []string{"Findings"}

	// The research done gate — the edge that decomposes the document into
	// feature cards. This one is keyed on what crossing CONSUMES rather
	// than on what the departing stage wrote, because for research the
	// departing stage wrote nothing: verify is a read-only critique whose
	// verdict lives in the session, not in the document. Findings is what
	// the evidence was supposed to be, so a document reaching done without
	// it has nothing to have proved anything with — the same shape as a
	// feature owing its Verification plan here.
	//
	// `## Slices` is deliberately NOT required, even though it is the
	// decomposition's input. A research card that concludes no follow-on
	// work is needed is a legitimate terminal, not a stalled one:
	// DecomposeForCard treats a doc with no unsettled rows as a cheap
	// no-op rather than an error, and TestZeroSliceRSExitsDoneCleanly
	// pins that a zero-slice RS reaches done without ever spawning an
	// architect. Demanding Slices here would forbid the honest answer
	// "nothing to build", which is exactly the judgement this stage is
	// allowed to reach.
	case kind == domain.KindResearch && to == domain.StageDone:
		return []string{"Findings"}

	default:
		return nil
	}
}

// UndraftedGateSections names the required section(s) the stage crossing
// from `from` to `to` left blank in an artifact's text — the read-only
// half of the undrafted-sections gate, split out so a caller can name the
// same blocker Advance would refuse on without re-deriving which sections
// an edge owes. Nil when the edge owes nothing or nothing is blank.
func UndraftedGateSections(kind domain.Kind, from, to domain.Stage, artifact string) []string {
	want := requiredSections(kind, from, to)
	if len(want) == 0 {
		return nil
	}
	return spec.UndraftedSections(artifact, want)
}

// undraftedBlockingGate returns the required section(s) the departing
// stage left undrafted, or nil when the gate has nothing to check or the
// artifact can't be read. Mirrors openQuestionsBlockingGate's zero-on-error
// contract: a missing or unreadable artifact must never wedge the gate
// shut, because a card whose artifact moved (or hasn't been created yet)
// would otherwise become permanently unadvanceable.
func (e *Engine) undraftedBlockingGate(f domain.Feature) []string {
	path := e.artifactFile(&f)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return UndraftedGateSections(f.Kind, f.Stage, e.nextStage(f), string(raw))
}

// openQuestionsBlockingGate returns the number of open, USER-authored `%%`
// annotations in an item's artifact (DESIGN §6.1: unresolved annotations
// block the gate). It reads wherever the artifact lives right now; zero for
// a missing or unreadable artifact, so a failed read never wedges the gate.
func (e *Engine) openQuestionsBlockingGate(f domain.Feature) int {
	path := e.artifactFile(&f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(spec.Parse(string(raw)).UserOpenThreads())
}

// omissionGateBlocksAdvance is the Advance-side counterpart of
// gateVerifyVerdict. It re-runs env probes fresh against the feature's
// worktree and, if the artifact can be read, asks omissionGateReason whether
// the verify→done edge should be held open. A missing or unreadable artifact
// falls through (returns blocked=false), mirroring openQuestionsBlockingGate's
// zero-on-error pattern and gateVerifyVerdict's skip-on-error behavior.
func (e *Engine) omissionGateBlocksAdvance(ctx context.Context, f domain.Feature) (reason string, blocked bool) {
	if f.Kind != domain.KindBug {
		return "", false
	}
	if !e.probeCleanPresent(ctx, &f) {
		return "", false
	}
	path := e.artifactFile(&f)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	reason = omissionGateReason(f.Kind, true, string(raw))
	if reason == "" {
		return "", false
	}
	return reason, true
}

// openDiffCommentsBlockingGate returns the number of unresolved diff
// annotations on an item — the diff-backend half of §6.1's gate check.
// Zero on any store error: like an unreadable artifact, a failed read never
// wedges the gate shut.
func (e *Engine) openDiffCommentsBlockingGate(ctx context.Context, id domain.FeatureID) int {
	anns, err := e.cfg.Store.ListDiffAnnotations(ctx, id)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range anns {
		if !a.Resolved {
			n++
		}
	}
	return n
}

// promoteDraft moves the artifact draft (spec or bug report) to its
// workspace home, from wherever it currently lives. It is idempotent: an
// artifact already at home is left alone, and an item that never had a
// draft gets a fresh template, so the artifact always exists from the
// approval gate on. The draft directory is gummi workspace content — it
// never enters the worktree and is never committed.
func (e *Engine) promoteDraft(f *domain.Feature) error {
	root := e.pool.Root()
	return spec.Promote(
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
		f,
	)
}
