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
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/sandbox"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflow"
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

// flavor strings are the durable form of runFlavor, persisted on the
// session row so Restore recovers a session's pass identity without
// re-deriving it from role/stage.
const (
	flavorStageString    = "stage"
	flavorCritiqueString = "critique"
	flavorRebaseString   = "rebase"
)

// flavorString is the persist form of a session's pass flavor.
func flavorString(f runFlavor) string {
	switch f {
	case flavorCritique:
		return flavorCritiqueString
	case flavorRebase:
		return flavorRebaseString
	}
	return flavorStageString
}

// parseFlavor recovers a restored session's pass identity from its
// persisted form. An empty (legacy) or unknown flavor reads as a plain
// stage run.
func parseFlavor(s string) (critique, rebase bool) {
	switch s {
	case flavorCritiqueString:
		return true, false
	case flavorRebaseString:
		return false, true
	}
	return false, false
}

// interactiveKickoff opens a fresh interactive session with the agent
// leading — the user shouldn't have to know what to say to start an
// interview (DESIGN §3: brainstorm develops a one-line description).
func interactiveKickoff(f domain.Feature) string {
	switch f.Stage {
	case domain.StageSpec:
		if f.Skip.Quick {
			return "The user just opened the quick spec chat. Read the spec draft, explore the " +
				"repo, and either put your few design-changing clarifying questions to the user " +
				"(recommended answers attached) or — if the description already decides them — " +
				"draft the complete spec now and present it for review. Keep chat turns short; " +
				"the detail belongs in the spec."
		}
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
	case domain.StageBrainstorm:
		return "The user just opened the brainstorm chat. Read the spec draft, then open the " +
			"interview: state the problem as you understand it in a sentence or two and ask the " +
			"single highest-leverage question, with your recommended answer. Keep it short."
	case domain.StageShape:
		return "The user just opened the shape chat. Read the research artifact at its workspace " +
			"home, then drive convergence: recommend how the topic should be shaped into its " +
			"final form, and put the most consequential open question to the user first. Keep it short."
	default:
		// Every interactive stage is enumerated above; a fall-through
		// means a new interactive stage landed without a kickoff opener,
		// and the model would otherwise get a wrong-stage copy.
		panic(fmt.Sprintf("interactiveKickoff: unknown interactive stage %s", f.Stage))
	}
}

// Config wires an engine to its backends. Model is the M1 stand-in for
// profiles: the fallback model when no profile applies. The default
// backend is the entry stored under the "" key in Agents (matched by
// agentFor's fallback).
type Config struct {
	// Agents maps a backend name (matching a profile role's `backend:`)
	// to its adapter. The empty-string key "" designates the default
	// backend used when no profile applies or a role omits `backend:`.
	// buildAgents in cmd/gummi seeds both the default's Name() and ""
	// with the same adapter, so lookups by either always resolve.
	Agents map[string]agent.Agent
	Store  *state.Store
	// Worktrees is a single repository's manager, retained for callers that
	// bind one repository directly. New callers pass Pool instead.
	Worktrees *worktree.Manager
	// Pool caches one manager per configured repository; every per-card git
	// operation resolves its manager here. New selects Pool when set and
	// otherwise wraps Worktrees as a single-repo pool.
	Pool       *worktree.Pool
	Workspace  state.Workspace
	Model      string
	Permission agent.Permission
	// Sandbox is the workspace-wide confinement mode (enforce|warn|off),
	// taken from .gummi/config.yaml. Empty means a profile that also omits
	// a value falls back to the built-in default (warn).
	Sandbox string
	// MaxActive is the number of concurrent autonomous slots (default 1).
	MaxActive int
	// Persist writes session transcripts to Store so they survive a
	// restart (Restore reloads them).
	Persist bool
	// Profiles maps a feature's profile + role to a concrete backend +
	// model. Empty falls back to Model + the default agent for every role.
	Profiles config.Profiles
	// StageBudget is a flat per-autonomous-stage credit budget (0 = no
	// budget). The session cap is set ~10% below it (soft-stop
	// headroom); the model is told its budget and nudged at thresholds.
	// It is the fallback for features without a budget envelope; a
	// feature with an envelope draws every stage from what's left of it
	// (§5.1 layer 3, see stageBudget).
	StageBudget float64
	// TurnReserve is one agent turn's worth of credits, the floor for
	// every envelope-derived budget (0 = domain.TurnReserveCredits).
	// Enforcement runs between turns, so smaller caps cannot be held.
	TurnReserve float64
	// Instructions are absolute paths to extra instruction files appended
	// to the workspace environment card, in user-then-workspace order.
	Instructions []string
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

	// wg tracks the pump and kickoff goroutines so Close can join them
	// before returning: a barrier for any filesystem touch (git subprocess
	// snapshots, persist writes) those goroutines may still be mid-way
	// through when teardown begins.
	wg sync.WaitGroup

	// mcpSeq is the atomic source of engine-side MCP call ids, so a
	// session's in-flight dispatches are unique and never collide with a
	// backend's own tool-call ids (disjoint namespaces).
	mcpSeq atomic.Uint64

	// persistMu serializes a session save against a delete of the same
	// feature: it spans persist's finalized-check-and-write and
	// persistDelete so an in-flight save can't land after the delete and
	// resurrect a dropped row.
	persistMu sync.Mutex

	// pool resolves each card to its repository's manager (see mgr).
	pool *worktree.Pool

	// dirtyPathsFn returns the sorted set of paths dirty on the card's own
	// main checkout. Bound at construction to the pool's ManagerFor(...)
	// MainDirtyPaths; it is the tripwire's sole injection point, so tests
	// can substitute a call-counter or fault-injecting closure without
	// reaching into the worktree package or swapping the concrete pool.
	dirtyPathsFn func(context.Context, *domain.Feature) ([]string, error)

	// envOnce reads and caches the workspace environment card once per
	// Engine lifetime; envCard holds the (possibly truncated) card text.
	// Editing the file requires an Engine restart.
	envOnce sync.Once
	envCard string
	// envMu guards envNotices buffered before a session exists to flush
	// them onto its activity feed. A nil envWarn disables warning
	// collection (tests); otherwise warnings are emitted at most once per
	// Engine lifetime because they sit inside envOnce.
	envMu      sync.Mutex
	envNotices []string
	envWarn    func(string)
}

// New builds an engine from the config. The caller owns every agent's
// lifetime. A single-repository config (Worktrees) is wrapped into a
// one-repo pool so every per-card path resolves uniformly.
func New(cfg Config) *Engine {
	if cfg.Permission == "" {
		cfg.Permission = agent.PermissionAllowAll
	}
	max := cfg.MaxActive
	if max < 1 {
		max = 1
	}
	pool := cfg.Pool
	if pool == nil && cfg.Worktrees != nil {
		pool = worktree.WrapSingle(cfg.Worktrees)
	}
	var dirtyPathsFn func(context.Context, *domain.Feature) ([]string, error)
	if pool != nil {
		dirtyPathsFn = func(ctx context.Context, f *domain.Feature) ([]string, error) {
			wt, err := pool.ManagerFor(ctx, f)
			if err != nil {
				return nil, err
			}
			return wt.MainDirtyPaths(ctx)
		}
	} else {
		dirtyPathsFn = func(_ context.Context, _ *domain.Feature) ([]string, error) {
			return nil, nil
		}
	}
	e := &Engine{
		cfg:          cfg,
		maxActive:    max,
		now:          time.Now,
		raw:          make(chan Event, 256),
		events:       make(chan Event),
		stopped:      make(chan struct{}),
		live:         map[domain.FeatureID]*Session{},
		pool:         pool,
		dirtyPathsFn: dirtyPathsFn,
	}
	e.envWarn = func(msg string) {
		e.envMu.Lock()
		e.envNotices = append(e.envNotices, msg)
		e.envMu.Unlock()
	}
	go e.forward()
	return e
}

// mgr resolves the manager for a card's repository. With a pool configured
// it routes through the pool (per-card); the single-manager wrap falls back
// to that manager.
func (e *Engine) mgr(ctx context.Context, f *domain.Feature) (*worktree.Manager, error) {
	if e.pool != nil {
		return e.pool.ManagerFor(ctx, f)
	}
	return e.cfg.Worktrees, nil
}

// ClientTools reports whether the default backend supports gummi's
// client tools, so hint compilers (engine- and UI-side) only mention a
// tool the agent can actually call. Callers that touch a specific role's
// backend should query that adapter's Capabilities() directly instead.
func (e *Engine) ClientTools() bool {
	a := e.defaultAgent()
	return a != nil && a.Capabilities().ClientTools
}

// WorktreesFor returns the worktree manager for a card's repository. It is
// exposed for one-shot commands (headless merge/clean) that perform worktree
// mutations directly but share the engine's manager and its lock.
func (e *Engine) WorktreesFor(ctx context.Context, f *domain.Feature) (*worktree.Manager, error) {
	return e.mgr(ctx, f)
}

// RepoKnown reports whether name is a configured managed repository (the
// empty name is the workspace default and is always known). Creation
// surfaces reject an unknown repo name at creation, before any drive-time
// resolution.
func (e *Engine) RepoKnown(name string) bool {
	if e.pool != nil {
		return e.pool.Known(name)
	}
	return name == ""
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

	// A run whose profile resolves to enforce must not start while any role
	// names a backend without tool coverage — feature-level, before any
	// session, queue slot, or engine event.
	if res := e.resolveSandbox(f); res.Mode == sandbox.ModeEnforce && len(res.Gaps) > 0 {
		return nil, &sandbox.RefusalError{Mode: res.Mode, Gaps: res.Gaps}
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
	sess, specPath, mcpTeardown, err := e.newAgentSession(ctx, f, role, 0, flavorStage)
	if err != nil {
		return nil, err
	}
	// The session's lifecycle context is bound to nothing: it is canceled by
	// Session.stop, not by the caller's ctx going away. Keep it distinct from
	// the caller's ctx so the initial kickoff Send stays on the caller's
	// cancellation semantics (only the tripwire snapshots switch to s.ctx).
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{Feature: f, Role: role, Interactive: true, state: StateInteractive, done: make(chan struct{}), ctx: sctx, cancel: cancel, specPath: specPath}
	s.setMCPTeardown(mcpTeardown)
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
	e.flushEnvNotices(s)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pump(s) }()
	var ko string
	if fresh {
		ko = interactiveKickoff(f)
		s.appendSystem(ko)
		s.setBusy(true)
	}
	e.persist(s)
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventStarted})
	if fresh {
		e.beforeTurn(s)
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
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return err
	}
	head, err := wt.MainHead(ctx)
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

	// Feature-level refusal at session start: enforce + any coverage gap
	// fails the whole run before any stage begins. No auto-degrade to warn,
	// no --force — the operator edits the profile to lift the guarantee.
	if res := e.resolveSandbox(f); res.Mode == sandbox.ModeEnforce && len(res.Gaps) > 0 {
		return &sandbox.RefusalError{Mode: res.Mode, Gaps: res.Gaps}
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
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{Feature: f, Role: role, Critique: flavor == flavorCritique, Rebase: flavor == flavorRebase, ReadOnly: researchReadOnly(f), state: StateQueued, done: make(chan struct{}), ctx: ctx, cancel: cancel, kickoffNote: note}
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
	// price this session's token spend at the resolved adapter's rate
	// (0 = default), and use the same rate for the remaining-envelope
	// baseline so the budget math is self-consistent.
	rc, backend := e.resolveRole(s.Feature.Profile, s.Role)
	rate := 0.0
	if a := e.agentFor(backend); a != nil {
		rate = a.CreditRate(rc.Model)
	}
	s.setByokRate(rate)
	// compute the stage budget once so the enforced cap, the budget-aware
	// hint, and the session's own budget all agree.
	budget := e.stageBudget(s.Feature, rate)
	// a budgeted feature with nothing left must not run uncapped (a 0
	// budget elsewhere means "unbudgeted"): gate it immediately.
	if s.Feature.Budget.Envelope > 0 && budget <= 0 && !interactiveStage(s.Feature.Stage) {
		e.exhaust(s)
		return
	}
	// A research autonomous stage runs in the main checkout (no worktree),
	// so the operator's pre-existing dirt is a hard stop before any
	// session: a dirty main is exactly the state the tripwire exists to
	// keep the agent out of, and with no session yet created nothing
	// spawns against it. Fail-open on a git error (like beforeTurn) so a
	// flaky snapshot never blocks a run; the mid-turn checkTrip remains
	// the armed layer.
	if s.ReadOnly {
		if paths, derr := e.dirtyPathsFn(s.ctx, &s.Feature); derr != nil {
			s.appendActivity("main-checkout tripwire: pre-start snapshot failed — skipping start check for this run: " + derr.Error())
		} else if len(paths) > 0 {
			e.trip(s, paths)
			return
		}
	}
	sess, specPath, mcpTeardown, err := e.newAgentSession(context.Background(), s.Feature, s.Role, budget, s.flavor())
	if err != nil {
		s.setError(err)
		s.setState(StatePaused)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		e.freeSlot(s)
		return
	}
	s.setSpecPath(specPath)
	s.setBudget(budget)
	s.setMCPTeardown(mcpTeardown)
	// Pause/Drop/Close may have finalized this session while newAgentSession
	// was spawning the backend (seconds). If so, attachAgent refuses: close
	// the orphaned agent and free the slot rather than run it unwatched.
	if !s.attachAgent(sess) {
		_ = sess.Close()
		e.freeSlot(s)
		return
	}
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pump(s) }()
	e.flushEnvNotices(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStarted})

	s.appendUser(s.kickoffMessage())
	s.setBusy(true)
	e.persist(s)
	// The kickoff is sent off the scheduler goroutine: the Verify stage
	// runs the repo's fixed checks gummi-side first, which can take
	// minutes, and must not stall slot scheduling.
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.sendKickoff(s, sess) }()
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
	if s.Feature.Stage == domain.StageVerify && !s.Rebase {
		// env probes run even in guarded mode — they are operator config
		// from .gummi/config.yaml, not agent-authored artifact checks.
		if pre := e.runEnvProbes(s); pre != "" {
			msg = pre + "\n\n" + msg
		}
		if s.Feature.Kind == domain.KindBug && hasCleanPresentProbe(s) {
			msg = armedVerifyNote + "\n\n" + msg
		}
		if e.cfg.Permission != agent.PermissionGuarded {
			if pre := e.runSpecChecks(s); pre != "" {
				msg = pre + "\n\n" + msg
			}
		}
	}
	e.beforeTurn(s)
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

// probeCleanPresent is a session-free counterpart of hasCleanPresentProbe:
// it loads the layered env config and runs envprobe.Run fresh against the
// feature's worktree, reporting whether any probe came back clean-present
// (Err nil and Present true). It does not mutate or persist session state.
func (e *Engine) probeCleanPresent(ctx context.Context, f *domain.Feature) bool {
	userPath, err := config.UserConfigPath()
	if err != nil {
		if e.envWarn != nil {
			e.envWarn(fmt.Sprintf("user config path could not be resolved: %v", err))
		}
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, e.cfg.Workspace.ConfigFile())
	if err != nil {
		return false
	}
	if len(cfg.Env) == 0 {
		return false
	}
	workDir := filepath.Join(e.pool.Root(), f.WorktreePath())
	for _, r := range envprobe.Run(ctx, workDir, cfg.Env) {
		if r.Err == nil && r.Present {
			return true
		}
	}
	return false
}

// runEnvProbes loads the layered user+workspace config, probes every
// declared environment prerequisite in the card's worktree, records each
// result in the activity feed, persists the snapshot, and returns a compact
// report block. Env probes run in all sandbox/permission modes because their
// command source is operator config from outside the worktree.
func (e *Engine) runEnvProbes(s *Session) string {
	userPath, err := config.UserConfigPath()
	if err != nil {
		if e.envWarn != nil {
			e.envWarn(fmt.Sprintf("user config path could not be resolved: %v", err))
		}
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, e.cfg.Workspace.ConfigFile())
	if err != nil {
		msg := "Environment prerequisites could not be probed: " + err.Error()
		s.appendActivity(msg)
		e.persist(s)
		return msg
	}
	if len(cfg.Env) == 0 {
		return ""
	}
	workDir := filepath.Join(e.pool.Root(), s.Feature.WorktreePath())
	results := envprobe.Run(context.Background(), workDir, cfg.Env)
	s.mu.Lock()
	s.envProbes = results
	s.mu.Unlock()
	for _, r := range results {
		ok := r.Err == nil && r.Present
		s.appendToolDone(fmt.Sprintf("env %s: %s", r.Name, envprobe.StatusString(r)), ok, r.Output)
	}
	e.persist(s)
	return "Environment prerequisites probed in this worktree:\n" + envprobe.FormatReport(results)
}

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
	workDir := filepath.Join(e.pool.Root(), s.Feature.WorktreePath())
	ctx, cancel := context.WithTimeout(context.Background(), verifyStageTimeout)
	defer cancel()
	// The shared context bounds the whole set; a per-check bound stops one
	// hung command from consuming it and starving the rest (verify.CheckTimeout).
	results := verify.RunBounded(ctx, workDir, checks, verify.CheckTimeout)

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
		var status string
		switch r.Status {
		case verify.StatusPass:
			status = "pass"
		case verify.StatusTimeout:
			status = "TIMEOUT (killed by deadline)"
		case verify.StatusNotRun:
			status = "NOT RUN (check budget exhausted)"
		default:
			if base, ok := baseline[r.Name]; ok && base.Cmd == r.Cmd && !base.OK {
				status = fmt.Sprintf("FAIL (pre-existing, exit %d)", r.ExitCode)
				preexisting = true
			} else {
				status = fmt.Sprintf("FAIL (exit %d)", r.ExitCode)
			}
		}
		s.appendToolDone(fmt.Sprintf("check %s: %s", r.Name, status), r.OK, r.Output)
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, status)
		if !r.OK && len(r.Output) > 0 {
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

// stampSpawnInfo records on the session which backend and model its
// profile/role resolve to — and the adapter's token→credit rate — so
// status displays (and interactive budget math) have them from the
// moment the session exists, not after the first usage event.
func (e *Engine) stampSpawnInfo(s *Session) {
	rc, backend := e.resolveRole(s.Feature.Profile, s.Role)
	name := ""
	rate := 0.0
	clientTools := false
	if a := e.agentFor(backend); a != nil {
		name = a.Name()
		rate = a.CreditRate(rc.Model)
		clientTools = a.Capabilities().ClientTools
	}
	s.setSpawnInfo(name, rc.Model, clientTools)
	s.setByokRate(rate)
	s.setSandboxMode(e.resolveSandbox(s.Feature).Mode)
}

// newAgentSession builds an agent session for a feature's stage, with
// the backend/model chosen by the feature's profile for this role. It
// also returns the resolved spec path so the caller can record it on the
// Session (ask_user answer capture writes there), and — when the resolved
// backend cannot call client tools — the MCP inbound-endpoint teardown
// stub, so the caller can bind it to the Session's lifecycle before the
// child inherits GUMMI_MCP_SOCK.
func (e *Engine) newAgentSession(ctx context.Context, f domain.Feature, role agent.Role, budget float64, flavor runFlavor) (agent.Session, string, func(), error) {
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, "", nil, err
	}
	rc, backend := e.resolveRole(f.Profile, role)
	ag := e.agentFor(backend)
	if ag == nil {
		if backend == "" {
			return nil, "", nil, fmt.Errorf("no agent configured for feature %s stage %s", f.ID, f.Stage)
		}
		return nil, "", nil, fmt.Errorf("no agent registered for backend %q (feature %s role %s)", backend, f.ID, role)
	}
	// An autonomous research session runs in the main checkout with the
	// artifact at its workspace home — no worktree, so a read-write agent
	// could mutate the operator's repo. Fail closed: a backend that cannot
	// structurally strip its write tools (copilot, headless, codex) is
	// refused here, before any session, so "documented no-op" can never
	// silently downgrade the read-only guarantee to the tripwire alone.
	readOnly := researchReadOnly(f)
	if readOnly && !ag.Capabilities().ReadOnlyEnforce {
		return nil, "", nil, fmt.Errorf("backend %q cannot enforce a read-only research session "+
			"(feature %s stage %s); point this role at `claude` or `opencode`, or accept that "+
			"autonomous research cannot run on that backend", backend, f.ID, f.Stage)
	}
	hints := stageHints(f, specPath, flavor)
	if card := e.environmentCard(); card != "" {
		hints = append([]string{card}, hints...)
	}
	// implementation runs carry any open diff review comments so a fix-up
	// (bounce from the diff surface's "request changes") addresses each
	// (DESIGN §6.1). The store is the source of truth, so this reaches
	// every implement run, not just the one that triggered it.
	if flavor == flavorStage && (f.Stage == domain.StageImplement || f.Stage == domain.StageFix) {
		hints = append(hints, e.diffReviewHints(ctx, f.ID, ag.Capabilities().ClientTools)...)
	}
	var maxCredits float64
	// autonomous stages get a budget cap + budget-aware hint (interactive
	// chat is human-paced, so it isn't capped). Envelope-derived budgets
	// arrive already floored at one turn's reserve (stageBudget), so the
	// enforced cap is never an un-holdable sliver.
	if budget > 0 && !interactiveStage(f.Stage) {
		maxCredits = budget * capHeadroom
		// Read-mostly stages don't edit files: critique judges the plan,
		// verify runs the artifact's checks. The write-focused hint
		// pulls them in the wrong direction (critique needs breadth to
		// walk closure tables; verify never batches edits).
		if flavor == flavorCritique || f.Stage == domain.StageVerify {
			hints = append(hints, budgetHintReadMostly(budget))
		} else {
			hints = append(hints, budgetHint(budget))
		}
	}
	// gummi-owned client tools per stage. When the resolved backend
	// supports them, register the tools and tell the agent they exist;
	// otherwise fall back to prompt conventions (ask_user has a fenced-
	// block convention; spec_annotate and submit_verdict degrade to the
	// %% and VERDICT: text forms the stage hints already describe).
	// A backend that reaches gummi's tools over MCP is told they exist the
	// same way, so its stage sessions still receive the toolHint.
	var tools []agent.ToolDef
	if caps := ag.Capabilities(); caps.ClientTools || caps.MCPTools {
		tools = filterReadOnlyTools(stageTools(f.Stage, flavor), readOnly)
		// A read-only session's standard toolHint would describe the
		// stripped spec_replace_section; the research stage hints carry
		// the read-only surface instead.
		if h := toolHint(f.Stage, flavor); h != "" && !readOnly {
			hints = append(hints, h)
		}
	} else if interactiveStage(f.Stage) {
		hints = append(hints, askConventionHint)
	}
	// Every stage session gets its own inbound MCP endpoint, so a backend
	// that consumes gummi's tools over MCP (rather than opts.Tools) has a
	// socket to dial; one whose transport hasn't landed simply ignores it.
	// The endpoint is bound before spawning the child so a child that dials
	// on start never races the bind. The teardown is returned for the caller
	// to stash on the Session's lifecycle; on any failure below the endpoint
	// is released here, so callers see a nil teardown alongside an error.
	mcpPath, mcpTeardown, err := e.startMCPEndpoint(ctx, f, flavor, readOnly)
	if err != nil {
		return nil, "", nil, err
	}
	sess, specErr := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:        workDir,
		ArtifactPath:   specPath,
		Role:           role,
		Model:          rc.Model,
		SystemHints:    hints,
		Permission:     e.cfg.Permission,
		MaxCredits:     maxCredits,
		Tools:          tools,
		OutputTokenMax: rc.OutputTokenMax,
		Provider:       rc.Provider,
		MCPSockPath:    mcpPath,
		FeatureID:      string(f.ID),
		ReadOnly:       readOnly,
	})
	if specErr != nil {
		mcpTeardown()
		return nil, "", nil, fmt.Errorf("starting %s session: %w", role, specErr)
	}
	return sess, specPath, mcpTeardown, nil
}

// recoverMissingWorktree rebuilds a work-stage feature's worktree after it
// vanished out from under an active branch (an environment or sandbox
// filesystem glitch, not a clean Remove) — the self-heal the operator
// otherwise has to perform by hand. It refuses when the feature
// has no recorded fork point: recreating from current main would silently
// re-anchor the branch onto a base it never actually forked from, which
// AssertNoForkDrift exists specifically to catch elsewhere.
func (e *Engine) recoverMissingWorktree(ctx context.Context, wt *worktree.Manager, f *domain.Feature) error {
	fork, err := wt.ForkPoint(ctx, f)
	if err != nil {
		return err
	}
	if fork == "" {
		return errors.New("no recorded fork point to recover from")
	}
	_, err = wt.Recreate(ctx, f)
	return err
}

// locate resolves the working directory and spec path for a feature's
// stage. Interactive pre-worktree stages run in the main checkout
// against the draft — materialized here so the agent never starts
// against a missing spec. Later stages require the worktree but read and
// write the artifact at its workspace home in the main checkout
// (.gummi/specs|bugs, never committed) — promoted here in case a crash
// or a legacy committed-artifact item left promotion undone.
func (e *Engine) locate(ctx context.Context, f domain.Feature) (workDir, specPath string, err error) {
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return "", "", err
	}
	root := e.pool.Root()
	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	// Every research stage — interactive (shape) or autonomous (investigate/
	// review/verify) — is worktree-less and runs in the main checkout
	// against the artifact at its workspace home. Research has no
	// draft-then-promote step (Create seeds the artifact directly, never a
	// draft), and never enters a worktree — the only path that promotes a
	// draft into its artifact — so routing shape through a draft the way
	// brainstorm/spec do would orphan its edits: nothing ever merges them
	// back. Promote here is a no-op cleanup once the artifact exists (the
	// common case); it only materializes a fresh one for a crash-recovery
	// or legacy edge case.
	if f.Kind == domain.KindResearch {
		artifact := filepath.Join(root, f.ArtifactPath())
		if err := spec.Promote(artifact, draft, "", &f); err != nil {
			return "", "", err
		}
		return wt.RepoRoot(), artifact, nil
	}
	if interactiveStage(f.Stage) {
		if err := spec.EnsureDraft(draft, &f); err != nil {
			return "", "", err
		}
		// interactive stages run in the repo root (the main checkout), not
		// the workspace root: the repo may live in a nested subdirectory.
		return wt.RepoRoot(), draft, nil
	}
	hasWT, err := wt.Exists(ctx, &f)
	if err != nil {
		return "", "", err
	}
	if !hasWT {
		if rerr := e.recoverMissingWorktree(ctx, wt, &f); rerr != nil {
			return "", "", fmt.Errorf("feature %s at stage %s has no worktree and could not be recreated (%v); recreate .gummi/worktrees/%s from the feature's branch manually, or approve the spec again if the design phase was never completed", f.ID, f.Stage, rerr, f.ID)
		}
	}
	// A rewrite of main reported after the worktree was created makes the
	// on-disk branch's base incoherent with main; refuse before promoting the
	// artifact or handing the agent a workdir it can only deepen the
	// divergence in. The operator recreates the worktree from current main.
	if err := wt.AssertNoForkDrift(ctx, &f); err != nil {
		return "", "", err
	}
	workDir = filepath.Join(root, f.WorktreePath())
	artifact := filepath.Join(root, f.ArtifactPath())
	if err := spec.Promote(artifact, draft, filepath.Join(workDir, f.ArtifactPath()), &f); err != nil {
		return "", "", err
	}
	return workDir, artifact, nil
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
	// deliver any queued budget nudge before the orchestrator's own text
	// (DESIGN §5.1 layer 2: the mid-session threshold is folded into the
	// next turn rather than injected mid-flight).
	if n := s.takePendingNudge(); n != "" {
		msg = n + "\n\n" + msg
	}
	s.appendUser(msg)
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})
	e.beforeTurn(s)
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
// the resumed stage always has real multi-turn headroom rather than a
// sliver.
//
// The spend is priced at the default credit rate here; an adapter with
// a much higher per-token rate can still re-gate after a top-up, since
// the stage-budget math at session start prices spend at that adapter's
// rate.
func (e *Engine) TopUp(ctx context.Context, id domain.FeatureID) error {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if f.Budget.Envelope > 0 {
		raised := f.Budget.RaisedEnvelope(f.Spend.CreditEquivalent())
		if int(raised) > f.Budget.Envelope {
			f.Budget.Envelope = int(raised)
			if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
				return err
			}
		}
	}
	return e.Run(f)
}

// RaiseEnvelope durably sets a feature's envelope to an explicit credit
// figure — the proactive counterpart of TopUp's automatic raise. It only
// persists: no stage is resumed, and a running session keeps the cap it
// was spawned with (stage budgets re-read the envelope at session
// start). The figure is validated against EnvelopeFloor so the next
// stage cannot gate immediately; any figure above the floor is
// accepted, so a too-generous envelope can also be tightened. Zero
// removes the cap.
func (e *Engine) RaiseEnvelope(ctx context.Context, id domain.FeatureID, to int) error {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if to != 0 {
		if floor := int(math.Ceil(domain.EnvelopeFloor(f.Spend.CreditEquivalent()))); to < floor {
			return fmt.Errorf("%s: %d credits is below the %d-credit floor (spend plus resume headroom)", id, to, floor)
		}
	}
	f.Budget.Envelope = to
	return e.cfg.Store.UpdateFeature(ctx, &f)
}

// beforeTurn snapshots main's dirty set immediately before a Send hands
// work to the agent, arming the tripwire's post-turn comparison. On a
// MainDirtyPaths error it records a diagnostic activity line and skips
// the snapshot (s.beginTurn is not called), so takePreTurn reports "unset"
// at post-turn and checkTrip returns nil — fail-open: a broken git skips
// the trip decision for that turn rather than misattributing the
// operator's pre-existing dirt to the agent.
func (e *Engine) beforeTurn(s *Session) {
	// An off-mode run arms nothing: skip the pre-turn snapshot entirely, so
	// the tripwire is genuinely disarmed (checkTrip short-circuits too).
	if s.SandboxMode() == sandbox.ModeOff {
		return
	}
	paths, err := e.dirtyPathsFn(s.ctx, &s.Feature)
	if err != nil {
		s.appendActivity("main-checkout tripwire: pre-turn snapshot failed — skipping trip check for this turn: " + err.Error())
		return
	}
	s.beginTurn(paths)
}

// checkTrip compares the post-turn dirty set against the pre-turn
// snapshot, returning the newly-dirty paths (sorted) when the agent made
// a clean→dirty transition. It returns nil — no trip — when no pre-turn
// snapshot was taken this turn (takePreTurn "unset": a resumed session, a
// race, or a pre-turn snapshot error), or when the post-turn call itself
// errors (a diagnostic activity line records the git flake). A missing
// pair thus fails safe rather than tripping on a spurious empty pre-set.
func (e *Engine) checkTrip(s *Session) []string {
	if s.SandboxMode() == sandbox.ModeOff {
		return nil
	}
	pre := s.takePreTurn()
	if pre == nil {
		return nil
	}
	post, err := e.dirtyPathsFn(s.ctx, &s.Feature)
	if err != nil {
		s.appendActivity("main-checkout tripwire: post-turn snapshot failed — checking skipped: " + err.Error())
		return nil
	}
	var delta []string
	for _, p := range post {
		if _, ok := pre[p]; !ok {
			delta = append(delta, p)
		}
	}
	return delta
}

// trip aborts a session on a main-checkout tripwire hit: the agent
// dirtied paths that were clean before its turn. It is a hard stop — the
// run is dead, no top-up, no resume. The working tree is left exactly as
// the agent left it (no settle, no revert, no checkpoint commit): only
// engine-internal state changes (activity line, session state, slot
// release). The operator resolves the main dirt and re-runs the stage.
func (e *Engine) trip(s *Session, paths []string) {
	if !s.markTripped() {
		return // already tripped; a stale event must not duplicate the abort
	}
	s.appendActivity("main-checkout tripwire: new dirty paths — " + strings.Join(paths, ", "))
	s.setState(StateDone)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventTripwire, DirtyPaths: paths})
	s.stop() // finalizes the session, closing the underlying agent: a follow-up Send fails at a.Send
	e.freeSlot(s)
}

// exhaust checkpoints and stops a session that hit its credit budget —
// whether the CLI reported it or gummi-side enforcement tripped first —
// moving it to the needs-attention queue (never a silent death). When the
// budget is reached at a stage that already committed its work (a
// wrap-up exhaustion — the cap arithmetic tips over after the deliverable
// is on the branch), the park says so instead of reading like lost work:
// the top-up affordance stays, but the message reflects that nothing was
// stranded.
func (e *Engine) exhaust(s *Session) {
	if !s.markExhausted() {
		return // already checkpointed; a re-raised event must not duplicate the gate
	}
	// partial work survives on the branch across the gate; a fatal
	// (worktree-gone) checkpoint is reported below via stageWorkCommitted
	// returning false rather than by failing the run — exhaustion parks
	// the stage for review either way, it never advances it (unlike the
	// EventIdle completion path settle also serves).
	_ = e.settle(s)
	committed := e.stageWorkCommitted(s)
	if committed {
		s.appendActivity("budget reached — stage work committed, ready to review")
	} else {
		s.appendActivity("budget exhausted — stage stopped for review")
	}
	s.setState(StateDone)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventExhausted, Committed: committed})
	e.freeSlot(s)
}

// stageWorkCommitted reports whether the exhausted stage left its work
// safely committed — a submitted review verdict, or (after settle's
// checkpoint) committed branch commits with a clean worktree. Best-effort
// and conservative: any uncertainty (a git error, a rebase pass, an
// interactive stage) reports false, so the park keeps its cautious
// "stopped for review" wording rather than falsely claiming work is safe.
func (e *Engine) stageWorkCommitted(s *Session) bool {
	if s.Interactive || s.Rebase {
		return false
	}
	if s.Snapshot().Verdict != "" {
		return true // review/critique delivered its verdict
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	wt, err := e.mgr(ctx, &s.Feature)
	if err != nil {
		return false
	}
	dirty, err := wt.Dirty(ctx, &s.Feature)
	if err != nil || dirty {
		return false // uncommitted work remains, or can't tell
	}
	ahead, err := wt.BranchAhead(ctx, &s.Feature)
	return err == nil && ahead
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
	// Join the pump and kickoff goroutines so no git subprocess or persist
	// write is still in flight against the workspace when Close returns.
	e.wg.Wait()
	close(e.stopped)
	return nil
}

// errSessionDied reports an agent session whose event stream ended without
// a terminal turn (no Idle, no Error): the backend process died mid-flight
// rather than finishing or failing cleanly.
var errSessionDied = errors.New("agent session died without finishing")

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
				// The agent's event stream ended without a terminal turn.
				// Distinguish a genuine backend death — the session was still
				// running, so it never reached Idle/Error — from a benign
				// teardown (replace/drop/pause set a terminal state before
				// stopping the agent, so the stream closing there is expected).
				// A death surfaces as an error so the driver escalates promptly
				// instead of hanging out the whole stage timeout and misreading
				// a dead agent as a backend stall.
				// A backend death is not a teardown: nothing will run the
				// session's MCP teardown to release in-flight bridge calls,
				// so their liveness flag would otherwise stay stuck at
				// "waiting" forever and Answer would keep delivering into a
				// channel nobody will ever read. Clear every waiter's flag
				// so Answer treats the in-flight calls as gone and fails
				// loudly instead of silently succeeding into the void.
				s.clearResolversWaiting()
				if s.State() == StateRunning {
					e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: errSessionDied})
				}
				e.emitStopped(s)
				return
			}
			// Process-backed adapters learn their durable conversation id from
			// the first backend event, after NewSession has returned.
			if id, ok := s.agent().(agent.Identified); ok {
				s.setAgentSessionID(id.SessionID())
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
		// the credit-equivalent (not raw credits): a token-only stage reports
		// tokens with zero credits, and storing that raw would make its spend
		// invisible to the credits-denominated envelope once any credits-
		// metered stage contributed credits. Metered events already carry
		// credits, so this leaves them unchanged and never double-counts.
		if e.cfg.Persist && e.cfg.Store != nil {
			credits := s.creditEquivalent(ev.Usage)
			// estimated is the token/rate-derived portion of credits, kept as
			// its own accumulator so displays can label live figures instead
			// of presenting them as real. A settle event retires the model's
			// outstanding estimates: the adapter's own mid-turn estimates are
			// already inside its signed correction, while the engine's
			// token-priced fallback (recorded before the adapter knew a rate)
			// is not — so that portion comes off the credit total here too,
			// leaving exactly the provider-metered figure.
			var estimated float64
			switch {
			case ev.Usage.Settled:
				credits = ev.Usage.Credits // signed correction; never token-priced
				tokenEst, adapterEst := s.takePendingEst(ev.Usage.Model)
				credits -= tokenEst
				estimated = -(tokenEst + adapterEst)
			case ev.Usage.Metered:
				// the provider's metered figure, authoritative even at zero:
				// token-pricing it would invent spend the provider never
				// charged (and a later settle delta would then double-count
				// it), so it passes through signed with no estimate booked.
				credits = ev.Usage.Credits
			case ev.Usage.Credits <= 0:
				estimated = credits
				s.notePendingEst(ev.Usage.Model, credits, 0)
			case ev.Usage.Estimate:
				estimated = credits
				s.notePendingEst(ev.Usage.Model, 0, credits)
			}
			if credits != 0 || estimated != 0 || ev.Usage.InputTokens != 0 || ev.Usage.OutputTokens != 0 {
				_ = e.cfg.Store.AddSpend(context.Background(), s.Feature.ID,
					credits, estimated, ev.Usage.InputTokens, ev.Usage.OutputTokens)
				// the same sample attributed to (stage, model, role) for the
				// breakdown; same credit-equivalent, so stage_spend sums to
				// spend_credits. A backend's internal side-model call is booked
				// to the helper role, not the stage role it ran under — else a
				// token-less title/summary call inflates and mis-attributes the
				// working role's row.
				role := s.Role
				if ev.Usage.Helper {
					role = agent.RoleHelper
				}
				_ = e.cfg.Store.RecordStageSpend(context.Background(), s.Feature.ID,
					s.Feature.Stage, string(role), ev.Usage.Model,
					credits, estimated, ev.Usage.InputTokens, ev.Usage.CachedTokens, ev.Usage.OutputTokens)
			}
		}
		// budget awareness: on crossing a threshold, record a nudge, queue
		// it for the next turn sent to the model, and signal the UI
		// (DESIGN §5.1 layer 2).
		if pct, spent := s.crossedThreshold(); pct > 0 {
			s.appendActivity(nudge(pct, spent, s.Budget()))
			s.queueNudge(nudge(pct, spent, s.Budget()))
			e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventBudget, Threshold: pct})
		}
		// gummi-side enforcement: interrupt and checkpoint once spend
		// reaches the budget (covers token-only backends and sub-floor
		// budgets the CLI cap can't).
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
		// the turn made a clean→dirty transition on main: abort the run
		// before any "finished" gate can read it as healthy.
		if paths := e.checkTrip(s); len(paths) > 0 {
			e.trip(s, paths)
			return
		}
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
			// a fatal settle (the worktree itself is gone, not just dirty or
			// uncommitted) must fail the run instead of reading as a clean
			// finish — otherwise the caller advances the stage with no
			// worktree left to review it against. failRun already frees the
			// slot and sends its own terminal event, so return here rather
			// than fall into the idle-finish send below.
			if err := e.settle(s); err != nil {
				e.failRun(s, err)
				return
			}
			e.stageReceipt(s)
			e.gateVerifyVerdict(s)
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
// invariant. Returns a non-nil error only for checkpoint's one fatal
// case (the worktree itself is gone) — every other failure stays
// best-effort and surfaces through the activity feed instead.
func (e *Engine) settle(s *Session) error {
	if !s.Rebase {
		return e.checkpoint(s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	wt, err := e.mgr(ctx, &s.Feature)
	if err != nil {
		s.appendActivity("rebase cleanup failed: " + err.Error())
		return nil
	}
	aborted, err := wt.AbortRebase(ctx, &s.Feature)
	if err != nil {
		s.appendActivity("rebase cleanup failed: " + err.Error())
		return nil
	}
	if aborted {
		s.appendActivity("rebase left mid-flight — aborted, worktree restored")
	}
	return nil
}

// checkpoint commits whatever the stage left in the feature's worktree
// to its branch, so agent work is never stranded uncommitted (DESIGN:
// gummi owns the branch's commits; the user lands it as one squash
// commit, so checkpoint granularity never reaches main's history). It
// runs as an autonomous turn completes and at the budget-exhaustion
// gate. Best-effort for every failure but one: a missing worktree means
// there are no leftovers on disk for the merge flow to pick up later, so
// that case is reported back instead of swallowed — callers on the
// completion path (not the exhaustion gate, which never advances a
// stage) must fail the run rather than let it read as a clean finish.
func (e *Engine) checkpoint(s *Session) error {
	if s.Interactive || interactiveStage(s.Feature.Stage) {
		return nil // design-phase chats run in the main checkout; never auto-commit there
	}
	// Research stages are worktree-less by design (workflow.NeedsWorktree),
	// not merely worktree-less because one went missing — CommitAll's
	// ErrNoWorktree there is the expected, permanently-benign case, never
	// the total-loss one this function otherwise treats as fatal.
	needsWT := workflow.NeedsWorktree(s.Feature.Kind, s.Feature.Stage)
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	msg := fmt.Sprintf("%s: %s checkpoint", s.Feature.ID, s.Feature.Stage)
	wt, err := e.mgr(ctx, &s.Feature)
	if err != nil {
		s.appendActivity("checkpoint commit failed: " + err.Error())
		return nil
	}
	committed, err := wt.CommitAll(ctx, &s.Feature, msg)
	if err != nil {
		s.appendActivity("checkpoint commit failed: " + err.Error())
		if needsWT && errors.Is(err, worktree.ErrNoWorktree) {
			return err
		}
		return nil
	}
	if committed {
		s.appendActivity("worktree committed: " + msg)
	}
	return nil
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
	seen := make(map[string]bool)
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
		if r.Model != "" && !seen[r.Model] {
			seen[r.Model] = true
			models++
		}
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
