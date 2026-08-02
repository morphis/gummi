package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
)

// GateApproval selects who approves a design gate. The quality floor
// (review/verify, blockers) runs the same either way — this only decides
// whether a design gate auto-crosses or checkpoints to the caller.
const (
	GateAuto   = "auto"
	GateCaller = "caller"
)

// Options configures one run. Envelope is required (D6); a missing agent
// or envelope fails loud before any work starts.
type Options struct {
	Envelope     int
	Profile      string
	Full         bool          // opt into the brainstorm+plan route (default: quick)
	GateApproval string        // GateAuto (default) | GateCaller
	StageTimeout time.Duration // per-stage inactivity budget (0 disables)
	Autonomous   bool          // auto-take the recommended answer instead of checkpointing (D5)
	Verbose      bool          // add per-tool-call activity lines to the stream
	Ref          string        // optional external correlation id, echoed in NDJSON + persisted as ExternalRef (D11)
	Acceptance   string        // optional acceptance-criteria text, seeded into the draft's Verification plan (D10)
	Until        domain.Stage  // stop cleanly before crossing the gate that leaves this design stage (B3); "" runs to verified
}

// Driver runs one feature through the engine's gate floor headlessly. It
// is the sole consumer of engine.Events() for its process — one feature
// per run (D2/D7) — so it drains the stream continuously and acts
// synchronously between reads, the same contract the TUI's update loop
// relies on.
type Driver struct {
	eng   *engine.Engine
	store *state.Store
	ws    state.Workspace
	out   *emitter
	opts  Options
	actor string // transition actor recorded in history ("auto" | "caller")

	// loop state for the single feature this process governs.
	reviewRounds int          // automatic review→fix rounds burned in the current loop
	planRounds   int          // automatic plan→critique rounds burned
	reviewsRun   int          // review stages entered so far (for the done receipt)
	opening      string       // one-shot message to send on the next interactive stage (resume answer / change note)
	curStage     domain.Stage // stage currently being driven (for verbose activity lines)
	activityCur  int          // cursor into the live session's activity feed
}

// New builds a driver writing its NDJSON stream to out. The caller owns
// the engine and agent lifetime (as cmd/gummi/ingest.go does).
func New(eng *engine.Engine, store *state.Store, ws state.Workspace, out interface{ Write([]byte) (int, error) }, opts Options) *Driver {
	if opts.GateApproval == "" {
		opts.GateApproval = GateAuto
	}
	actor := "auto"
	if opts.GateApproval == GateCaller {
		actor = "caller"
	}
	return &Driver{
		eng:   eng,
		store: store,
		ws:    ws,
		out:   newEmitter(out, opts.Verbose),
		opts:  opts,
		actor: actor,
	}
}

// Run creates one feature from a free-form description and drives it to a
// terminal Outcome. The quick route is the default; --full opts into the
// brainstorm+plan route (D3).
func (d *Driver) Run(ctx context.Context, desc string) (Outcome, error) {
	// validate --until against the route this run will take before minting a
	// feature, so a bad stop target never leaves a stray FD in the backlog.
	skip := domain.QuickRoute()
	if d.opts.Full {
		skip = domain.SkipFlags{}
	}
	if err := ValidateUntil(d.opts.Until, domain.KindFeature, skip); err != nil {
		return d.fail("", err)
	}
	f, err := d.createFeature(ctx, desc)
	if err != nil {
		return d.fail("", err)
	}
	route := "quick"
	if d.opts.Full {
		route = "full"
	}
	d.out.emit(createdEvent{
		Event: "created", ID: string(f.ID), Ref: d.opts.Ref,
		Branch: f.BranchName(), Route: route, Envelope: d.opts.Envelope,
	})
	return d.drive(ctx, f.ID)
}

// ResumeInput carries a resume's decision. Exactly one field is set:
// Answer resolves a delegated ask_user; Approve/RequestChanges resolve a
// caller design gate; all-zero re-runs the parked stage (after an
// exhaustion top-up, a timeout, or an escalation).
type ResumeInput struct {
	Answer         *string
	Approve        bool
	RequestChanges *string
}

// Resume rehydrates the engine's persisted sessions, applies the caller's
// decision, and drives on. gummi's restartability (SQLite state, spec on
// branch, session resume) makes this free (DESIGN §4).
func (d *Driver) Resume(ctx context.Context, id domain.FeatureID, in ResumeInput) (Outcome, error) {
	if err := d.eng.Restore(ctx); err != nil {
		return d.fail(string(id), fmt.Errorf("restoring sessions: %w", err))
	}
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		return d.fail(string(id), err)
	}
	if err := ValidateUntil(d.opts.Until, f.Kind, f.Skip); err != nil {
		return d.fail(string(id), err)
	}
	// the correlation line first, so a resume's stream is self-identifying.
	d.out.emit(resumedEvent{Event: "resumed", ID: string(id), Ref: d.opts.Ref, Stage: string(f.Stage)})

	switch {
	case in.Approve:
		// an explicit approval crosses the gate now, regardless of the
		// process's gate-approval mode — the caller already decided.
		out, err := d.autoAdvance(ctx, f)
		if err != nil {
			return d.fail(string(id), err)
		}
		if out.terminal() {
			return out, nil
		}
	case in.RequestChanges != nil:
		d.opening = *in.RequestChanges
	case in.Answer != nil:
		d.opening = *in.Answer
	}
	return d.drive(ctx, id)
}

// drive is the checkpoint loop: it advances the feature stage by stage
// until it reaches a terminal Outcome (a decision the caller must make,
// or a verified branch). Autonomous stretches carry no caller decisions
// under --gate-approval=auto, so one call streams the whole tail and
// returns only at done or an escalation.
func (d *Driver) drive(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	for {
		if err := ctx.Err(); err != nil {
			return d.fail(string(id), err)
		}
		f, err := d.store.GetFeature(ctx, id)
		if err != nil {
			return d.fail(string(id), err)
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
			return d.fail(string(id), err)
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

	if _, err := d.eng.Attach(ctx, f); err != nil {
		return Outcome{}, err
	}
	// a resume seeds the answer / change note as the opening turn; a fresh
	// attach relies on the engine's own stage kickoff.
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
		round = d.reviewsRun
	case domain.StageImplement, domain.StageFix:
		round = d.reviewRounds
	}
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Round: round})

	if err := d.eng.Run(f); err != nil {
		return Outcome{}, err
	}
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

// applyVerdict routes a finished autonomous stage per the loop rules
// (mirrors internal/ui/reviewloop.go), returning a terminal Outcome or a
// non-terminal one (the stage advanced in-floor; drive loops).
func (d *Driver) applyVerdict(ctx context.Context, f domain.Feature) (Outcome, error) {
	snap := d.snapshot(f.ID)
	switch f.Stage {
	case domain.StageReview:
		v := sessionVerdict(snap)
		d.emitResult(f, v)
		switch v {
		case verdictPass:
			d.reviewRounds = 0
			return d.stepTo(ctx, f.ID, domain.StageVerify)
		case verdictChanges:
			if d.reviewRounds >= maxReviewRounds {
				d.reviewRounds = 0
				return d.escalation(f, fmt.Sprintf("review still requesting changes after %d rounds", maxReviewRounds)), nil
			}
			d.reviewRounds++
			return d.stepTo(ctx, f.ID, workflow.WorkStage(f.Kind))
		default:
			d.reviewRounds = 0
			return d.escalation(f, "review finished with no clear verdict"), nil
		}

	case domain.StageVerify:
		v := sessionVerdict(snap)
		d.emitResult(f, v)
		switch v {
		case verdictPass:
			// stop at the verified branch: Advance reports NeedsMerge (branch
			// ahead) or transitions to Done (nothing to land). Never merges.
			return d.crossGate(ctx, f)
		case verdictBlocked:
			return d.escalation(f, "verify BLOCKED — the environment cannot run the verification plan; see the artifact"), nil
		case verdictFail, verdictChanges:
			return d.escalation(f, "verify FAILED — read the evidence in the artifact"), nil
		default:
			return d.escalation(f, "verify finished with no clear verdict"), nil
		}

	case domain.StageImplement, domain.StageFix:
		// the implementation floor always continues into review — review is
		// mandatory and never skipped; the driver never waits for a human here.
		return d.stepTo(ctx, f.ID, domain.StageReview)

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
	default:
		return d.judgePlanCritique(ctx, f, d.snapshot(f.ID))
	}
}

// judgePlanCritique applies the critique verdict: pass crosses the plan
// approval gate, changes replan under the cap, else escalate.
func (d *Driver) judgePlanCritique(ctx context.Context, f domain.Feature, snap engine.Snapshot) (Outcome, error) {
	v := sessionVerdict(snap)
	d.emitResult(f, v)
	switch v {
	case verdictPass:
		d.planRounds = 0
		return d.crossGate(ctx, f)
	case verdictChanges:
		if d.planRounds >= maxPlanRounds {
			d.planRounds = 0
			return d.escalation(f, fmt.Sprintf("plan critique still requesting changes after %d rounds", maxPlanRounds)), nil
		}
		d.planRounds++
		if err := d.eng.RunWith(f, replanNote); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "replanning", Round: d.planRounds})
		return d.awaitReplan(ctx, f)
	default:
		d.planRounds = 0
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
	default:
		// re-critique the revised plan (mirrors reCritiqueNote intent).
		if err := d.eng.RunCritique(f, reCritiqueNote); err != nil {
			return Outcome{}, err
		}
		d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: "re-critiquing"})
		return d.awaitPlanCritique(ctx, f)
	}
}

// replanNote / reCritiqueNote mirror internal/ui/reviewloop.go: the
// critique's findings live in the spec (single source of truth), so the
// architect is pointed at the threads rather than handed a copy.
const replanNote = "The plan critique found issues. Address each open `%% @reviewer:` " +
	"thread in the spec: revise the plan in Implementation notes accordingly and " +
	"mark each thread resolved with a line like `%% @architect: resolved — <how>`."

const reCritiqueNote = "This is a re-critique: a prior round's findings were addressed " +
	"and the plan revised. Start from the resolved `%% @reviewer:` threads and verify " +
	"each resolution against the revised plan — reopen a thread only if its resolution " +
	"does not hold. Raise a new finding only if it is blocking."

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
		specOpen, diffOpen, err := d.eng.GateBlockers(ctx, f.ID)
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
		next := forwardEdge(f)
		d.out.emit(gatePendingEvent{Event: "gate", ID: string(f.ID), From: string(f.Stage), To: string(next), Resume: string(f.ID)})
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
	case engine.StatusNeedsMerge:
		return d.done(ctx, res.Feature)
	case engine.StatusNoop:
		return d.done(ctx, res.Feature)
	case engine.StatusAdvanced:
		if res.To == domain.StageDone {
			return d.done(ctx, res.Feature)
		}
		// the todo→first-stage kickoff is "start", not an approval, so it
		// emits no gate milestone; every real gate does.
		if res.From != domain.StageTodo {
			d.out.emit(gateEvent{Event: "gate", ID: string(f.ID), From: string(res.From), To: string(res.To), Decision: "auto-approved"})
		}
		return Outcome{}, nil // non-terminal: drive loops into res.To
	default:
		return d.escalation(f, "unexpected gate status"), nil
	}
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
	})
	return Outcome{Status: StatusDone, ID: string(f.ID)}, nil
}

func (d *Driver) exhausted(ctx context.Context, f domain.Feature, committed bool) Outcome {
	if got, err := d.store.GetFeature(ctx, f.ID); err == nil {
		f = got
	}
	d.out.emit(exhaustedEvent{
		Event: "exhausted", ID: string(f.ID), Stage: string(f.Stage),
		Spent: f.Spend.Credits, Envelope: f.Budget.Envelope, Committed: committed, Resume: string(f.ID),
	})
	return Outcome{Status: StatusExhausted, ID: string(f.ID)}
}

func (d *Driver) timeout(f domain.Feature) Outcome {
	d.out.emit(timeoutEvent{Event: "timeout", ID: string(f.ID), Stage: string(f.Stage), Resume: string(f.ID)})
	return Outcome{Status: StatusTimeout, ID: string(f.ID)}
}

// stopped is the --until early-stop terminal: a clean, deliberate halt at a
// design boundary (the feature stays parked at f.Stage, resumable). It exits
// 0 — not an escalation — so a caller distinguishes it from `done` by the
// event name, not the exit code.
func (d *Driver) stopped(f domain.Feature) Outcome {
	d.out.emit(stoppedEvent{Event: "stopped", ID: string(f.ID), Stage: string(f.Stage), Resume: string(f.ID)})
	return Outcome{Status: StatusStopped, ID: string(f.ID)}
}

func (d *Driver) escalation(f domain.Feature, reason string) Outcome {
	d.out.emit(escalationEvent{Event: "escalation", ID: string(f.ID), Stage: string(f.Stage), Reason: reason, Resume: string(f.ID)})
	return Outcome{Status: StatusEscalation, ID: string(f.ID)}
}

// fail emits the error line and returns the StatusError outcome plus the
// error, so the CLI can also log it to stderr.
func (d *Driver) fail(id string, err error) (Outcome, error) {
	d.out.emit(errorEvent{Event: "error", ID: id, Error: err.Error()})
	return Outcome{Status: StatusError, ID: id}, err
}

// --- helpers -----------------------------------------------------------

// createFeature mints and persists a new feature, seeding the spec draft
// from the description's overflow (mirrors ui/msgs.go:createFeature). The
// quick route is default; --full keeps brainstorm+plan.
func (d *Driver) createFeature(ctx context.Context, desc string) (domain.Feature, error) {
	title, oneLiner, seed := domain.SplitFreeform(desc)
	slug, err := domain.Slugify(title)
	if err != nil {
		return domain.Feature{}, err
	}
	num, err := d.store.MintFeatureNum(ctx, d.ws.SeqFile())
	if err != nil {
		return domain.Feature{}, err
	}
	id, err := domain.NewFeatureID(num)
	if err != nil {
		return domain.Feature{}, err
	}
	skip := domain.QuickRoute()
	if d.opts.Full {
		skip = domain.SkipFlags{}
	}
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFeature, Title: title, OneLiner: oneLiner,
		Slug: slug, Stage: workflow.Initial(domain.KindFeature), Skip: skip,
		Profile: d.opts.Profile, Budget: domain.Budget{Envelope: d.opts.Envelope},
		ExternalRef: d.opts.Ref, CreatedAt: now, UpdatedAt: now,
	}
	// seed the draft before persisting: the description's overflow fills the
	// Problem section (a title-sized description seeds nothing there), and
	// --acceptance fills the Verification plan (D10). Either input alone is
	// enough to warrant a draft; both are just a pre-fill the spec agent
	// still owns and approves.
	if seed != "" || d.opts.Acceptance != "" {
		draft := filepath.Join(d.ws.DraftsDir(), spec.DraftFilename(&f))
		content := spec.SeededTemplate(&f, domain.DraftSeed{Problem: seed, Acceptance: d.opts.Acceptance}, domain.DraftProvenance{})
		if err := os.MkdirAll(d.ws.DraftsDir(), 0o750); err != nil {
			return domain.Feature{}, err
		}
		if err := atomicfile.Write(draft, []byte(content), 0o600); err != nil {
			return domain.Feature{}, err
		}
	}
	if err := d.store.CreateFeature(ctx, &f); err != nil {
		return domain.Feature{}, err
	}
	return f, nil
}

// enterStage resets per-stage state (the verbose activity cursor points
// into a session's own feed, which is fresh each stage).
func (d *Driver) enterStage(stage domain.Stage) {
	d.curStage = stage
	d.activityCur = 0
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
func (d *Driver) emitResult(f domain.Feature, v verdict) {
	d.out.emit(stageEvent{Event: "stage", ID: string(f.ID), Stage: string(f.Stage), Result: verdictString(v)})
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

func verdictString(v verdict) string {
	switch v {
	case verdictPass:
		return "pass"
	case verdictChanges:
		return "changes"
	case verdictFail:
		return "fail"
	case verdictBlocked:
		return "blocked"
	default:
		return "unclear"
	}
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
			case engine.EventUpdated, engine.EventMessage, engine.EventAnnotations:
				d.emitActivity(id)
			default:
				// Started, Budget, Stopped: not a decision boundary.
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
