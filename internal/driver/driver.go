package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/reentry"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/gatepolicy"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/workflow"
	"github.com/morphis/gummi/internal/worktree"
)

// GateApproval selects whether a gate stops for the caller. The quality
// floor (the critique, verify, blockers) runs the same either way. The
// canonical values live in domain (they are what is persisted on the
// card); these alias them so the driver's callers keep a local name.
const (
	GateAttended  = domain.GateAttended
	GateAutopilot = domain.GateAutopilot
)

// Options configures one run. Envelope is required (D6); a missing agent
// or envelope fails loud before any work starts.
type Options struct {
	Envelope int
	// SubstrateRuns and SubstrateMinutes raise a goal's substrate budget on
	// resume; zero leaves that ceiling where it is. Like Envelope they only
	// ever raise.
	SubstrateRuns, SubstrateMinutes int
	Profile                         string
	GateApproval                    string // GateAttended (default) | GateAutopilot
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
	// Base is the branch the created card's work forks from and lands on
	// ("" for whatever the repository has checked out, which is what
	// every card did before bases were selectable).
	Base string
	// GoalDoc, for a goal, is a complete goal doc to start the plan from
	// (--plan-file) instead of the template seeded with the objective.
	GoalDoc string
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
	rounds     map[roundKey]int // automatic loop rounds burned, keyed by (id, round_kind)
	reviewsRun int              // review stages entered so far (for the done receipt)
	opening    string           // one-shot message to send on the next interactive stage (resume answer / change note)
	// openingIsAnswer marks d.opening as a --answer: it rides engine.Answer
	// (which records the ask's round trip and closes the decision) rather
	// than Send (a plain turn, what --request-changes sends).
	openingIsAnswer bool
	bounceNote      string       // one-shot addendum to the next plan/implement kickoff after a --bounce resume
	curStage        domain.Stage // stage currently being driven (for verbose activity lines)
	activityCur     int          // cursor into the live session's activity feed
	// resumePrimed is the one-shot "skip what is already there" flag a
	// resume sets before it streams anything. See emitActivity.
	resumePrimed bool
	// activitySession identifies the session activityCur points into. A
	// STAGE is not one session: the writer, its critique and every
	// replan round are separate sessions, each with its own Activity
	// feed starting at zero. Keying the cursor to the stage alone left
	// it parked at the previous session's high-water mark, so every
	// shorter session that followed streamed nothing — which on a
	// three-round plan meant the critique and both replans, two thirds
	// of the run's spend, ran silent under --verbose.
	activitySession time.Time
	sentTurn        bool // a turn was dispatched to the agent this stage (drives the timeout diagnosis)

	// events is the stream this driver reads when it is not the engine's
	// own (a goal's cards share one process, split by hub); nil reads
	// engine.Events() directly.
	events <-chan engine.Event
	hub    *eventHub
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
		opts.GateApproval = GateAttended
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
// GateAttended. It is called at construction and again on resume once the
// card's persisted mode is known.
func (d *Driver) setGate(mode string) {
	if mode == "" {
		mode = GateAttended
	}
	d.opts.GateApproval = mode
	d.actor = "auto"
	if mode == GateAttended {
		d.actor = "caller"
	}
}

// Run creates one feature from a free-form description and drives it to a
// terminal Outcome. The quick route is the default; --full opts into the
// brainstorm+plan route (D3). It is the convenience form of Create + Drive
// for callers that need no lock between the two; the CLI drives via the
// split so it can hold the card's per-card lock for the whole drive.
func (d *Driver) Run(ctx context.Context, desc string) (Outcome, error) {
	f, err := d.Create(ctx, domain.CardType{Kind: domain.KindFeature}, desc)
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
func (d *Driver) Create(ctx context.Context, ct domain.CardType, desc string) (domain.Feature, error) {
	// validate --until against the route this run will take before minting a
	// feature, so a bad stop target never leaves a stray FD in the backlog.
	if err := ValidateUntil(d.opts.Until); err != nil {
		return domain.Feature{}, err
	}
	f, err := d.createFeature(ctx, ct, desc)
	if err != nil {
		return domain.Feature{}, err
	}
	// One workflow, so one route. The field survives on the wire for a
	// consumer that still reads it; it can no longer vary.
	route := "full"
	// a branch only where one will exist: a research card is
	// worktree-less at every stage and never gets one.
	branch := ""
	if ct.Kind != domain.KindResearch {
		branch = f.BranchName()
	}
	d.out.emit(createdEvent{
		Event: "created", ID: string(f.ID), Ref: d.opts.Ref,
		Branch: branch, Route: route, Envelope: d.opts.Envelope,
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
// caller design gate; Bounce rewinds a card one rerun edge — a
// verify-failed feature back to the work stage, an implement-stage one
// back to the plan that produced it — the headless counterpart of the
// TUI's `b` key, with the (possibly empty) string carried as an addendum
// to the reborn stage's kickoff; all-zero re-runs the parked stage (after
// an exhaustion top-up, a timeout, or an escalation).
//
// There is no decision-id field: when more than one decision is open on a
// card (a verify gate and a budget stop can co-exist, DESIGN §6.3's R2),
// the newest open decision is the one an answer resolves — the card
// stopped there last.
type ResumeInput struct {
	Answer         *string
	Approve        bool
	RequestChanges *string
	Bounce         *string
	// Say runs the composer's reader over a line and reports what the
	// card would do with it — intent, act, target, artifact edit, and
	// whether it would wait for a confirm — as one `say` event, then
	// stops without acting. It is how a script sees a reading without a
	// screen to read a chip off; the explicit flags stay the way to act.
	Say *string
	// Goals only. Note adds one of your notes to a running goal (its lead
	// reads it next); Reverse reverses a decision for review ("D-3") and
	// sends the goal back; WrapUp tells a running goal to finish now. A
	// RequestChanges on a goal that is ready for you sends it back with
	// the notes, and Envelope raises the goal budget.
	Note    *string
	Reverse *string
	WrapUp  bool
	// Deliver reports that this resume may only hand its goal decision
	// over: another process holds the card lock and is conducting the
	// goal, so the decision is recorded for that conductor to read on its
	// next tick and nothing is driven here. The CLI sets it when the lock
	// is busy; a free lock drives as usual.
	Deliver bool
}

// goalDecisionName names the decision for the `noted` event.
func (in ResumeInput) goalDecisionName() string {
	switch {
	case in.Note != nil:
		return "note"
	case in.WrapUp:
		return "wrap-up"
	case in.Reverse != nil:
		return "reverse " + *in.Reverse
	}
	return ""
}

// GoalDecision reports that this resume carries a goal-only decision —
// the three verbs whose whole point is to reach a goal WHILE it runs.
// They write a row the conductor reads and drive nothing themselves,
// which is what lets them be delivered without the card lock.
func (in ResumeInput) GoalDecision() bool {
	return in.Note != nil || in.Reverse != nil || in.WrapUp
}

// Resume rehydrates the engine's persisted sessions, applies the caller's
// decision, and drives on. gummi's restartability (SQLite state, spec on
// branch, session resume) makes this free (DESIGN §4).
func (d *Driver) Resume(ctx context.Context, id domain.FeatureID, in ResumeInput) (Outcome, error) {
	// Whatever activity this card already carries belongs to the run that
	// parked it and has been streamed once already; emitActivity skips to
	// the end of it the first time it looks. Run does not set this — a run
	// creates the card, so there is nothing before it.
	d.resumePrimed = true
	if err := d.eng.Restore(ctx); err != nil {
		return d.fail(ctx, string(id), fmt.Errorf("restoring sessions: %w", err))
	}
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if err := ValidateUntil(d.opts.Until); err != nil {
		return d.fail(ctx, string(id), err)
	}

	// gate-approval mode persists on the card. A resume that re-passes
	// --gate-approval overrides (and re-persists) it; one that doesn't
	// inherits the mode `run` chose, instead of silently reverting to auto.
	if d.opts.GateApprovalSet {
		mode := d.opts.GateApproval // canonical (attended|autopilot), retired spellings already resolved by the CLI
		stored := f.GateApproval
		if stored == "" {
			stored = GateAttended
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

	if in.Say != nil {
		return d.say(ctx, f, *in.Say)
	}

	// --answer resolves a delegated ask_user question, and the durable
	// decision is the record of one being open. Before decisions were
	// durable, an answer to a card parked anywhere else was silently
	// delivered as a plain turn — a change note wearing an answer's flag.
	// The record makes that checkable: no open ask means the flag does not
	// apply, and the caller is told which verb this stop actually takes.
	if in.Answer != nil && d.pendingAsk(id) == nil {
		if open := d.newestOpenDecision(ctx, id); open == nil || open.Kind != state.DecisionKindAsk {
			return d.fail(ctx, string(id), fmt.Errorf(
				"%s has no open question to answer — it waits at a design gate (--approve/--request-changes) or an escalation (--bounce)", id))
		}
	}

	if f.IsGoal() {
		if out, handled, err := d.resumeGoal(ctx, f, in); handled || err != nil {
			if err != nil {
				return d.fail(ctx, string(id), err)
			}
			return out, nil
		}
		if in.Deliver {
			// another process is conducting this goal: the decision is
			// recorded and that conductor acts on it. Driving it here
			// would be a second driver on the same cards.
			d.out.emit(notedEvent{Event: "noted", ID: string(id), What: in.goalDecisionName()})
			return Outcome{Status: StatusNoted, ID: string(id)}, nil
		}
		return d.drive(ctx, id)
	}
	if in.Note != nil || in.Reverse != nil || in.WrapUp {
		return d.fail(ctx, string(id), fmt.Errorf("%s is not a goal; --note, --reverse and --wrap-up apply to goals", id))
	}

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
		// the feature to its work stage, and a card whose plan turned out
		// wrong by rewinding it to plan — the same rerun edges the TUI's `b`
		// key takes via bounceStage. The optional note becomes an addendum to
		// the reborn stage's kickoff, alongside any open diff/spec
		// annotations the engine folds in independently.
		back, ok := workflow.RerunTarget(f.Stage)
		if !ok {
			return d.fail(ctx, string(id),
				fmt.Errorf("%s is at %s; --bounce only rewinds verify to %s or implement to %s",
					id, f.Stage, domain.StageImplement, domain.StagePlan))
		}
		if _, err := d.store.Transition(ctx, id, back, d.actor); err != nil {
			return d.fail(ctx, string(id), err)
		}
		d.eng.Drop(id) // the stale implement/verify session must not restart
		d.bounceNote = *in.Bounce
	case in.RequestChanges != nil:
		d.opening = *in.RequestChanges
	case in.Answer != nil:
		d.opening = *in.Answer
		d.openingIsAnswer = true
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
		case engine.StatusBlockedUndrafted:
			d.out.emit(blockedEvent{Event: "blocked", ID: string(id), Gate: string(res.Advance.From), Undrafted: res.Advance.Undrafted, Resume: string(id)})
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
// landed commit's sha and returns StatusVerified.
func (d *Driver) Merge(ctx context.Context, id domain.FeatureID, message string) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if f.IsGoal() {
		return d.mergeGoal(ctx, f, message)
	}
	wt, err := d.eng.WorktreesFor(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}

	// the verified-branch precondition, stricter than the TUI's any-stage m
	// key: this command exists to land a verified branch.
	//
	// A HANDED-OFF card is the one exception, and it is exactly the TUI's:
	// hand-off closes the card with its branch left unlanded, and landing
	// it after all stays available for as long as the branch is there. Such
	// a card is at done with a verified stamp already on it, so both
	// preconditions below would refuse it on a technicality rather than on
	// anything about the branch.
	if !f.HandedOff() {
		if f.Stage == domain.StageDone {
			return d.fail(ctx, string(id), fmt.Errorf("%s is already done", id))
		}
		if f.Stage != domain.StageVerify || f.VerifiedAt.IsZero() {
			return d.fail(ctx, string(id),
				fmt.Errorf("%s is not at a verified branch (stage %s); run `gummi verify %s` first if it lost its finalize", id, f.Stage, id))
		}
	}
	// a card lands either via its linked PR or locally, never both: refuse
	// before any git mutation, naming the PR and the unlink escape.
	if !f.PullRequest.Empty() {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s is linked to %s#%d (%s); land it via the PR, or run `gummi pr unlink %s` to land it locally instead",
				id, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, id))
	}
	// A stacked card carries the commits of every card below it, so it
	// cannot land before they have. The only ordering a stack imposes —
	// and it constrains landing alone, never the work.
	if blocker, blocked := d.eng.StackLandBlocker(ctx, &f); blocked {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s sits on %s in its stack; land %s first, or its commits would ride in under %s",
				id, blocker, blocker, id))
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
	// the branch this card actually lands on, read off the manager it
	// resolved to rather than written as the literal "main": a `master`
	// repo is one case, and a card inside a goal — whose manager is rooted
	// at the GOAL's worktree, so it lands on the goal branch — is the
	// other.
	base := wt.BaseBranch(ctx)
	if dirty, err := wt.MainTrackedDirty(ctx); err != nil {
		return d.fail(ctx, string(id), err)
	} else if dirty {
		return d.fail(ctx, string(id), fmt.Errorf("%s checkout has uncommitted changes — commit or stash them before merging", base))
	}
	if landed, err := wt.Landed(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if landed {
		return d.fail(ctx, string(id), fmt.Errorf("%s already landed on %s — run `gummi clean %s`", id, base, id))
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
	// Landing retracts a hand-off: the card ended on main after all, and
	// the stamp is what every surface reads to say how it ended.
	if f.HandedOff() {
		if err := d.store.ClearHandedOffAt(ctx, id); err != nil {
			return d.fail(ctx, string(id), fmt.Errorf("landed %s but clearing its hand-off mark failed: %w", id, err))
		}
	}
	// A handed-off card is already at done; only a card arriving from
	// verify has a transition to record.
	if f.Stage != domain.StageDone {
		if _, err := d.store.Transition(ctx, id, domain.StageDone, d.actor); err != nil {
			return d.fail(ctx, string(id), fmt.Errorf("landed %s but moving it to done failed: %w", id, err))
		}
	}
	d.out.emit(mergedEvent{Event: "merged", ID: string(id), Branch: f.BranchName(), Commit: sha})
	return Outcome{Status: StatusVerified, ID: string(id)}, nil
}

// Clean removes a landed card's worktree and branch — the headless
// counterpart of the TUI's c key. It keeps the card record (it stays as a
// done entry) and never removes anything that has not actually landed or
// that carries tracked-dirty rework. On success it emits a `cleaned` event
// and returns StatusVerified.
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
		if f.HandedOff() {
			return d.fail(ctx, string(id),
				fmt.Errorf("%s was handed off, not landed — cleaning up would delete %s", id, f.BranchName()))
		}
		return d.fail(ctx, string(id), fmt.Errorf("%s has not landed on %s — nothing to clean", id, wt.BaseBranch(ctx)))
	}
	if dirty, err := wt.TrackedDirty(ctx, &f); err != nil {
		return d.fail(ctx, string(id), err)
	} else if dirty {
		return d.fail(ctx, string(id), fmt.Errorf("%s worktree has tracked-dirty rework; resolve or commit it before cleaning", id))
	}
	// A goal's cards resolve to a manager rooted at the goal's tree, so
	// they come out before it does: after this remove their checkouts are
	// unreachable and their branches read as unlanded for good
	// (Engine.CleanGoalCards). A sweep that fails is not fatal to the
	// goal's own cleanup — it leaves checkouts behind, which is what
	// happened before it existed.
	var swept engine.GoalCardCleanup
	if f.IsGoal() {
		swept, _ = d.eng.CleanGoalCards(ctx, id)
	}
	if err := wt.Remove(ctx, &f, true); err != nil {
		return d.fail(ctx, string(id), err)
	}
	// A research card runs in a scratch tree rather than a branch worktree
	// (a research branch never receives a commit); absent is success, so
	// this runs unconditionally for every kind.
	if err := wt.RemoveScratch(ctx, &f); err != nil {
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
	d.out.emit(cleanedEvent{Event: "cleaned", ID: string(id), Branch: f.BranchName(),
		Cards: idStrings(swept.Took), Kept: idStrings(swept.Left)})
	return Outcome{Status: StatusVerified, ID: string(id)}, nil
}

// idStrings is a card-id slice as the NDJSON surface carries it.
func idStrings(ids []domain.FeatureID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// HandOff ends a card without landing it — the headless counterpart of the
// TUI's h key and the third ending a verified card has. The branch stays
// exactly where it is and the card moves to done; the caller owns whatever
// happens to the branch next (a push, a PR opened by hand, a cherry-pick,
// or nothing at all).
//
// The engine owns the three steps (final checkpoint commit, the hand-off
// stamp, then Advance through the unchanged gate floor); this maps the
// result to NDJSON + Outcome. A gate blocker is NOT a hand-off: the card
// stays at verify and the refusal is reported as the error it is, because
// waiving the landing never waived the quality floor.
func (d *Driver) HandOff(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if f.Stage == domain.StageDone {
		return d.fail(ctx, string(id), fmt.Errorf("%s is already done", id))
	}
	if f.Kind == domain.KindResearch {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s carries no branch to hand off; advance it instead", id))
	}
	if f.IsGoal() && (f.Stage != domain.StageVerify || f.VerifiedAt.IsZero()) {
		// a goal handed off before it is ready is abandoned: its unfinished
		// cards are dropped and its branch is kept
		res, err := d.eng.AbandonGoal(ctx, id, d.actor)
		if err != nil {
			return d.fail(ctx, string(id), err)
		}
		if res.Status != engine.StatusAdvanced {
			return d.fail(ctx, string(id), handOffRefusal(id, res))
		}
		d.out.emit(handedOffEvent{Event: "handed off", ID: string(id), Branch: f.BranchName()})
		return Outcome{Status: StatusVerified, ID: string(id)}, nil
	}
	// the verified-branch precondition, the same one Merge applies: this
	// verb ends a card that finished, not one abandoned mid-flight.
	if f.Stage != domain.StageVerify || f.VerifiedAt.IsZero() {
		return d.fail(ctx, string(id),
			fmt.Errorf("%s is not at a verified branch (stage %s); run `gummi verify %s` first if it lost its finalize", id, f.Stage, id))
	}

	res, err := d.eng.HandOff(ctx, id, d.actor)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	if res.Status != engine.StatusAdvanced {
		return d.fail(ctx, string(id), handOffRefusal(id, res))
	}
	d.out.emit(handedOffEvent{Event: "handed off", ID: string(id), Branch: f.BranchName()})
	return Outcome{Status: StatusVerified, ID: string(id)}, nil
}

// handOffRefusal turns a non-advancing gate result into the sentence a
// headless caller gets. Every one of these is a floor a hand-off does not
// waive, so each names what to resolve rather than how to force it.
func handOffRefusal(id domain.FeatureID, res engine.AdvanceResult) error {
	switch res.Status {
	case engine.StatusBlockedQuestions:
		return fmt.Errorf("%s has %d unresolved spec thread(s) blocking the gate", id, res.Blockers)
	case engine.StatusBlockedDiff:
		return fmt.Errorf("%s has %d unresolved diff annotation(s) blocking the gate", id, res.Blockers)
	case engine.StatusBlockedOmission:
		return fmt.Errorf("%s: %s", id, res.Reason)
	case engine.StatusBlockedUndrafted:
		return fmt.Errorf("%s: %s wrote nothing in %s — the gate stays shut until the section is drafted",
			id, res.From, strings.Join(res.Undrafted, ", "))
	case engine.StatusNoop:
		return fmt.Errorf("%s has nothing left to advance", id)
	}
	return fmt.Errorf("%s: the verify gate refused the hand-off", id)
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
		return d.fail(ctx, string(id), fmt.Errorf("%s is already landed on %s", id, wt.BaseBranch(ctx)))
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
		return Outcome{Status: StatusVerified, ID: string(id)}, nil
	}
	d.out.emit(squashedEvent{
		Event: "squashed", ID: string(id), Branch: f.BranchName(),
		BeforeSHA: beforeSHA, AfterSHA: sha, BaseSHA: base,
		MessageSubject: commitSubject(message),
	})
	return Outcome{Status: StatusVerified, ID: string(id)}, nil
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
// StatusVerified with no `committed` event, not an error.
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
		return Outcome{Status: StatusVerified, ID: string(id)}, nil
	}

	sha, err := wt.Head(ctx, &f)
	if err != nil {
		return d.fail(ctx, string(id), err)
	}
	d.out.emit(committedEvent{Event: "committed", ID: string(id), Branch: f.BranchName(), Commit: sha})
	return Outcome{Status: StatusVerified, ID: string(id)}, nil
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
	tookOver := false
	for {
		if err := ctx.Err(); err != nil {
			return d.fail(ctx, string(id), err)
		}
		f, err := d.store.GetFeature(ctx, id)
		if err != nil {
			return d.fail(ctx, string(id), err)
		}
		if f.Stage == domain.StageDone || workflow.Terminal(f.Stage) {
			return d.done(ctx, f)
		}
		if !tookOver && f.Stage != domain.StageTodo {
			// Log the takeover once per call, and only once we're at the
			// flow's first real stage — never at todo, which every call
			// (fresh or resumed) passes through on its way there regardless
			// of whether anything genuinely new is starting. Waiting for
			// the real stage also keeps the row's dedupe key (logTookOver,
			// below) stable across a resume that re-enters the exact stage
			// the loop was already stuck at: logging at todo would record
			// that transient hop's stage instead, which a resume never
			// re-observes (todo has already been crossed), defeating the
			// dedupe entirely.
			d.logTookOver(f)
			tookOver = true
		}

		var out Outcome
		switch {
		case f.IsGoal() && f.Stage == domain.StageImplement:
			// a goal's implement stage is conducted, not written
			out, err = d.driveGoal(ctx, f)
		case f.Stage == domain.StagePlan:
			out, err = d.driveDesign(ctx, f)
		case f.Stage == domain.StageTodo:
			// todo is a pure kickoff gate (no agent action): advance into the
			// flow's first real stage. This is "start", not a design decision,
			// so it always auto-crosses regardless of --gate-approval.
			out, err = d.autoAdvance(ctx, f)
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
// driveDesign drives the design stage — the one that converses. It is
// keyed on the stage rather than on an "interactive" flag because the
// flag is gone: chat is a session you open, not a state a stage is in.
// What is true of THIS stage is that it can stop for an answer, and the
// headless driver has to present that stop rather than await a turn that
// will never come.
//
// It ends by handing the finished conversation to the stage's critique
// (designComplete), not straight to the gate: every agent stage ends
// with one now, and the design stage is no exception.
func (d *Driver) driveDesign(ctx context.Context, f domain.Feature) (Outcome, error) {
	d.enterStage(f.Stage)
	// Seed the loop's round counter from the persisted value so a resume
	// honours (and reports) the rounds already burned. A failed read
	// aborts entry: the count is the budget.
	if err := d.seedRounds(ctx, f, domain.RoundKindPlan); err != nil {
		return Outcome{}, err
	}
	d.out.emit(stageEvent{
		Event: "stage", ID: string(f.ID), Stage: string(f.Stage),
		Round: d.round(f.ID, domain.RoundKindPlan),
	})

	// A bare resume that lands on an interactive stage already driven to its
	// gate has no turn to send: the restored/live session carries the
	// finished interview, so Attach reattaches silently (interactive stages
	// advance on human turns, and a completed one has nothing to send).
	// Re-entering would park a turn-less session that can only time out —
	// the deadlock the operator report caught. Present the checkpoint the
	// card actually stopped at, read from the durable decision record where
	// one exists: an open ask is answered (re-presented), not crossed; only
	// its absence defers to the transcript-emptiness proxy, which legacy
	// cards written before decisions were durable still need. crossGate
	// auto-advances under --gate-approval=auto, checkpoints under caller,
	// and surfaces any open-question blockers — so an incomplete interview
	// reports its blockers rather than hanging, and neither path waits on a
	// turn that was never sent. (A resume carrying an answer / change note
	// does have a turn to send, so it falls through to Attach + answer
	// below.)
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
		} else if open := d.newestOpenDecision(ctx, f.ID); open != nil && open.Kind == state.DecisionKindAsk {
			// the card is blocked on an answer, not on the gate: present the
			// question checkpoint again (the restored session re-armed its
			// pending ask, so the next --answer lands through engine.Answer).
			d.out.emit(questionEvent{
				Event: "question", ID: string(f.ID), Q: open.Question,
				Decision: open.ID, FreeForm: true,
				Resume: string(f.ID),
				Next:   d.resumeCmd(string(f.ID), "--answer", `"<answer>"`),
			})
			d.logPark(f, state.ParkReasonNeedsYou, open.Question)
			return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
		} else if out, handled, err := d.resumeCritiqueLoop(ctx, f); handled || err != nil {
			// the loop stopped mid-critique, or awaiting the judge: put
			// the resume back there rather than restarting the
			// conversation from the top.
			return out, err
		} else if d.reattachSilent(f) {
			return d.designComplete(ctx, f)
		}
	}

	// A rewind's note (--bounce from the work stage) rides the fresh
	// plan session's kickoff — the same delivery Engine.RunWith gives the
	// work stage's reborn run. The stash is one-shot and this stage's
	// dispatch is fresh by construction (Resume dropped the rewound
	// session), so consume it here rather than sending it as a turn.
	if note := d.bounceNote; note != "" {
		d.bounceNote = ""
		if err := d.eng.RunWith(f, note); err != nil {
			return Outcome{}, err
		}
	} else if _, err := d.eng.Attach(ctx, f); err != nil {
		return Outcome{}, err
	}
	// past the guard a turn is always dispatched — a fresh Attach (or
	// RunWith) kicks off the stage, and a resume seeds the answer / change
	// note below.
	d.sentTurn = true
	if d.opening != "" {
		msg := d.opening
		answers := d.openingIsAnswer
		d.opening, d.openingIsAnswer = "", false
		// An answer is routed through engine.Answer, not Send: Answer
		// records the ask's round trip in the card's own log — who
		// answered, and which decision it closed — and delivers as a fresh
		// turn when the blocked tool call is gone (every restored ask).
		// A request-changes note is a turn, never an answer.
		if answers {
			if err := d.eng.Answer(ctx, f.ID, msg); err != nil {
				return Outcome{}, err
			}
		} else if err := d.eng.Send(ctx, f.ID, msg); err != nil {
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
		case endQuestion:
			ask := d.pendingAsk(f.ID)
			if ask == nil {
				// no question actually pending — treat as a finished turn.
				return d.designComplete(ctx, f)
			}
			if d.opts.Autonomous {
				rec := engine.RecommendedOption(ask)
				// a goal card's question goes to its goal's lead first; the
				// answerer on record stays the unattended loop (no person
				// typed it), and the goal's log names the lead
				if f.InGoal() {
					if ans, ok, gerr := d.eng.GoalAnswer(ctx, f.ID, ask); gerr == nil && ok && ans != "" {
						rec = ans
					}
				}
				// the answerer declares itself: the record must say an
				// unattended loop took it, whoever's stored mode the card
				// runs under, or the morning receipt under-counts it.
				if err := d.eng.AnswerAs(ctx, f.ID, rec, state.ActorAutopilot); err != nil {
					return Outcome{}, err
				}
				d.out.activity(string(f.ID), string(f.Stage), "auto-answered: "+rec)
				continue // the turn resumes with the answer; keep reading
			}
			decisionID := ""
			if ask.DecisionID != "" {
				decisionID = ask.DecisionID
			}
			d.out.emit(questionEvent{
				Event: "question", ID: string(f.ID), Q: ask.Question,
				Options: askLabels(ask), Recommended: engine.RecommendedOption(ask),
				FreeForm: true, Resume: string(f.ID),
				Next:     d.resumeCmd(string(f.ID), "--answer", `"<answer>"`),
				Decision: decisionID,
			})
			d.logPark(f, state.ParkReasonNeedsYou, ask.Question)
			return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
		case endIdle:
			// a finished conversation with no open question: hand it to
			// the critique, which decides whether the gate is reached.
			if d.pendingAsk(f.ID) != nil {
				continue
			}
			return d.designComplete(ctx, f)
		}
	}
}

// designComplete ends the design stage: critique what the conversation
// produced, then let the verdict decide. A pass reaches the human gate
// (judgeCritique's RaiseGate arm), a changes verdict re-runs the stage.
//
// A session that is already the critique is judged rather than
// re-critiqued — the conversation loop can land here more than once.
func (d *Driver) designComplete(ctx context.Context, f domain.Feature) (Outcome, error) {
	if snap := d.snapshot(f.ID); snap.Feature.Stage == f.Stage && snap.Critique {
		return d.judgeCritique(ctx, f, snap)
	}
	if err := d.dispatchCritique(f, ""); err != nil {
		return Outcome{}, err
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "critiquing"})
	return d.awaitCritique(ctx, f)
}

// driveAutonomous drives an autonomous stage (plan/implement/review/
// verify/fix) to completion, then applies the verdict rules that either
// step the in-floor loop forward, escalate, or reach a verified branch.
func (d *Driver) driveAutonomous(ctx context.Context, f domain.Feature) (Outcome, error) {
	d.enterStage(f.Stage)
	round := 0
	// Seed the loop's round counter from the persisted value so a resume
	// honours (and reports) the rounds already burned this cycle. A failed
	// read aborts stage entry: the count is the budget. Which counter is
	// the stage's own business (engine.CritiqueRoundKind).
	switch f.Stage {
	case domain.StageImplement:
		if err := d.seedRounds(ctx, f, domain.RoundKindReview); err != nil {
			return Outcome{}, err
		}
		round = d.round(f.ID, domain.RoundKindReview)
	case domain.StagePlan:
		if err := d.seedRounds(ctx, f, domain.RoundKindPlan); err != nil {
			return Outcome{}, err
		}
		round = d.round(f.ID, domain.RoundKindPlan)
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Round: round})

	// A resume that lands on a stage with a critique must not re-invoke the
	// stage's writer: the restored/live session tells us where the loop was
	// when it stopped. A finished writer (revised output already on disk)
	// resumes the critique; a finished critique routes to the judge (rework
	// on changes / approve on pass); a paused critique (its session died
	// mid-turn, e.g. a recoverable backend failure) re-dispatches the
	// critique; an in-flight session keeps awaiting. Only a fresh entry — no
	// restored session for this stage, or a restored paused writer —
	// starts/restarts the writer below. The snapshot's feature stage is the
	// guard, so a leftover done session from a prior stage is never mistaken
	// for a resume of this one.
	if out, handled, err := d.resumeFinishedVerify(ctx, f); handled || err != nil {
		return out, err
	}
	if out, handled, err := d.resumeCritiqueLoop(ctx, f); handled || err != nil {
		return out, err
	}

	// a --bounce resume stashed a kickoff note for the first work-stage run
	// that follows the rewind; consume it on that exact dispatch so it
	// reaches the reborn implement/fix as an addendum to the kickoff (the
	// same path Engine.RunWith takes for the diff surface's request-changes;
	// a plan rewind's note is consumed by driveDesign, which dispatches the
	// design stage).
	var err error
	if d.bounceNote != "" && (f.Stage == domain.StageImplement) {
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

// burnCorrective tallies an outcome that spent a corrective round against
// the card's unified budget — the running total of every piece of work
// done a second time: review bounces, verify bounces, conflict handoffs.
// Each loop keeps its own cap; this is the number that says what the
// rework cost overall, and it is the one the TUI reports back. It never
// resets mid-run.
//
// Best-effort, like the TUI's counterpart: the loop's own persisted cap
// is written above and must not drift. A missed tally miscounts a report;
// it never re-grants budget.
func (d *Driver) burnCorrective(ctx context.Context, id domain.FeatureID, out gatepolicy.Outcome) {
	if !out.Burns {
		return
	}
	_ = rounds.Bump(ctx, d.roundStore, id, domain.RoundKindCorrective)
	d.setRound(id, domain.RoundKindCorrective, d.round(id, domain.RoundKindCorrective)+1)
}

// applyVerdict routes a finished autonomous stage per the loop rules
// (mirrors internal/ui/reviewloop.go), returning a terminal Outcome or a
// non-terminal one (the stage advanced in-floor; drive loops).
func (d *Driver) applyVerdict(ctx context.Context, f domain.Feature) (Outcome, error) {
	snap := d.snapshot(f.ID)
	switch f.Stage {
	case domain.StageVerify:
		v := verdict.SessionVerdict(snap)
		d.emitResult(f, v)
		out := gatepolicy.Decide(gatepolicy.Input{
			Stage:       domain.StageVerify,
			Kind:        f.Kind,
			Verdict:     v,
			Environment: verdict.BlockedByEnvironment(snap),
			WorkStage:   domain.StageImplement,
			// verify never auto-bounces here: a failed verify always
			// escalates today (gatepolicy documents the eligible-to-bounce
			// rule as dormant; this keeps it switched off).
			VerifyMayBounce: false,
		})
		switch {
		case out.Action == gatepolicy.RaiseGate:
			// stop at the verified branch: Advance reports NeedsMerge (branch
			// ahead) or transitions to Done (nothing to land). Never merges.
			return d.crossGate(ctx, f)
		case out.Reason == gatepolicy.ReasonNoEnvironment && f.IsGoal():
			// Before the rework path, not after it: a verify that could
			// not run judged nothing, and sending the goal back to its
			// cards over it spends a rework round on work nobody found
			// fault with. Two of those used to end a goal partial.
			return d.goalVerifyBlocked(ctx, f), nil
		case f.IsGoal():
			// a goal's verify that did not pass goes back to its cards, or —
			// partial already, or out of rework rounds — stops ready for you
			// with what was not met on the report
			return d.goalVerifyNotPassed(ctx, f, out.Reason)
		case out.Reason == gatepolicy.ReasonNoEnvironment:
			return d.blockedEscalation(f, "verify BLOCKED — the environment cannot run the verification plan; see the artifact"), nil
		case out.Reason == "verify-blocked":
			return d.escalation(f, "verify BLOCKED — the environment cannot run the verification plan; see the artifact"), nil
		case out.Reason == "verify-fail":
			return d.bounceEscalation(f, "verify FAILED — read the evidence in the artifact"), nil
		default: // verify-unclear
			return d.escalation(f, "verify finished with no clear verdict"), nil
		}

	case domain.StageImplement, domain.StagePlan:
		if !snap.Critique {
			// the stage's output was just written: critique it before the
			// gate. This is what the Review stage did, minus the transition.
			if err := d.dispatchCritique(f, ""); err != nil {
				return Outcome{}, err
			}
			d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "critiquing"})
			// wait out the critique pass in-place (same stage, fresh session).
			return d.awaitCritique(ctx, f)
		}
		return d.judgeCritique(ctx, f, snap)

	default:
		return d.escalation(f, "unexpected autonomous stage "+string(f.Stage)), nil
	}
}

// awaitCritique waits for a stage's critique session (RunCritique borrows
// the stage without advancing it) and re-judges — a critique loop is
// invisible to the state machine, so this stays inside the stage it
// started in, whichever that is.
func (d *Driver) awaitCritique(ctx context.Context, f domain.Feature) (Outcome, error) {
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
	default:
		return d.judgeCritique(ctx, f, d.snapshot(f.ID))
	}
}

// judgeCritique applies a critique verdict: pass crosses the stage's gate,
// changes re-runs the stage under its own cap, else escalate.
//
// The round kind comes from the stage (engine.CritiqueRoundKind), so the
// plan critique burns plan rounds and the work stage's critique burns
// review rounds — the same budgets both loops had before the critique
// became a pass. A stage with no counter never reaches here: RunCritique
// refuses to start one.
func (d *Driver) judgeCritique(ctx context.Context, f domain.Feature, snap engine.Snapshot) (Outcome, error) {
	kind, ok := engine.CritiqueRoundKind(f.Stage)
	if !ok {
		return d.escalation(f, "no critique round counter for stage "+string(f.Stage)), nil
	}
	v := verdict.SessionVerdict(snap)
	d.emitResult(f, v)
	max := verdict.MaxRounds(kind)
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:         f.Stage,
		Forward:       forwardEdge(f),
		Kind:          f.Kind,
		Verdict:       v,
		Corrective:    d.round(f.ID, kind),
		CorrectiveMax: max,
		WorkStage:     domain.StageImplement,
		GoalSettled:   f.GoalSettled(),
	})
	switch out.Action {
	case gatepolicy.RaiseGate:
		// the plan gate: crossing it is the user's call (or autopilot's)
		if err := rounds.Reset(ctx, d.roundStore, f.ID, kind); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, kind, 0)
		return d.crossGate(ctx, f)
	case gatepolicy.Advance:
		// A work stage's critique passing is still a crossing, so it is
		// held to the same floor a design gate is: a stage that wrote
		// nothing into the section its forward edge owes has not finished,
		// whatever its critique thought. Checked here rather than inside
		// stepTo because this is the only caller and the blocked outcome
		// is a terminal the caller reports.
		if names := d.eng.UndraftedBlocking(f); len(names) > 0 {
			d.recordBlocked(f, fmt.Sprintf("undrafted %s blocks %s.", strings.Join(names, ", "), f.Stage))
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage),
				Undrafted: names, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		if err := rounds.Reset(ctx, d.roundStore, f.ID, kind); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, kind, 0)
		return d.stepTo(ctx, f.ID, out.Stage)
	case gatepolicy.HandOver:
		// A goal's review asked for changes nothing can make — it has
		// wrapped up or has already given an item up, or it has spent its
		// rounds proving the same thing. The request belongs on the
		// hand-over, not in a retry. gatepolicy owns which of those it
		// was; this arm only carries it out.
		if err := rounds.Reset(ctx, d.roundStore, f.ID, kind); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, kind, 0)
		return d.goalReviewUnactionable(ctx, f, out.Reason)
	case gatepolicy.BounceToWork:
		if err := rounds.Bump(ctx, d.roundStore, f.ID, kind); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, kind, d.round(f.ID, kind)+1)
		d.burnCorrective(ctx, f.ID, out)
		if err := d.eng.RunWith(f, reworkNote(f.Stage)); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: reworkLabel(f.Stage), Round: d.round(f.ID, kind)})
		if f.IsGoal() && f.Stage == domain.StageImplement {
			// a goal's rework is its lead's, recorded by RunWith: conduct
			// the stage again rather than await a session that never runs
			return Outcome{}, nil
		}
		return d.awaitRework(ctx, f)
	default: // Park: the cap was hit, or the verdict was unclear
		if err := rounds.Reset(ctx, d.roundStore, f.ID, kind); err != nil {
			return Outcome{}, err
		}
		d.setRound(f.ID, kind, 0)
		if out.Reason == "critique-changes-cap" {
			return d.bounceEscalation(f, fmt.Sprintf("%s critique still requesting changes after %d rounds", f.Stage, max)), nil
		}
		return d.escalation(f, string(f.Stage)+" critique finished with no clear verdict"), nil
	}
}

// reworkNote is the kickoff a stage's rework round carries: the plan
// stage's own replan note, or — for a work stage — the note the review
// bounce used to carry into implement/fix.
func reworkNote(stage domain.Stage) string {
	if stage == domain.StagePlan {
		return verdict.ReplanNote
	}
	return verdict.ReworkNote
}

// reworkLabel names the rework round on the event stream.
func reworkLabel(stage domain.Stage) string {
	if stage == domain.StagePlan {
		return "replanning"
	}
	return "reworking"
}

// awaitRework waits for a rework pass to finish, then critiques again.
func (d *Driver) awaitRework(ctx context.Context, f domain.Feature) (Outcome, error) {
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
		return Outcome{}, firstErr(end.err, errors.New("rework session failed"))
	default:
		// re-critique the revised output (mirrors ReCritiqueNote intent).
		if err := d.dispatchCritique(f, verdict.ReCritiqueNote); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "re-critiquing"})
		return d.awaitCritique(ctx, f)
	}
}

// dispatchCritique starts a stage's critique pass, counting the work
// stage's critiques for the done receipt. review_rounds still means "how
// many times was the diff judged", which is what the Review stage's own
// entries used to count; the stage is gone but the question is the same.
// The plan critique is not counted here — it has its own round kind and
// its own reporting.
func (d *Driver) dispatchCritique(f domain.Feature, note string) error {
	switch f.Stage {
	case domain.StageImplement, domain.StagePlan:
		d.reviewsRun++
	}
	return d.eng.RunCritique(f, note)
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
	if d.opts.GateApproval == GateAttended && f.Stage != domain.StageVerify {
		// a caller gate on a design stage: report any blockers, else
		// checkpoint for --approve/--request-changes. Verify is never a
		// caller gate — it is the floor's stop-at-verified, always auto.
		specOpen, diffOpen, deps, err := d.eng.GateBlockers(ctx, f.ID)
		if err != nil {
			return Outcome{}, err
		}
		if specOpen > 0 {
			d.recordBlocked(f, fmt.Sprintf("%d open spec threads block %s.", specOpen, f.Stage))
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), OpenSpec: specOpen, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		if diffOpen > 0 {
			d.recordBlocked(f, fmt.Sprintf("%d open diff comments block %s.", diffOpen, f.Stage))
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), OpenDiff: diffOpen, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		if len(deps) > 0 {
			d.recordBlocked(f, dependencyBlockedQuestion(deps))
			d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(f.Stage), BlockingDeps: deps, Resume: string(f.ID)})
			return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
		}
		next := forwardEdge(f)
		question := string(f.Stage) + " is ready for your decision."
		d.logPark(f, state.ParkReasonNeedsYou, question)
		decisionID := d.openDecision(f, state.DecisionKindGate, question)
		d.out.emit(gatePendingEvent{
			Event: "gate", ID: string(f.ID), From: string(f.Stage), To: string(next), Resume: string(f.ID),
			Decision: decisionID,
			Next:     d.resumeCmd(string(f.ID), "--approve"),
		})
		return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
	}
	// a goal card's plan is read by its goal's lead before it implements
	if f.InGoal() && f.Stage == domain.StagePlan {
		if approve, note, ok, err := d.eng.GoalPlanCheck(ctx, f.ID); err == nil && ok && !approve {
			d.opening = "The goal's lead sent this plan back before implementation: " + note
			d.out.emit(gateEvent{Event: "gate", ID: string(f.ID), From: string(f.Stage), To: string(f.Stage), Decision: "sent back by the goal's lead"})
			return Outcome{}, nil
		}
	}
	return d.autoAdvance(ctx, f)
}

// autoAdvance crosses the current gate via the shared engine floor and
// maps the result to NDJSON + Outcome. StatusNeedsMerge (verify→done) is
// the stop-at-verified point; a non-terminal Outcome means the gate
// advanced and drive should loop.
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
		d.recordBlocked(f, fmt.Sprintf("%d open spec threads block %s.", res.Blockers, res.From))
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), OpenSpec: res.Blockers, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDiff:
		d.recordBlocked(f, fmt.Sprintf("%d open diff comments block %s.", res.Blockers, res.From))
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), OpenDiff: res.Blockers, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDependency:
		d.recordBlocked(f, dependencyBlockedQuestion(res.BlockingDeps))
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), BlockingDeps: res.BlockingDeps, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedDocument:
		d.recordBlocked(f, "the citation/coverage floor blocks the decompose gate.")
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), Document: newDocumentSummary(res.DocumentReport), Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedOmission:
		d.recordBlocked(f, res.Reason)
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), Reason: res.Reason, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusBlockedUndrafted:
		d.recordBlocked(f, fmt.Sprintf("undrafted %s blocks %s.", strings.Join(res.Undrafted, ", "), res.From))
		d.out.emit(blockedEvent{Event: "blocked", ID: string(f.ID), Gate: string(res.From), Undrafted: res.Undrafted, Resume: string(f.ID)})
		return Outcome{Status: StatusBlocked, ID: string(f.ID)}, nil
	case engine.StatusNeedsMerge:
		d.predraftLanding(ctx, res.Feature)
		return d.done(ctx, res.Feature)
	case engine.StatusNoop:
		return d.done(ctx, res.Feature)
	case engine.StatusAdvanced:
		if res.EnteredWorktree {
			d.discoverAndBaselineChecks(ctx, res.Feature, res.From)
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

// recordBlocked records a blocked-gate decision (the threads, dependency
// or floor holding a gate open are a stop a person resolves), then hands
// back nothing — the caller's NDJSON emit already carried the detail. It
// also parks the card (ParkReasonNeedsYou): this is a terminal outcome
// for the run, not an escalation, but it still stops and waits on a
// person, so it must close the autopilot period the same as one does.
func (d *Driver) recordBlocked(f domain.Feature, question string) {
	d.logPark(f, state.ParkReasonNeedsYou, question)
	d.openDecision(f, state.DecisionKindGate, question)
}

// dependencyBlockedQuestion names each outstanding dependency in the
// question a dependency block records.
func dependencyBlockedQuestion(deps []engine.BlockingDep) string {
	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		ids = append(ids, string(dep.ID)+" ("+string(dep.Stage)+")")
	}
	return "a dependency is still short of done — wait for " + strings.Join(ids, ", ") + " to land."
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
func (d *Driver) discoverAndBaselineChecks(ctx context.Context, f domain.Feature, from domain.Stage) {
	timeout := d.opts.StageTimeout
	if timeout <= 0 {
		timeout = discoverStageTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Both passes are model-and-shell work that can run for minutes on a
	// large repository, and neither had a line on the stream: a --verbose
	// drive went silent between the plan's verdict and the gate crossing,
	// which reads as a hang and was investigated as one. The stage feed
	// covers sessions the engine owns; these two are the engine's
	// one-shots, so the driver narrates them itself.
	//
	// Both run AT a gate, not in a stage: the card's own stage field has
	// already advanced by the time they start, so reading it labelled the
	// work with the stage that had not begun — and a caller watching for
	// "the plan stage is still going" saw implement instead. The stage
	// crossed FROM is the one whose gate this is, which is what
	// HEADLESS.md documents.
	stage := string(from)
	if stage == "" {
		stage = string(f.Stage)
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: stage, Result: "discovering checks"})
	checks, err := d.eng.DiscoverChecks(ctx, f)
	if err != nil {
		// Best-effort, as it always was — the card crosses and verify's own
		// fallback applies — but not silent. This is the one pass that can
		// spend a fifth of a card and leave nothing behind, and a stream
		// that says "discovering checks" and then nothing about it is the
		// same silence the narration below exists to end.
		d.out.activity(string(f.ID), stage, "check discovery failed: "+err.Error()+
			" — crossing without a discovered checks block")
		return
	}
	// What the survey decided, not just that it ran. This is the most
	// expensive single pass a card makes — 95 to 148 credits on the lxd
	// drive, 14–25% of each card — and the stream said nothing about it
	// either way, so a caller could not tell a good check set from a bad
	// one without opening the artifact afterwards. The checks ARE the
	// definition of green this card is about to be held to.
	for _, c := range checks {
		d.out.activity(string(f.ID), stage, "check discovered: "+c.Name+" — "+c.Cmd)
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: stage, Result: "baselining checks"})
	results, _ := d.eng.BaselineChecks(ctx, f)
	for _, r := range results {
		outcome := "pass"
		if !r.OK {
			outcome = "FAIL"
		}
		d.out.activity(string(f.ID), stage, "baseline "+outcome+": "+r.Name)
	}
}

// predraftLanding composes the card's landing commit message at the
// moment the drive stops on a verified branch, so the person who comes
// back to land it opens a merge dialog that already holds one
// (engine.PredraftCommitMessage).
//
// It is the headless half of a fact the TUI learns at its own verify gate
// (internal/ui/reviewloop.go): a card driven by `gummi run` never passes
// through that gate, so without this the entire autonomous fleet — the
// way most cards reach a verified branch — would be exactly the set of
// cards that still pay the ~60s pass at the keypress, one after another,
// in the close-out ritual where they all come due at once.
//
// It runs where discoverAndBaselineChecks runs, for the same reasons and
// with the same manners: at a gate rather than in a stage, best-effort
// (the drive stops on its verified branch either way), and narrated —
// this is a model pass of a couple of minutes at the very end of a run,
// and a stream that goes silent there reads as a hang.
func (d *Driver) predraftLanding(ctx context.Context, f domain.Feature) {
	// asked before the stream is told anything: a card whose message is
	// written by something else (a goal's, a linked card's) must not be
	// announced as drafting one.
	if !engine.PredraftEligible(f) {
		return
	}
	stage := string(domain.StageVerify)
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: stage, Result: "drafting the landing message"})
	// the verify pass's closing report, while it is still in hand: the one
	// input the merge dialog could never have had.
	if _, err := d.eng.PredraftCommitMessage(ctx, f, verdict.LastAssistant(d.snapshot(f.ID))); err != nil {
		d.out.activity(string(f.ID), stage, "landing-message draft failed: "+err.Error()+
			" — the message is drafted when someone lands the card")
		return
	}
	d.out.activity(string(f.ID), stage, "landing message drafted — the merge dialog opens on it")
}

// --- terminal outcomes -------------------------------------------------

func (d *Driver) done(ctx context.Context, f domain.Feature) (Outcome, error) {
	// reload for the freshest spend/stage.
	if got, err := d.store.GetFeature(ctx, f.ID); err == nil {
		f = got
	}
	if f.InGoal() && f.Stage != domain.StageDone {
		// a goal card's verified branch is its goal's to land, not a stop
		// for a person: no park, and no terminal event a caller could
		// mistake for the goal's own
		d.out.emit(cardVerifiedEvent{Event: "card_verified", ID: string(f.ID), Goal: string(f.GoalID), Branch: f.BranchName(), Spent: f.Spend.Credits})
		return Outcome{Status: StatusVerified, ID: string(f.ID)}, nil
	}
	d.logPark(f, state.ParkReasonNeedsYou, "reached the landing gate — the branch is ready to merge.")
	// The card's own rework total, not this process's: a card driven over
	// four `resume` calls reports each call's critique count in
	// review_rounds, and a caller cannot add those up to anything true.
	corrective, err := rounds.Load(ctx, d.roundStore, f.ID, domain.RoundKindCorrective)
	if err != nil {
		corrective = 0
	}
	ev := verifiedEvent{
		Event: "verified", ID: string(f.ID), Branch: f.BranchName(),
		Spec: f.ArtifactPath(), Spent: f.Spend.Credits, ReviewRounds: d.reviewsRun,
		CorrectiveRounds: corrective,
		UnprovenFiles:    unprovenPaths(f),
		Message:          f.PullRequest.NextStepsHint(true),
		PullRequest:      f.PullRequest.StatusPayload(),
	}
	if f.IsGoal() {
		ev.Goal = d.goalDone(ctx, f)
	}
	d.out.emit(ev)
	return Outcome{Status: StatusVerified, ID: string(f.ID)}, nil
}

// unprovenPaths reads verify's UNPROVEN declarations off the card's
// artifact, for the done receipt. An unreadable artifact reports none.
func unprovenPaths(f domain.Feature) []string {
	raw, err := os.ReadFile(f.ArtifactPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, u := range spec.UnprovenFiles(string(raw)) {
		out = append(out, u.Path)
	}
	return out
}

// decomposeGate runs the FD-081 decompose side-effect off an RS card's
// verify→done crossing (auto-trigger, note "") or a manual
// --request-changes re-run (note carries the operator's feedback). No
// decompose exit — success, question, exhaustion, or a hard error — moves
// the card off done: a failure is swallowed into a decompose_failed
// escalation, and a successful pass only ever checkpoints as a `question`
// for a later --approve, never auto-mints.
func (d *Driver) decomposeGate(ctx context.Context, f domain.Feature, note string) (Outcome, error) {
	if note != "" {
		// the caller sent the previous decompose checkpoint back: its
		// answer closes that decision before the pass re-runs and opens a
		// fresh one.
		d.answerDecomposeDecision(ctx, f, "changes requested: "+note)
	}
	res, err := d.eng.DecomposeForCard(ctx, f.ID, note)
	if err != nil {
		d.out.emit(escalationEvent{
			Event: "decompose_failed", ID: string(f.ID), Stage: string(f.Stage), Reason: err.Error(),
			Resume: string(f.ID), Next: d.resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
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
			Resume: string(f.ID), Next: d.resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
		})
		return Outcome{Status: StatusEscalation, ID: string(f.ID)}, nil
	}
	d.logPark(f, state.ParkReasonNeedsYou, fmt.Sprintf("decompose — %d proposal(s) ready for your review.", len(res.Proposals)))
	d.out.emit(decomposeQuestionEvent{
		Event: "question", ID: string(f.ID),
		Decision:  d.openDecision(f, state.DecisionKindGate, "decompose — review the proposals and mint them, or send the pass back with a note."),
		Proposals: wireDecomposeProposals(res), Coverage: wireDecomposeCoverage(res),
		Resume: string(f.ID), Next: d.resumeCmd(string(f.ID), "--approve"),
	})
	return Outcome{Status: StatusQuestion, ID: string(f.ID)}, nil
}

// answerDecomposeDecision records the caller's answer to the newest open
// decision on a decompose checkpoint: the ask-shaped round trip whose id
// closes that decision (an approve, or a request-changes note). Without
// it the decision would read open forever — decompose never moves the
// card, so no gate crossing would ever close it (R3's closure rule).
func (d *Driver) answerDecomposeDecision(ctx context.Context, f domain.Feature, answer string) {
	open := d.newestOpenDecision(ctx, f.ID)
	if open == nil {
		return
	}
	if d.store == nil {
		return
	}
	payload, err := json.Marshal(state.AskPayload{
		Question: "decompose — review the proposals",
		Answer:   answer,
		Actor:    state.ActorUser, By: state.ActorUser,
		ID: open.ID,
	})
	if err != nil {
		return
	}
	_ = d.store.AppendEvent(context.Background(), state.CardEvent{
		Feature: f.ID, Stage: f.Stage, Kind: state.EventAsk, At: time.Now(),
		Payload: string(payload), Dedupe: "decision:" + open.ID,
	})
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
	// the caller's answer closes the checkpoint's decision whether or not
	// the mint fully landed: the pending file is cleared above either way,
	// and the card never leaves done.
	d.answerDecomposeDecision(ctx, f, fmt.Sprintf("approved — %d minted", len(minted)))
	ids := make([]string, len(minted))
	for i, m := range minted {
		ids[i] = string(m.ID)
	}
	if mintErr != nil {
		d.out.emit(escalationEvent{
			Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: mintErr.Error(),
			MintedIDs: ids, Resume: string(f.ID), Next: d.resumeCmd(string(f.ID), "--request-changes", "'<note>'"),
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
	d.logPark(f, state.ParkReasonNeedsYou, fmt.Sprintf("ran out of its %d-credit budget at %s.", f.Budget.Envelope, f.Stage))
	d.out.emit(exhaustedEvent{
		Event: "exhausted", ID: string(f.ID), Stage: string(f.Stage),
		Spent: f.Spend.Credits, Envelope: f.Budget.Envelope, Committed: committed, Resume: string(f.ID),
		Next:          d.resumeCmd(string(f.ID), "--envelope", fmt.Sprintf("%d", suggested)),
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
// this stage: the stage is parked at a checkpoint with nothing to drive, so
// the fault is caller-side, not the backend. The decision record names the
// verb that actually advances this stop — the static verb list this hint
// used to guess from is what a durable open decision replaces.
func (d *Driver) timeoutHintParked(f domain.Feature) string {
	verb := "--approve (or --request-changes / --answer)"
	if open := d.newestOpenDecision(context.Background(), f.ID); open != nil {
		switch open.Kind {
		case state.DecisionKindAsk:
			verb = "--answer"
		case state.DecisionKindVerify:
			verb = "--bounce (or --approve to overrule)"
		}
	}
	return "the stage is parked at a checkpoint and no turn was sent this stage — advance it with " +
		verb + ", not by a bare resume, which has nothing to drive here"
}

func (d *Driver) timeout(f domain.Feature) Outcome {
	hint := timeoutHintStalled
	if !d.sentTurn {
		hint = d.timeoutHintParked(f)
	}
	used := ""
	if d.opts.StageTimeout > 0 {
		used = d.opts.StageTimeout.String()
	}
	d.logPark(f, state.ParkReasonNeedsYou, hint)
	d.out.emit(timeoutEvent{
		Event: "timeout", ID: string(f.ID), Stage: string(f.Stage), Hint: hint,
		StageTimeoutUsed: used,
		Resume:           string(f.ID),
		Next:             d.resumeCmd(string(f.ID)),
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

// resumeCmd is resumeCmd with this run's own driving mode carried into it.
//
// `--gate-approval` is persisted on the card, so a resume inherits it
// whether or not the command says so. `--autonomous` is not: it is a
// per-invocation flag the driver holds right here in d.opts, and leaving
// it out of the suggested command hands the caller a resume that is
// strictly less autonomous than the run it continues — one that crosses
// its own gates but parks (exit 2) on the first question this run would
// have answered itself. A CI loop that does what the stream tells it
// would change its own card's driving mode halfway through, which is
// exactly what the `next` field exists to stop happening.
//
// It goes directly after the id so a free-form placeholder an operator
// has to edit (`--answer "<answer>"`, `--note "<why>"`) stays last.
func (d *Driver) resumeCmd(id string, args ...string) string {
	if d != nil && d.opts.Autonomous {
		args = append([]string{"--autonomous"}, args...)
	}
	return resumeCmd(id, args...)
}

// stopped is the --until early-stop terminal: a clean, deliberate halt at a
// design boundary (the feature stays parked at f.Stage, resumable). It exits
// 0 — not an escalation — so a caller distinguishes it from `done` by the
// event name, not the exit code.
func (d *Driver) stopped(f domain.Feature) Outcome {
	d.logPark(f, state.ParkReasonNeedsYou, "stopped early at --until "+string(f.Stage)+", as requested.")
	d.out.emit(stoppedEvent{
		Event: "stopped", ID: string(f.ID), Stage: string(f.Stage), Resume: string(f.ID),
		Next: d.resumeCmd(string(f.ID), "--approve"),
	})
	return Outcome{Status: StatusStopped, ID: string(f.ID)}
}

// logPark records a card stopping in its own history, so a run driven
// headlessly leaves the same account of why it stopped as one driven
// from the board. Best-effort and silent: the escalation itself is
// already on the output stream, and the run's exit status does not
// depend on the log.
func (d *Driver) logPark(f domain.Feature, reason, detail string) {
	if d.store == nil {
		return
	}
	_ = d.store.AppendPark(context.Background(), f.ID, f.Stage, reason, detail, "", time.Now())
}

// logTookOver records the unattended loop taking a card over, so a card
// driven by `run`/`resume` leaves the same opening half of a thread
// stretch that pressing the TUI's autopilot switch does — the closing
// half is already covered: whenever this run parks the card (logPark,
// above), the reader treats that park as the stretch's close, and this
// package never writes a handed-back row itself.
//
// Gated on d.actor == "auto" and nothing else: that field is exactly the
// "is a human or script waiting on every gate" distinction (driver.go's
// setGate), and AutopilotPayload exists precisely so this history never
// claims the machine acted on its own when a caller was really sitting
// at each gate — see its doc comment in state/cardevents.go. A
// --gate-approval=off run (d.actor == "caller") must never reach
// AppendAutopilot from here.
//
// Best-effort and silent, like logPark beside it: the run's own output
// stream and exit status already say what happened, so a failed history
// write must not unwind or block the drive (internal/ui/shell.go's
// logPark carries the same reasoning for the TUI side).
//
// No dedupe key, deliberately, and the asymmetry is the argument. The
// caller's own tookOver flag already holds this to one row per call, so
// the only rows a key could collapse are those of two separate calls —
// and a key narrow enough to catch a crash-and-resume on one stage is
// necessarily wide enough to swallow two genuinely separate runs that
// begin at that same stage, which is the ordinary shape of a card
// bounced back to implement twice.
//
// The two mistakes are not equally bad, because the reader is not
// symmetric about them. A duplicate costs nothing: a took-over arriving
// while a stretch is already open is ignored, since one uninterrupted
// period of being driven is one stretch however many times the process
// restarted inside it. A dropped row costs a whole stretch — with no
// opening there is nothing to open one, and everything that period did
// falls back to rendering as though no one had ever handed the card
// over. So this writes every time and lets the reader collapse them.
func (d *Driver) logTookOver(f domain.Feature) {
	if d.store == nil || d.actor != "auto" {
		return
	}
	reason := "the headless run is driving it unattended, with no caller waiting on its gates"
	_ = d.store.AppendAutopilot(context.Background(), f.ID, f.Stage,
		state.AutopilotTookOver, reason, d.opts.GateApproval, "", time.Now())
}

// escalationDecisionKind maps an escalation's stage to the decision kind
// its stop records: a failed verify escalates as the verify decision it
// is; every other give-up is a gate the human judges.
func escalationDecisionKind(f domain.Feature) string {
	if f.Stage == domain.StageVerify {
		return state.DecisionKindVerify
	}
	return state.DecisionKindGate
}

func (d *Driver) escalation(f domain.Feature, reason string) Outcome {
	d.logPark(f, state.ParkReasonGaveUp, reason)
	d.openDecision(f, escalationDecisionKind(f), reason)
	d.out.emit(escalationEvent{
		Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID),
		Next: d.resumeCmd(string(f.ID)),
	})
	return Outcome{Status: StatusEscalation, ID: string(f.ID)}
}

// blockedEscalation is an escalation whose cause is the environment: the
// park carries state.ParkReasonBlocked, the one value a goal reads to tell
// a card to wait for from a card to give up on.
func (d *Driver) blockedEscalation(f domain.Feature, reason string) Outcome {
	d.logPark(f, state.ParkReasonBlocked, reason)
	d.openDecision(f, escalationDecisionKind(f), reason)
	d.out.emit(escalationEvent{
		Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID),
		Next: d.resumeCmd(string(f.ID)),
	})
	return Outcome{Status: StatusEscalation, ID: string(f.ID)}
}

// bounceEscalation is the escalation flavor used when the human's follow-up
// is to rewind review/verify back to implement/fix — a review cap-hit or a
// verify-fail. The `next` field names `--bounce` so a caller driving the
// stream never has to recall which verb un-parks this stop; `--note` is
// carried as a placeholder for the caller's own change note.
func (d *Driver) bounceEscalation(f domain.Feature, reason string) Outcome {
	d.logPark(f, state.ParkReasonGaveUp, reason)
	d.openDecision(f, escalationDecisionKind(f), reason)
	d.out.emit(escalationEvent{
		Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID),
		Next: d.resumeCmd(string(f.ID), "--bounce", "--note", `"<why>"`),
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
			ev.Resumable = !workflow.Terminal(f.Stage)
			ev.Stage = string(f.Stage)
			if ev.Resumable {
				ev.Next = d.resumeCmd(id)
			}
			// Close the card's autopilot period with the reason, as every
			// other terminal does (recordBlocked, escalation, exhausted,
			// done). Without it the log has a period that was never closed,
			// and the board reads that as a driving process that vanished:
			// it drew "autopilot stopped without saying so" directly under
			// the transcript line where the run had said, in as many words,
			// "claude run failed: You've hit your session limit".
			d.logPark(f, state.ParkReasonGaveUp, err.Error())
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
func (d *Driver) createFeature(ctx context.Context, ct domain.CardType, desc string) (domain.Feature, error) {
	repo := d.opts.Repo
	if ct.Kind == domain.KindGoal && repo == "" {
		// A goal names no repository — its cards do, and its plan gate
		// settles its own home from them. Until then its branch has to be
		// cut somewhere, and in a `repos:`-only workspace there is no
		// default to cut it in.
		repo = d.eng.ProvisionalRepo()
	}
	return cardmint.Mint(ctx, d.store, d.ws, cardmint.Input{
		Kind: ct.Kind, Mode: ct.Mode, Description: desc, Profile: d.opts.Profile, Envelope: d.opts.Envelope,
		Repo: repo, RequireRepo: d.eng.RequireRepo, Base: d.opts.Base,
		ExternalRef: d.opts.Ref, Acceptance: d.opts.Acceptance, GateApproval: d.opts.GateApproval,
		GoalDoc: d.opts.GoalDoc,
	})
}

// enterStage resets per-stage state. The activity cursor is reset here
// for the stage boundary and again by emitActivity whenever the session
// behind the feed changes, which is the finer of the two grains.
func (d *Driver) enterStage(stage domain.Stage) {
	d.curStage = stage
	d.activityCur = 0
	d.activitySession = time.Time{}
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

// newestOpenDecision returns the card's newest still-open durable
// decision, or nil. The durable record is what a resume reasons from:
// it survives the process, where the in-memory pending ask does not.
func (d *Driver) newestOpenDecision(ctx context.Context, id domain.FeatureID) *state.OpenDecision {
	opens, err := d.store.OpenDecisions(ctx)
	if err != nil {
		return nil
	}
	list := opens[id]
	if len(list) == 0 {
		return nil
	}
	last := list[len(list)-1]
	return &state.OpenDecision{
		ID: last.ID, Kind: last.Kind, Question: last.Question,
		Stage: last.Stage, At: last.At,
	}
}

// openDecision records the decision a checkpoint just raised — the one
// row §10.18 requires for every stop, the same seam the TUI's review
// loop raises through. Best-effort like the park it sits beside: the
// stream already told the caller the card stopped, and a log failure
// must never unwind a checkpoint that was already announced.
func (d *Driver) openDecision(f domain.Feature, kind, question string) string {
	id := kind + ":" + string(f.ID) + ":" + string(f.Stage) + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if d.store == nil {
		return id
	}
	_ = d.store.OpenDecision(context.Background(), f.ID, f.Stage, state.DecisionPayload{
		ID: id, Kind: kind, Question: question,
	}, time.Now())
	return id
}

// emitResult emits a stage result line (verify pass/fail, review pass/
// changes) — the semantic milestone at a verdict boundary.
func (d *Driver) emitResult(f domain.Feature, v verdict.Verdict) {
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: v.String()})
}

// UntilStops lists the stages --until may name for an item's route: the
// UntilStops are the stages `--until` may name: the design gate, the one
// deliberate pre-implementation boundary where stopping is meaningful
// and unambiguous.
//
// It used to be a per-kind, per-skip list because each workflow had two
// or three design stages and a skipped one was not on the route. There
// is one design stage now and nothing to skip, so there is one stop.
func UntilStops() []domain.Stage {
	return []domain.Stage{domain.StagePlan}
}

func ValidateUntil(until domain.Stage) error {
	if until == "" {
		return nil
	}
	stops := UntilStops()
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
	nexts := workflow.Next(f.Stage)
	if len(nexts) == 0 {
		return f.Stage
	}
	// nexts[0]: forward edges are listed before rerun edges, and this
	// names the forward one. See Engine.nextStage for why it is no longer
	// the last entry.
	return nexts[0]
}

// firstErr returns a if non-nil, else fallback — for turning an optional
// error detail into a guaranteed non-nil error.
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
)

type stageEnd struct {
	kind      endKind
	err       error
	committed bool // endExhausted: the stage's work was committed, not stranded
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
		case ev, ok := <-d.stream():
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
//
// The cursor is rebased whenever the feed belongs to a different session
// than the one it was last advanced against. Activity is per session, not
// per stage, and a stage runs several in sequence, so a cursor that only
// reset at the stage boundary sat past the end of every subsequent
// session's feed and emitted nothing for it.
func (d *Driver) emitActivity(id domain.FeatureID) {
	if !d.out.verbose {
		return
	}
	snap := d.snapshot(id)
	act := snap.Activity
	if snap.StartedAt != d.activitySession {
		d.activitySession = snap.StartedAt
		d.activityCur = 0
		// The first feed a resume looks at is the card's history: the
		// stage it is picking up carries the activity of the run that
		// parked it, seeded back onto the session from the store. Those
		// lines were streamed once already, by the process that produced
		// them. Start at the end of them and emit only what happens from
		// here.
		//
		// Replaying them meant every `resume` re-emitted the card's whole
		// activity history before adding anything new: an agent driving
		// gummi through several stops re-read what it had already consumed
		// on each one, and could tell old from new only by remembering how
		// many lines it saw last time.
		if d.resumePrimed {
			d.resumePrimed = false
			d.activityCur = len(act)
			return
		}
	}
	for i := d.activityCur; i < len(act); i++ {
		d.out.activity(string(id), string(d.curStage), act[i])
	}
	if len(act) > d.activityCur {
		d.activityCur = len(act)
	}
}

// resumeFinishedVerify crosses the gate on a verify verdict this card has
// already earned, instead of paying for it twice.
//
// resumeCritiqueLoop is the only path that consults a stored verdict on
// resume, and it is keyed on CritiqueRoundKind, which knows plan and
// implement — not verify. So a resume at verify fell straight through to
// a fresh session. Verify is the last stage and among the most expensive
// (117.7 credits on the lxd autopilot drive's case A, more than its plan
// architect), which makes it exactly where a card is most likely to run
// dry: that card's verify returned "pass — all gummi-checks pass, all 5
// plan invariants confirmed, an independent 1853-input fuzz comparison
// found 0 regressions", ran out four seconds later, and the 60-credit
// top-up bought a second verify from scratch, which ran out too. 123.8 of
// its 241.5 verify credits — 51% — went on a verdict already in the store.
//
// A recorded verdict IS the stage's output, so the budget stop that landed
// after it does not invalidate it: exhausted with a verdict is finished
// work that stopped being paid for. Exhausted WITHOUT one has nothing to
// honour and re-runs, as before.
func (d *Driver) resumeFinishedVerify(ctx context.Context, f domain.Feature) (Outcome, bool, error) {
	if f.Stage != domain.StageVerify {
		return Outcome{}, false, nil
	}
	snap := d.snapshot(f.ID)
	if snap.Feature.Stage != domain.StageVerify || snap.State != engine.StateDone {
		return Outcome{}, false, nil
	}
	switch v := verdict.SessionVerdict(snap); {
	case v == verdict.Unclear:
		return Outcome{}, false, nil // nothing was decided: verify again
	case v == verdict.Blocked && verdict.BlockedByEnvironment(snap):
		// Nothing was decided here either. A pass or a fail is a verdict
		// the card earned; "the environment cannot run this" is a fact
		// about a moment, and the only reason anyone resumes a card that
		// stopped on it is that the moment has passed. Honouring it made
		// the resume a way to be told the same thing again for free.
		return Outcome{}, false, nil
	}
	out, err := d.applyVerdict(ctx, f)
	return out, true, err
}

// resumeCritiqueLoop puts a resume back where the stage's loop actually
// stopped, rather than restarting the stage's writer from the top.
//
// handled is false when there is nothing to resume — no restored session
// for this stage, or a paused writer that died before producing anything
// to critique — and the caller starts the stage normally.
//
// Shared by both drive paths: the design stage converses and the work
// stage does not, but both end with a critique, and a resume must land in
// the same place either way.
func (d *Driver) resumeCritiqueLoop(ctx context.Context, f domain.Feature) (Outcome, bool, error) {
	kind, ok := engine.CritiqueRoundKind(f.Stage)
	if !ok {
		return Outcome{}, false, nil
	}
	{
		if snap := d.snapshot(f.ID); snap.Feature.Stage == f.Stage {
			// A resume that was bought to finish the work must not spend
			// itself judging work that was never written.
			//
			// Two facts have to line up for that to be the case, and both
			// are recorded: the stage stopped because the envelope ran out
			// (snap.Exhausted — the budget stop and a clean completion both
			// save as StateDone, so nothing else tells them apart), and the
			// section its forward edge owes is still blank. That is exactly
			// the lxd autopilot drive's research card: its survey ran out
			// with Findings still the seeded placeholder, the loop moved on
			// to critique it, and the 500 credits the top-up added went to
			// critiquing and verifying a document nobody had written.
			//
			// Both conditions matter. A writer that ran out having already
			// produced its output is finished work that merely stopped
			// being paid for — it is critiqued, as before — and a stage
			// that owes no section (a feature's implement edge) never
			// takes this path at all.
			if snap.Exhausted && !snap.Critique && len(d.eng.UndraftedBlocking(f)) > 0 {
				return Outcome{}, false, nil // the caller restarts the writer
			}
			if snap.State == engine.StateDone && !snap.Critique {
				// the revised plan is on disk: critique it, using the
				// re-critique kickoff when a prior round was burned.
				kickoff := ""
				result := "critiquing"
				if d.round(f.ID, kind) > 0 {
					kickoff = verdict.ReCritiqueNote
					result = "re-critiquing"
				}
				if err := d.dispatchCritique(f, kickoff); err != nil {
					return Outcome{}, true, err
				}
				d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: result})
				out, err := d.awaitCritique(ctx, f)
				return out, true, err
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
					if err := d.dispatchCritique(f, verdict.ReCritiqueNote); err != nil {
						return Outcome{}, true, err
					}
					d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "re-critiquing"})
					out, err := d.awaitCritique(ctx, f)
					return out, true, err
				}
				out, err := d.judgeCritique(ctx, f, snap)
				return out, true, err
			}
			if snap.State == engine.StatePaused && snap.Critique {
				// the critique session died mid-turn and was restored
				// paused: re-dispatch it instead of awaiting a pass that
				// already ended (the bug this guards against: awaiting
				// forever burns the whole --stage-timeout with nothing
				// dispatched). Mirrors the TUI's StatePaused+Critique
				// branch (internal/ui/shell.go:1279-1286).
				if err := d.dispatchCritique(f, ""); err != nil {
					return Outcome{}, true, err
				}
				d.sentTurn = true
				d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "resuming " + string(f.Stage) + " critique"})
				out, err := d.awaitCritique(ctx, f)
				return out, true, err
			}
			if snap.State != engine.StatePaused {
				// still in flight (running/queued): keep awaiting the
				// running pass, spawn nothing.
				out, err := d.awaitCritique(ctx, f)
				return out, true, err
			}
			// a restored paused writer (!Critique): the writer died
			// mid-turn before producing a plan to critique. Fall through
			// to the fresh-writer dispatch below — mirrors the TUI's
			// paused/non-critique fallthrough to engine.Run
			// (internal/ui/shell.go:1279-1296).
		}
	}
	return Outcome{}, false, nil
}

// say reads a line the way the card page's composer would and reports
// the reading without acting on it.
//
// What "go on" may mean is read off the card the way the TUI reads it
// off the answer set — from the stop the card is parked at: a gate or a
// verify checkpoint offers the crossing, anything else re-runs the
// parked stage, and an open spec or diff thread (or an unmet
// dependency) blocks the crossing outright. No reader configured is not
// an error: the event says so, and the act reported is the one a bare
// resume would take.
func (d *Driver) say(ctx context.Context, f domain.Feature, line string) (Outcome, error) {
	in := reentry.Input{Stage: f.Stage, Kind: f.Kind, Note: line}
	forwardLabel := ""
	if open := d.newestOpenDecision(ctx, f.ID); open != nil && (open.Kind == state.DecisionKindGate || open.Kind == state.DecisionKindVerify) {
		if next := workflow.Next(f.Stage); len(next) > 0 {
			in.Forward = next[0]
			forwardLabel = "advance to " + string(next[0])
			if next[0] == domain.StageDone {
				forwardLabel = "land on main"
			}
		}
	} else if !workflow.Terminal(f.Stage) {
		in.Rerun = true
		forwardLabel = "run " + string(f.Stage)
	}
	if specOpen, diffOpen, deps, err := d.eng.GateBlockers(ctx, f.ID); err == nil {
		switch {
		case specOpen > 0:
			in.Blocked = "open comments"
		case diffOpen > 0:
			in.Blocked = "open diff comments"
		case len(deps) > 0:
			in.Blocked = "unmet dependencies"
		}
	}
	ev := sayEvent{Event: "say", ID: string(f.ID), Line: line, Reader: true}
	intent, err := d.eng.ClassifyReentry(ctx, f, line, forwardLabel)
	switch {
	case errors.Is(err, engine.ErrNoScribe):
		ev.Reader = false
	case err != nil:
		return d.fail(ctx, string(f.ID), err)
	}
	in.Intent = intent
	out := reentry.Decide(in)
	ev.Intent = string(intent)
	ev.Action = out.Action.String()
	ev.Target = string(out.Target)
	for _, st := range out.Path {
		ev.Path = append(ev.Path, string(st))
	}
	if !out.Edit.Empty() {
		ev.Edit = &sayEdit{Section: out.Edit.Section, Text: out.Edit.Text}
	}
	ev.Confirms = out.Confirm
	ev.Reason = out.Reason
	d.out.emit(ev)
	return Outcome{Status: StatusSaid, ID: string(f.ID)}, nil
}
