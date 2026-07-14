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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// rebaseKickoff opens a rebase-resolve session; the run's kickoff note
// carries the target commit and the files expected to conflict.
const rebaseKickoff = "Proceed with the rebase per your instructions."

// runFlavor selects what an autonomous session does: the stage's own
// work, the plan-critique pass (RunCritique), or the rebase-resolve
// pass (RunRebase). The latter two borrow a stage without advancing it —
// the state machine never sees them.
type runFlavor int

const (
	flavorStage runFlavor = iota
	flavorCritique
	flavorRebase
)

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
		return "The user just opened the triage chat. Read the bug report, try to reproduce " +
			"the bug from it, and report what you found. Then ask the single question you most " +
			"need to reproduce it (steps, environment, expected vs actual), with your " +
			"recommended answer. Keep it short."
	case domain.StageDiagnose:
		return "The user just opened the diagnose chat. Read the bug report and its " +
			"reproduction, then drive toward root cause: state your leading hypothesis with your " +
			"reasoning, and put the most consequential open question to the user first. Keep it short."
	default:
		return "The user just opened the brainstorm chat. Read the spec draft, then open the " +
			"interview: state the problem as you understand it in a sentence or two and ask the " +
			"single highest-leverage question, with your recommended answer. Keep it short."
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

	mu      sync.Mutex
	live    map[domain.FeatureID]*Session
	queue   []domain.FeatureID // autonomous features awaiting a slot, FIFO
	running int                // autonomous sessions currently holding slots
	closed  bool

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
	}
	go e.forward()
	return e
}

// ClientTools reports whether the configured backend supports gummi's
// client tools, so hint compilers (engine- and UI-side) only mention a
// tool the agent can actually call.
func (e *Engine) ClientTools() bool {
	return e.cfg.Agent != nil && e.cfg.Agent.Capabilities().ClientTools
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
	sess, specPath, err := e.newAgentSession(ctx, f, role, 0, flavorStage)
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
	return e.run(f, note, flavorStage)
}

// RunCritique runs the plan-critique pass: a fresh-context reviewer
// session on the Plan stage that refutes the written plan (security,
// correctness, completeness) before the human gate, writing findings
// as %% marker threads and ending with a verdict. It replaces the done
// plan session like any re-run; the state machine never sees it — the
// feature stays at Plan throughout. note is appended to the kickoff;
// the UI uses it on re-critique rounds to point the fresh session at
// the prior round's resolved threads.
func (e *Engine) RunCritique(f domain.Feature, note string) error {
	if f.Stage != domain.StagePlan {
		return fmt.Errorf("critique runs on the plan stage, not %s", f.Stage)
	}
	return e.run(f, note, flavorCritique)
}

// RunRebase runs the rebase-resolve pass: an implementer session in the
// feature's worktree that rebases the branch onto main and resolves the
// conflicts a plain rebase stopped on — the agent hand-off behind the
// UI's rebase key when RebaseOnMain aborts. files names the paths that
// conflicted, so the kickoff can point the agent at them. Like the
// critique, it borrows the current stage without advancing it; the
// caller judges success by the resulting git state, not the transcript.
func (e *Engine) RunRebase(ctx context.Context, f domain.Feature, files []string) error {
	head, err := e.cfg.Worktrees.MainHead(ctx)
	if err != nil {
		return err
	}
	note := "Rebase this branch onto main's current HEAD: run `git rebase " + head + "`."
	if len(files) > 0 {
		note += "\nExpect conflicts in: " + strings.Join(files, ", ") + "."
	}
	return e.run(f, note, flavorRebase)
}

// run is the shared autonomous-run path behind RunWith, RunCritique,
// and RunRebase.
func (e *Engine) run(f domain.Feature, note string, flavor runFlavor) error {
	role, ok := roleForStage(f.Stage)
	if !ok {
		return fmt.Errorf("stage %s has no agent action", f.Stage)
	}
	switch flavor {
	case flavorCritique:
		role = agent.RoleReviewer
	case flavorRebase:
		role = agent.RoleImplementer
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
	s := &Session{Feature: f, Role: role, Critique: flavor == flavorCritique, Rebase: flavor == flavorRebase, state: StateQueued, done: make(chan struct{}), kickoffNote: note}
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
	sess, specPath, err := e.newAgentSession(context.Background(), s.Feature, s.Role, budget, s.flavor())
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
// allow-all mode it first runs the artifact's gummi-checks commands
// gummi-side and prepends their results, so the verify agent only does the
// spec's feature-specific live checks and the write-up — no frontier
// model shepherding `go test` output.
func (e *Engine) sendKickoff(s *Session, sess agent.Session) {
	msg := s.kickoffMessage()
	// only the stage's own run gets the pre-run check results: a rebase
	// session borrowing the Verify stage isn't verifying anything yet.
	if s.Feature.Stage == domain.StageVerify && !s.Rebase && e.cfg.Permission != agent.PermissionGuarded {
		if pre := e.runSpecChecks(s); pre != "" {
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

// runSpecChecks executes the artifact's gummi-checks commands in the
// feature's worktree, records each outcome in the activity feed, and
// returns a compact summary to hand the verify agent (empty when the artifact
// carries no checks or can't be read — the verify agent then discovers and
// runs them itself, per its stage hint).
func (e *Engine) runSpecChecks(s *Session) string {
	raw, err := os.ReadFile(s.SpecPath())
	if err != nil {
		return ""
	}
	checks, _, _ := spec.ParseChecks(string(raw))
	if len(checks) == 0 {
		return ""
	}
	workDir := filepath.Join(e.cfg.Worktrees.Root(), s.Feature.WorktreePath())
	ctx, cancel := context.WithTimeout(context.Background(), verifyStageTimeout)
	defer cancel()
	results := verify.Run(ctx, workDir, checks)

	// The approval-time baseline separates failures the feature caused
	// from ones the branch was born with. A baseline entry speaks for a
	// live check only when the command is unchanged — an edited command
	// invalidates what the old run proved. No baseline (older features,
	// guarded mode) degrades to today's unlabeled FAIL.
	baseline := map[string]state.CheckResult{}
	if rows, err := e.cfg.Store.CheckBaseline(ctx, s.Feature.ID); err == nil {
		for _, r := range rows {
			baseline[r.Name] = r
		}
	}

	var b strings.Builder
	preexisting := false
	b.WriteString("gummi already ran the spec's gummi-checks commands in this worktree — do NOT re-run them:\n")
	for _, r := range results {
		status := "pass"
		if !r.OK {
			if base, ok := baseline[r.Name]; ok && base.Cmd == r.Cmd && !base.OK {
				status = fmt.Sprintf("FAIL (pre-existing, exit %d)", r.ExitCode)
				preexisting = true
			} else {
				status = fmt.Sprintf("FAIL (exit %d)", r.ExitCode)
			}
		}
		s.appendToolDone(fmt.Sprintf("check %s: %s", r.Name, status), r.OK, r.Output)
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, status)
		if !r.OK {
			fmt.Fprintf(&b, "%s\n", indentLines(tailLines(r.Output, 20)))
		}
	}
	if preexisting {
		b.WriteString("\nChecks marked pre-existing already failed on the freshly created " +
			"branch before this feature changed anything: report them, but do not fail " +
			"verification because of them — only regressions count against this feature.\n")
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
func (e *Engine) newAgentSession(ctx context.Context, f domain.Feature, role agent.Role, budget float64, flavor runFlavor) (agent.Session, string, error) {
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, "", err
	}
	model, provider := e.resolveRole(f.Profile, role)
	hints := stageHints(f, specPath, flavor)
	// implementation runs carry any open diff review comments so a fix-up
	// (bounce from the diff surface's "request changes") addresses each
	// (DESIGN §6.1). The store is the source of truth, so this reaches
	// every implement run, not just the one that triggered it.
	if flavor == flavorStage && (f.Stage == domain.StageImplement || f.Stage == domain.StageFix) {
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
	if e.ClientTools() {
		tools = stageTools(f.Stage, flavor)
		if h := toolHint(f.Stage, flavor); h != "" {
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

// TopUp durably raises a feature's envelope and resumes the exhausted
// stage from its checkpoint — the "top up" action of a budget-exhaustion
// gate (DESIGN §5.1 layer 3). The raise is persisted to the store, so it
// survives stage advances and gummi restarts; RaisedEnvelope sizes it so
// the resumed stage always has real headroom rather than a sliver.
//
// The spend is priced at the default credit rate here; a BYOK provider
// with a much higher per-token rate can still re-gate after a top-up,
// since the stage-budget math at session start prices spend at that
// provider's rate.
func (e *Engine) TopUp(ctx context.Context, id domain.FeatureID) error {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if f.Budget.Envelope > 0 {
		raised := domain.PlanFor(f.Kind, float64(f.Budget.Envelope)).
			RaisedEnvelope(f.Stage, f.Spend.CreditEquivalent())
		if int(raised) > f.Budget.Envelope {
			f.Budget.Envelope = int(raised)
			if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
				return err
			}
		}
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
	e.settle(s) // partial work survives on the branch across the gate
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

// toolLine composes a tool-call event into one activity line: the tool
// name, then its salient argument after a double space — the separator
// the UI splits on to style name and detail differently.
func toolLine(ev agent.Event) string {
	if ev.Detail == "" {
		return ev.Tool
	}
	return ev.Tool + "  " + ev.Detail
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
		s.appendToolCall(ev.CallID, toolLine(ev))
	case agent.EventToolResult:
		if ev.Result != nil {
			s.resolveToolResult(ev.CallID, ev.Result.OK, ev.Result.Output)
		}
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
			credits := s.creditEquivalent(ev.Usage)
			// an event without provider-reported credits was priced from its
			// tokens — an estimate, not a metered cost; carry the split so
			// displays can label it instead of presenting it as real.
			var estimated float64
			if ev.Usage.Credits <= 0 {
				estimated = credits
			}
			_ = e.cfg.Store.AddSpend(context.Background(), s.Feature.ID,
				credits, estimated, ev.Usage.InputTokens, ev.Usage.OutputTokens)
			// the same sample attributed to (stage, model) for the breakdown;
			// same credit-equivalent, so stage_spend sums to spend_credits.
			_ = e.cfg.Store.RecordStageSpend(context.Background(), s.Feature.ID,
				s.Feature.Stage, string(s.Role), ev.Usage.Model,
				credits, estimated, ev.Usage.InputTokens, ev.Usage.CachedTokens, ev.Usage.OutputTokens)
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
			e.settle(s)
			e.stageReceipt(s)
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

// checkpointTimeout bounds the checkpoint's git work; a commit is local
// and fast, so a hang here is pathological and must not wedge the pump.
const checkpointTimeout = 30 * time.Second

// settle runs a finishing autonomous session's git epilogue: the
// checkpoint commit for stage work, or — for a rebase session, which
// must never CommitAll (a mid-rebase commit would capture conflict
// markers onto a detached HEAD) — the abort of anything the agent left
// mid-rebase, restoring the worktree's never-at-rest-mid-rebase
// invariant.
func (e *Engine) settle(s *Session) {
	if !s.Rebase {
		e.checkpoint(s)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	aborted, err := e.cfg.Worktrees.AbortRebase(ctx, &s.Feature)
	if err != nil {
		s.appendActivity("rebase cleanup failed: " + err.Error())
		return
	}
	if aborted {
		s.appendActivity("rebase left mid-flight — aborted, worktree restored")
	}
}

// checkpoint commits whatever the stage left in the feature's worktree
// to its branch, so agent work is never stranded uncommitted (DESIGN:
// gummi owns the branch's commits; the user lands it as one squash
// commit, so checkpoint granularity never reaches main's history). It
// runs as an autonomous turn completes and at the budget-exhaustion
// gate. Best-effort: a failure surfaces in the activity feed but never
// fails the run — the work is still on disk and the merge flow commits
// leftovers itself.
func (e *Engine) checkpoint(s *Session) {
	if s.Interactive || interactiveStage(s.Feature.Stage) {
		return // design-phase chats run in the main checkout; never auto-commit there
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	msg := fmt.Sprintf("%s: %s checkpoint", s.Feature.ID, s.Feature.Stage)
	committed, err := e.cfg.Worktrees.CommitAll(ctx, &s.Feature, msg)
	if err != nil {
		s.appendActivity("checkpoint commit failed: " + err.Error())
		return
	}
	if committed {
		s.appendActivity("worktree committed: " + msg)
	}
}

// stageReceipt appends a muted one-line spend receipt to the session's
// activity feed as an autonomous stage completes — "review · $0.42 ·
// gpt-5-codex" — read from the stage_spend rollup so it reports the same
// realized cost the dashboard shows. Best-effort: with no store, a read
// error, or a stage that recorded no spend, it simply adds nothing.
func (e *Engine) stageReceipt(s *Session) {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return
	}
	rows, err := e.cfg.Store.StageBreakdown(context.Background(), s.Feature.ID)
	if err != nil {
		return
	}
	var total, estimated float64
	var dom state.StageSpend
	models := 0
	for _, r := range rows {
		if r.Stage != s.Feature.Stage {
			continue
		}
		total += r.Credits
		estimated += r.EstimatedCredits
		// rows are ordered credits-desc within a stage, so the first match
		// is the dominant model; guard on Model to also handle reordering.
		if dom.Model == "" || r.Credits > dom.Credits {
			dom = r
		}
		models++
	}
	if models == 0 {
		return
	}
	cost := domain.FormatDollars(total)
	if estimated > 0 {
		cost = "~" + cost
	}
	line := fmt.Sprintf("%s · %s · %s", s.Feature.Stage, cost, dom.Model)
	if models > 1 {
		line += fmt.Sprintf(" +%d more", models-1)
	}
	s.appendActivity(line)
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
