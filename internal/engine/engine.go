// Package engine is gummi's orchestrator: it binds features' stages to
// agent sessions, schedules autonomous runs across a bounded number of
// attention slots (DESIGN §4.2), routes turns, and streams typed
// activity to the UI.
//
// Interactive sessions (brainstorm/spec chat) run whenever you attach
// and hold no slot — you are the scarce resource. Autonomous sessions
// (plan/implement/review/verify) consume one of max_active slots;
// excess runs queue and start automatically as slots free (a session
// freeing its slot on pause, or on going idle when its turn completes).
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
)

// kickoff is the go-ahead sent to start an autonomous stage; the stage
// hints already tell the agent what to do.
const kickoff = "Proceed with this stage per your instructions and the spec."

// interactiveKickoff opens a fresh interactive session with the agent
// leading — the user shouldn't have to know what to say to start an
// interview (DESIGN §3: brainstorm develops a one-line description).
func interactiveKickoff(s domain.Stage) string {
	switch s {
	case domain.StageSpec:
		return "The user just opened the spec chat. Read the spec and its open %% threads, " +
			"then drive convergence: recommend one approach with your reasoning, and put the " +
			"most consequential open question to the user first. Keep it short."
	case domain.StageTriage:
		return "The user just opened the triage chat. Read the bug report, then start " +
			"reproducing: state the bug as you understand it in a sentence, and ask the two or " +
			"three questions you most need to reproduce it (steps, environment, expected vs " +
			"actual). Keep it short."
	case domain.StageDiagnose:
		return "The user just opened the diagnose chat. Read the bug report and its " +
			"reproduction, then drive toward root cause: state your leading hypothesis with your " +
			"reasoning, and put the most consequential open question to the user first. Keep it short."
	default:
		return "The user just opened the brainstorm chat. Read the spec draft, then open the " +
			"interview: state the problem as you understand it in a sentence or two and ask the " +
			"user the two or three highest-leverage questions. Keep it short."
	}
}

// Config wires an engine to its backend. Model/Provider are the M1
// stand-in for profiles (M3): one model config for every role.
type Config struct {
	Agent      agent.Agent
	Store      *state.Store
	Worktrees  *worktree.Manager
	Workspace  state.Workspace
	Model      string
	Provider   agent.Provider
	Permission agent.Permission
	// MaxActive is the number of concurrent autonomous slots (default 1).
	MaxActive int
	// Persist writes session transcripts to Store so they survive a
	// restart (Restore reloads them).
	Persist bool
	// Profiles maps a feature's profile + role to a concrete model /
	// BYOK provider. Empty falls back to Model/Provider for every role.
	Profiles config.Profiles
	// StageBudget is a flat per-autonomous-stage credit budget (0 = no
	// budget). The session cap is set ~10% below it (soft-stop
	// headroom); the model is told its budget and nudged at thresholds.
	// It is the fallback for features without a spend-plan envelope; a
	// feature with an envelope uses per-stage allocations instead (§5.1
	// layer 3, see stageBudget).
	StageBudget float64
}

// Engine orchestrates all live sessions and the autonomous run queue.
type Engine struct {
	cfg       Config
	maxActive int
	now       func() time.Time // injectable clock (spec-capture timestamps)

	// raw carries events from pump goroutines to the forwarder; events
	// is the UI-facing stream, owned solely by the forwarder.
	raw     chan Event
	events  chan Event
	stopped chan struct{}

	mu       sync.Mutex
	live     map[domain.FeatureID]*Session
	queue    []domain.FeatureID        // autonomous features awaiting a slot, FIFO
	running  int                       // autonomous sessions currently holding slots
	released map[domain.FeatureID]bool // features whose reserve a top-up released
	closed   bool

	// persistMu serializes a session save against a delete of the same
	// feature: it spans persist's finalized-check-and-write and
	// persistDelete so an in-flight save can't land after the delete and
	// resurrect a dropped row.
	persistMu sync.Mutex
}

// New builds an engine. The caller owns cfg.Agent's lifetime.
func New(cfg Config) *Engine {
	if cfg.Permission == "" {
		cfg.Permission = agent.PermissionAllowAll
	}
	max := cfg.MaxActive
	if max < 1 {
		max = 1
	}
	e := &Engine{
		cfg:       cfg,
		maxActive: max,
		now:       time.Now,
		raw:       make(chan Event, 256),
		events:    make(chan Event),
		stopped:   make(chan struct{}),
		live:      map[domain.FeatureID]*Session{},
		released:  map[domain.FeatureID]bool{},
	}
	go e.forward()
	return e
}

// Events is the UI-facing stream. It stays open for the engine's life
// and closes on Close.
func (e *Engine) Events() <-chan Event { return e.events }

// forward is the only writer to e.events.
func (e *Engine) forward() {
	defer close(e.events)
	for {
		select {
		case <-e.stopped:
			return
		case ev := <-e.raw:
			select {
			case e.events <- ev:
			case <-e.stopped:
				return
			}
		}
	}
}

// Get returns the live session for a feature, or nil.
func (e *Engine) Get(id domain.FeatureID) *Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live[id]
}

// Sessions returns a snapshot of every live session, keyed by feature.
func (e *Engine) Sessions() map[domain.FeatureID]*Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[domain.FeatureID]*Session, len(e.live))
	for id, s := range e.live {
		out[id] = s
	}
	return out
}

// Attach starts (or reuses) an interactive chat session for a feature's
// current stage. Interactive sessions hold no attention slot.
func (e *Engine) Attach(ctx context.Context, f domain.Feature) (*Session, error) {
	role, ok := roleForStage(f.Stage)
	if !ok {
		return nil, fmt.Errorf("stage %s has no agent action", f.Stage)
	}
	if !interactiveStage(f.Stage) {
		return nil, fmt.Errorf("stage %s is autonomous; use Run", f.Stage)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("engine is closed")
	}
	prior := e.live[f.ID]
	e.mu.Unlock()

	// A live agent session for this stage is reused (keeps its context);
	// a restored session (no agent) or a different stage starts fresh,
	// carrying the prior transcript over so the history stays visible.
	if prior != nil && prior.Feature.Stage == f.Stage && prior.agent() != nil {
		return prior, nil
	}

	// interactive chat is human-paced: no budget cap.
	sess, specPath, err := e.newAgentSession(ctx, f, role, 0, false)
	if err != nil {
		return nil, err
	}
	s := &Session{Feature: f, Role: role, Interactive: true, state: StateInteractive, done: make(chan struct{}), specPath: specPath}
	e.stampSpawnInfo(s)
	if prior != nil && prior.Feature.Stage == f.Stage {
		ps := prior.Snapshot()
		s.transcript = append(s.transcript, ps.Transcript...)
		s.activity = append(s.activity, ps.Activity...)
		s.spend = ps.Spend
	}
	// A fresh conversation opens with a stage kickoff so the agent leads;
	// a carried-over transcript means the interview is already underway,
	// so reattaching stays silent.
	fresh := len(s.transcript) == 0
	// s is not yet reachable by Pause/Drop/Close (not in e.live), so
	// attachAgent can't be racing a finalize here; the bool is checked for
	// symmetry with the autonomous path.
	if !s.attachAgent(sess) {
		_ = sess.Close()
		return nil, errors.New("engine is closed")
	}
	s.setState(StateInteractive)

	if !e.replace(f.ID, s) {
		s.stop() // engine closed during startup: don't leave the agent live
		return nil, errors.New("engine is closed")
	}
	go e.pump(s)
	var ko string
	if fresh {
		ko = interactiveKickoff(f.Stage)
		s.appendSystem(ko)
		s.setBusy(true)
	}
	e.persist(s)
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventStarted})
	if fresh {
		if err := sess.Send(ctx, ko); err != nil {
			s.setError(err)
			e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventError, Err: err})
		}
	}
	return s, nil
}

// Run enqueues an autonomous stage for a feature and fills any free
// slot. A no-op if the feature is already queued or running.
func (e *Engine) Run(f domain.Feature) error { return e.RunWith(f, "") }

// RunWith is Run with the user's review comments attached: note (the
// open %% annotations, compiled by the UI) is appended to the stage
// kickoff so the fresh session starts by addressing them — the
// "request changes" path for autonomous stages, which have no chat to
// send a turn to (DESIGN §6.1).
func (e *Engine) RunWith(f domain.Feature, note string) error {
	return e.run(f, note, false)
}

// RunCritique runs the plan-critique pass: a fresh-context reviewer
// session on the Plan stage that refutes the written plan (security,
// correctness, completeness) before the human gate, writing findings
// as %% marker threads and ending with a verdict. It replaces the done
// plan session like any re-run; the state machine never sees it — the
// feature stays at Plan throughout.
func (e *Engine) RunCritique(f domain.Feature) error {
	if f.Stage != domain.StagePlan {
		return fmt.Errorf("critique runs on the plan stage, not %s", f.Stage)
	}
	return e.run(f, "", true)
}

// run is the shared autonomous-run path behind RunWith and RunCritique.
func (e *Engine) run(f domain.Feature, note string, critique bool) error {
	role, ok := roleForStage(f.Stage)
	if !ok {
		return fmt.Errorf("stage %s has no agent action", f.Stage)
	}
	if critique {
		role = agent.RoleReviewer
	}
	if interactiveStage(f.Stage) {
		return fmt.Errorf("stage %s is interactive; use Attach", f.Stage)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("engine is closed")
	}
	old := e.live[f.ID]
	if old != nil {
		// State() takes old.mu (state is written under it from pump
		// goroutines); the engine's lock order is e.mu → s.mu, so taking it
		// here while holding e.mu is safe.
		if st := old.State(); st == StateRunning || st == StateQueued {
			e.mu.Unlock()
			return nil // already scheduled
		}
	}
	s := &Session{Feature: f, Role: role, Critique: critique, state: StateQueued, done: make(chan struct{}), kickoffNote: note}
	e.stampSpawnInfo(s)
	e.dropLocked(f.ID)
	e.live[f.ID] = s
	e.queue = append(e.queue, f.ID)
	e.mu.Unlock()

	if old != nil {
		old.stop() // a replaced done/paused session; free its goroutine
		e.freeSlot(old)
	}
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventUpdated})
	e.schedule()
	return nil
}

// schedule fills free slots from the queue.
func (e *Engine) schedule() {
	for {
		e.mu.Lock()
		if e.running >= e.maxActive || len(e.queue) == 0 {
			e.mu.Unlock()
			return
		}
		id := e.queue[0]
		e.queue = e.queue[1:]
		s := e.live[id]
		if s == nil || s.State() != StateQueued {
			e.mu.Unlock()
			continue
		}
		e.running++
		s.takeSlot()
		e.mu.Unlock()
		e.startAutonomous(s)
	}
}

// startAutonomous creates the agent session for a queued run and kicks
// it off. On setup failure it frees the slot and records the error.
func (e *Engine) startAutonomous(s *Session) {
	// price this session's token spend at its provider's rate (0 =
	// default), and use the same rate for the rollover baseline so the
	// budget math is self-consistent.
	_, provider := e.resolveRole(s.Feature.Profile, s.Role)
	s.setByokRate(provider.CreditsPer1KTokens)
	// compute the stage budget once so the enforced cap, the budget-aware
	// hint, and the session's own budget all agree.
	budget := e.stageBudget(s.Feature, provider.CreditsPer1KTokens)
	// a budgeted feature with nothing left must not run uncapped (a 0
	// budget elsewhere means "unbudgeted"): gate it immediately.
	if s.Feature.Budget.Envelope > 0 && budget <= 0 && !interactiveStage(s.Feature.Stage) {
		e.exhaust(s)
		return
	}
	sess, specPath, err := e.newAgentSession(context.Background(), s.Feature, s.Role, budget, s.Critique)
	if err != nil {
		s.setError(err)
		s.setState(StatePaused)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		e.freeSlot(s)
		return
	}
	s.setSpecPath(specPath)
	s.setBudget(budget)
	// Pause/Drop/Close may have finalized this session while newAgentSession
	// was spawning the backend (seconds). If so, attachAgent refuses: close
	// the orphaned agent and free the slot rather than run it unwatched.
	if !s.attachAgent(sess) {
		_ = sess.Close()
		e.freeSlot(s)
		return
	}
	go e.pump(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStarted})

	s.appendUser(s.kickoffMessage())
	s.setBusy(true)
	e.persist(s)
	// The kickoff is sent off the scheduler goroutine: the Verify stage
	// runs the repo's fixed checks gummi-side first, which can take
	// minutes, and must not stall slot scheduling.
	go e.sendKickoff(s, sess)
}

// sendKickoff delivers the stage kickoff. For the Verify stage in
// allow-all mode it first runs the repo's fixed checks gummi-side and
// prepends their results, so the scribe only does the spec's
// feature-specific live checks and the write-up — no frontier model
// shepherding `go test` output.
func (e *Engine) sendKickoff(s *Session, sess agent.Session) {
	msg := s.kickoffMessage()
	if s.Feature.Stage == domain.StageVerify && e.cfg.Permission != agent.PermissionGuarded {
		if pre := e.runFixedChecks(s); pre != "" {
			msg = pre + "\n\n" + msg
		}
	}
	if err := sess.Send(context.Background(), msg); err != nil {
		e.failRun(s, err)
	}
}

// failRun records an unrecoverable autonomous-run failure: it sets the
// error, moves the session to paused (so Run can retry it), frees its
// attention slot, and promotes the queue. Without this a failed run would
// hold its slot forever — and Run, seeing StateRunning, would treat the
// feature as still scheduled and silently refuse to retry. Interactive
// sessions hold no slot and keep their state; freeSlot is a no-op for
// them. Idempotent via freeSlot's exactly-once latch.
func (e *Engine) failRun(s *Session, err error) {
	s.setError(err)
	if !s.Interactive {
		s.setState(StatePaused)
	}
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
	e.freeSlot(s)
}

// verifyStageTimeout bounds the gummi-side check run at the Verify stage
// (mirrors the manual verify dialog's cap).
const verifyStageTimeout = 10 * time.Minute

// runFixedChecks executes the repo's config.yaml checks in the feature's
// worktree, records each outcome in the activity feed, and returns a
// compact summary to hand the scribe (empty when there are no checks or
// the config can't be read — the scribe then runs them itself, as before).
func (e *Engine) runFixedChecks(s *Session) string {
	cfg, err := config.Load(e.cfg.Workspace.ConfigFile())
	if err != nil || len(cfg.Checks) == 0 {
		return ""
	}
	workDir := filepath.Join(e.cfg.Worktrees.Root(), s.Feature.WorktreePath())
	ctx, cancel := context.WithTimeout(context.Background(), verifyStageTimeout)
	defer cancel()
	results := verify.Run(ctx, workDir, cfg.Checks)

	var b strings.Builder
	b.WriteString("gummi already ran the repo's fixed checks in this worktree — do NOT re-run them:\n")
	for _, r := range results {
		status := "pass"
		if !r.OK {
			status = fmt.Sprintf("FAIL (exit %d)", r.ExitCode)
		}
		s.appendActivity(fmt.Sprintf("check %s: %s", r.Name, status))
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, status)
		if !r.OK {
			fmt.Fprintf(&b, "%s\n", indentLines(tailLines(r.Output, 20)))
		}
	}
	b.WriteString("\nNow execute the spec's Verification plan (the feature-specific live " +
		"checks), record all results in the spec's Verification plan and a summary " +
		"line in Progress, and report pass or fail with the evidence.")
	e.persist(s)
	return b.String()
}

// tailLines keeps the last n lines of s (check failures are most
// informative at the end).
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{"…(earlier output trimmed)"}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

func indentLines(s string) string {
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// stampSpawnInfo records on the session which backend, model, and
// provider its profile/role resolve to — and the provider's token→credit
// rate — so status displays (and interactive budget math) have them from
// the moment the session exists, not after the first usage event.
func (e *Engine) stampSpawnInfo(s *Session) {
	model, provider := e.resolveRole(s.Feature.Profile, s.Role)
	name := ""
	if e.cfg.Agent != nil {
		name = e.cfg.Agent.Name()
	}
	s.setSpawnInfo(name, model, provider)
	s.setByokRate(provider.CreditsPer1KTokens)
}

// newAgentSession builds an agent session for a feature's stage, with
// the model/provider chosen by the feature's profile for this role. It
// also returns the resolved spec path so the caller can record it on the
// Session (ask_user answer capture writes there).
func (e *Engine) newAgentSession(ctx context.Context, f domain.Feature, role agent.Role, budget float64, critique bool) (agent.Session, string, error) {
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, "", err
	}
	model, provider := e.resolveRole(f.Profile, role)
	hints := stageHints(f, specPath, critique)
	// implementation runs carry any open diff review comments so a fix-up
	// (bounce from the diff surface's "request changes") addresses each
	// (DESIGN §6.1). The store is the source of truth, so this reaches
	// every implement run, not just the one that triggered it.
	if f.Stage == domain.StageImplement || f.Stage == domain.StageFix {
		hints = append(hints, e.diffReviewHints(ctx, f.ID)...)
	}
	var maxCredits float64
	// autonomous stages get a budget cap + budget-aware hint (interactive
	// chat is human-paced, so it isn't capped).
	if budget > 0 && !interactiveStage(f.Stage) {
		maxCredits = budget * capHeadroom
		hints = append(hints, budgetHint(budget))
	}
	// gummi-owned client tools per stage. When the backend supports them,
	// register the tools and tell the agent they exist; otherwise fall
	// back to prompt conventions (ask_user has a fenced-block convention;
	// spec_annotate and submit_verdict degrade to the %% and VERDICT:
	// text forms the stage hints already describe).
	var tools []agent.ToolDef
	if e.cfg.Agent != nil && e.cfg.Agent.Capabilities().ClientTools {
		tools = stageTools(f.Stage, critique)
		if h := toolHint(f.Stage, critique); h != "" {
			hints = append(hints, h)
		}
	} else if interactiveStage(f.Stage) {
		hints = append(hints, askConventionHint)
	}
	sess, err := e.cfg.Agent.NewSession(ctx, agent.SessionOpts{
		WorkDir:     workDir,
		Role:        role,
		Model:       model,
		SystemHints: hints,
		Provider:    provider,
		Permission:  e.cfg.Permission,
		MaxCredits:  maxCredits,
		Tools:       tools,
	})
	if err != nil {
		return nil, "", fmt.Errorf("starting %s session: %w", role, err)
	}
	return sess, specPath, nil
}

// locate resolves the working directory and spec path for a feature's
// stage. Interactive pre-worktree stages run in the main checkout
// against the draft — materialized here so the agent never starts
// against a missing spec; later stages require the worktree.
func (e *Engine) locate(ctx context.Context, f domain.Feature) (workDir, specPath string, err error) {
	root := e.cfg.Worktrees.Root()
	if interactiveStage(f.Stage) {
		draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
		if err := spec.EnsureDraft(draft, &f); err != nil {
			return "", "", err
		}
		return root, draft, nil
	}
	hasWT, err := e.cfg.Worktrees.Exists(ctx, &f)
	if err != nil {
		return "", "", err
	}
	if !hasWT {
		return "", "", fmt.Errorf("feature %s at stage %s has no worktree; approve the spec to create one first", f.ID, f.Stage)
	}
	workDir = filepath.Join(root, f.WorktreePath())
	return workDir, filepath.Join(workDir, f.ArtifactPath()), nil
}

// Send routes a user/orchestrator turn to a feature's session.
func (e *Engine) Send(ctx context.Context, id domain.FeatureID, msg string) error {
	s := e.Get(id)
	if s == nil {
		return fmt.Errorf("no session for %s", id)
	}
	a := s.agent()
	if a == nil {
		return fmt.Errorf("%s is queued, not yet running", id)
	}
	s.appendUser(msg)
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})
	if err := a.Send(ctx, msg); err != nil {
		e.failRun(s, err)
		return err
	}
	return nil
}

// Interrupt aborts a feature's in-flight turn.
func (e *Engine) Interrupt(ctx context.Context, id domain.FeatureID) error {
	s := e.Get(id)
	if s == nil {
		return fmt.Errorf("no session for %s", id)
	}
	if a := s.agent(); a != nil {
		return a.Interrupt(ctx)
	}
	return nil
}

// Pause stops a feature's autonomous session, freeing its slot and
// promoting the queue. The stage is unchanged; Run resumes it.
func (e *Engine) Pause(ctx context.Context, id domain.FeatureID) error {
	s := e.Get(id)
	if s == nil {
		return fmt.Errorf("no session for %s", id)
	}
	if a := s.agent(); a != nil {
		_ = a.Interrupt(ctx)
	}
	// dequeue if it was still waiting
	e.mu.Lock()
	e.removeFromQueue(id)
	e.mu.Unlock()
	s.setState(StatePaused)
	e.persist(s) // record the paused state before finalizing
	s.stop()
	e.freeSlot(s)
	return nil
}

// Drop stops and forgets a feature's session (on stage advance/delete).
func (e *Engine) Drop(id domain.FeatureID) {
	e.mu.Lock()
	s := e.live[id]
	e.dropLocked(id)
	// a deleted feature's reserve-release state is gone for good; clean it
	// here (not in dropLocked, which also fires on a TopUp's re-Run and
	// would drop the flag it just set).
	delete(e.released, id)
	e.mu.Unlock()
	if s != nil {
		s.stop()
		e.freeSlot(s)
	}
	e.persistDelete(id)
}

// dropLocked removes a feature's live session and any queue entry.
// Caller holds e.mu.
func (e *Engine) dropLocked(id domain.FeatureID) {
	delete(e.live, id)
	e.removeFromQueue(id)
}

func (e *Engine) removeFromQueue(id domain.FeatureID) {
	for i, q := range e.queue {
		if q == id {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)
			return
		}
	}
}

// replace installs a session for a feature, stopping any prior one. It
// reports false without installing when the engine has since closed, so a
// session created concurrently with Close isn't left live (and its agent
// running) past shutdown — the caller then stops the orphan.
func (e *Engine) replace(id domain.FeatureID, s *Session) bool {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	old := e.live[id]
	e.removeFromQueue(id)
	e.live[id] = s
	e.mu.Unlock()
	if old != nil {
		old.stop()
		e.freeSlot(old)
	}
	return true
}

// freeSlot releases an autonomous session's attention slot and promotes
// the queue. It is a no-op for a session that never took a slot
// (interactive, or queued-and-dropped), and idempotent for one that did.
func (e *Engine) freeSlot(s *Session) {
	if !s.releaseSlot() {
		return
	}
	e.mu.Lock()
	if e.running > 0 {
		e.running--
	}
	e.mu.Unlock()
	e.schedule()
}

// TopUp releases a feature's held reserve into its stage caps and resumes
// the exhausted stage from its checkpoint with the extra headroom — the
// "top up" action of a budget-exhaustion gate (DESIGN §5.1 layer 3).
func (e *Engine) TopUp(ctx context.Context, id domain.FeatureID) error {
	e.mu.Lock()
	e.released[id] = true
	e.mu.Unlock()
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	return e.Run(f)
}

// exhaust checkpoints and stops a session that hit its credit budget —
// whether the CLI reported it or gummi-side enforcement tripped first —
// moving it to the needs-attention queue (never a silent death).
func (e *Engine) exhaust(s *Session) {
	if !s.markExhausted() {
		return // already checkpointed; a re-raised event must not duplicate the gate
	}
	s.appendActivity("budget exhausted — stage stopped for review")
	s.setState(StateDone)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventExhausted})
	e.freeSlot(s)
}

// Close stops every session and closes the event stream.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	sessions := make([]*Session, 0, len(e.live))
	for _, s := range e.live {
		sessions = append(sessions, s)
	}
	e.live = map[domain.FeatureID]*Session{}
	e.queue = nil
	e.mu.Unlock()

	for _, s := range sessions {
		s.stop()
	}
	close(e.stopped)
	return nil
}

// pump relays one session's agent events into the engine stream and
// accumulates its transcript/activity/spend. It exits when the session
// stops or its agent channel closes.
func (e *Engine) pump(s *Session) {
	events := s.agent().Events()
	for {
		select {
		case <-s.done:
			e.emitStopped(s)
			return
		case ev, ok := <-events:
			if !ok {
				e.emitStopped(s)
				return
			}
			e.handle(s, ev)
		}
	}
}

func (e *Engine) handle(s *Session, ev agent.Event) {
	kind := EventUpdated
	switch ev.Kind {
	case agent.EventTextDelta:
		s.appendDelta(ev.Text)
	case agent.EventReasoningDelta:
		// thinking is not transcript text and carries no state change;
		// relaying it would only emit an EventUpdated per chunk.
		return
	case agent.EventMessage:
		s.finishAssistant(ev.Text)
		kind = EventMessage
	case agent.EventToolCall:
		s.appendActivity(ev.Tool)
	case agent.EventClientToolCall:
		e.handleClientTool(s, ev.ToolCall)
		return
	case agent.EventContext:
		s.setContext(ev.Context)
	case agent.EventUsage:
		s.addSpend(ev.Usage)
		// accumulate the feature's running total across all stages. Persist
		// the credit-equivalent (not raw credits): a BYOK stage reports
		// tokens with zero credits, and storing that raw would make its spend
		// invisible to the credits-denominated rollover once any hosted stage
		// contributed credits. Hosted events already carry credits, so this
		// leaves them unchanged and never double-counts.
		if e.cfg.Persist && e.cfg.Store != nil {
			_ = e.cfg.Store.AddSpend(context.Background(), s.Feature.ID,
				s.creditEquivalent(ev.Usage), ev.Usage.InputTokens, ev.Usage.OutputTokens)
		}
		// budget awareness: on crossing a threshold, record a nudge and
		// signal the UI (DESIGN §5.1 layer 2).
		if pct, spent := s.crossedThreshold(); pct > 0 {
			s.appendActivity(nudge(pct, spent, s.Budget()))
			e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventBudget, Threshold: pct})
		}
		// gummi-side enforcement: interrupt and checkpoint once spend
		// reaches the budget (covers BYOK and sub-floor budgets the CLI
		// cap can't).
		if s.overBudget() {
			if a := s.agent(); a != nil {
				_ = a.Interrupt(context.Background())
			}
			e.exhaust(s)
			return
		}
	case agent.EventBudgetExhausted:
		// the credit cap was hit (CLI-reported): checkpoint and stop.
		e.exhaust(s)
		return
	case agent.EventIdle:
		s.setBusy(false)
		// a turn that already exhausted its budget has raised the
		// budget gate and freed its slot; the trailing idle must not
		// downgrade that gate to a generic "finished" one.
		if s.isExhausted() {
			e.persist(s)
			return
		}
		// convention-path ask (backends without client tools): a
		// gummi-ask block in the final message becomes a pending question
		// instead of a finished turn.
		if e.maybeConventionAsk(s) {
			e.persist(s)
			e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventQuestion})
			return
		}
		kind = EventIdle
		// an autonomous turn completing frees the slot (atomically, so a
		// racing Pause isn't overwritten)
		if !s.Interactive && s.finishRunning() {
			e.freeSlot(s)
		}
	case agent.EventError:
		// a terminal error ends the turn with no trailing idle (the
		// opencode/copilot failure paths emit only this), so recover the
		// slot and mark the run failed here — otherwise it wedges the
		// scheduler and Run refuses to retry the feature.
		e.failRun(s, ev.Err)
		return
	}
	// persist once per turn, at idle (which follows the finalized
	// message), rather than on every message + idle.
	if ev.Kind == agent.EventIdle {
		e.persist(s)
	}
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: kind})
}

func (e *Engine) emitStopped(s *Session) {
	if s.markStopped() {
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStopped})
	}
}

// send hands an event to the forwarder, applying backpressure rather
// than dropping. A closed engine unblocks via stopped.
func (e *Engine) send(ev Event) {
	select {
	case e.raw <- ev:
	case <-e.stopped:
	}
}
