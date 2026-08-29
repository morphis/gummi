package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/workflow"
	"github.com/morphis/gummi/internal/worktree"
)

// GateApproval selects who approves a design gate. The quality floor
// (review/verify, blockers) runs the same either way — this only decides
// whether a design gate auto-crosses or checkpoints to the caller. The
// canonical values live in domain (persisted on the card); these alias them
// so the driver's callers keep a local name.
const (
	GateAuto   = domain.GateAuto
	GateCaller = domain.GateCaller
)

// Options configures one run. Envelope is required (D6); a missing agent
// or envelope fails loud before any work starts.
type Options struct {
	Envelope     int
	Profile      string
	Full         bool   // opt into the brainstorm+plan route (default: quick)
	GateApproval string // GateAuto (default) | GateCaller
	// GateApprovalSet reports that the caller passed --gate-approval
	// explicitly on this invocation. A resume uses it to decide between
	// overriding the card's persisted mode (set) and inheriting it (unset),
	// so an unattended resume no longer silently reverts to auto.
	GateApprovalSet bool
	StageTimeout    time.Duration // per-stage inactivity budget (0 disables)
	Autonomous      bool          // auto-take the recommended answer instead of checkpointing (D5)
	Verbose         bool          // add per-tool-call activity lines to the stream
	Ref             string        // optional external correlation id, echoed in NDJSON + persisted as ExternalRef (D11)
	Acceptance      string        // optional acceptance-criteria text, seeded into the draft's Verification plan (D10)
	Until           domain.Stage  // stop cleanly before crossing the gate that leaves this design stage (B3); "" runs to verified
	// Repo is the managed repository the created card belongs to (a
	// configured `repos:` name, or "" for the workspace default).
	Repo string
}

// Driver runs one feature through the engine's gate floor headlessly. It
// is the sole consumer of engine.Events() for its process — one feature
// per run (D2/D7) — so it drains the stream continuously and acts
// synchronously between reads, the same contract the TUI's update loop
// relies on.
type Driver struct {
	eng        *engine.Engine
	store      *state.Store
	roundStore rounds.Store // round-counter persistence seam (defaults to store)
	ws         state.Workspace
	out        *emitter
	opts       Options
	actor      string // transition actor recorded in history ("auto" | "caller")

	// loop state for the single feature this process governs.
	rounds      map[roundKey]int // automatic loop rounds burned, keyed by (id, round_kind)
	reviewsRun  int              // review stages entered so far (for the done receipt)
	opening     string           // one-shot message to send on the next interactive stage (resume answer / change note)
	bounceNote  string           // one-shot addendum to the next implement/fix kickoff after a --bounce resume
	curStage    domain.Stage     // stage currently being driven (for verbose activity lines)
	activityCur int              // cursor into the live session's activity feed
	sentTurn    bool             // a turn was dispatched to the agent this stage (drives the timeout diagnosis)
}

// roundKey is the fast-path round-counter map's key: one entry per
// (feature, round kind), matching the keyed store row.
type roundKey struct {
	id   domain.FeatureID
	kind domain.RoundKind
}

// round reads the fast-path count for (id, kind), defaulting to 0.
func (d *Driver) round(id domain.FeatureID, kind domain.RoundKind) int {
	return d.rounds[roundKey{id, kind}]
}

// setRound writes the fast-path count for (id, kind).
func (d *Driver) setRound(id domain.FeatureID, kind domain.RoundKind, n int) {
	d.rounds[roundKey{id, kind}] = n
}

// New builds a driver writing its NDJSON stream to out. The caller owns
// the engine and agent lifetime (as cmd/gummi/ingest.go does).
func New(eng *engine.Engine, store *state.Store, ws state.Workspace, out interface{ Write([]byte) (int, error) }, opts Options) *Driver {
	if opts.GateApproval == "" {
		opts.GateApproval = GateAuto
	}
	d := &Driver{
		eng:        eng,
		store:      store,
		roundStore: store,
		rounds:     map[roundKey]int{},
		ws:         ws,
		out:        newEmitter(out, opts.Verbose),
		opts:       opts,
	}
	d.setGate(opts.GateApproval)
	return d
}

// setGate points the driver at a gate-approval mode, keeping the derived
// transition actor ("auto"|"caller") in lockstep. An empty mode reads as
// GateAuto. It is called at construction and again on resume once the
// card's persisted mode is known.
func (d *Driver) setGate(mode string) {
	if mode == "" {
		mode = GateAuto
	}
	d.opts.GateApproval = mode
	d.actor = "auto"
	if mode == GateCaller {
		d.actor = "caller"
	}
}

// Run creates one feature from a free-form description and drives it to a
// terminal Outcome. The quick route is the default; --full opts into the
// brainstorm+plan route (D3). It is the convenience form of Create + Drive
// for callers that need no lock between the two; the CLI drives via the
// split so it can hold the card's per-card lock for the whole drive.
func (d *Driver) Run(ctx context.Context, desc string) (Outcome, error) {
	f, err := d.Create(ctx, domain.KindFeature, desc)
	if err != nil {
		return d.fail(ctx, "", err)
	}
	return d.Drive(ctx, f)
}

// Create mints one feature from a free-form description and persists it,
// but does not drive it — the caller owns the drive (and any lock that
// should span it). The quick route is the default; --full opts into the
// brainstorm+plan route (D3). kind selects the route: KindFeature/KindBug
// use the existing draft-seeded shape; KindResearch mints an RS card and
// seeds its `## Brief` directly (research has no draft step).
func (d *Driver) Create(ctx context.Context, kind domain.Kind, desc string) (domain.Feature, error) {
	// validate --until against the route this run will take before minting a
	// feature, so a bad stop target never leaves a stray FD in the backlog.
	skip := domain.QuickRoute()
	if d.opts.Full {
		skip = domain.SkipFlags{}
	}
	if kind == domain.KindResearch {
		skip = domain.SkipFlags{}
	}
	if err := ValidateUntil(d.opts.Until, kind, skip); err != nil {
		return domain.Feature{}, err
	}
	f, err := d.createFeature(ctx, kind, desc)
	if err != nil {
		return domain.Feature{}, err
	}
	route := "quick"
	if d.opts.Full {
		route = "full"
	}
	d.out.emit(createdEvent{
		Event: "created", ID: string(f.ID), Ref: d.opts.Ref,
		Branch: f.BranchName(), Route: route, Envelope: d.opts.Envelope,
	})
	return f, nil
}

// Drive runs an already-created feature to a terminal Outcome. It is the
// driving half of Run, split out so a caller can mint a card with Create,
// take its per-card lock, and only then drive it.
func (d *Driver) Drive(ctx context.Context, f domain.Feature) (Outcome, error) {
	return d.drive(ctx, f.ID)
}

// ResumeInput carries a resume's decision. Exactly one field is set:
// Answer resolves a delegated ask_user; Approve/RequestChanges resolve a
// caller design gate; Bounce sends a verify-failed (or review-failed)
// feature back to the work stage — the headless counterpart of the TUI's
// `b` key — with the (possibly empty) string carried as an addendum to the
// next implement/fix kickoff; all-zero re-runs the parked stage (after an
// exhaustion top-up, a timeout, or an escalation).
type ResumeInput struct {
	Answer         *string
	Approve        bool
	RequestChanges *string
	Bounce         *string
}

// Resume rehydrates the engine's persisted sessions, applies the caller's
// decision, and drives on. gummi's restartability (SQLite state, spec on
// branch, session resume) makes this free (DESIGN §4).
func (d *Driver) Resume(ctx context.Context, id domain.FeatureID, in ResumeInput) (Outcome, error) {
	if err := d.eng.Restore(ctx); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("restoring sessions: %w", err))
	}
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if err := ValidateUntil(d.opts.Until, f.Kind, f.Skip); err != nil {
		return d.fail(ctx, string(id), err)
	}

	// gate-approval mode persists on the card. A resume that re-passes
	// --gate-approval overrides (and re-persists) it; one that doesn't
	// inherits the mode `run` chose, instead of silently reverting to auto.
	if d.opts.GateApprovalSet {
		mode := d.opts.GateApproval // "auto"|"caller", validated by the CLI
		stored := f.GateApproval
		if stored == "" {
			stored = GateAuto
		}
		if mode != stored {
			if err := d.store.SetGateApproval(ctx, id, mode); err != nil {
				return d.fail(ctx, string(id), fmt.Errorf("persisting gate-approval: %w", err))
			}
			f.GateApproval = mode
		}
		d.setGate(mode)
	} else if f.GateApproval != "" {
		d.setGate(f.GateApproval)
	}

	// the correlation line first, so a resume's stream is self-identifying.
	d.out.emit(resumedEvent{Event: "resumed", ID: string(id), Ref: d.opts.Ref, Stage: string(f.Stage)})

	// --envelope raises the feature's credit budget before the parked stage
	// re-runs — the headless path to clear an `exhausted` exit. It is a floor:
	// a value at or below the current envelope is a no-op, so a caller passing
	// `--envelope` on a routine resume can never shrink an in-flight budget.
	if d.opts.Envelope > f.Budget.Envelope {
		from := f.Budget.Envelope
		f.Budget.Envelope = d.opts.Envelope
		if err := d.store.UpdateFeature(ctx, &f); err != nil {
			return d.fail(ctx, string(id), fmt.Errorf("raising envelope: %w", err))
		}
		d.out.emit(envelopeRaisedEvent{Event: "envelope", ID: string(id), From: from, To: f.Budget.Envelope})
	}

	// A done RS card has no gate left to cross — --approve/--request-changes
	// here resolve the FD-081 decompose checkpoint instead of the ordinary
	// gate switch below (which assumes an in-flight stage). Never calls
	// Store.Transition, so the card cannot leave StageDone through either
	// verb; a bare Resume with neither flag falls through to drive's
	// terminal-stage check and just re-reports done.
	if f.Kind == domain.KindResearch && f.Stage == domain.StageDone {
		switch {
		case in.Approve:
			return d.approveDecompose(ctx, f)
		case in.RequestChanges != nil:
			return d.decomposeGate(ctx, f, *in.RequestChanges)
		}
		return d.drive(ctx, id)
	}

	switch {
	case in.Approve:
		// an explicit approval crosses the gate now, regardless of the
		// process's gate-approval mode — the caller already decided.
		out, err := d.autoAdvance(ctx, f)
		if err != nil {
			return d.fail(ctx, string(id), err)
		}
		if out.terminal() {
			return out, nil
		}
	case in.Bounce != nil:
		// A verify-fail (or review-fail) escalation is un-parked by rewinding
		// the feature to its work stage — the same rerun edge the TUI's `b`
		// key takes via bounceStage. The optional note becomes an addendum to
		// the reborn implement/fix kickoff, alongside any open diff/spec
		// annotations the engine folds in independently.
		if f.Stage != domain.StageVerify && f.Stage != domain.StageReview {
			return d.fail(ctx, string(id),
				fmt.Errorf("%s is at %s; --bounce only rewinds review/verify to %s",
					id, f.Stage, workflow.WorkStage(f.Kind)))
		}
		back := workflow.WorkStage(f.Kind)
		if _, err := d.store.Transition(ctx, id, back, d.actor); err != nil {
			return d.fail(ctx, string(id), err)
		}
		d.eng.Drop(id) // the stale review/verify session must not restart
		d.bounceNote = *in.Bounce
	case in.RequestChanges != nil:
		d.opening = *in.RequestChanges
	case in.Answer != nil:
		d.opening = *in.Answer
	}
	return d.drive(ctx, id)
}

// Verify is the cheap re-attach for a run whose verify already passed but
// whose card lost its finalize to a crash in the tail (stage stuck at
// verify, verified:false). It re-runs the feature's gummi-side acceptance
// checks on the existing branch and, if they pass, finalizes the verify
// gate (stamping verified_at and reporting the branch ready to land) with
// no fresh agent verify pass. Checks that still fail escalate; anywhere a
// cheap re-attach can't be trusted it fails with a hint to `resume`.
func (d *Driver) Verify(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	if err := d.eng.Restore(ctx); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("restoring sessions: %w", err))
	}
	d.out.emit(resumedEvent{Event: "verify", ID: string(id), Ref: d.opts.Ref, Stage: string(domain.StageVerify)})
	res, err := d.eng.Reverify(ctx, id, d.actor)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	switch res.Status {
	case engine.ReverifyFinalized:
		return d.done(ctx, res.Feature)
	case engine.ReverifyBlocked:
		// the checks passed but the finalize gate is held open by an
		// unresolved thread or diff annotation (Advance's block check runs
		// before the verify→done branch). Keep the feature at verify and
		// report the same blocked outcome autoAdvance maps elsewhere — exit 3,
		// matching status --json (verified:false) rather than "done".
		switch res.Advance.Status {
		case engine.StatusBlockedQuestions:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), OpenSpec: res.Advance.Blockers, Resume: string(id)})
		case engine.StatusBlockedDiff:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), OpenDiff: res.Advance.Blockers, Resume: string(id)})
		case engine.StatusBlockedDependency:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), BlockingDeps: res.Advance.BlockingDeps, Resume: string(id)})
		case engine.StatusBlockedDocument:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), Document: newDocumentSummary(res.Advance.DocumentReport), Resume: string(id)})
		case engine.StatusBlockedOmission:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), Reason: res.Advance.Reason, Resume: string(id)})
		default:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), Resume: string(id)})
		}
		return Outcome{Status: StatusBlocked, ID: string(id)}, nil
	case engine.ReverifyFailed:
		return d.escalation(res.Feature,
			"re-verify FAILED — acceptance checks still failing: "+strings.Join(res.Failed, ", ")), nil
	default: // ReverifyUnavailable
		return d.fail(ctx, string(id), errors.New(res.Reason))
	}
}

// Merge lands a verified feature's branch on main as one squash commit
// carrying the caller-supplied message, then moves the card to Done — the
// headless counterpart of the TUI's ctrl+s at the verify→done gate. The
// caller supplying the message IS the landing review: nothing is drafted or
// auto-generated, and a missing or invalid message fails loudly. It enforces
// the same floor Advance does (a verified branch with no open blockers) plus
// message validation, and performs zero git mutations on any precondition or
// validation failure. On success it emits a `merged` event carrying the
// landed commit's sha and returns StatusDone.
func (d *Driver) Merge(ctx context.Context, id domain.FeatureID, message string) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	wt, err := d.eng.WorktreesFor(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	// the verified-branch precondition, stricter than the TUI's any-stage m
	// key: this command exists to land a verified branch.
	if f.Stage == domain.StageDone {
		return d.fail(ctx, string(id), fmt.Errorf("%s is already done", id))
	}
	if f.Stage != domain.StageVerify || f.VerifiedAt.IsZero() {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s is not at a verified branch (stage %s); run `gummi verify %s` first if it lost its finalize", id, f.Stage, id))
	}
	// a card lands either via its linked PR or locally, never both: refuse
	// before any git mutation, naming the PR and the unlink escape.
	if !f.PullRequest.Empty() {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s is linked to %s#%d (%s); land it via the PR, or run `gummi pr unlink %s` to land it locally instead",
				id, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, id))
	}
	// the same open-thread / open-diff floor Advance applies before the
	// verify→done gate; unresolved ones hold the merge.
	if specOpen, diffOpen, _, err := d.eng.GateBlockers(ctx, id); err != nil {
		return d.fail(ctx, string(id), err)
	} else if specOpen > 0 {
		return d.fail(ctx, string(id), fmt.Errorf("%s has %d unresolved spec threads blocking the merge", id, specOpen))
	} else if diffOpen > 0 {
		return d.fail(ctx, string(id), fmt.Errorf("%s has %d unresolved diff annotations blocking the merge", id, diffOpen))
	}
	// SquashMerge re-enforces these, but checking first fails with a clear
	// reason before any git mutation.
	if dirty, err := wt.MainTrackedDirty(ctx); err != nil {
		return d.fail(ctx, string(id), err)
	} else if dirty {
		return d.fail(ctx, string(id), errors.New("main checkout has uncommitted changes — commit or stash them before merging"))
	}
	if landed, err := wt.Landed(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if landed {
		return d.fail(ctx, string(id), fmt.Errorf("%s already landed on main — run `gummi clean %s`", id, id))
	}
	if ahead, err := wt.BranchAhead(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if !ahead {
		return d.fail(ctx, string(id), fmt.Errorf("%s branch has no commits to land", id))
	}

	// the headless sharp edge: the message must be valid before any git
	// mutation, or the command refuses loudly rather than guessing.
	if err := engine.ValidateCommitMessage(message); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("invalid commit message: %w", err))
	}

	// commit any final uncommitted worktree work (matching the TUI's
	// prepareMerge) so only committed work merges.
	if _, err := wt.CommitAll(ctx, &f, string(id)+": final checkpoint"); err != nil {
		return d.fail(ctx, string(id), err)
	}

	sha, err := wt.SquashMerge(ctx, &f, message)
	if err != nil {
		var ce *worktree.MergeConflictError
		if errors.As(err, &ce) {
			return d.fail(ctx, string(id),
				fmt.Errorf("%s: %s — rebase the branch onto main and retry the merge", id, ce.Error()))
		}
		return d.fail(ctx, string(id), err)
	}
	if _, err := d.store.Transition(ctx, id, domain.StageDone, d.actor); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("landed %s but moving it to done failed: %w", id, err))
	}
	d.out.emit(mergedEvent{Event: "merged", ID: string(id), Branch: f.BranchName(), Commit: sha})
	return Outcome{Status: StatusDone, ID: string(id)}, nil
}

// Clean removes a landed card's worktree and branch — the headless
// counterpart of the TUI's c key. It keeps the card record (it stays as a
// done entry) and never removes anything that has not actually landed or
// that carries tracked-dirty rework. On success it emits a `cleaned` event
// and returns StatusDone.
func (d *Driver) Clean(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	wt, err := d.eng.WorktreesFor(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	landed, err := wt.Landed(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if !landed {
		return d.fail(ctx, string(id), fmt.Errorf("%s has not landed on main — nothing to clean", id))
	}
	if dirty, err := wt.TrackedDirty(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if dirty {
		return d.fail(ctx, string(id), fmt.Errorf("%s worktree has tracked-dirty rework; resolve or commit it before cleaning", id))
	}
	if err := wt.Remove(ctx, &f, true); err != nil {
		return d.fail(ctx, string(id), err)
	}
	if err := wt.DeleteLandedBranch(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	}
	// Durable zz session transcripts (FD-104) live outside the worktree,
	// under the workspace state dir; a cleaned card must leave no
	// conversation state behind. Scoped to this card's featureID prefix so
	// a co-resident card's transcripts are untouched. A refused clean above
	// never reaches here, so nothing partial is left by an early return.
	if matches, err := filepath.Glob(filepath.Join(d.ws.StateDir(), "sessions", string(id)+"-*.jsonl")); err == nil {
		for _, p := range matches {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return d.fail(ctx, string(id), fmt.Errorf("removing session transcript %s: %w", p, err))
			}
		}
	}
	d.out.emit(cleanedEvent{Event: "cleaned", ID: string(id), Branch: f.BranchName()})
	return Outcome{Status: StatusDone, ID: string(id)}, nil
}

// Squash collapses a card's branch to a single commit carrying the
// caller-supplied message, in place — the preflight that keeps checkpoint
// commits off main regardless of how the card's linked PR is eventually
// merged (merge commit, rebase-merge, or GitHub's squash button). It never
// touches main and never contacts a remote: on success the caller still owns
// the follow-up `git push --force-with-lease`. A `done` or already-landed
// card is refused before any git mutation, matching Merge's shape.
func (d *Driver) Squash(ctx context.Context, id domain.FeatureID, message string) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	wt, err := d.eng.WorktreesFor(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	if f.Stage == domain.StageDone {
		return d.fail(ctx, string(id), fmt.Errorf("%s is done, nothing to collapse", id))
	}
	if landed, err := wt.Landed(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if landed {
		return d.fail(ctx, string(id), fmt.Errorf("%s is already landed on main", id))
	}

	if err := engine.ValidateCommitMessage(message); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("invalid commit message: %w", err))
	}

	base, err := worktree.ResolveCollapseBase(ctx, d.store, wt, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	// Captured before Collapse runs, so BeforeSHA is accurate even on the
	// no-op path (Collapse returns "" without moving the branch).
	beforeSHA, err := wt.Head(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	sha, err := wt.Collapse(ctx, &f, message, base)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if sha == "" {
		return Outcome{Status: StatusDone, ID: string(id)}, nil
	}
	d.out.emit(squashedEvent{
		Event: "squashed", ID: string(id), Branch: f.BranchName(),
		BeforeSHA: beforeSHA, AfterSHA: sha, BaseSHA: base,
		MessageSubject: commitSubject(message),
	})
	return Outcome{Status: StatusDone, ID: string(id)}, nil
}

// Commit commits exactly the target card's own uncommitted worktree changes
// onto the card's own branch, using the caller-supplied message — the
// headless counterpart of the "final checkpoint" commit Merge and the TUI's
// m key already make internally, now addressable on its own with a
// caller-chosen message instead of an auto-generated one. It has no PR or
// stage precondition of its own: any card in any stage can commit its own
// stray changes. It composes with Squash to replace the raw-git "commit the
// stray changes, then collapse" workaround a PR-linked card with a dirty
// worktree otherwise needs. A clean worktree is a no-op, reported as
// StatusDone with no `committed` event, not an error.
func (d *Driver) Commit(ctx context.Context, id domain.FeatureID, message string) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	wt, err := d.eng.WorktreesFor(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	if err := engine.ValidateCommitMessage(message); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("invalid commit message: %w", err))
	}

	committed, err := wt.CommitAll(ctx, &f, message)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if !committed {
		return Outcome{Status: StatusDone, ID: string(id)}, nil
	}

	sha, err := wt.Head(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	d.out.emit(committedEvent{Event: "committed", ID: string(id), Branch: f.BranchName(), Commit: sha})
	return Outcome{Status: StatusDone, ID: string(id)}, nil
}

// commitSubject returns msg's first line — the subject a squashed/merged
// event reports, without the body.
func commitSubject(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i]
	}
	return msg
}

// OpenReviewThreads reports the number of open review threads on id's
// linked outbound PR (the diff-annotation half of GateBlockers) alongside
// the PR's URL, for the CLI's `--force` gate on `gummi squash`: collapsing a
// branch a reviewer is actively commenting on force-pushes their comments
// out from under them unless the operator explicitly acknowledges it.
func (d *Driver) OpenReviewThreads(ctx context.Context, id domain.FeatureID) (count int, prURL string, err error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return 0, "", err
	}
	_, diffOpen, _, err := d.eng.GateBlockers(ctx, id)
	if err != nil {
		return 0, "", err
	}
	return diffOpen, f.PullRequest.URL, nil
}

// drive is the checkpoint loop: it advances the feature stage by stage
// until it reaches a terminal Outcome (a decision the caller must make,
// or a verified branch). Autonomous stretches carry no caller decisions
// under --gate-approval=auto, so one call streams the whole tail and
// returns only at done or an escalation.
func (d *Driver) drive(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	for {
		if err := ctx.Err(); err != nil {
			return d.fail(ctx, string(id), err)
		}
		f, err := d.store.GetFeature(ctx, id)
		if err != nil {
			return d.fail(ctx, string(id), err)
		}
		if f.Stage == domain.StageDone || workflow.Terminal(f.Kind, f.Stage) {
			return d.done(ctx, f)
		}

		var out Outcome
		switch {
		case f.Stage == domain.StageTodo:
			// todo is a pure kickoff gate (no agent action): advance into the
			// flow's first real stage. This is "start", not a design decision,
			// so it always auto-crosses regardless of --gate-approval.
			out, err = d.autoAdvance(ctx, f)
		case workflow.Interactive(f.Stage):
			out, err = d.driveInteractive(ctx, f)
		default:
			out, err = d.driveAutonomous(ctx, f)
		}
		if err != nil {
			return d.fail(ctx, string(id), err)
		}
		if out.terminal() {
			return out, nil
		}
		// a non-terminal outcome means the stage advanced in-floor; loop.
	}
}

// driveInteractive drives a gummi-native chat stage (brainstorm/spec):
// the agent leads, ask_user questions become the `question` checkpoint
// (or, under --autonomous, auto-take the recommended option), and a
// finished turn with no open question is the design gate.
func (d *Driver) driveInteractive(ctx context.Context, f domain.Feature) (Outcome, error) {
	d.enterStage(f.Stage)
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage)})

	// A bare resume that lands on an interactive stage already driven to its
	// gate has no turn to send: the restored/live session carries the
	// finished interview, so Attach reattaches silently (interactive stages
	// advance on human turns, and a completed one has nothing to send).
	// Re-entering would park a turn-less session that can only time out —
	// the deadlock the operator report caught. Present the gate instead: the
	// identical checkpoint a fresh run reaches here. crossGate auto-advances
	// under --gate-approval=auto, checkpoints under caller, and surfaces any
	// open-question blockers — so an incomplete interview reports its
	// blockers rather than hanging, and neither path waits on a turn that
	// was never sent. (A resume carrying an answer / change note does have a
	// turn to send, so it falls through to Attach + Send below.)
	if d.opening == "" {
		if snap := d.snapshot(f.ID); snap.Feature.Stage == f.Stage && snap.Err != nil {
			// The backend died mid-turn on this interactive stage: failRun
			// leaves an Interactive session at state='interactive' with the
			// kickoff message already on the transcript, so it reads
			// identically to a completed interview to reattachSilent's
			// transcript-emptiness proxy. Drop the dead session so Attach
			// below opens a fresh one (empty transcript -> fresh kickoff
			// send) instead of crossing the gate on an interview that never
			// ran.
			d.eng.Drop(f.ID)
		} else if d.reattachSilent(f) {
			return d.crossGate(ctx, f)
		}
	}

	if _, err := d.eng.Attach(ctx, f); err != nil {
		return Outcome{}, err
	}
	// past the guard a turn is always dispatched — a fresh Attach kicks off
	// the stage, and a resume seeds the answer / change note below.
	d.sentTurn = true
	if d.opening != "" {
		msg := d.opening
		d.opening = ""
		if err := d.eng.Send(ctx, f.ID, msg); err != nil {
			return Outcome{}, err
		}
	}

	for {
		end, err := d.awaitStage(ctx, f.ID)
		if err != nil {
			return Outcome{}, err
		}
		switch end.kind {
		case endExhausted:
			return d.exhausted(ctx, f, end.committed), nil
		case endTimeout:
			return d.timeout(f), nil
		case endError:
			return Outcome{}, firstErr(end.err, errors.New("agent session failed"))
		case endTripwire:
			return d.tripwire(f, end.dirtyPaths), nil
		case endQuestion:
			ask := d.pendingAsk(f.ID)
			if ask == nil {
				// no question actually pending — treat as a finished turn.
				return d.crossGate(ctx, f)
			}
			if d.opts.Autonomous {
				rec := recommendedOption(ask)
				if err := d.eng.Answer(ctx, f.ID, rec); err != nil {
					return Outcome{}, err
				}
				d.out.activity(string(f.ID), string(f.Stage), "auto-answered: "+rec)
				continue // the turn resumes with the answer; keep reading
			}
			d.out.emit(questionEvent{
				Event: "question", ID: string(f.ID), Q: ask.Question,
				Options: askLabels(ask), Recommended: recommendedOption(ask),
				FreeForm: ask.FreeForm, Resume: string(f.ID),
				Next: resumeCmd(string(f.ID), "--answer", `"<answer>"`),
			})
			return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
		case endIdle:
			// a finished turn with no open question: the design gate.
			if d.pendingAsk(f.ID) != nil {
				continue
			}
			return d.crossGate(ctx, f)
		}
	}
}

// driveAutonomous drives an autonomous stage (plan/implement/review/
// verify/fix) to completion, then applies the verdict rules that either
// step the in-floor loop forward, escalate, or reach a verified branch.
func (d *Driver) driveAutonomous(ctx context.Context, f domain.Feature) (Outcome, error) {
	d.enterStage(f.Stage)
	if f.Stage == domain.StageReview {
		d.reviewsRun++
	}
	round := 0
	switch f.Stage {
	case domain.StageReview:
		// seed the in-memory counter from the persisted value so a resume
		// into the loop honors (and reports) the rounds already burned this
		// cycle. A failed read aborts entry: the count is the budget.
		if err := d.seedRounds(ctx, f, domain.RoundKindReview); err != nil {
			return Outcome{}, err
		}
		round = d.reviewsRun
	case domain.StageImplement, domain.StageFix, domain.StageInvestigate:
		// the work leg of the review loop can be the resume landing point,
		// so seed it too — the review-round budget must survive the fresh
		// process, not just a review-stage entry. Investigate is research's
		// work leg (workflow.WorkStage(KindResearch)), exactly as Implement
		// and Fix are for features and bugs.
		if err := d.seedRounds(ctx, f, domain.RoundKindReview); err != nil {
			return Outcome{}, err
		}
		round = d.round(f.ID, domain.RoundKindReview)
	case domain.StagePlan:
		// seed the in-memory counter from the persisted value so a resume
		// honors (and reports) the rounds already burned this cycle. A
		// failed read aborts plan-stage entry: the count is the budget.
		if err := d.seedRounds(ctx, f, domain.RoundKindPlan); err != nil {
			return Outcome{}, err
		}
		round = d.round(f.ID, domain.RoundKindPlan)
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Round: round})

	// A resume that lands on the Plan stage must not re-invoke the plan
	// writer: the restored/live session tells us where the loop was when it
	// stopped. A finished writer (revised plan already on disk) resumes the
	// critique; a finished critique routes to the judge (replan on changes /
	// approve on pass); a paused critique (its session died mid-turn, e.g. a
	// recoverable backend failure) re-dispatches the critique; an in-flight
	// session keeps awaiting. Only a fresh plan entry — no restored Plan-stage
	// session, or a restored paused writer — starts/restarts the writer below.
	// The snapshot's feature stage is the guard, so a leftover done session
	// from a prior stage is never mistaken for a plan resume.
	if f.Stage == domain.StagePlan {
		if snap := d.snapshot(f.ID); snap.Feature.Stage == domain.StagePlan {
			if snap.State == engine.StateDone && !snap.Critique {
				// the revised plan is on disk: critique it, using the
				// re-critique kickoff when a prior round was burned.
				kickoff := ""
				result := "critiquing"
				if d.round(f.ID, domain.RoundKindPlan) > 0 {
					kickoff = verdict.ReCritiqueNote
					result = "re-critiquing"
				}
				if err := d.eng.RunCritique(f, kickoff); err != nil {
					return Outcome{}, err
				}
				d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: result})
				return d.awaitPlanCritique(ctx, f)
			}
			if snap.State == engine.StateDone && snap.Critique {
				// awaiting replan/approval: the judge decides (replan writer
				// on changes, gate on pass). Never re-run the critique —
				// unless its verdict is unrecoverable: a session judged in
				// a prior process whose structured verdict never persisted
				// re-derives Unclear from an empty field, and re-judging
				// that dead snapshot would escalate identically forever.
				// Run a fresh critique instead so the loop recovers.
				if verdict.SessionVerdict(snap) == verdict.Unclear {
					if err := d.eng.RunCritique(f, verdict.ReCritiqueNote); err != nil {
						return Outcome{}, err
					}
					d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "re-critiquing"})
					return d.awaitPlanCritique(ctx, f)
				}
				return d.judgePlanCritique(ctx, f, snap)
			}
			if snap.State == engine.StatePaused && snap.Critique {
				// the critique session died mid-turn and was restored
				// paused: re-dispatch it instead of awaiting a pass that
				// already ended (the bug this guards against: awaiting
				// forever burns the whole --stage-timeout with nothing
				// dispatched). Mirrors the TUI's StatePaused+Critique
				// branch (internal/ui/shell.go:1279-1286).
				if err := d.eng.RunCritique(f, ""); err != nil {
					return Outcome{}, err
				}
				d.sentTurn = true
				d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "resuming plan critique"})
				return d.awaitPlanCritique(ctx, f)
			}
			if snap.State != engine.StatePaused {
				// still in flight (running/queued): keep awaiting the
				// running pass, spawn nothing.
				return d.awaitPlanCritique(ctx, f)
			}
			// a restored paused writer (!Critique): the writer died
			// mid-turn before producing a plan to critique. Fall through
			// to the fresh-writer dispatch below — mirrors the TUI's
			// paused/non-critique fallthrough to engine.Run
			// (internal/ui/shell.go:1279-1296).
		}
	}

	// a --bounce resume stashed a kickoff note for the first work-stage run
	// that follows the rewind; consume it on that exact dispatch so it
	// reaches the reborn implement/fix as an addendum to the kickoff (the
	// same path Engine.RunWith takes for the diff surface's request-changes).
	var err error
	if d.bounceNote != "" && (f.Stage == domain.StageImplement || f.Stage == domain.StageFix) {
		note := d.bounceNote
		d.bounceNote = ""
		err = d.eng.RunWith(f, note)
	} else {
		err = d.eng.Run(f)
	}
	if err != nil {
		return Outcome{}, err
	}
	d.sentTurn = true
	end, err := d.awaitStage(ctx, f.ID)
	if err != nil {
		return Outcome{}, err
	}
	switch end.kind {
	case endExhausted:
		return d.exhausted(ctx, f, end.committed), nil
	case endTimeout:
		return d.timeout(f), nil
	case endError:
		return Outcome{}, firstErr(end.err, errors.New("agent session failed"))
	case endTripwire:
		return d.tripwire(f, end.dirtyPaths), nil
	case endQuestion:
		// autonomous stages register no ask_user tool, so this is anomalous;
		// don't guess an answer — escalate.
		return d.escalation(f, "autonomous stage raised a question it cannot answer"), nil
	default: // endIdle: the stage finished its turn
		return d.applyVerdict(ctx, f)
	}
}

// seedRounds hydrates the in-memory round counter for kind from the store
// on loop entry, so a resume honors the rounds already burned instead of
// a fresh budget. A failed read returns the error and leaves the
// fast-path map untouched; the caller aborts rather than proceeding on a
// guessed-zero count.
func (d *Driver) seedRounds(ctx context.Context, f domain.Feature, kind domain.RoundKind) error {
	persisted, err := rounds.Load(ctx, d.roundStore, f.ID, kind)
	if err != nil {
		return err
	}
	d.setRound(f.ID, kind, persisted)
	return nil
}

// applyVerdict routes a finished autonomous stage per the loop rules
// (mirrors internal/ui/reviewloop.go), returning a terminal Outcome or a
// non-terminal one (the stage advanced in-floor; drive loops).
func (d *Driver) applyVerdict(ctx context.Context, f domain.Feature) (Outcome, error) {
	snap := d.snapshot(f.ID)
	switch f.Stage {
	case domain.StageReview:
		v := verdict.SessionVerdict(snap)
		d.emitResult(f, v)
		switch v {
		case verdict.Pass:
			// clear the persisted count so the next review loop starts fresh.
			if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindReview); err != nil {
				return Outcome{}, err
			}
			d.setRound(f.ID, domain.RoundKindReview, 0)
			return d.stepTo(ctx, f.ID, domain.StageVerify)
		case verdict.Changes:
			max := verdict.MaxRounds(domain.RoundKindReview)
			if d.round(f.ID, domain.RoundKindReview) >= max {
				// escalation hands the loop to a human; clear the cap so the
				// next review cycle starts a fresh budget.
				if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindReview); err != nil {
					return Outcome{}, err
				}
				d.setRound(f.ID, domain.RoundKindReview, 0)
				return d.bounceEscalation(f, fmt.Sprintf("review still requesting changes after %d rounds", max)), nil
			}
			// persist the burned round before it lands in the fast path, so a
			// mid-loop resume observes it.
			if err := rounds.Bump(ctx, d.roundStore, f.ID, domain.RoundKindReview); err != nil {
				return Outcome{}, err
			}
			d.setRound(f.ID, domain.RoundKindReview, d.round(f.ID, domain.RoundKindReview)+1)
			return d.stepTo(ctx, f.ID, workflow.WorkStage(f.Kind))
		default:
			if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindReview); err != nil {
				return Outcome{}, err
			}
			d.setRound(f.ID, domain.RoundKindReview, 0)
			return d.escalation(f, "review finished with no clear verdict"), nil
		}

	case domain.StageVerify:
		v := verdict.SessionVerdict(snap)
		d.emitResult(f, v)
		switch v {
		case verdict.Pass:
			// stop at the verified branch: Advance reports NeedsMerge (branch
			// ahead) or transitions to Done (nothing to land). Never merges.
			return d.crossGate(ctx, f)
		case verdict.Blocked:
			return d.escalation(f, "verify BLOCKED — the environment cannot run the verification plan; see the artifact"), nil
		case verdict.Fail, verdict.Changes:
			return d.bounceEscalation(f, "verify FAILED — read the evidence in the artifact"), nil
		default:
			return d.escalation(f, "verify finished with no clear verdict"), nil
		}

	case domain.StageImplement, domain.StageFix:
		// the implementation floor always continues into review — review is
		// mandatory and never skipped; the driver never waits for a human here.
		return d.stepTo(ctx, f.ID, domain.StageReview)

	case domain.StageInvestigate:
		// research's work leg: the forward edge to shape (never straight to
		// review — research has no direct investigate→review edge). This is
		// what makes the review loop's changes bounce (review→investigate)
		// re-enter the loop instead of parking with no case to drive it.
		return d.stepTo(ctx, f.ID, domain.StageShape)

	case domain.StagePlan:
		if !snap.Critique {
			// the plan was just written: critique it before the approval gate.
			if err := d.eng.RunCritique(f, ""); err != nil {
				return Outcome{}, err
			}
			d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "critiquing"})
			// wait out the critique pass in-place (same stage, fresh session).
			return d.awaitPlanCritique(ctx, f)
		}
		return d.judgePlanCritique(ctx, f, snap)

	default:
		return d.escalation(f, "unexpected autonomous stage "+string(f.Stage)), nil
	}
}

// awaitPlanCritique waits for the critique session (RunCritique borrows
// the Plan stage without advancing it) and re-judges — the plan loop is
// invisible to the state machine, so this stays inside the Plan stage.
func (d *Driver) awaitPlanCritique(ctx context.Context, f domain.Feature) (Outcome, error) {
	end, err := d.awaitStage(ctx, f.ID)
	if err != nil {
		return Outcome{}, err
	}
	switch end.kind {
	case endExhausted:
		return d.exhausted(ctx, f, end.committed), nil
	case endTimeout:
		return d.timeout(f), nil
	case endError:
		return Outcome{}, firstErr(end.err, errors.New("critique session failed"))
	case endTripwire:
		return d.tripwire(f, end.dirtyPaths), nil
	default:
		return d.judgePlanCritique(ctx, f, d.snapshot(f.ID))
	}
}

// judgePlanCritique applies the critique verdict: pass crosses the plan
// approval gate, changes replan under the cap, else escalate.
func (d *Driver) judgePlanCritique(ctx context.Context, f domain.Feature, snap engine.Snapshot) (Outcome, error) {
	v := verdict.SessionVerdict(snap)
	d.emitResult(f, v)
	switch v {
	case verdict.Pass:
		if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindPlan); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, domain.RoundKindPlan, 0)
		return d.crossGate(ctx, f)
	case verdict.Changes:
		max := verdict.MaxRounds(domain.RoundKindPlan)
		if d.round(f.ID, domain.RoundKindPlan) >= max {
			if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindPlan); err != nil {
				return Outcome{}, err
			}
			d.setRound(f.ID, domain.RoundKindPlan, 0)
			return d.escalation(f, fmt.Sprintf("plan critique still requesting changes after %d rounds", max)), nil
		}
		if err := rounds.Bump(ctx, d.roundStore, f.ID, domain.RoundKindPlan); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, domain.RoundKindPlan, d.round(f.ID, domain.RoundKindPlan)+1)
		if err := d.eng.RunWith(f, verdict.ReplanNote); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "replanning", Round: d.round(f.ID, domain.RoundKindPlan)})
		return d.awaitReplan(ctx, f)
	default:
		if err := rounds.Reset(ctx, d.roundStore, f.ID, domain.RoundKindPlan); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, domain.RoundKindPlan, 0)
		return d.escalation(f, "plan critique finished with no clear verdict"), nil
	}
}

// awaitReplan waits for a replan pass to finish, then critiques again.
func (d *Driver) awaitReplan(ctx context.Context, f domain.Feature) (Outcome, error) {
	end, err := d.awaitStage(ctx, f.ID)
	if err != nil {
		return Outcome{}, err
	}
	switch end.kind {
	case endExhausted:
		return d.exhausted(ctx, f, end.committed), nil
	case endTimeout:
		return d.timeout(f), nil
	case endError:
		return Outcome{}, firstErr(end.err, errors.New("replan session failed"))
	case endTripwire:
		return d.tripwire(f, end.dirtyPaths), nil
	default:
		// re-critique the revised plan (mirrors reCritiqueNote intent).
		if err := d.eng.RunCritique(f, verdict.ReCritiqueNote); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "re-critiquing"})
		return d.awaitPlanCritique(ctx, f)
	}
}

// stepTo records an in-floor transition (actor "auto") and returns a
// non-terminal Outcome so drive re-dispatches and runs the new stage. The
// stale session is left for engine.Run to replace on the next dispatch.
func (d *Driver) stepTo(ctx context.Context, id domain.FeatureID, to domain.Stage) (Outcome, error) {
	if _, err := d.store.Transition(ctx, id, to, "auto"); err != nil {
		return Outcome{}, err
	}
	return Outcome{}, nil // non-terminal: drive loops into `to`
}

// crossGate resolves a design/approval gate. Under --gate-approval=auto it
// advances now (honoring blockers); under caller it checkpoints. It is
// also the verify→done stop-at-verified path (Advance never merges).
func (d *Driver) crossGate(ctx context.Context, f domain.Feature) (Outcome, error) {
	// --until: a deliberate early stop at a design boundary. crossGate is
	// exactly the gate that leaves the current design stage (spec via
	// driveInteractive, plan via judgePlanCritique), so intercepting here —
	// before any advance/blocker/caller-gate logic — stops the run cleanly
	// with the feature parked at the Until stage, resumable, exit 0 (B3). A
	// pending in-stage question still checkpoints first: driveInteractive
	// only reaches crossGate once the stage has no open ask.
	if d.opts.Until != "" && f.Stage == d.opts.Until {
		return d.stopped(f), nil
	}
	if d.opts.GateApproval == GateCaller && f.Stage != domain.StageVerify {
		// a caller gate on a design stage: report any blockers, else
		// checkpoint for --approve/--request-changes. Verify is never a
		// caller gate — it is the floor's stop-at-verified, always auto.
		specOpen, diffOpen, deps, err := d.eng.GateBlockers(ctx, f.ID)
		if err != nil {
			return Outcome{}, err
		}
		if specOpen > 0 {
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), OpenSpec: specOpen, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		if diffOpen > 0 {
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), OpenDiff: diffOpen, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		if len(deps) > 0 {
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), BlockingDeps: deps, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		next := forwardEdge(f)
		d.out.emit(gatePendingEvent{
			Event: "gate", ID: string(f.ID), From: string(f.Stage), To: string(next), Resume: string(f.ID),
			Next: resumeCmd(string(f.ID), "--approve"),
		})
		return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
	}
	return d.autoAdvance(ctx, f)
}

// autoAdvance crosses the current gate via the shared engine floor and
// maps the result to NDJSON + Outcome. StatusNeedsMerge (verify→done) is
// the stop-at-verified point; a non-terminal Outcome means the gate
// advanced and drive should loop.
func (d *Driver) autoAdvance(ctx context.Context, f domain.Feature) (Outcome, error) {
	res, err := d.eng.Advance(ctx, f.ID, d.actor)
	if err != nil {
		return Outcome{}, err
	}
	switch res.Status {
	case engine.StatusBlockedQuestions:
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), OpenSpec: res.Blockers, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDiff:
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), OpenDiff: res.Blockers, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDependency:
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), BlockingDeps: res.BlockingDeps, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDocument:
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), Document: newDocumentSummary(res.DocumentReport), Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedOmission:
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), Reason: res.Reason, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusNeedsMerge:
		return d.done(ctx, res.Feature)
	case engine.StatusNoop:
		return d.done(ctx, res.Feature)
	case engine.StatusAdvanced:
		if res.EnteredWorktree {
			d.discoverAndBaselineChecks(ctx, res.Feature)
		}
		if res.To == domain.StageDone {
			if res.Feature.Kind == domain.KindResearch {
				return d.decomposeGate(ctx, res.Feature, "")
			}
			return d.done(ctx, res.Feature)
		}
		// the todo→first-stage kickoff is "start", not an approval, so it
		// emits no gate milestone; every real gate does.
		if res.From != domain.StageTodo {
			decision := "auto-approved"
			if d.actor == "caller" {
				decision = "caller-approved"
			}
			d.out.emit(gateEvent{Event: "gate", ID: string(f.ID), From: string(res.From), To: string(res.To), Decision: decision})
		}
		return Outcome{}, nil // non-terminal: drive loops into res.To
	default:
		return d.escalation(f, "unexpected gate status"), nil
	}
}

// discoverStageTimeout bounds discoverAndBaselineChecks when the driver
// has no --stage-timeout of its own, so a scribe session that never
// reaches one of DiscoverChecks's terminal events (idle, error, budget
// exhaustion) can't hang the drive forever.
const discoverStageTimeout = 2 * time.Minute

// discoverAndBaselineChecks mirrors the TUI's discover→baseline chain
// (msgs.go discoverChecks/baselineChecks) for the headless path: it
// surveys the fresh worktree for the repo's build/test/lint commands and,
// once they're recorded as a gummi-checks block, runs them once to record
// a baseline. Best-effort like the TUI's version — a discovery or baseline
// failure leaves the block absent/unbaselined and the drive continues;
// Verify's own fallback still applies. Bounded by the driver's
// --stage-timeout (or discoverStageTimeout when unset) so a stalling
// scribe session returns promptly instead of hanging the gate crossing.
func (d *Driver) discoverAndBaselineChecks(ctx context.Context, f domain.Feature) {
	timeout := d.opts.StageTimeout
	if timeout <= 0 {
		timeout = discoverStageTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := d.eng.DiscoverChecks(ctx, f); err != nil {
		return
	}
	_, _ = d.eng.BaselineChecks(ctx, f)
}

// --- terminal outcomes -------------------------------------------------

func (d *Driver) done(ctx context.Context, f domain.Feature) (Outcome, error) {
	// reload for the freshest spend/stage.
	if got, err := d.store.GetFeature(ctx, f.ID); err == nil {
		f = got
	}
	d.out.emit(doneEvent{
		Event: "done", ID: string(f.ID), Branch: f.BranchName(),
		Spec: f.ArtifactPath(), Spent: f.Spend.Credits, ReviewRounds: d.reviewsRun,
		Message:     f.PullRequest.NextStepsHint(true),
		PullRequest: f.PullRequest.StatusPayload(),
	})
	return Outcome{Status: StatusDone, ID: string(f.ID)}, nil
}

// decomposeGate runs the FD-081 decompose side-effect off an RS card's
// verify→done crossing (auto-trigger, note "") or a manual
// --request-changes re-run (note carries the operator's feedback). No
// decompose exit — success, question, exhaustion, or a hard error — moves
// the card off done: a failure is swallowed into a decompose_failed
// escalation, and a successful pass only ever checkpoints as a `question`
// for a later --approve, never auto-mints.
func (d *Driver) decomposeGate(ctx context.Context, f domain.Feature, note string) (Outcome, error) {
	res, err := d.eng.DecomposeForCard(ctx, f.ID, note)
	if err != nil {
		d.out.emit(escalationEvent{
			Event: "decompose_failed", ID: string(f.ID), Stage: string(f.Stage), Reason: err.Error(),
			Resume: string(f.ID), Next: resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
		})
		return Outcome{Status: StatusEscalation, ID: string(f.ID)}, nil
	}
	if len(res.Proposals) == 0 {
		// nothing unsettled — a zero-slice RS, or every row already carries
		// an id (including a doc hand-edited settled since the last pass).
		return d.done(ctx, f)
	}
	if err := d.eng.SavePendingDecompose(f.ID, res); err != nil {
		d.out.emit(escalationEvent{
			Event: "decompose_failed", ID: string(f.ID), Stage: string(f.Stage), Reason: err.Error(),
			Resume: string(f.ID), Next: resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
		})
		return Outcome{Status: StatusEscalation, ID: string(f.ID)}, nil
	}
	d.out.emit(decomposeQuestionEvent{
		Event: "question", ID: string(f.ID),
		Proposals: wireDecomposeProposals(res), Coverage: wireDecomposeCoverage(res),
		Resume: string(f.ID), Next: resumeCmd(string(f.ID), "--approve"),
	})
	return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
}

// approveDecompose mints a done RS card's pending decompose proposals
// (`resume <RS-id> --approve`). A clean mint clears the pending file and
// reports done with the minted ids; a partial-mint failure still clears
// the pending file (the doc's `## Slices` rows are authoritative — a
// re-run's unsettledSliceRows naturally excludes the already-settled
// prefix) and escalates with the ids that did land — per the never-un-
// approve invariant, the RS card stays at done either way.
func (d *Driver) approveDecompose(ctx context.Context, f domain.Feature) (Outcome, error) {
	res, ok, err := d.eng.LoadPendingDecompose(f.ID)
	if err != nil {
		return d.fail(ctx, string(f.ID), err)
	}
	if !ok {
		return d.fail(ctx, string(f.ID), fmt.Errorf("%s: no pending decomposition to approve", f.ID))
	}
	minted, mintErr := d.eng.MintProposals(ctx, f.ID, res)
	_ = d.eng.ClearPendingDecompose(f.ID)
	ids := make([]string, len(minted))
	for i, m := range minted {
		ids[i] = string(m.ID)
	}
	if mintErr != nil {
		d.out.emit(escalationEvent{
			Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: mintErr.Error(),
			MintedIDs: ids, Resume: string(f.ID), Next: resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
		})
		return Outcome{Status: StatusEscalation, ID: string(f.ID)}, nil
	}
	d.out.emit(decomposeMintedEvent{Event: "decompose_minted", ID: string(f.ID), FeatureIDs: ids})
	return d.done(ctx, f)
}

func (d *Driver) exhausted(ctx context.Context, f domain.Feature, committed bool) Outcome {
	if got, err := d.store.GetFeature(ctx, f.ID); err == nil {
		f = got
	}
	// suggest a concrete raise so `next` is runnable as-is; it is a floor the
	// driver never lowers, and the caller can edit it. Doubling the envelope
	// alone can land below already-recorded spend when spend overshoots the
	// envelope before an agent's next usage report lands, guaranteeing a
	// second exhaustion with zero work done — so the suggestion also has to
	// clear recorded spend plus headroom.
	suggested := f.Budget.Envelope * 2
	if bySpend := int(math.Ceil(f.Spend.Credits * 1.2)); bySpend > suggested {
		suggested = bySpend
	}
	d.out.emit(exhaustedEvent{
		Event: "exhausted", ID: string(f.ID), Stage: string(f.Stage),
		Spent: f.Spend.Credits, Envelope: f.Budget.Envelope, Committed: committed, Resume: string(f.ID),
		Next:          resumeCmd(string(f.ID), "--envelope", fmt.Sprintf("%d", suggested)),
		Preconditions: d.resumePreconditions(f.ID),
	})
	return Outcome{Status: StatusExhausted, ID: string(f.ID)}
}

// timeoutHintStalled is the cause note when the stage went silent AFTER
// gummi dispatched the agent a turn. Two things realistically go wrong here:
// the backend agent stalled/lost its connection, OR the timeout was simply
// too short for the backend's turn on this spec (a frontier reviewer on a
// dense plan can legitimately run past 10 minutes producing one critique).
// It also points at the pid-file probe so a caller who is orchestrating
// gummi from a wrapper the harness may have killed doesn't confuse an
// orphan-that-is-still-working with a hang.
const timeoutHintStalled = "the stage went silent for the whole --stage-timeout window after its turn was sent. " +
	"Before retrying, verify gummi isn't still running (preconditions.check_running) — a wrapper the harness killed " +
	"can leave a live gummi behind, and a bare retry there just fights the lock. If nothing is running the backend " +
	"either stalled/lost auth, or the turn genuinely needs longer than stage_timeout_used — resume with a larger " +
	"--stage-timeout (e.g. double the current value) before blaming the backend."

// timeoutHintParked is the cause note when gummi never sent the agent a turn
// this stage: the stage is parked at a gate with nothing to drive, so the
// fault is caller-side, not the backend. It points at the decision that
// advances the gate instead of sending the operator to debug the backend —
// the misdiagnosis the operator report flagged.
const timeoutHintParked = "the stage is parked at a gate and no turn was sent this stage — advance it with " +
	"--approve (or --request-changes / --answer), not by a bare resume, which has nothing to drive here"

func (d *Driver) timeout(f domain.Feature) Outcome {
	hint := timeoutHintStalled
	if !d.sentTurn {
		hint = timeoutHintParked
	}
	used := ""
	if d.opts.StageTimeout > 0 {
		used = d.opts.StageTimeout.String()
	}
	d.out.emit(timeoutEvent{
		Event: "timeout", ID: string(f.ID), Stage: string(f.Stage), Hint: hint,
		StageTimeoutUsed: used,
		Resume:           string(f.ID),
		Next:             resumeCmd(string(f.ID)),
		Preconditions:    d.resumePreconditions(f.ID),
	})
	return Outcome{Status: StatusTimeout, ID: string(f.ID)}
}

// resumePreconditions builds the pid-probe caller-side check attached to
// terminal events whose `next` command starts a new gummi run: exhaustion,
// timeout. The probe warns when this card's recorded pid is still alive —
// an orphan gummi from a killed wrapper — so an orchestrating agent knows
// to wait instead of hitting ErrLocked on immediate retry. The path is
// scoped to id (BG-006), so it never reads a different card's pid, and is
// derived from the live workspace, so relocating the state dir keeps the
// probe correct.
func (d *Driver) resumePreconditions(id domain.FeatureID) *resumePreconditions {
	pid := d.ws.PIDFile(id)
	if pid == "" {
		return nil
	}
	return &resumePreconditions{
		CheckRunning: "pid=$(cat " + pid + " 2>/dev/null); " +
			"[ -n \"$pid\" ] && kill -0 \"$pid\" 2>/dev/null && " +
			"echo \"gummi still running as pid $pid — wait before resuming\"",
	}
}

// backendHint returns a short remediation note when a failure's message
// looks like a backend disconnect, mid-stream stall, or auth problem — the
// conditions an operator most often misreads as a gummi bug. Empty for
// anything else, so the hint field only appears when it helps.
func backendHint(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "auth") || strings.Contains(l, "401") || strings.Contains(l, "403") ||
		strings.Contains(l, "unauthorized") || strings.Contains(l, "forbidden") || strings.Contains(l, "credential"):
		return "the backend agent looks unauthenticated — re-auth it (run the agent's login) and resume"
	case strings.Contains(l, "stall") || strings.Contains(l, "stream") || strings.Contains(l, "mid-session") ||
		strings.Contains(l, "aborted") || strings.Contains(l, "disconnect") || strings.Contains(l, "eof"):
		return "the backend agent's stream died mid-turn — usually a transient backend/network drop or lost auth; check the agent and resume"
	}
	return ""
}

// resumeCmd formats the copy-pasteable command a caller runs next to advance
// a parked feature — the `next` field on terminal events. It keeps the stream
// self-documenting so a driver never has to recall which resume verb a given
// stop takes (the exact confusion that lets a bare resume land on a gate).
// args are appended after `gummi resume <id>`; a free-form value a caller must
// supply is passed as a <placeholder>.
func resumeCmd(id string, args ...string) string {
	cmd := "gummi resume " + id
	if len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}
	return cmd
}

// stopped is the --until early-stop terminal: a clean, deliberate halt at a
// design boundary (the feature stays parked at f.Stage, resumable). It exits
// 0 — not an escalation — so a caller distinguishes it from `done` by the
// event name, not the exit code.
func (d *Driver) stopped(f domain.Feature) Outcome {
	d.out.emit(stoppedEvent{
		Event: "stopped", ID: string(f.ID), Stage: string(f.Stage), Resume: string(f.ID),
		Next: resumeCmd(string(f.ID), "--approve"),
	})
	return Outcome{Status: StatusStopped, ID: string(f.ID)}
}

func (d *Driver) escalation(f domain.Feature, reason string) Outcome {
	d.out.emit(escalationEvent{
		Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID),
		Next: resumeCmd(string(f.ID)),
	})
	return Outcome{Status: StatusEscalation, ID: string(f.ID)}
}

// tripwire reports a main-checkout tripwire hit as a typed escalation: the
// agent dirtied paths on main that were clean before its turn, so the run
// is hard-stopped (the engine leaves the dirt in place for the operator to
// resolve) and the stage is re-run from a clean checkout. It maps to the
// escalation exit — never a timeout — and names the dirty paths so the
// stream carries the actionable cause instead of blaming the backend.
func (d *Driver) tripwire(f domain.Feature, paths []string) Outcome {
	reason := "the agent dirtied the main checkout; resolve these new paths, then re-run: " + strings.Join(paths, ", ")
	return d.escalation(f, reason)
}

// bounceEscalation is the escalation flavor used when the human's follow-up
// is to rewind review/verify back to implement/fix — a review cap-hit or a
// verify-fail. The `next` field names `--bounce` so a caller driving the
// stream never has to recall which verb un-parks this stop; `--note` is
// carried as a placeholder for the caller's own change note.
func (d *Driver) bounceEscalation(f domain.Feature, reason string) Outcome {
	d.out.emit(escalationEvent{
		Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID),
		Next: resumeCmd(string(f.ID), "--bounce", "--note", `"<why>"`),
	})
	return Outcome{Status: StatusEscalation, ID: string(f.ID)}
}

// fail emits the error line and returns the StatusError outcome plus the
// error, so the CLI can also log it to stderr. It computes resumability
// best-effort: a failure that left a durable, non-terminal feature card
// behind (id set, card present, stage not terminal) is one `resume` from
// possibly finishing — distinct from a pre-id setup failure where nothing
// landed. The exit code stays 1 either way; the `resumable`/`stage` fields
// carry the distinction (status.go).
func (d *Driver) fail(ctx context.Context, id string, err error) (Outcome, error) {
	ev := errorEvent{Event: "error", ID: id, Error: err.Error(), Hint: backendHint(err.Error())}
	if id != "" {
		// detach from ctx: a cancelled/timed-out ctx (the very failure being
		// reported) must not suppress the resumability lookup — the card is
		// exactly what a caller needs to know survives.
		if f, gerr := d.store.GetFeature(context.WithoutCancel(ctx), domain.FeatureID(id)); gerr == nil {
			ev.Resumable = !workflow.Terminal(f.Kind, f.Stage)
			ev.Stage = string(f.Stage)
			if ev.Resumable {
				ev.Next = resumeCmd(id)
			}
		}
	}
	d.out.emit(ev)
	return Outcome{Status: StatusError, ID: id}, err
}

// --- helpers -----------------------------------------------------------

// createFeature mints and persists a new item, seeding its design artifact
// from the description (mirrors ui/msgs.go:createFeature). For
// KindFeature/KindBug the quick route is default (--full keeps
// brainstorm+plan) and the overflow seeds a draft under ws.DraftsDir().
// KindResearch has no brainstorm/plan and no draft step: the brief is
// rendered straight to the RS artifact path via SeededResearchTemplate.
// The actual recipe lives in internal/cardmint, shared with the workspace
// MCP endpoint's card_new tool — this is now just the translation from a
// Driver's own Options to a cardmint.Input.
func (d *Driver) createFeature(ctx context.Context, kind domain.Kind, desc string) (domain.Feature, error) {
	return cardmint.Mint(ctx, d.store, d.ws, cardmint.Input{
		Kind: kind, Description: desc, Profile: d.opts.Profile, Envelope: d.opts.Envelope,
		Full: d.opts.Full, Repo: d.opts.Repo, RepoKnown: d.eng.RepoKnown,
		ExternalRef: d.opts.Ref, Acceptance: d.opts.Acceptance, GateApproval: d.opts.GateApproval,
	})
}

// enterStage resets per-stage state (the verbose activity cursor points
// into a session's own feed, which is fresh each stage).
func (d *Driver) enterStage(stage domain.Stage) {
	d.curStage = stage
	d.activityCur = 0
	d.sentTurn = false
}

// reattachSilent reports whether Attach would reattach to f's interactive
// stage without sending a turn: a restored or still-live session for the
// same stage already carries the interview transcript, so the engine treats
// the conversation as underway (or finished) and stays quiet on attach. It
// mirrors Attach's own `fresh` test (transcript emptiness), computed before
// attaching so the driver can present the gate instead of parking a
// turn-less session. A fresh stage (no session, empty transcript) reads
// false — Attach will kick it off.
func (d *Driver) reattachSilent(f domain.Feature) bool {
	snap := d.snapshot(f.ID)
	return snap.Feature.Stage == f.Stage && len(snap.Transcript) > 0
}

// snapshot returns the live session snapshot for id, or a zero snapshot.
func (d *Driver) snapshot(id domain.FeatureID) engine.Snapshot {
	if s := d.eng.Get(id); s != nil {
		return s.Snapshot()
	}
	return engine.Snapshot{}
}

// pendingAsk returns the feature's open ask_user question, or nil.
func (d *Driver) pendingAsk(id domain.FeatureID) *engine.Ask {
	return d.snapshot(id).PendingAsk
}

// emitResult emits a stage result line (verify pass/fail, review pass/
// changes) — the semantic milestone at a verdict boundary.
func (d *Driver) emitResult(f domain.Feature, v verdict.Verdict) {
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: v.String()})
}

// UntilStops lists the stages --until may name for an item's route: the
// design-side stages actually present on it (Interactive stages plus a
// feature's Plan), in workflow order. These are the deliberate
// pre-implementation boundaries where stopping is meaningful and
// unambiguous (unlike the implement↔review rerun loop). A skipped stage
// is not on the route, so it is never a valid stop — on the quick route
// (brainstorm + plan skipped) only Spec remains.
func UntilStops(kind domain.Kind, skip domain.SkipFlags) []domain.Stage {
	var out []domain.Stage
	if kind == domain.KindResearch {
		return []domain.Stage{domain.StageShape}
	}
	if kind == domain.KindBug {
		if !skip.Triage {
			out = append(out, domain.StageTriage)
		}
		if !skip.Diagnose {
			out = append(out, domain.StageDiagnose)
		}
		return out
	}
	if !skip.Brainstorm {
		out = append(out, domain.StageBrainstorm)
	}
	out = append(out, domain.StageSpec)
	if !skip.Plan {
		out = append(out, domain.StagePlan)
	}
	return out
}

// ValidateUntil accepts an empty Until (run to verified) or a stage that is
// a legal stop on the item's route (UntilStops); anything else — an
// off-route stage (e.g. --until plan on the quick route) or an unknown
// stage — is a usage error naming the valid choices.
func ValidateUntil(until domain.Stage, kind domain.Kind, skip domain.SkipFlags) error {
	if until == "" {
		return nil
	}
	stops := UntilStops(kind, skip)
	for _, s := range stops {
		if s == until {
			return nil
		}
	}
	labels := make([]string, len(stops))
	for i, s := range stops {
		labels[i] = string(s)
	}
	return fmt.Errorf("--until %q is not a valid stop on this route; choose one of: %s", until, strings.Join(labels, ", "))
}

// forwardEdge is the primary forward stage out of f's current stage, for
// labeling a caller-gate's `to`. It mirrors Advance's edge choice.
func forwardEdge(f domain.Feature) domain.Stage {
	nexts := workflow.Next(f.Kind, f.Stage, f.Skip)
	if len(nexts) == 0 {
		return f.Stage
	}
	return nexts[len(nexts)-1]
}

// firstErr returns a if non-nil, else fallback — for turning an optional error
// detail into a guaranteed non-nil error.
func firstErr(a, fallback error) error {
	if a != nil {
		return a
	}
	return fallback
}

// terminal reports whether an Outcome ends the drive loop (a set Status).
func (o Outcome) terminal() bool { return o.Status != "" }

// --- event pump --------------------------------------------------------

type endKind int

const (
	endIdle endKind = iota
	endQuestion
	endExhausted
	endError
	endTimeout
	endTripwire
)

type stageEnd struct {
	kind       endKind
	err        error
	committed  bool     // endExhausted: the stage's work was committed, not stranded
	dirtyPaths []string // endTripwire: paths the agent dirtied on the main checkout
}

// awaitStage reads the engine stream for feature id until the stage
// reaches a decision boundary, enforcing the per-stage inactivity
// timeout. It emits verbose activity lines as tool calls stream. It is
// the driver's only read of engine.Events(), so it must run whenever a
// session is live or the engine's pump goroutines would block.
func (d *Driver) awaitStage(ctx context.Context, id domain.FeatureID) (stageEnd, error) {
	var timer *time.Timer
	var tick <-chan time.Time
	if d.opts.StageTimeout > 0 {
		timer = time.NewTimer(d.opts.StageTimeout)
		defer timer.Stop()
		tick = timer.C
	}
	reset := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d.opts.StageTimeout)
	}

	for {
		select {
		case <-ctx.Done():
			return stageEnd{}, ctx.Err()
		case <-tick:
			return stageEnd{kind: endTimeout}, nil
		case ev, ok := <-d.eng.Events():
			if !ok {
				return stageEnd{kind: endError, err: errors.New("engine event stream closed")}, nil
			}
			if ev.Feature != id {
				continue // single feature per process, but stay defensive
			}
			reset()
			switch ev.Kind {
			case engine.EventQuestion:
				return stageEnd{kind: endQuestion}, nil
			case engine.EventExhausted:
				return stageEnd{kind: endExhausted, committed: ev.Committed}, nil
			case engine.EventError:
				return stageEnd{kind: endError, err: ev.Err}, nil
			case engine.EventIdle:
				return stageEnd{kind: endIdle}, nil
			case engine.EventTripwire:
				// a clean->dirty transition on the main checkout: the run is
				// hard-stopped (engine.trip emits this then kills the session),
				// so treat it as an end and carry the dirtied paths for the
				// escalation. The trip's own EventStopped follows, but we return
				// here before reading it. Nothing further can arrive for this
				// feature.
				return stageEnd{kind: endTripwire, dirtyPaths: ev.DirtyPaths}, nil
			case engine.EventUpdated, engine.EventMessage, engine.EventAnnotations:
				d.emitActivity(id)
			case engine.EventCheckpointFailed:
				// non-terminal: the stage keeps running, so this is never a
				// decision boundary — unlike emitActivity, unconditional
				// (not gated by verbose): it belongs in the default
				// milestone stream, not the verbose-only activity feed.
				d.out.emit(checkpointFailedEvent{Event: "checkpoint_failed", ID: string(id), Stage: string(ev.Stage), Error: ev.Err.Error()})
			default:
				// Started, Budget, Stopped: not a decision boundary. Stopped is
				// emitted on benign session teardowns too (a gate-advance drops
				// the outgoing session, a within-stage replan replaces one), so
				// it can't be trusted as a death signal here; the engine
				// surfaces a genuine death as an EventError before the
				// trailing stop, and that path escalates above.
			}
		}
	}
}

// emitActivity streams any new lines from the live session's activity
// feed (verbose only).
func (d *Driver) emitActivity(id domain.FeatureID) {
	if !d.out.verbose {
		return
	}
	act := d.snapshot(id).Activity
	for i := d.activityCur; i < len(act); i++ {
		d.out.activity(string(id), string(d.curStage), act[i])
	}
	if len(act) > d.activityCur {
		d.activityCur = len(act)
	}
}
