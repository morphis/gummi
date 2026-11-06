// Package driver is gummi's unattended session loop, behind `gummi run`:
// one session (a freeform card) minted from a prompt and kept moving until
// it is done, with no person at the keyboard — for evals and CI.
//
// The person who types `gummi run` is the one who allows what the session
// does, once, up front: its workflow runs are always allowed (DESIGN
// §25.11), the envelope their only cap. A question that would wait on a
// person is answered on their behalf, and the answer is put on the stream:
// the session's or a step's ask_user takes its recommended option (the one
// RecommendedOption picks, the board's own rule), or is told to decide for
// itself when it offered none; a parked run is resumed; a repeat at its max,
// or one that cannot judge its condition, carries on past the loop; a
// revised plan is declined, keeping the plan the run was allowed with.
//
// Money is the one thing never decided here: the envelope running dry, or
// a run paused on a spent pool, stops the drive as exhausted and leaves the
// card for a person to top up on the board. A run a person paused stops it
// too. The drive is done when the session is idle with no workflow run open
// or queued and every run's report has been answered.
//
// A drive may instead start a workflow itself (Options.Workflow or
// WorkflowYAML): gummi writes the session's spec — the one given
// (Options.Spec), or one it writes from the prompt, the run's inputs and
// Options.Checks — and proposes the run with those inputs, with no session
// turn before it. Without a spec given the run's report is the product (a
// bug triaged, a question answered): it is kept from the session and the
// drive ends with the run (Options.NoSession). The run's end is on the
// stream as workflow_ended, its artifacts and notes with it, and a run
// that ends partial makes the drive's status partial.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// The formats of a drive's stream on its writer.
const (
	FormatJSONL = "jsonl"
	FormatText  = "text"
)

// Options configures one drive. Envelope is required.
type Options struct {
	// Envelope is the session's credit budget.
	Envelope int
	Profile  string
	// Repo and Base place the session: a configured `repos:` name ("" for
	// the workspace default) and the branch its work forks from ("" for
	// what the repository has checked out).
	Repo string
	Base string
	// Ref is an external correlation id, persisted on the card and echoed
	// on the created line: an eval's case id, a CI job's.
	Ref string
	// Timeout is the inactivity budget: a drive that hears nothing from
	// the session or its workflow run for this long stops as a timeout. 0
	// disables it.
	Timeout time.Duration
	// Verbose adds the session's tool-call activity lines to the stream.
	Verbose bool
	// Format is how the drive's events read on the writer given to New:
	// FormatJSONL (the default) or FormatText, for a person at a terminal.
	Format string
	// JSONL, if set, receives every event as NDJSON, activity lines
	// included, whatever Format is.
	JSONL io.Writer
	// Errs, if set, receives the text form's errors in place of the
	// stream.
	Errs io.Writer

	// Workflow names the recipe the drive runs itself (a workspace's or a
	// bundled one), or WorkflowYAML writes it out; at most one is set.
	Workflow     string
	WorkflowYAML string
	// Inputs are the run's inputs, checked against the recipe's.
	Inputs map[string]string
	// Spec is the spec the run is judged against, as markdown. Empty,
	// gummi writes one from the prompt, the inputs and Checks.
	Spec string
	// Checks are done-when commands for the spec gummi writes, one item
	// each. They are refused beside Spec: that spec says its own.
	Checks []string
	// NoSession keeps the run's report from the session: the drive ends
	// with the run, and the session never takes a turn.
	NoSession bool
}

// RunsWorkflow reports whether the drive starts a workflow run itself. Such
// a drive turns workflows on for its session, whatever the workspace says.
func (o Options) RunsWorkflow() bool {
	return strings.TrimSpace(o.Workflow) != "" || strings.TrimSpace(o.WorkflowYAML) != ""
}

// Status is how a drive ended; each maps to the process's exit code.
type Status string

const (
	StatusDone      Status = "done"
	StatusError     Status = "error"
	StatusPaused    Status = "paused"
	StatusExhausted Status = "exhausted"
	StatusTimeout   Status = "timeout"
	// StatusPartial is a drive whose workflow run ended partial: a checked
	// done-when item does not pass, or the run could not finish.
	StatusPartial Status = "partial"
)

// ExitCode is the process exit for s: 0 only when the session is done.
func (s Status) ExitCode() int {
	switch s {
	case StatusDone:
		return 0
	case StatusPaused:
		return 2
	case StatusExhausted:
		return 3
	case StatusTimeout:
		return 4
	case StatusPartial:
		return 5
	}
	return 1
}

// Outcome is a drive's end: its status and the session it drove.
type Outcome struct {
	Status Status
	ID     domain.FeatureID
}

// unattendedAnswer is the answer to a question that offered no options.
const unattendedAnswer = "No one is attending this session: it runs under `gummi run`, " +
	"with no person to answer. Decide for yourself and carry on."

// unattendedNote follows an option the drive chose for the person: the
// choice is gummi's, and nobody is there to do what an option asks of
// them — paste output, run a command, check a machine.
const unattendedNote = "\n\n(Chosen by `gummi run` on the person's behalf: no one is attending this " +
	"session, so nobody can provide information, run commands or check anything for you. Go on from " +
	"what you can read and run yourself.)"

// workflowPoll is how often the loop ticks the session's run with nothing
// else happening: a run's steps send EventWorkflow when they settle, and
// this is the backstop for one whose event was lost — the board's
// workflow loop polls for the same reason.
const workflowPoll = 5 * time.Second

// Driver runs one session unattended. It is the sole consumer of
// engine.Events() for its process, so it drains the stream continuously
// while it waits: the engine's pumps block on a reader that has stopped.
type Driver struct {
	eng   *engine.Engine
	store *state.Store
	ws    state.Workspace
	out   *emitter
	opts  Options
	poll  time.Duration

	// what the stream has already carried: the session's replies, its
	// activity feed (verbose) and each workflow run's last state
	replies     int
	activity    int
	activityGen time.Time
	runStates   map[int]domain.RunState
	// runs seen live: one that ends owes the session its report as a
	// turn, and the drive is not done until that turn has been answered
	seenLive map[int]bool
	// answered holds the decisions this drive has answered, so one that
	// lingers while the engine settles it is not answered twice
	answered map[string]bool
	// partial is set once a run this drive saw live ends partial
	partial bool
}

// New builds a driver writing its event stream to out, as NDJSON unless
// opts.Format says text. The caller owns
// the engine and agent lifetime.
func New(eng *engine.Engine, store *state.Store, ws state.Workspace, out io.Writer, opts Options) *Driver {
	return &Driver{
		eng: eng, store: store, ws: ws, out: newEmitter(sinks{Out: out, Format: opts.Format, JSONL: opts.JSONL, Errs: opts.Errs}, opts.Verbose), opts: opts,
		poll:      workflowPoll,
		runStates: map[int]domain.RunState{},
		seenLive:  map[int]bool{},
		answered:  map[string]bool{},
	}
}

// Run mints the session the prompt starts, always allows its workflow
// runs, sends the prompt as its first turn and drives it to an Outcome. A
// failure is on the stream as an error line and returned beside a
// StatusError outcome.
func (d *Driver) Run(ctx context.Context, prompt string) (Outcome, error) {
	description := prompt
	if d.opts.RunsWorkflow() {
		var err error
		if description, err = d.checkWorkflow(prompt); err != nil {
			return d.fail(ctx, "", err)
		}
	} else if strings.TrimSpace(prompt) == "" {
		return d.fail(ctx, "", errors.New("run needs a prompt"))
	}
	if d.opts.Envelope <= 0 {
		return d.fail(ctx, "", errors.New("run needs an envelope"))
	}
	f, err := cardmint.Mint(ctx, d.store, d.ws, cardmint.Input{
		Description: description, Profile: d.opts.Profile, Envelope: d.opts.Envelope,
		Repo: d.opts.Repo, RequireRepo: d.eng.RequireRepo, Base: d.opts.Base,
		ExternalRef: d.opts.Ref,
		// the same prompt run again, an eval's next pass, takes the next
		// free title rather than failing on the last one's
		Unattended: true,
	})
	if err != nil {
		return d.fail(ctx, "", err)
	}
	d.out.Emit(createdEvent{
		Event: "created", ID: string(f.ID), Ref: d.opts.Ref, Title: f.Title,
		Branch: f.BranchName(), Envelope: f.Budget.Envelope,
	})
	if err := d.store.SetWorkflowAlwaysAllow(ctx, f.ID, true); err != nil {
		return d.fail(ctx, f.ID, err)
	}
	if d.opts.RunsWorkflow() {
		// a --workflow drive runs workflows whatever the workspace says
		on := true
		if err := d.eng.SetSessionWorkflows(ctx, f.ID, &on, state.ActorAutopilot); err != nil {
			return d.fail(ctx, f.ID, err)
		}
	}
	ff, err := d.eng.OpenFreeform(ctx, f)
	if err != nil {
		return d.fail(ctx, f.ID, err)
	}
	if d.opts.RunsWorkflow() {
		if err := d.startWorkflow(ctx, f, prompt); err != nil {
			return d.fail(ctx, f.ID, err)
		}
		return d.loop(ctx, f.ID)
	}
	if err := ff.KickoffWith(ctx, prompt); err != nil {
		return d.fail(ctx, f.ID, err)
	}
	return d.loop(ctx, f.ID)
}

// loop drives the session until it stops: it ticks the session's run,
// streams what happened, answers what is asked and checks for an end
// after every event.
func (d *Driver) loop(ctx context.Context, id domain.FeatureID) (Outcome, error) {
	poll := time.NewTicker(d.poll)
	defer poll.Stop()
	var quiet <-chan time.Time
	var timer *time.Timer
	if d.opts.Timeout > 0 {
		timer = time.NewTimer(d.opts.Timeout)
		defer timer.Stop()
		quiet = timer.C
	}
	tick := true
	for {
		if out, stop, err := d.check(ctx, id); stop {
			return out, err
		}
		if tick {
			if _, err := d.eng.WorkflowTickSession(ctx, id); err != nil {
				d.out.Activity(string(id), "workflow", "workflow tick failed: "+err.Error())
			}
			tick = false
		}
		select {
		case <-ctx.Done():
			return d.fail(ctx, id, ctx.Err())
		case <-quiet:
			return d.timeout(ctx, id), nil
		case <-poll.C:
			tick = true
		case ev, ok := <-d.eng.Events():
			if !ok {
				return d.fail(ctx, id, errors.New("engine event stream closed"))
			}
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(d.opts.Timeout)
			}
			if ev.Kind == engine.EventWorkflow || ev.Kind == engine.EventQuestion {
				tick = true
			}
		}
	}
}

// check streams what is new, answers any open question, and decides
// whether the drive stops here.
func (d *Driver) check(ctx context.Context, id domain.FeatureID) (Outcome, bool, error) {
	f, err := d.store.GetFeature(ctx, id)
	if err != nil {
		out, err := d.fail(ctx, id, err)
		return out, true, err
	}
	ff := d.eng.Freeform(id)
	var snap engine.Snapshot
	if ff != nil {
		snap = ff.Snapshot()
	}
	d.stream(id, snap)
	d.streamRuns(ctx, id)
	run, decision := d.eng.WorkflowDecision(ctx, id)
	step, stepAsk := d.stepAsk(ctx, id)
	open, _ := d.store.OpenRun(ctx, id)
	queued, _ := d.store.QueuedRun(ctx, id)

	var answerErr error
	switch {
	case f.Stage == domain.StageDone:
		return d.done(ctx, id), true, nil
	case decision != nil && decision.Kind == domain.RunDecideBudget:
		return d.exhausted(ctx, id, run), true, nil
	case snap.PendingAsk != nil:
		answerErr = d.answerAsk(ctx, id, id, nil, snap.PendingAsk)
	case decision != nil:
		answerErr = d.answerDecision(ctx, id, run, decision)
	case stepAsk != nil:
		answerErr = d.answerAsk(ctx, id, step.Card, step, stepAsk)
	case open != nil && open.State == domain.RunPaused && open.OutOfBudget:
		return d.exhausted(ctx, id, open), true, nil
	case open != nil && open.State == domain.RunPaused && !open.Stopping:
		return d.paused(ctx, id, open), true, nil
	case snap.Exhausted:
		return d.exhausted(ctx, id, nil), true, nil
	case snap.Err != nil && !snap.Busy:
		out, err := d.fail(ctx, id, snap.Err)
		return out, true, err
	default:
		// a report kept from the session is never answered: the run's end
		// is the drive's
		pending := !d.opts.NoSession && d.reportPending(ctx, id, snap)
		if snap.Busy || len(snap.Queued) > 0 || (ff != nil && ff.Watching()) || open != nil || queued != nil || pending {
			return Outcome{}, false, nil
		}
		return d.done(ctx, id), true, nil
	}
	if answerErr != nil {
		out, err := d.fail(ctx, id, answerErr)
		return out, true, err
	}
	return Outcome{}, false, nil
}

// answerAsk answers an ask_user — the session's own (step nil), or a
// step's on its card — with its recommended option, or, when it offered
// none, with the word that no one is there.
func (d *Driver) answerAsk(ctx context.Context, session, card domain.FeatureID, step *domain.RunStep, ask *engine.Ask) error {
	key := "ask:" + string(card) + ":" + ask.DecisionID + ":" + ask.CallID
	if d.answered[key] {
		return nil
	}
	answer := engine.RecommendedOption(ask)
	sent := answer + unattendedNote
	if answer == "" {
		answer, sent = unattendedAnswer, unattendedAnswer
	}
	ev := answeredEvent{Event: "answered", ID: string(session), Kind: "ask", Q: ask.Question, Answer: answer}
	if step != nil {
		ev.Kind, ev.Run, ev.Step = "workflow_ask", step.Run, step.Path
	}
	if err := d.eng.AnswerCall(ctx, card, ask.CallID, sent, state.ActorAutopilot); errors.Is(err, engine.ErrQuestionMoved) {
		// asked again under the drive since it read the question: the
		// next check reads the one open now and answers that
		return nil
	} else if err != nil {
		return fmt.Errorf("answering %q: %w", firstLine(ask.Question), err)
	}
	d.answered[key] = true
	d.out.Emit(ev)
	return nil
}

// answerDecision answers a run's decision the way the package comment
// says. The budget's is never answered here: check stops on it first.
func (d *Driver) answerDecision(ctx context.Context, id domain.FeatureID, run *domain.Run, dec *domain.RunDecision) error {
	if run == nil || d.answered[dec.ID] {
		return nil
	}
	var answer string
	switch dec.Kind {
	case domain.RunDecideProposal:
		answer = engine.WorkflowAllow
	case domain.RunDecidePlan:
		answer = engine.WorkflowKeepPlan
	case domain.RunDecideMax, domain.RunDecideUnjudged:
		answer = engine.WorkflowContinue
	case domain.RunDecideResume:
		answer = engine.WorkflowResumeRun
	case domain.RunDecideWatch:
		// the recommended option is the first; a PR that has been quiet for
		// the recipe's quiet is not waited on forever in a drive
		answer = dec.Options[0]
		if row, err := d.store.RunStep(ctx, id, run.N, dec.Step); err == nil && row != nil && row.Watch != nil &&
			strings.HasPrefix(row.Watch.Asked, "quiet@") {
			answer = engine.WorkflowStopRun
		}
	default:
		return fmt.Errorf("workflow run %d asks %q, which an unattended run cannot answer", run.N, firstLine(dec.Question))
	}
	if err := d.eng.WorkflowAnswer(ctx, id, run.N, dec.ID, answer); err != nil {
		return fmt.Errorf("answering workflow run %d: %w", run.N, err)
	}
	d.answered[dec.ID] = true
	d.out.Emit(answeredEvent{
		Event: "answered", ID: string(id), Kind: "workflow_" + string(dec.Kind),
		Q: dec.Question, Answer: answer, Run: run.N, Step: dec.Step,
	})
	// a run paused for the decision, and held by nothing else now, carries on
	r, err := d.store.Run(ctx, id, run.N)
	if err != nil || r.State != domain.RunPaused || r.OutOfBudget || r.Pending != nil || r.Stopping {
		return nil
	}
	return d.eng.WorkflowResume(ctx, id)
}

// stepAsk is a step of the session's open run waiting on its ask_user
// (§25.7), nil when none is.
func (d *Driver) stepAsk(ctx context.Context, id domain.FeatureID) (*domain.RunStep, *engine.Ask) {
	open, err := d.store.OpenRun(ctx, id)
	if err != nil || open == nil {
		return nil, nil
	}
	steps, err := d.store.RunStepsLite(ctx, id, open.N)
	if err != nil {
		return nil, nil
	}
	for i := range steps {
		s := &steps[i]
		if s.Card == "" || !s.State.Live() {
			continue
		}
		ff := d.eng.Freeform(s.Card)
		if ff == nil {
			continue
		}
		if ask := ff.Snapshot().PendingAsk; ask != nil {
			return s, ask
		}
	}
	return nil, nil
}

// reportPending says whether a run this drive saw live has ended with a
// report the session has not answered yet. The run is written ended a
// moment before its report goes to the session as a turn, and the turn is
// on the transcript a moment before the session reads as busy with it; in
// both gaps the session is idle with no run open — which is not done.
func (d *Driver) reportPending(ctx context.Context, id domain.FeatureID, snap engine.Snapshot) bool {
	runs, err := d.store.SessionRuns(ctx, id)
	if err != nil {
		return false
	}
	for _, r := range runs {
		if !d.seenLive[r.N] || !r.State.Ended() || r.Report == "" {
			continue
		}
		answered := false
		for i, m := range snap.Transcript {
			if m.Author != engine.AuthorUser || m.By != state.ActorWorkflow || !strings.Contains(m.Content, r.Report) {
				continue
			}
			for _, rep := range snap.Transcript[i+1:] {
				if rep.Author == engine.AuthorAssistant && !rep.Streaming {
					answered = true
					break
				}
			}
		}
		if !answered {
			return true
		}
	}
	return false
}

// stream emits the session's finished replies, and under verbose its new
// activity lines.
func (d *Driver) stream(id domain.FeatureID, snap engine.Snapshot) {
	if d.replies > len(snap.Transcript) {
		// the conversation was restarted under it (/compact, /handoff)
		d.replies = len(snap.Transcript)
	}
	for ; d.replies < len(snap.Transcript); d.replies++ {
		m := snap.Transcript[d.replies]
		if m.Streaming {
			break
		}
		if m.Author == engine.AuthorAssistant && strings.TrimSpace(m.Content) != "" {
			d.out.Emit(replyEvent{Event: "reply", ID: string(id), Text: m.Content})
		}
	}
	if !d.out.Verbose() {
		return
	}
	if snap.StartedAt != d.activityGen || d.activity > len(snap.Activity) {
		// a respawned backend starts a feed of its own
		d.activityGen, d.activity = snap.StartedAt, 0
	}
	for ; d.activity < len(snap.Activity); d.activity++ {
		d.out.Activity(string(id), "session", snap.Activity[d.activity])
	}
}

// streamRuns emits a workflow line whenever one of the session's runs
// changes state.
func (d *Driver) streamRuns(ctx context.Context, id domain.FeatureID) {
	runs, err := d.store.SessionRuns(ctx, id)
	if err != nil {
		return
	}
	var ended []domain.Run
	defer func() {
		// each after its own state line
		for i := range ended {
			d.emitEnded(ctx, id, &ended[i])
		}
	}()
	for _, r := range runs {
		if !r.State.Ended() && r.State != domain.RunDenied {
			d.seenLive[r.N] = true
		}
		if d.runStates[r.N] == r.State {
			continue
		}
		d.runStates[r.N] = r.State
		if r.State.Ended() && d.seenLive[r.N] {
			ended = append(ended, r)
			if r.State == domain.RunPartial {
				d.partial = true
			}
		}
		name := r.RecipeName
		if name == "" {
			name = r.Recipe.Name // an inline recipe's own
		}
		d.out.Emit(workflowEvent{
			Event: "workflow", ID: string(id), Run: r.N, Recipe: name, Spec: r.Spec,
			State: string(r.State), Spent: r.Spent, Steps: len(workflowrecipe.Steps(r.Recipe)), Report: r.Report,
		})
	}
}

// --- ends ----------------------------------------------------------------

func (d *Driver) done(ctx context.Context, id domain.FeatureID) Outcome {
	// check streamed the runs before it read none open: one that ended
	// between the two reads is put on the stream, and counted, here
	d.streamRuns(ctx, id)
	f, _ := d.store.GetFeature(ctx, id)
	// the session's spend is its pool's: its own turns and its runs' steps
	status := StatusDone
	if d.partial {
		status = StatusPartial
	}
	d.out.Emit(doneEvent{
		Event: "done", ID: string(id), Status: string(status), Branch: f.BranchName(), Spent: d.eng.SessionPoolSpent(ctx, f),
	})
	return Outcome{Status: status, ID: id}
}

// exhausted is the envelope spent: the session's own turn stopped on it,
// or run (when not nil) paused for a top-up no one here may give.
func (d *Driver) exhausted(ctx context.Context, id domain.FeatureID, run *domain.Run) Outcome {
	f, _ := d.store.GetFeature(ctx, id)
	ev := exhaustedEvent{Event: "exhausted", ID: string(id), Spent: d.eng.SessionPoolSpent(ctx, f), Envelope: f.Budget.Envelope}
	if run != nil {
		ev.Run = run.N
	}
	d.out.Emit(ev)
	return Outcome{Status: StatusExhausted, ID: id}
}

// paused is a run a person paused from the board while it was driven: only
// a person runs it again.
func (d *Driver) paused(ctx context.Context, id domain.FeatureID, run *domain.Run) Outcome {
	d.out.Emit(pausedEvent{Event: "paused", ID: string(id), Run: run.N, Reason: run.Reason, Spent: d.spent(ctx, id)})
	return Outcome{Status: StatusPaused, ID: id}
}

func (d *Driver) timeout(ctx context.Context, id domain.FeatureID) Outcome {
	d.out.Emit(timeoutEvent{
		Event: "timeout", ID: string(id), After: d.opts.Timeout.String(), Spent: d.spent(ctx, id),
		Hint: "nothing happened on the session or its workflow run for the whole --timeout window: " +
			"the backend stalled or lost auth, or the work needs a larger --timeout",
	})
	return Outcome{Status: StatusTimeout, ID: id}
}

// fail emits the error line and returns the StatusError outcome beside the
// error.
func (d *Driver) fail(ctx context.Context, id domain.FeatureID, err error) (Outcome, error) {
	ev := errorEvent{Event: "error", ID: string(id), Error: err.Error()}
	if id != "" {
		ev.Spent = d.spent(ctx, id)
	}
	d.out.Emit(ev)
	return Outcome{Status: StatusError, ID: id}, err
}

// spent is the session's spend so far, its runs' steps included, for an
// end line: an eval charges a run that timed out or failed for what it
// spent as surely as one that finished.
func (d *Driver) spent(ctx context.Context, id domain.FeatureID) float64 {
	f, err := d.store.GetFeature(context.WithoutCancel(ctx), id)
	if err != nil {
		return 0
	}
	return d.eng.SessionPoolSpent(context.WithoutCancel(ctx), f)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
