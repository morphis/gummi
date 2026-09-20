// Package engine is gummi's orchestrator: it binds features' stages to
// agent sessions, schedules autonomous runs (DESIGN §4.2), routes turns,
// and streams typed activity to the UI.
//
// Interactive sessions (brainstorm/spec chat) run whenever you attach
// and hold no slot — you are the scarce resource. Autonomous sessions
// (plan/implement/review/verify) compete for one of two independent
// attention pools: attended (a card whose gate-approval mode reads as
// domain.GateAttended — a human is expected to stay with it, and the
// empty default reads that way too) and autopilot (domain.GateAutopilot,
// and only that). Each pool has its own
// cap and its own FIFO queue, so a slot freed in one pool is never handed
// to a session waiting in the other — an attended card never queues
// behind autopilot work. Excess runs queue and start automatically as
// slots free (a session freeing its slot on pause, or on going idle when
// its turn completes). A pool's cap of 0 means uncapped.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/sandbox"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/substrate"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
)

// kickoff is the go-ahead sent to start an autonomous stage; the stage
// hints already tell the agent what to do.
//
// It names the card's own document — a bug's report, a research card's
// research document — rather than always saying "spec": the agent is
// being told to go read a file whose name and sections belong to the
// kind, and the thread renders this line back to the reader, who sees
// the same document named correctly everywhere else on the page.
func kickoff(k domain.Kind) string {
	return "Proceed with this stage per your instructions and the " + k.ArtifactNoun() + "."
}

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

// designKickoff opens a fresh design session with the agent leading — the
// user shouldn't have to know what to say to start (DESIGN §3).
//
// One opener where there were five. Brainstorm, spec, triage, diagnose
// and shape each had their own because each was its own stage; they are
// one stage now, and what differs between a feature, a bug and a research
// topic is the KIND, which the opener names rather than the stage.
//
// The MODE decides the rest of it. The opener used to say "the user just
// opened the design chat" and "put the most consequential open question to
// the user first" to every card, autopilot included, where both halves are
// false: nobody opened anything, and the question is answered by the card's
// own recommendation without a person ever seeing it. It is the first thing
// a session reads, so it won: on the lxd autopilot drive all four cards
// opened by asking, and all four were bounced by the ask toll — a tool call
// and a model turn each, spent to be told not to ask. Two other mechanisms
// (unattendedAskHint, askChangesSomething) existed to undo what this
// sentence had just instructed.
//
// So an unattended card is told what is true. It is not told to stop
// asking — unattendedAskHint still invites a real decision to be recorded
// as a question, which is what the receipt is built from — only to stop
// opening with one addressed to a reader who is not there.
func designKickoff(f domain.Feature) string {
	if f.GateApproval == domain.GateAutopilot {
		return unattendedDesignKickoff(f)
	}
	switch f.Kind {
	case domain.KindBug:
		return "The user just opened the design chat on a bug. Read the report, try to " +
			"reproduce it, and report what you found. Then drive toward root cause: state your " +
			"leading hypothesis with your reasoning, and put the most consequential open " +
			"question to the user first, with your recommended answer. Keep it short."
	case domain.KindResearch:
		return "The user just opened the design chat on a research topic. Read the research " +
			"artifact at its workspace home, then shape the question with them: recommend " +
			"how to scope it and which direction the survey should take, and put the most " +
			"consequential open question to the user first. Keep it short."
	default:
		return "The user just opened the design chat. Read the spec draft and its open %% " +
			"threads, then drive convergence: state the problem as you understand it, " +
			"recommend one approach with your reasoning, and put the most consequential open " +
			"question to the user first, with your recommended answer. Keep chat turns short; " +
			"the detail belongs in the spec."
	}
}

// unattendedDesignKickoff is designKickoff for a card running on
// autopilot: same job, same artifact, no reader. Each one ends where its
// attended twin ends — with the decision written down — because the
// artifact is the only place an unattended decision can go.
const unattendedPreamble = "This card is running unattended: no one is at the keyboard, and " +
	"the design stage will be judged on what it writes, not on what it asks. "

func unattendedDesignKickoff(f domain.Feature) string {
	switch f.Kind {
	case domain.KindBug:
		return unattendedPreamble + "Read the report, try to reproduce it, and report what " +
			"you found. Then drive toward root cause: state your leading hypothesis with the " +
			"evidence for it, and write the diagnosis into the report. Keep it short."
	case domain.KindResearch:
		return unattendedPreamble + "Read the research artifact at its workspace home, then " +
			"shape the question yourself: scope it, fix the constraints and success criteria " +
			"it must meet, and pick the direction the survey will take, writing each decision " +
			"into the document as it settles. Keep it short."
	default:
		return unattendedPreamble + "Read the spec draft and its open %% threads, then drive " +
			"convergence: state the problem as you understand it, choose one approach with " +
			"your reasoning, and write the decision into the spec where a reader will find it " +
			"afterwards. Keep turns short; the detail belongs in the spec."
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
	Pool      *worktree.Pool
	Workspace state.Workspace
	// CardLocks makes this engine take the workspace's per-card lock for
	// every card it drives, so a headless run/resume/merge/clean for the
	// same card is refused instead of racing it (and vice versa). The
	// board sets it — it is the long-lived process that drives many cards
	// at once. Nil (the headless driver, which already holds the card's
	// lock around the whole command, and tests) disables card locking.
	CardLocks  *state.CardLocks
	Model      string
	Permission agent.Permission
	// Sandbox is the workspace-wide confinement mode (enforce|warn|off),
	// taken from .gummi/config.yaml. Empty means a profile that also omits
	// a value falls back to the built-in default (warn).
	Sandbox string
	// MaxActive caps concurrent autonomous slots in the ATTENDED pool — a
	// card whose gate-approval mode reads as domain.GateAttended, which
	// includes the empty default (domain.Feature.GateMode). Zero or
	// negative — the default — means no cap: every attended run started
	// begins immediately. cmd/gummi's own default for this field is 1;
	// GUMMI_MAX_ACTIVE overrides it from there. A positive value queues
	// attended runs beyond it.
	MaxActive int
	// AutopilotLanes caps concurrent autonomous slots in the AUTOPILOT
	// pool — every card whose gate-approval mode is domain.GateAutopilot,
	// and only those: the empty default reads as attended and competes in
	// the other pool (see domain.Feature.GateMode). Zero or negative means no cap, the
	// same "unlimited" semantics MaxActive has always had — kept
	// available here for tests and any caller that wants both pools
	// uncapped. internal/config.Config's autopilot_lanes key supplies
	// cmd/gummi's real default (2).
	AutopilotLanes int
	// StageTimeout is how long a stage may be silent before its driver
	// cuts it off (the headless --stage-timeout; zero disables it). The
	// engine does not enforce it — the driver does — but a goal's quiet
	// backstop has to stay clear of it, or a stage the operator gave
	// longer than goalpolicy.MaxQuiet to be silent in trips a backstop
	// meant for goals that have genuinely stopped.
	StageTimeout time.Duration
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

// lanePool identifies which of the two attention pools an autonomous
// session competes in. See lanePoolFor.
type lanePool int

const (
	poolAttended lanePool = iota
	poolAutopilot
	numLanePools
)

// laneState is one pool's scheduling bookkeeping: its cap (0 =
// uncapped), how many sessions currently hold a slot, and the FIFO of
// features waiting for one. Each pool schedules from its own queue —
// see Engine.schedule — so a slot freed in one pool is never handed to a
// session waiting in the other.
type laneState struct {
	max     int
	running int
	queue   []domain.FeatureID // autonomous features awaiting this pool's slot, FIFO
}

// lanePoolFor decides which attention pool an autonomous session for f
// competes in. Attended is the mode that reads as attended: a human is
// expected to stay with that card, so it must never queue behind
// unattended work. Only GateAutopilot belongs in the autopilot pool.
//
// The test goes through GateMode(), never the raw field: an unset
// GateApproval is storable and documented as reading like GateAttended,
// and comparing the raw string put every one of those cards — every card
// `bugs new` and the GitHub import ever minted — in the autopilot pool
// while its own card page read "autopilot: off".
func lanePoolFor(f domain.Feature) lanePool { return lanePoolForMode(f.GateMode()) }

// lanePoolForMode is lanePoolFor over a bare mode string, for the callers
// that have the mode a card was just given rather than the row it was
// written to (Repool). It applies GateMode's own rule — only
// GateAutopilot is autopilot, everything else including the empty default
// is attended — so a raw stored value is safe to pass.
func lanePoolForMode(mode string) lanePool {
	if mode == domain.GateAutopilot {
		return poolAutopilot
	}
	return poolAttended
}

// Engine orchestrates all live sessions and the autonomous run queue.
type Engine struct {
	cfg Config
	now func() time.Time // injectable clock (spec-capture timestamps)

	// raw carries events from pump goroutines to the forwarder; events
	// is the UI-facing stream, owned solely by the forwarder.
	raw     chan Event
	events  chan Event
	stopped chan struct{}

	mu   sync.Mutex
	live map[domain.FeatureID]*Session
	// oneShot counts the engine's session-less passes currently running
	// per card — check discovery and its baseline. They are the only work
	// a card does that e.live cannot see, and on a repository the size of
	// lxd they run for minutes: 4.6 on one drive's card, 14–25% of what
	// the card cost. Everything that asks "is this card still working?"
	// asked e.live and was told no, so the board rendered a live card as
	// "autopilot stopped without saying so" and offered to restart the
	// stage, its footer counted 0 of 2 autopilot lanes in use, and a goal
	// declared its own healthy child stuck and spent a lead turn
	// restarting it. Refcounted, not a flag: baseline follows discovery
	// and a card can be re-entered.
	oneShots map[domain.FeatureID]int
	lanes    [numLanePools]laneState // per-pool cap/running/queue — see laneState
	// board is the engine's single workspace-scoped agent session (see
	// boardsession.go), or nil until OpenBoard is first called. Unlike a
	// card's session it takes no attention slot and needs no lane
	// bookkeeping, so it lives beside e.live rather than inside it.
	board  *BoardSession
	closed bool

	// consult holds every card's consult session, keyed by feature —
	// engine.ConsultSession's own map, mirroring board's single-entry
	// field but per card: OpenConsult is idempotent per card for the
	// engine's whole lifetime (see consultsession.go), so once a card's
	// entry exists here it is reused, never replaced, until Close.
	consult map[domain.FeatureID]*ConsultSession
	// consultMu serializes OpenConsult end to end, the same job boardMu
	// does for OpenBoard and for the same reason: spawning a backend is
	// too slow to do under e.mu, so the check-then-act around a released
	// lock needs a lock of its own or two concurrent callers for the same
	// card both see "not open yet" and both spawn one. Held only by
	// OpenConsult, and never while e.mu is also held.
	consultMu sync.Mutex
	// consultIdleTimeout bounds how long a ConsultSession's backend stays
	// spawned with no turns sent (Implementation notes: 20 minutes,
	// closing only the backend, never the transcript). A field rather
	// than a bare constant so a test can shrink it and observe the real
	// timer fire deterministically instead of waiting out the golden
	// value.
	consultIdleTimeout time.Duration

	// boardMu serializes OpenBoard end to end. e.mu cannot do that job:
	// opening spawns a real backend process (and possibly binds an MCP
	// endpoint), which is far too slow to hold the engine's main lock
	// across, so OpenBoard has to drop e.mu before the spawn — and a
	// check-then-act around a released lock is exactly how two callers
	// both see "no board yet" and both spawn one. The card path avoids
	// this by taking the per-card lock before any expensive work
	// (Attach); a board session has no card and therefore no such lock,
	// so this stands in for it.
	//
	// Held by OpenBoard, ReopenBoard and Close — every path that can
	// create or destroy the board session, which is what makes "is there
	// a board, and is it going to still be there a moment from now" a
	// question with one answer. Always taken BEFORE e.mu, never while
	// e.mu is already held.
	boardMu sync.Mutex

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

	// repoCards caches the repository orientation card per repository
	// root, computed at most once per root per Engine lifetime. A
	// workspace with several managed repos gets one card each; the map is
	// keyed by root rather than by card so a repo whose card comes out
	// empty is not recomputed on every session.
	repoCardMu sync.Mutex
	repoCards  map[string]string
	// repoInstructions caches the managed repository's own instruction
	// card (AGENTS.md/CLAUDE.md, quoted) per repository root, under the
	// same mutex and with the same lifetime as repoCards.
	repoInstructions map[string]string

	// goalLocks serializes the conductor per goal (goal.go's goalLock).
	// spawnExperiment starts the process that makes a prepared run; nil
	// means spawnDetached
	spawnExperiment ExperimentSpawner

	// substrates remembers the last reading of each substrate, so a goal
	// that ticks every few seconds does not probe real machines that often
	substrates substrateCache

	goalLocksMu sync.Mutex
	goalLocks   map[domain.FeatureID]*sync.Mutex

	// discoverLocks serializes check discovery per repository root
	// (checkscache.go's discoveryLock), so two cards crossing their plan
	// gates at once survey the repo once between them instead of each
	// paying for the same answer.
	discoverLocksMu sync.Mutex
	discoverLocks   map[string]*sync.Mutex

	// stackLocks serializes the restack walk per stack (stack.go's
	// stackLock), so two ticks never replay two members at once.
	stackLocksMu sync.Mutex
	stackLocks   map[domain.StackID]*sync.Mutex
}

// oneShotBusy reports whether a session-less pass (check discovery, its
// baseline) is running on the card. It is the other half of "is this
// card working?" that e.live cannot see — the gap that once had the
// board call a live card stopped.
func (e *Engine) oneShotBusy(id domain.FeatureID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.oneShots[id] > 0
}

// New builds an engine from the config. The caller owns every agent's
// lifetime. A single-repository config (Worktrees) is wrapped into a
// one-repo pool so every per-card path resolves uniformly.
func New(cfg Config) *Engine {
	if cfg.Permission == "" {
		cfg.Permission = agent.PermissionAllowAll
	}
	// 0 = uncapped; a negative value is normalized to it so every
	// "no cap configured" spelling behaves the same, in either pool.
	attendedMax := normalizeCap(cfg.MaxActive)
	autopilotMax := normalizeCap(cfg.AutopilotLanes)
	pool := cfg.Pool
	if pool == nil && cfg.Worktrees != nil {
		pool = worktree.WrapSingle(cfg.Worktrees)
	}
	if pool != nil && cfg.Store != nil {
		// a goal card resolves through its goal's worktree; the pool
		// asks the store whether a goal whose worktree is gone has ended
		pool.SetGoalLookup(cfg.Store.GetFeature)
	}
	e := &Engine{
		cfg:      cfg,
		now:      time.Now,
		raw:      make(chan Event, 256),
		events:   make(chan Event),
		stopped:  make(chan struct{}),
		live:     map[domain.FeatureID]*Session{},
		oneShots: map[domain.FeatureID]int{},
		consult:  map[domain.FeatureID]*ConsultSession{},
		pool:     pool,
	}
	e.consultIdleTimeout = consultIdleTimeout
	e.lanes[poolAttended].max = attendedMax
	e.lanes[poolAutopilot].max = autopilotMax
	e.envWarn = func(msg string) {
		e.envMu.Lock()
		e.envNotices = append(e.envNotices, msg)
		e.envMu.Unlock()
	}
	go e.forward()
	return e
}

// normalizeCap folds a negative cap to 0, so every "no cap configured"
// spelling (unset, explicit 0, or negative) behaves identically: uncapped.
func normalizeCap(n int) int {
	if n < 0 {
		return 0
	}
	return n
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

// LaneCounts is a point-in-time snapshot of each attention pool's
// occupancy and cap, for display (e.g. the board's "attended 1/1 ·
// autopilot 2/2"). A Max of 0 means that pool is uncapped.
type LaneCounts struct {
	AttendedRunning, AttendedMax   int
	AutopilotRunning, AutopilotMax int
}

// LaneCounts reports the current running/cap figures for both attention
// pools.
func (e *Engine) LaneCounts() LaneCounts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return LaneCounts{
		AttendedRunning:  e.lanes[poolAttended].running,
		AttendedMax:      e.lanes[poolAttended].max,
		AutopilotRunning: e.lanes[poolAutopilot].running,
		AutopilotMax:     e.lanes[poolAutopilot].max,
	}
}

// Repool moves a card's live autonomous run into the attention pool its
// gate-approval mode now names, and is what every writer of that mode
// must call after persisting it.
//
// A session's pool used to be decided once, at dispatch, from the feature
// snapshot run() was handed. The mode is not fixed for a run's lifetime:
// the `A` switch hands a card to autopilot mid-stage — the "set a running
// card to autopilot and go to bed" flow it is largely there for — and the
// run went on holding the ATTENDED slot it took, so the next attended
// card queued behind unattended work. That is the one thing the split
// pools exist to prevent. The reverse leaked the other way: cards taken
// back from autopilot kept their unattended lanes, and two of them ran at
// once under an attended cap of one.
//
// Both live states are handled, and the queued one is the more important:
// a card still waiting in a queue has not started, so there is no reason
// for it to take a slot in the pool it has stopped belonging to.
//
//   - queued: the entry moves to the other pool's FIFO, at the back —
//     a card changing pools takes its turn in the new one, it does not
//     inherit a position it earned somewhere else.
//   - running: the slot moves with it, which can briefly put the
//     destination pool over its cap. That is accounted honestly rather
//     than avoided: the run really is in that pool now, and the
//     alternative is stopping a working agent to satisfy a count. The cap
//     throttles the next start, and the over-subscription drains on its
//     own as the run ends.
//
// The session's own Feature copy is deliberately left alone. It is read
// without a lock all over the engine, and the pool is the only thing that
// has to agree with the row here; every continuation reloads the feature
// from the store anyway, so the next stage's session is built from the
// new mode regardless.
//
// A no-op when the card has no live session, when its session never
// competes for a slot (interactive), or when the mode maps to the pool it
// is already in.
func (e *Engine) Repool(id domain.FeatureID, mode string) {
	want := lanePoolForMode(mode)
	e.mu.Lock()
	s := e.live[id]
	if s == nil || s.Interactive {
		e.mu.Unlock()
		return
	}
	queued := s.State() == StateQueued
	moved, held, from := s.repool(want)
	if !moved {
		e.mu.Unlock()
		return
	}
	switch {
	case held:
		if e.lanes[from].running > 0 {
			e.lanes[from].running--
		}
		e.lanes[want].running++
	case queued:
		e.removeFromQueue(id)
		e.lanes[want].queue = append(e.lanes[want].queue, id)
	}
	e.mu.Unlock()
	// the pool it left may now have room, and the pool it joined may have
	// gained a waiter; schedule covers both.
	e.schedule()
}

// noAgentAtStage is the refusal for a stage no role is mapped to — in
// practice todo, the one stage that exists before any agent has been
// asked for anything. It names the way forward rather than only the
// wall: the old wording ("stage todo has no agent action") was what a
// person met after being told to request changes instead of approving,
// and it left them with nothing to do at all.
func noAgentAtStage(stage domain.Stage) error {
	if stage == domain.StageTodo {
		return errors.New("nothing has run yet — start the card, and the comments in its artifact go to the agent with it")
	}
	return fmt.Errorf("stage %s runs no agent", stage)
}

// Attach starts (or reuses) an interactive chat session for a feature's
// current stage. Interactive sessions hold no attention slot.
func (e *Engine) Attach(ctx context.Context, f domain.Feature) (*Session, error) {
	role, ok := roleForStage(f)
	if !ok {
		return nil, noAgentAtStage(f.Stage)
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

	// Nothing expensive happens before the card is ours: a backend spawn
	// against a card another gummi is already driving is exactly the race
	// this lock exists to lose early. A prior session of ours holds a ref
	// of its own, so this joins rather than fights it.
	unlock, err := e.lockCard(f.ID)
	if err != nil {
		return nil, err
	}

	// The session's lifecycle context is bound to nothing: it is canceled by
	// Session.stop, not by the caller's ctx going away. Keep it distinct from
	// the caller's ctx so the initial kickoff Send stays on the caller's
	// cancellation semantics.
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{Feature: f, Role: role, Interactive: true, state: StateInteractive, done: make(chan struct{}), ctx: sctx, cancel: cancel, cardUnlock: unlock, startedAt: time.Now()}
	var priorAgentSession string
	if prior != nil && prior.Feature.Stage == f.Stage {
		ps := prior.Snapshot()
		// The backend conversation is only this session's to continue when
		// the prior one was the same role doing the same job. A card keeps
		// ONE session row per stage, so the id sitting there may belong to
		// the critique pass that ran last (reviewer, flavorCritique) —
		// handing that conversation to the architect would resume the
		// wrong side of the argument. The transcript still carries over
		// either way; only the resume is withheld.
		if prior.Role == role && prior.flavor() == flavorStage {
			priorAgentSession = ps.AgentSessionID
		}
		s.transcript = append(s.transcript, ps.Transcript...)
		s.activity = append(s.activity, ps.Activity...)
		s.spend = ps.Spend
		// a restored ask rides the swap: the prior session was re-armed
		// with the durably recorded question (DESIGN §6.3's reopen path),
		// and the fresh backend behind this attach has never seen it —
		// dropping the pending ask here would strand the question the
		// record says is open.
		s.setPendingAsk(ps.PendingAsk)
	}
	// A fresh conversation opens with a stage kickoff so the agent leads;
	// a carried-over transcript means the interview is already underway
	// (this is a restart-reattach), so reattaching stays silent. The same
	// distinction decides the durable transcript's fate: a fresh attach
	// must not inherit an unrelated earlier attempt's file, while a
	// restart-reattach's carried transcript is exactly what that file
	// preserves — it must be left alone.
	fresh := len(s.transcript) == 0
	if fresh {
		e.clearResumeTranscript(s, flavorStage)
	}

	// A reattach continues a conversation: the backend is handed the id of
	// the one it is continuing, so a restored ask lands in a session that
	// still has the repository open instead of one that must re-read every
	// file the last one read before it can act on a one-word answer. A
	// fresh attach passes nothing — there is no conversation to continue,
	// and the adapter opens a blank one exactly as it always has.
	var resumeID string
	if !fresh {
		resumeID = priorAgentSession
	}

	// interactive chat is human-paced: no budget cap.
	sess, specPath, mcpTeardown, err := e.newAgentSession(ctx, f, role, 0, flavorStage, true, resumeID)
	if err != nil {
		cancel()
		unlock()
		return nil, err
	}
	s.setSpecPath(specPath)
	s.setMCPTeardown(mcpTeardown)
	e.stampSpawnInfo(s)
	// interactive chat is uncapped but not free: its spend moves the
	// card's total the same way, so the masthead needs the same seed.
	e.seedCardSpend(s)
	// s is not yet reachable by Pause/Drop/Close (not in e.live), so
	// attachAgent can't be racing a finalize here; the bool is checked for
	// symmetry with the autonomous path.
	if !s.attachAgent(sess) {
		_ = sess.Close()
		unlock()
		return nil, errors.New("engine is closed")
	}
	e.trackAgentPID(f.ID, sess)
	s.setState(StateInteractive)

	if !e.replace(f.ID, s) {
		s.stop() // engine closed during startup: don't leave the agent live
		return nil, errors.New("engine is closed")
	}
	// replace stopped any prior session (closing its writer), so this one
	// can safely truncate the card's live file and take it over.
	e.bindLiveLog(s)
	e.flushEnvNotices(s)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pump(s) }()
	var ko string
	if fresh {
		ko = designKickoff(f)
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

// RunCritique runs a stage's critique pass: a fresh-context reviewer
// session that refutes what the stage just produced — the plan, or the
// diff — writing findings as %% marker threads and ending with a
// verdict. It replaces the done stage session like any re-run; the state
// machine never sees it, so the card stays where it is throughout. note
// is appended to the kickoff; a re-critique round uses it to point the
// fresh session at the prior round's resolved threads.
//
// Every stage that ends with a critique must declare which round counter
// that critique burns, and CritiqueRoundKind is where it declares it. A
// critique with no counter would loop forever, so this refuses to start
// one rather than defaulting: the failure mode is a loud error at the
// first call, not an unbounded loop discovered in production.
func (e *Engine) RunCritique(f domain.Feature, note string) error {
	if _, ok := CritiqueRoundKind(f.Stage); !ok {
		return fmt.Errorf("stage %s has no critique pass (no round counter declared for it)", f.Stage)
	}
	return e.run(f, note, flavorCritique)
}

// CritiqueRoundKind names the round counter a stage's critique burns, and
// by existing at all it says which stages HAVE a critique. ok is false for
// every other stage.
//
// Each critique burns the counter its predecessor burned, so no budget
// changes hands when Review stops being a stage: the plan critique keeps
// RoundKindPlan (cap 2), and every work stage's critique — which IS the
// old Review — keeps RoundKindReview (cap 3). Investigate is research's
// work stage and is in that group.
//
// A new stage that wants a critique has to add a row here, which is the
// point: verdict.MaxRounds caps by round kind, so a critique whose kind
// was left to a default would be capped by whatever that default happened
// to be, or not capped at all.
func CritiqueRoundKind(stage domain.Stage) (domain.RoundKind, bool) {
	switch stage {
	case domain.StagePlan:
		return domain.RoundKindPlan, true
	case domain.StageImplement:
		return domain.RoundKindReview, true
	}
	return "", false
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
	// A goal's implement stage is conducted, not written: there is no
	// stage agent to run. A run of it — the driving loop starting the
	// stage, or a review's changes and a failed verify sending the goal
	// back — records the note as work the goal owes and asks the loop to
	// tick the goal instead.
	if f.IsGoal() && f.Stage == domain.StageImplement && flavor == flavorStage {
		// A review that just asked for changes said what it found in its
		// own words, and those words — not the loop's generic rework note,
		// which speaks to an implementer about threads — are what the lead
		// has to act on.
		if s := e.Get(f.ID); s != nil {
			if snap := s.Snapshot(); snap.Critique {
				if found := lastAssistantText(snap.Transcript); found != "" {
					note = "The goal's review of the combined branch asked for changes. What it found:\n\n" + found
				}
			}
		}
		if strings.TrimSpace(note) != "" && e.cfg.Store != nil {
			e.goalLog(context.Background(), f.ID, state.GoalPayload{Action: state.GoalRework, Detail: note, By: ActorGoal})
		}
		e.Drop(f.ID)
		e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventGoal})
		return nil
	}
	// A card its goal dropped does not run on: the drop can land between two
	// steps of a driving loop that still holds the card from before it.
	if f.GoalID != "" && e.cfg.Store != nil {
		if cur, err := e.cfg.Store.GetFeature(context.Background(), f.ID); err == nil && cur.GoalDropped() {
			return fmt.Errorf("%s was dropped by %s and does not run on", f.ID, cur.GoalID)
		}
	}
	role, ok := roleForStage(f)
	if !ok {
		return noAgentAtStage(f.Stage)
	}
	switch flavor {
	case flavorCritique:
		role = agent.RoleReviewer
	case flavorRebase:
		role = agent.RoleImplementer
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
	unlock, err := e.lockCard(f.ID)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := lanePoolFor(f)
	s := &Session{Feature: f, Role: role, Critique: flavor == flavorCritique, Rebase: flavor == flavorRebase, ReadOnly: researchReadOnly(f), pool: pool, state: StateQueued, done: make(chan struct{}), ctx: ctx, cancel: cancel, kickoffNote: note, cardUnlock: unlock, startedAt: time.Now()}
	e.stampSpawnInfo(s)
	e.dropLocked(f.ID)
	e.live[f.ID] = s
	e.lanes[pool].queue = append(e.lanes[pool].queue, f.ID)
	e.mu.Unlock()

	if old != nil {
		old.stop() // a replaced done/paused session; free its goroutine
		e.freeSlot(old)
	}
	// after the replaced session's writer is closed, never before: two
	// writers on one live file would interleave.
	e.bindLiveLog(s)
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventUpdated})
	e.schedule()
	return nil
}

// schedule fills free slots from each pool's own queue. With a pool's cap
// at 0 (the default) there is no cap on it, so it drains that queue on
// every pass and a run never sits in StateQueued waiting on another card
// in the SAME pool. The two pools are scheduled independently — see
// scheduleLane — so a card waiting in one never starts by way of a slot
// freed in the other.
func (e *Engine) schedule() {
	for p := lanePool(0); p < numLanePools; p++ {
		e.scheduleLane(p)
	}
}

// scheduleLane drains pool p's queue into its free slots.
func (e *Engine) scheduleLane(p lanePool) {
	for {
		e.mu.Lock()
		lane := &e.lanes[p]
		if (lane.max > 0 && lane.running >= lane.max) || len(lane.queue) == 0 {
			e.mu.Unlock()
			return
		}
		id := lane.queue[0]
		lane.queue = lane.queue[1:]
		s := e.live[id]
		if s == nil || s.State() != StateQueued {
			e.mu.Unlock()
			continue
		}
		lane.running++
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
	// the same row the budget was just derived from, kept live on the
	// session so displays stop reading a snapshot taken before the run
	// (the masthead's "credits left" was frozen at spawn-time spend).
	e.seedCardSpend(s)
	// a budgeted feature with nothing left must not run uncapped (a 0
	// budget elsewhere means "unbudgeted"): gate it immediately.
	if s.Feature.Budget.Envelope > 0 && budget <= 0 && !s.Interactive {
		e.exhaust(s)
		return
	}
	// No dirty check on main: a research pass used to run in the main
	// checkout, which made the operator's uncommitted work a hard stop
	// before any session. It runs in the card's scratch tree now, so that
	// dirt is out of reach and refusing here would park a card over a state
	// it cannot touch.
	// run() always builds a brand-new in-process Session with no carried
	// transcript (kickoff, bounce, and restart-then-resume all dispatch
	// through the same path), so every startAutonomous spawn is fresh: any
	// transcript left at the derived path belongs to an unrelated earlier
	// attempt and must not leak into this one.
	e.clearResumeTranscript(s, s.flavor())
	sess, specPath, mcpTeardown, err := e.newAgentSession(context.Background(), s.Feature, s.Role, budget, s.flavor(), s.Interactive, "")
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
	e.trackAgentPID(s.Feature.ID, sess)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pump(s) }()
	e.flushEnvNotices(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStarted})

	// Only a stage's writer answers the human's spec comments: a critique
	// judges what was written, a rebase resolves conflicts, and verify
	// checks the result — any of them taking the comments would act on
	// them in the wrong pass.
	if !s.Critique && !s.Rebase && (s.Feature.Stage == domain.StagePlan || s.Feature.Stage == domain.StageImplement) {
		s.specComments = e.openSpecComments(s.Feature)
	}

	// The kickoff is gummi's own turn, not the user's, on this loop the
	// same as the interactive one (see startInteractive's appendSystem):
	// it's boilerplate gummi composed, with any review note the user
	// attached (RunWith) quoted inside it rather than spoken as it. The
	// TUI labels every transcript turn by its author now, so appendUser
	// here would put gummi's own words in the user's mouth on screen.
	s.appendSystem(s.kickoffMessage())
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
	// Implement gets the plan's file manifest. Measured: gummi's
	// implementer made its first edit at turn 32 of 97, while a bare agent
	// with no spec at all first edits at turn 20-31 of 62-72 — it explored
	// MORE than an agent working blind, despite a spec that had already
	// located every file. The architect knew; the answer was prose the
	// implementer re-derived from the repo.
	if (s.Feature.Stage == domain.StageImplement) && !s.Rebase {
		if pre := e.fileManifestPreamble(s); pre != "" {
			msg = pre + "\n\n" + msg
		}
	}
	// Review gets the same two things it was otherwise spending its own
	// turns assembling. Measured on one review session: 33 turns, 28 Bash
	// calls, zero edits — about twelve of them rebuilding `git diff
	// base..HEAD` one file at a time, and then `go build ./...` and
	// `go vet ./...` it could have been handed.
	//
	// The checks it is handed are the critique's own run, not verify's.
	// They answer different questions at different times: a critique reads
	// a branch that is not final, so a result here can be stale by the
	// time verify asks. Nothing is recorded, and verify still runs its own
	// — this only saves the critique from re-deriving what is already true
	// right now.
	if s.Critique && !s.Rebase && workStageCritique(s.Feature.Stage) {
		if pre := e.reviewDiffPreamble(s); pre != "" {
			msg = pre + "\n\n" + msg
		}
		if e.cfg.Permission != agent.PermissionGuarded {
			if pre := e.runSpecChecks(s); pre != "" {
				msg = pre + "\n\n" + msg
			}
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

// fileManifestPreamble hands the implement session the plan's file
// manifest — where the work goes — so the stage opens the right files
// instead of rediscovering them.
//
// It is stated as a starting point, never a closed set. A wrong or stale
// manifest is worse than none if the implementer treats it as exhaustive,
// so the text says what to do in both failure directions: a file the work
// needs that the manifest missed gets changed anyway (and noted), and a
// listed file that turns out irrelevant gets left alone rather than
// having work invented for it. Missing, empty, or malformed reads as no
// manifest, and the stage proceeds exactly as it did before.
func (e *Engine) fileManifestPreamble(s *Session) string {
	raw, err := os.ReadFile(s.SpecPath())
	if err != nil {
		return ""
	}
	files, _, err := spec.ParseFiles(string(raw))
	if err != nil || len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The plan's file manifest — where this work goes. Open these before searching for anything:\n")
	for _, f := range files {
		b.WriteString("  - " + f.Path)
		if f.New {
			b.WriteString("  (new file)")
		}
		if f.Role != "" {
			b.WriteString("  — " + f.Role)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nThis is where to start, not the whole boundary of the change. " +
		"If the work needs a file the manifest does not list, change it and add a line to Progress saying which and why — " +
		"the manifest was a plan-time guess and the next round should inherit the correction. " +
		"If a listed file turns out to have nothing to do with the change, leave it alone and say so; " +
		"do not invent work to justify an entry.")
	return b.String()
}

// reviewDiffInlineMax caps how much diff rides inline in the review
// kickoff. The kickoff preamble is re-read on every turn of the session,
// so anything put here is paid for repeatedly — that mechanism is how a
// ~32k floor came to be 41-44% of gummi's whole context volume. Below the
// cap, handing over the patch beats a reviewer rebuilding it a file at a
// time; above it, the stat plus the command is the cheaper shape, and the
// reviewer fetches only the parts it reads.
//
// GUMMI_REVIEW_DIFF_MAX overrides it in bytes, so the two shapes can be
// measured against each other without a rebuild; 0 forces the stat shape.
const reviewDiffInlineMax = 48 << 10

// reviewDiffLimit resolves the inline cap, honoring the override. A
// malformed or negative value falls back to the constant rather than
// silently disabling the preamble.
func reviewDiffLimit() int {
	raw := strings.TrimSpace(os.Getenv("GUMMI_REVIEW_DIFF_MAX"))
	if raw == "" {
		return reviewDiffInlineMax
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return reviewDiffInlineMax
	}
	return n
}

// reviewDiffPreamble hands the review session the diff it would otherwise
// spend its own turns reassembling, and — always — the base SHA to name a
// range against, so "review the diff" never becomes a guess at a revision.
//
// Two shapes, chosen by size (see reviewDiffInlineMax). Any git failure
// yields "" and the stage proceeds exactly as it did before: this is an
// economy, never a gate.
func (e *Engine) reviewDiffPreamble(s *Session) string {
	wt, err := e.mgr(context.Background(), &s.Feature)
	if err != nil {
		return ""
	}
	ctx := context.Background()
	base, err := wt.DiffBase(ctx, &s.Feature)
	if err != nil {
		return ""
	}
	diff, err := wt.Diff(ctx, &s.Feature)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(diff) == "" {
		return "" // nothing on the branch yet; the reviewer's own look is the honest one
	}
	header := fmt.Sprintf("gummi assembled this review's diff — it is `git diff %s` in your working directory. "+
		"Do NOT rebuild it file by file.", base)
	if len(diff) <= reviewDiffLimit() {
		return header + "\n\n```diff\n" + diff + "\n```"
	}
	stat, err := wt.DiffStat(ctx, &s.Feature)
	if err != nil {
		return header
	}
	return fmt.Sprintf("%s It is %d bytes — too large to carry in every turn of this session, "+
		"so here is the shape of it. Read the parts you need with `git diff %s -- <path>`.\n\n```\n%s```",
		header, len(diff), base, stat)
}

// verifyStageTimeout bounds the gummi-side check run at the Verify stage
// (mirrors the manual verify dialog's cap).
const verifyStageTimeout = 10 * time.Minute

// probeCleanPresent is a session-free counterpart of hasCleanPresentProbe:
// it loads the layered env config and runs envprobe.Run fresh against the
// feature's worktree, reporting whether any probe came back clean-present
// (Err nil and Present true). It does not mutate or persist session state.
func (e *Engine) probeCleanPresent(ctx context.Context, f *domain.Feature) bool {
	cfg, err := e.layeredConfig()
	if err != nil {
		return false
	}
	if len(cfg.EnvProbes()) == 0 {
		return false
	}
	m, err := e.Substrates()
	if err != nil {
		return false
	}
	workDir := filepath.Join(e.pool.Root(), f.WorktreePath())
	for _, r := range probeEnvironment(ctx, cfg, m, workDir) {
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
	cfg, err := e.layeredConfig()
	var m *substrate.Manager
	if err == nil {
		m, err = e.Substrates()
	}
	if err != nil {
		msg := "Environment prerequisites could not be probed: " + err.Error()
		s.appendActivity(msg)
		e.persist(s)
		return msg
	}
	if len(cfg.EnvProbes()) == 0 {
		return ""
	}
	workDir := filepath.Join(e.pool.Root(), s.Feature.WorktreePath())
	results := probeEnvironment(context.Background(), cfg, m, workDir)
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

// withDoneWhenChecks makes a goal's check list carry one check per
// commanded done-when item, with the item's own command. The done-when
// block is where those commands are agreed (and repaired, by
// done_when_check_fix); the copy in gummi-checks is only how they reach
// the runner, and agents edit that block. A copy rewritten into a shape
// that no longer parses, or into another command, must not silently stop
// the goal proving its items.
func withDoneWhenChecks(doc string, checks []domain.Check) []domain.Check {
	items, _, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return checks
	}
	out := append([]domain.Check(nil), checks...)
	no := false
	for _, it := range items {
		if it.Check == "" {
			continue
		}
		// an agent's copy may have renamed it to the bare item id
		i := slices.IndexFunc(out, func(c domain.Check) bool { return c.Name == it.CheckName() || c.Name == it.ID })
		if i < 0 {
			out = append(out, domain.Check{Name: it.CheckName(), Cmd: it.Check, Baseline: &no})
			continue
		}
		out[i].Name, out[i].Cmd = it.CheckName(), it.Check
	}
	return out
}

// runSpecChecks executes the artifact's gummi-checks commands in the
// feature's worktree, records each outcome in the activity feed, and
// returns a compact summary to hand the verify agent (empty when the artifact
// carries no checks or can't be read — the verify agent then discovers and
// runs them itself, per its stage hint).
//
// A block that does not parse is reported, not swallowed. It used to fall
// through the dropped error into len(checks) == 0 and read as "this card
// has no checks", so the deterministic floor ran zero commands and said
// nothing about why — the parse error was loud exactly once, at the
// approval gate, and silent for the rest of the card's life.
func (e *Engine) runSpecChecks(s *Session) string {
	raw, err := os.ReadFile(s.SpecPath())
	if err != nil {
		return ""
	}
	checks, _, parseErr := spec.ParseChecks(string(raw))
	if parseErr != nil && !s.Feature.IsGoal() {
		return "gummi could not run the artifact's gummi-checks: " + parseErr.Error() +
			"\nThis is a plan defect. Repair the block in the Verification section, append a bullet there reading " +
			"`finding: gummi-checks does not parse`, run the repaired commands yourself, and set your verdict to fail."
	}
	if s.Feature.IsGoal() {
		// a goal's done-when checks run whatever its checks block says
		checks = withDoneWhenChecks(string(raw), checks)
	}
	// A goal's items may be proved by an experiment rather than a command.
	// gummi does not run one here — a run is long, remote and exclusive,
	// and the conductor made it before the goal finished — it reads what
	// the runs say about the heads the goal has now, as results that sit
	// beside the commands' and count exactly as they do.
	var proven []verify.Result
	if s.Feature.IsGoal() {
		proven = e.goalExperimentResults(context.Background(), s.Feature, string(raw))
	}
	if len(checks) == 0 && len(proven) == 0 {
		return ""
	}
	workDir := filepath.Join(e.pool.Root(), s.Feature.WorktreePath())
	// The budgeted runner derives an overall deadline from the sum of the
	// checks' own timeouts (or the package default) plus a small slack,
	// bounded below by verifyStageTimeout. Each check also gets its own
	// per-check bound so one hung command cannot starve the rest.
	var results []verify.Result
	if s.Feature.IsGoal() {
		// A goal's checks do not all run in one place: a done-when item
		// names the repository its command proves the goal in, and each
		// group runs in that repository's goal tree (goalchecks.go).
		results, err = e.runGoalChecks(context.Background(), s.Feature, string(raw), checks)
	} else {
		results, err = verify.RunWithBudget(context.Background(), workDir, checks, verifyStageTimeout)
	}
	if err != nil {
		return ""
	}
	results = append(results, proven...)

	// The approval-time baseline separates failures the feature caused
	// from ones the branch was born with. A baseline entry speaks for a
	// live check only when the command is unchanged — an edited command
	// invalidates what the old run proved. No baseline (older features,
	// guarded mode) degrades to today's unlabeled FAIL.
	baseline := map[string]state.CheckResult{}
	if rows, err := e.cfg.Store.CheckBaseline(context.Background(), s.Feature.ID); err == nil {
		for _, r := range rows {
			baseline[r.Name] = r
		}
	}

	var b strings.Builder
	preexisting := false
	var liveFailures []string
	var recorded []goalCheckResult
	defer func() {
		if s.Feature.Stage == domain.StageVerify {
			e.recordGoalChecks(s.Feature, recorded)
		}
	}()
	b.WriteString("gummi already ran the spec's gummi-checks commands in this worktree — do NOT re-run them:\n")
	for _, r := range results {
		var status string
		switch {
		case strings.HasPrefix(r.Cmd, experimentCmdPrefix) && r.Status == verify.StatusNotRun:
			status = "NOT PROVEN (no conclusive run on these heads)"
			liveFailures = append(liveFailures, r.Name)
		case strings.HasPrefix(r.Cmd, experimentCmdPrefix) && r.Status == verify.StatusFail:
			status = "FAIL (the experiment's verdict)"
			liveFailures = append(liveFailures, r.Name)
		}
		if status != "" {
			s.appendToolDone(fmt.Sprintf("check %s: %s", r.Name, status), r.OK, r.Output)
			recorded = append(recorded, goalCheckResult{Name: r.Name, OK: r.OK, Status: status, Evidence: r.Output})
			fmt.Fprintf(&b, "- %s: %s\n%s\n", r.Name, status, indentLines(r.Output))
			continue
		}
		switch r.Status {
		case verify.StatusPass:
			status = "pass"
		case verify.StatusTimeout:
			status = "TIMEOUT (killed by deadline)"
			liveFailures = append(liveFailures, r.Name)
		case verify.StatusNotRun:
			status = "NOT RUN (check budget exhausted)"
			liveFailures = append(liveFailures, r.Name)
		default:
			if base, ok := baseline[r.Name]; ok && base.Cmd == r.Cmd && !base.OK {
				status = fmt.Sprintf("FAIL (pre-existing, exit %d)", r.ExitCode)
				preexisting = true
			} else {
				status = fmt.Sprintf("FAIL (exit %d)", r.ExitCode)
				liveFailures = append(liveFailures, r.Name)
			}
		}
		s.appendToolDone(fmt.Sprintf("check %s: %s", r.Name, status), r.OK, r.Output)
		recorded = append(recorded, goalCheckResult{Name: r.Name, OK: r.OK, Status: status,
			Evidence: firstNonEmpty(experimentEvidence(r), checkFailureNote(r))})
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
	// A live (non-pre-existing) check failure floors the verdict so a
	// model's self-reported pass can never outrank gummi's own machine
	// judgement — mirrors the floor gateVerifyVerdict already stamps for
	// the env-omission condition.
	// A goal's review is the exception: its checks include the done-when
	// commands, which a partial goal fails by definition — the review judges
	// the combined diff, and verify is where an unmet item counts.
	if len(liveFailures) > 0 && !(s.Feature.IsGoal() && s.Critique) {
		s.setVerdictFloor("blocked", fmt.Sprintf("check %s failed", strings.Join(liveFailures, ", ")))
	}
	// What the branch SHIPS, read from the tree rather than from the diff.
	// A committed build artifact is a fact, and the one part of the floor
	// that does not vary with the reviewer's model.
	if findings := e.diffHygiene(s); len(findings) > 0 {
		b.WriteString(hygieneBlock(findings))
		if names, blocking := blockingHygiene(findings); blocking {
			s.setVerdictFloor("fail", "branch ships "+strings.Join(names, ", "))
		}
	}
	// The inventory of what this branch changed, so the coverage question
	// is answerable rather than assumed. A verify that cannot see the file
	// list reports on the files it happened to look at, which is how a
	// branch with three files no check could compile reached
	// verified: true with the gap recorded only in prose.
	paths := e.changedPaths(s.Feature)
	b.WriteString(changedFileInventory(paths))
	b.WriteString(grammarSweepHint(string(raw), paths))
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
}

// UseCardLocks makes this engine take the workspace's per-card lock for
// every card it drives, so a headless run/resume/merge/clean for the same
// card is refused instead of racing it. Call it once, right after New and
// before anything drives a card: the board does, since it is the process
// that holds cards for a long time; one-shot commands leave it unset and
// keep taking the card's lock around the whole command themselves.
func (e *Engine) UseCardLocks(l *state.CardLocks) { e.cfg.CardLocks = l }

// lockCard takes this engine's hold on the workspace's per-card lock,
// returning the release to hand to the session that will own it. With no
// CardLocks configured — the headless driver, which already holds the
// card's lock around the whole command, and tests — it is a no-op that
// hands back a no-op release, so every call site stays branch-free.
//
// Holds inside one process share a single flock and are refcounted, so a
// second session (or a merge) on a card this engine already drives joins
// the lock rather than deadlocking against it; another gummi process is
// excluded throughout.
func (e *Engine) lockCard(id domain.FeatureID) (func(), error) {
	return e.cfg.CardLocks.Acquire(id)
}

// bindLiveLog opens the card's live file for s and binds it, so another
// gummi process can follow this session (internal/livelog). The live
// stream is a courtesy to watchers, never a dependency of the run: a
// workspace-less engine (tests, transient board helpers) and a file that
// cannot be opened both leave the session with a nil writer, which emits
// nowhere and costs nothing.
//
// Call it once per session, after any prior session for the card has been
// stopped — Create truncates, and two writers on one file interleave.
func (e *Engine) bindLiveLog(s *Session) {
	if e.cfg.Workspace.Root == "" {
		return
	}
	snap := s.Snapshot()
	w, err := livelog.Create(e.cfg.Workspace.LiveFile(s.Feature.ID), livelog.Record{
		Feature: string(s.Feature.ID),
		Stage:   string(s.Feature.Stage),
		Role:    string(s.Role),
		Agent:   snap.AgentName,
		Model:   snap.Model,
	})
	if err != nil {
		return
	}
	s.bindLive(w)
}

// trackAgentPID records sess's backing OS process at
// ws.AgentPGIDFile(id) (BG-002), so a driver that dies without running its
// own in-process cleanup (SIGKILL, crash, OOM-kill) still leaves behind a
// durable pointer a later run/resume/clean can use to kill the orphan
// before touching the card again (state.ReapOrphanAgent). sess implementing
// agent.OSProcess is optional — an adapter with no real OS process backing
// its session (or a test fake) simply isn't one, and nothing is recorded.
func (e *Engine) trackAgentPID(id domain.FeatureID, sess agent.Session) {
	p, ok := sess.(agent.OSProcess)
	if !ok {
		return
	}
	if pid := p.Pid(); pid > 0 {
		_ = state.WritePIDFile(e.cfg.Workspace.AgentPGIDFile(id), pid)
	}
}

// newAgentSession builds an agent session for a feature's stage, with
// the backend/model chosen by the feature's profile for this role. It
// also returns the resolved spec path so the caller can record it on the
// Session (ask_user answer capture writes there), and — when the resolved
// backend cannot call client tools — the MCP inbound-endpoint teardown
// stub, so the caller can bind it to the Session's lifecycle before the
// child inherits GUMMI_MCP_SOCK.
// resumeID, when non-empty, is the backend conversation this session
// continues (see agent.SessionOpts.ResumeID): a reattach carrying a prior
// transcript hands back the id that transcript came from, so the backend
// picks up where it stopped instead of rediscovering the repository it
// already had open. A fresh stage run passes "" — a stage is not one
// session, and a restart is not a replay.
//
// newAgentSession builds the backend session for a stage run. attached
// reports that this is a chat the user opened (Attach) rather than an
// autonomous pass: a chat is not budgeted against the card's envelope and
// is told the ask convention, which is the whole of what "interactive"
// used to mean. It is a property of the SESSION, not of the stage — chat
// is available against any stage now, and no stage is a chat by nature.
func (e *Engine) newAgentSession(ctx context.Context, f domain.Feature, role agent.Role, budget float64, flavor runFlavor, attached bool, resumeID string) (agent.Session, string, func(), error) {
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
	// silently downgrade the read-only guarantee to nothing at all.
	readOnly := researchReadOnly(f)
	if readOnly && !ag.Capabilities().ReadOnlyEnforce {
		// ag.Name(), not the resolved backend name: an empty name is
		// resolveRole's documented fallback to the engine's default
		// backend — the normal path on a workspace with no profiles.yaml,
		// not an edge — and `backend ""` names nothing the reader could
		// go and change. The nil-agent guard above makes the same
		// distinction for the same reason.
		return nil, "", nil, fmt.Errorf("backend %q cannot enforce a read-only research session "+
			"(feature %s stage %s); point this role at `claude` or `opencode`, or accept that "+
			"autonomous research cannot run on that backend", ag.Name(), f.ID, f.Stage)
	}
	// A real, per-card place for throwaway files, named in the boundary
	// hint. Best-effort: a directory that cannot be created leaves the
	// hint saying `mktemp -d`, as it always did.
	scratch := ""
	if dir := e.cfg.Workspace.ScratchFilesDir(f.ID); dir != "" && e.cfg.Workspace.Root != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			scratch = dir
		}
	}
	hints := stageHints(f, specPath, scratch, flavor)
	if nb := e.notebookHint(f); nb != "" {
		// what the goal knows that this card does not own: a line per
		// entry, however much that comes to
		hints = append(hints, nb)
	}
	if f.IsGoal() {
		// A goal may span repositories, and nothing else in its contract
		// says so: its plan has to know it may put a card in one, and its
		// review and verify have to know the combined change is in
		// several trees rather than the one they are standing in.
		if card := e.goalReposCard(ctx, f); card != "" {
			hints = append(hints, card)
		}
		// What a card has actually cost here. An architect asked to size
		// work it has not done yet is guessing, and one that knows it is
		// guessing writes nothing: "I have no prior run of this harness
		// to anchor a number on, so rather than invent one" is what one
		// wrote into a goal doc, leaving an envelope the gate refused.
		// The workspace knows the answer and had never been asked.
		if typical := e.typicalCardCredits(ctx); typical > 0 {
			hints = append(hints, fmt.Sprintf(
				"Cards completed in this workspace have cost about %d credits each, "+
					"median plus headroom. That is the anchor for both the Budget "+
					"section's ranges and any envelope you put on a gummi-cards row.",
				typical))
		}
	}
	// The repository orientation card sits directly under the operator's
	// environment card: the operator's own words lead, because they are a
	// deliberate instruction, and the file tree is reference material the
	// session reads rather than obeys. Prepended in that order — repo
	// first, environment second — so the environment card ends up first.
	if mgr, err := e.mgr(ctx, &f); err == nil && mgr != nil {
		if card := e.repoCard(mgr.RepoRoot()); card != "" {
			hints = append([]string{card}, hints...)
		}
		// The repo's own instructions sit above its file tree: they are
		// rules to follow, not reference material, and the precedence
		// hint every session already carries asserts they are in force.
		// Quoting them is what makes that assertion true — a session on a
		// backend that auto-loads only its own convention never saw an
		// AGENTS.md otherwise.
		if card := e.repoInstructionsCard(mgr.RepoRoot()); card != "" {
			hints = append([]string{card}, hints...)
		}
	}
	if card := e.environmentCard(); card != "" {
		hints = append([]string{card}, hints...)
	}
	// implementation runs carry any open diff review comments so a fix-up
	// (bounce from the diff surface's "request changes") addresses each
	// (DESIGN §6.1). The store is the source of truth, so this reaches
	// every implement run, not just the one that triggered it.
	if flavor == flavorStage && (f.Stage == domain.StageImplement) {
		hints = append(hints, e.diffReviewHints(ctx, f.ID, ag.Capabilities().ClientTools)...)
	}
	var maxCredits float64
	// autonomous stages get a budget cap + budget-aware hint (interactive
	// chat is human-paced, so it isn't capped). Envelope-derived budgets
	// arrive already floored at one turn's reserve (stageBudget), so the
	// enforced cap is never an un-holdable sliver.
	if budget > 0 && !attached {
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
		tools = stageTools(f.Stage, flavor, e.decidingHeadings(&f))
		// Every session that has the tools is told how to use them,
		// research included. This used to skip a read-only session,
		// because its surface had spec_replace_section stripped out from
		// under it and the hint would have described a tool it did not
		// have. Nothing is stripped now (see asktool.go), so the hint is
		// true for every session that gets one.
		if h := toolHint(f.Stage, flavor); h != "" {
			hints = append(hints, h)
		}
	} else {
		// No client tools: the agent asks through the fenced-block
		// convention instead. Every stage gets this now — the design
		// stage is autonomous, and a stage that cannot ask is a stage
		// that guesses.
		hints = append(hints, askConventionHint)
	}
	// A card left to run alone answers its own questions, so the agent is
	// told that before it asks one — whichever of the two routes above it
	// would have used.
	if f.GateApproval == domain.GateAutopilot && !readOnly {
		hints = append(hints, unattendedAskHint)
	}
	// Every stage session gets its own inbound MCP endpoint, so a backend
	// that consumes gummi's tools over MCP (rather than opts.Tools) has a
	// socket to dial; one whose transport hasn't landed simply ignores it.
	// The endpoint is bound before spawning the child so a child that dials
	// on start never races the bind. The teardown is returned for the caller
	// to stash on the Session's lifecycle; on any failure below the endpoint
	// is released here, so callers see a nil teardown alongside an error.
	mcpPath, mcpTeardown, err := e.startMCPEndpoint(ctx, f, flavor)
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
		MCPSockPath:    mcpPath,
		FeatureID:      string(f.ID),
		ReadOnly:       readOnly,
		ResumePath:     resumeSessionPath(e.cfg.Workspace, f.ID, role, flavor),
		ResumeID:       resumeID,
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
// stage. Pre-worktree stages run in the card's scratch tree
// (.gummi/scratch/<ID>, a detached checkout of main) against the draft —
// materialized here so the agent never starts against a missing spec.
// Later stages require the worktree but read and write the artifact at
// its workspace home in the main checkout (.gummi/specs|bugs, never
// committed) — promoted here in case a crash or a legacy
// committed-artifact item left promotion undone. No stage runs in the
// main checkout: every agent gets a real filesystem boundary its
// backend's write cage already enforces, and the artifact — which lives
// outside every working directory — is reached through gummi's spec
// tools, not the filesystem.
func (e *Engine) locate(ctx context.Context, f domain.Feature) (workDir, specPath string, err error) {
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return "", "", err
	}
	root := e.pool.Root()
	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	// Every research stage — interactive (shape) or autonomous (investigate/
	// review/verify) — is branch-worktree-less and reads the artifact at its
	// workspace home. Research has no draft-then-promote step (Create seeds
	// the artifact directly, never a draft), and never enters a worktree —
	// the only path that promotes a draft into its artifact — so routing
	// shape through a draft the way brainstorm/spec do would orphan its
	// edits: nothing ever merges them back. Promote here is a no-op cleanup
	// once the artifact exists (the common case); it only materializes a
	// fresh one for a crash-recovery or legacy edge case. The scratch tree
	// is the cwd for the same reason it is everywhere else — the ReadOnly
	// tool-stripping stays the research guarantee, and the tree is what
	// makes it structural rather than a promise about tool coverage.
	if f.Kind == domain.KindResearch {
		artifact := filepath.Join(root, f.ArtifactPath())
		if err := spec.Promote(artifact, draft, "", &f); err != nil {
			return "", "", err
		}
		scratch, serr := wt.EnsureScratch(ctx, &f)
		if serr != nil {
			return "", "", serr
		}
		return scratch, artifact, nil
	}
	// Every feature and bug stage — design and work alike — runs in the
	// card's own branch worktree, allocated here on its first stage run
	// and kept for the card's whole life. There is no scratch tree and no
	// hand-off: the design stages and the coding stages share one
	// directory, which is why an implement → plan bounce needs no tree
	// juggling. A plan that writes a spike writes it on the branch
	// implement will continue, and verify sees the whole diff regardless.
	hadWT, err := wt.Exists(ctx, &f)
	if err != nil {
		return "", "", err
	}
	if !hadWT && (f.Stage == domain.StageImplement || f.Stage == domain.StageVerify) {
		// A work stage with no worktree is not a first run — it is a tree
		// that went missing under a card already past its design gate.
		// Recover it from the branch rather than silently cutting a fresh
		// one off main, which would discard the work.
		if rerr := e.recoverMissingWorktree(ctx, wt, &f); rerr != nil {
			return "", "", fmt.Errorf("feature %s at stage %s has no worktree and could not be recreated (%v); recreate .gummi/worktrees/%s from the feature's branch manually", f.ID, f.Stage, rerr, f.ID)
		}
	}
	workDir, err = wt.Ensure(ctx, &f)
	if err != nil {
		return "", "", err
	}
	// A rewrite of main reported after the worktree was created makes the
	// on-disk branch's base incoherent with main; refuse before promoting
	// the artifact or handing the agent a workdir it can only deepen the
	// divergence in.
	if err := wt.AssertNoForkDrift(ctx, &f); err != nil {
		return "", "", err
	}
	// Promotion now happens on the card's first stage run rather than at
	// its approval gate: the worktree exists from here on, so there is no
	// later moment the artifact has to be moved into. Promote is a no-op
	// once the artifact is at its workspace home.
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
	// A turn blocked inside ask_user cannot take another one: the session
	// reports itself not-busy there (handleAsk drops the spinner so the
	// question can be read), but the backend's turn is very much still
	// open, waiting on the person. Refused up front, before anything is
	// consumed or recorded — and refused as ErrBusy, so the caller offers
	// the line again rather than treating it as a failed run.
	if s.Snapshot().PendingAsk != nil {
		return fmt.Errorf("%s is waiting on your answer: %w", id, agent.ErrBusy)
	}
	// deliver any queued budget nudge before the orchestrator's own text
	// (DESIGN §5.1 layer 2: the mid-session threshold is folded into the
	// next turn rather than injected mid-flight).
	nudge := s.takePendingNudge()
	if nudge != "" {
		msg = nudge + "\n\n" + msg
	}
	s.appendUser(msg)
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})
	if err := e.deliverTurn(ctx, s, msg); err != nil {
		// The backend is the authority on whether it can take a turn, and
		// it said no. Undo what this call consumed and recorded, so a
		// refusal leaves the session exactly as it found it: the nudge
		// goes back on the queue for the turn that does land, and the
		// echo comes back out of the transcript. An echo of a line the
		// agent never received is worse than no echo — the reader sees
		// their own sentence, believes it delivered, and has no way to
		// tell otherwise. That is what a line typed while the spinner was
		// up used to look like, right before failRun killed the stage
		// under it.
		if errors.Is(err, agent.ErrBusy) {
			s.dropUnsentUser(msg)
			s.requeueNudge(nudge)
			e.persist(s)
			e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})
		}
		return err
	}
	return nil
}

// deliverTurn dispatches msg as the session's next turn to the backend.
// It is Send's dispatch half with the transcript append already done —
// Answer's convention path uses it so an answer the transcript already
// recorded (and the durable decision log cites) is not appended a second
// time by the delivery that carries it.
func (e *Engine) deliverTurn(ctx context.Context, s *Session, msg string) error {
	if s.agent() == nil {
		return fmt.Errorf("%s is queued, not yet running", s.Feature.ID)
	}
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
	if err := s.agent().Send(ctx, msg); err != nil {
		// ErrBusy is the backend saying "not now", not "this session is
		// broken". Failing the run over it is how a second thought typed
		// while the spinner was up used to kill a stage — and the card
		// then reported "plan failed" while the same session went on
		// answering questions, because failRun leaves an interactive
		// session running and nothing ever clears the error it set.
		if errors.Is(err, agent.ErrBusy) {
			return err
		}
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

// removeFromQueue drops id from whichever pool's queue holds it (a
// feature is only ever queued in the one pool its session took, but the
// caller here doesn't always know which). Caller holds e.mu.
func (e *Engine) removeFromQueue(id domain.FeatureID) {
	for p := range e.lanes {
		q := e.lanes[p].queue
		for i, qid := range q {
			if qid == id {
				e.lanes[p].queue = append(q[:i], q[i+1:]...)
				return
			}
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

// freeSlot releases an autonomous session's attention slot — returning it
// to the pool the session actually took it from, per its remembered
// pool — and promotes that pool's queue. It is a no-op for a session that
// never took a slot (interactive, or queued-and-dropped), and idempotent
// for one that did.
func (e *Engine) freeSlot(s *Session) {
	held, p := s.releaseSlot()
	if !held {
		return
	}
	e.mu.Lock()
	if e.lanes[p].running > 0 {
		e.lanes[p].running--
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

// ChangeProfile switches a card's profile in place. It always persists
// profile to the card's Feature.Profile — the same field every later
// stage/role already reads, so this is not a one-off override that
// reverts on the next stage — and, when the card has a live session
// right now, restarts it immediately under the new profile rather than
// waiting for the next resume. profile must name a declared profile;
// an unknown name is refused before any store write or session is
// touched.
func (e *Engine) ChangeProfile(ctx context.Context, id domain.FeatureID, profile string) error {
	known := false
	for _, name := range e.cfg.Profiles.Names() {
		if name == profile {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("no profile named %q", profile)
	}
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	f.Profile = profile
	if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
		return err
	}

	s := e.Get(id)
	if !s.Live() {
		return nil // nothing running to restart
	}

	// captured before Pause, whose stop() finalizes s and would otherwise
	// leave these unreadable once the restart needs them.
	interactive := s.Interactive
	flavor := s.flavor()
	note := s.kickoffNote

	// the single-session interrupt/dequeue/persist/stop/freeSlot path —
	// not StopForQuit's bulk, GateApproval-gated, quit-park-event path,
	// which this restart has no use for.
	if err := e.Pause(ctx, id); err != nil {
		return err
	}
	if interactive {
		// Pause's stop() leaves s.agentSess non-nil (Live's own doc
		// comment); without clearing it, Attach's reuse check
		// (prior.agent() != nil) would hand back the now-dead session
		// instead of spawning fresh under the new profile.
		s.clearAgent()
		_, err := e.Attach(ctx, f)
		return err
	}
	return e.run(f, note, flavor)
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
	// the budget stop is a decision like any other (the one kind autopilot
	// must refuse — a top-up widens the card's reach, §10.17): the record
	// goes down where the stop happens, so both loops see it by
	// construction. Best-effort: the park above is already durable.
	if e.cfg.Store != nil {
		question := string(s.Feature.Stage) + " ran out of budget."
		if committed {
			question = string(s.Feature.Stage) + " ran out of budget with work committed."
		}
		_ = e.cfg.Store.OpenDecision(context.Background(), s.Feature.ID, s.Feature.Stage,
			state.DecisionPayload{
				ID:       "budget:" + strconv.FormatInt(time.Now().UnixNano(), 10),
				Kind:     state.DecisionKindBudget,
				Question: question,
			}, e.now())
	}
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventExhausted, Committed: committed})
	s.stop() // finalizes the session, closing the underlying agent and MCP teardown
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
//
// It takes boardMu first, and holds it throughout, because a board spawn
// deliberately releases e.mu across the slow backend start (boardMu's own
// comment on why). Without this, Close can flip closed, stop what it can
// see and reach e.wg.Wait() while a spawn that already got past
// replaceBoard is still on its way to the e.wg.Add(1) for its pump — an
// Add racing a Wait, which is a WaitGroup misuse panic rather than merely
// a goroutine left running. That was latent while a board was opened once
// at startup and never again; ReopenBoard makes a mid-life spawn something
// a user triggers by typing, so a quit landing on one is ordinary rather
// than exotic. Taking it here is safe from deadlock because no goroutine
// e.wg tracks ever reaches for boardMu — nothing inside this package calls
// OpenBoard or ReopenBoard.
func (e *Engine) Close() error {
	e.boardMu.Lock()
	defer e.boardMu.Unlock()
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
	board := e.board
	e.board = nil
	consults := make([]*ConsultSession, 0, len(e.consult))
	for _, c := range e.consult {
		consults = append(consults, c)
	}
	e.consult = map[domain.FeatureID]*ConsultSession{}
	for p := range e.lanes {
		e.lanes[p].queue = nil
	}
	e.mu.Unlock()

	for _, s := range sessions {
		s.stop()
	}
	if board != nil {
		board.sess.stop()
	}
	for _, c := range consults {
		c.stopBackend()
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
	// The caller binds the agent before launching this goroutine, but a
	// clearAgent (a drop, a pause, an Attach that replaces the session) can
	// land before this line runs. Read it once under the lock and leave
	// quietly if it is already gone: there is no stream left to relay, and
	// dereferencing nil would take the process down with it.
	a := s.agent()
	if a == nil {
		e.emitStopped(s)
		return
	}
	events := a.Events()
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
				// the backend is gone either way, so nothing of ours is
				// driving this card any more: release its lock rather than
				// holding a card hostage to a dead agent until the user
				// pauses or advances it.
				s.releaseCard()
				if s.State() == StateRunning {
					e.failRun(s, errSessionDied)
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
		s.appendToolCall(ev.CallID, toolLine(ev), ev.Tool, ev.Detail)
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
		e.recordUsage(s, s.Feature.ID, s.Feature.Stage, s.Role, ev.Usage)
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
			// the slot frees the moment the agent itself stops, not after
			// its git/bookkeeping epilogue below — settle's checkpoint
			// commit can run up to checkpointTimeout, and the footer must
			// not claim an occupied slot for a card whose agent has
			// already gone idle. freeSlot's own held-latch makes this
			// safe to call again from failRun's path below: the second
			// call is simply a no-op.
			e.freeSlot(s)
			// a fatal settle (the worktree itself is gone, not just dirty or
			// uncommitted) must fail the run instead of reading as a clean
			// finish — otherwise the caller advances the stage with no
			// worktree left to review it against.
			if err := e.settle(s); err != nil {
				e.failRun(s, err)
				return
			}
			e.stageReceipt(s)
			e.gateVerifyVerdict(s)
			// and the promises floor: a verify that passed while one of
			// the plan's own invariants is unanswered, or while a golden
			// it pinned is on the branch nowhere, is a pass about the
			// process rather than about the work.
			e.gatePromiseVerdict(s)
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

// recordUsage folds one usage sample into the store's running totals —
// the feature's overall spend and its per-(stage, role, model) breakdown
// — shared by the stage pump (handle's EventUsage case) and a card's
// consult session (handleConsult), so a question answered outside a
// stage still lands in the same figures a stage's own spend does. It
// persists the credit-equivalent (not raw credits): a token-only stage
// reports tokens with zero credits, and storing that raw would make its
// spend invisible to the credits-denominated envelope once any credits-
// metered stage contributed credits. Metered events already carry
// credits, so this leaves them unchanged and never double-counts.
//
// s is the session the sample belongs to — its credit rate and
// pending-estimate bookkeeping (creditEquivalent, take/notePendingEst)
// live there, regardless of whether s backs a stage session or a
// ConsultSession (both are *Session underneath). id/stage/role name what
// to attribute the sample to: a stage session passes its own
// Feature.ID/Feature.Stage/Role; handleConsult passes the bound card's
// id, its stage, and agent.RoleConsult.
func (e *Engine) recordUsage(s *Session, id domain.FeatureID, stage domain.Stage, role agent.Role, u agent.Usage) {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return
	}
	credits := s.creditEquivalent(u)
	// estimated is the token/rate-derived portion of credits, kept as its
	// own accumulator so displays can label live figures instead of
	// presenting them as real. A settle event retires the model's
	// outstanding estimates: the adapter's own mid-turn estimates are
	// already inside its signed correction, while the engine's
	// token-priced fallback (recorded before the adapter knew a rate) is
	// not — so that portion comes off the credit total here too, leaving
	// exactly the provider-metered figure.
	var estimated float64
	switch {
	case u.Settled:
		credits = u.Credits // signed correction; never token-priced
		tokenEst, adapterEst := s.takePendingEst(u.Model)
		credits -= tokenEst
		estimated = -(tokenEst + adapterEst)
	case u.Metered:
		// the provider's metered figure, authoritative even at zero:
		// token-pricing it would invent spend the provider never charged
		// (and a later settle delta would then double-count it), so it
		// passes through signed with no estimate booked.
		credits = u.Credits
	case u.Credits <= 0:
		estimated = credits
		s.notePendingEst(u.Model, credits, 0)
	case u.Estimate:
		estimated = credits
		s.notePendingEst(u.Model, 0, credits)
	}
	if credits == 0 && estimated == 0 && u.InputTokens == 0 && u.OutputTokens == 0 {
		return
	}
	_ = e.cfg.Store.AddSpend(context.Background(), id, credits, estimated, u.InputTokens, u.OutputTokens)
	// the card's running total moves by exactly what the row just did,
	// so a render can read the store's figure off the session instead of
	// the board snapshot it last reloaded.
	s.addCardSpent(credits)
	// the same sample attributed to (stage, session, model, role) for the
	// breakdown; same credit-equivalent, so stage_spend sums to
	// spend_credits. The session key is this generation's, so a stage that
	// bounced through review→fix keeps its first attempt and its redo in
	// separate rows instead of adding them together. A backend's internal side-model call is booked to
	// the helper role, not the working role it ran under — else a
	// token-less title/summary call inflates and mis-attributes the
	// working role's row.
	if u.Helper {
		role = agent.RoleHelper
	}
	_ = e.cfg.Store.RecordStageSpend(context.Background(), id, state.SpendSample{
		Stage: stage, Session: s.generation(), Role: string(role), Model: u.Model,
		Credits: credits, Estimated: estimated,
		InputTokens: u.InputTokens, CachedTokens: u.CachedTokens, OutputTokens: u.OutputTokens,
	})
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
	if s.Interactive {
		// A design chat runs in the card's own worktree now, so anything it
		// writes survives to implement without a hand-off — but it is a
		// conversation, not work, and checkpointing every turn of one would
		// bury the branch's real history. The tree is no longer discarded,
		// so nothing is lost by waiting.
		return nil
	}
	// Research stages are worktree-less by design (a research branch never
	// receives a commit), not merely worktree-less because one went missing. There is nothing
	// on disk to commit and there never will be, so the whole function is
	// a no-op here — and, the reason this is a return rather than a
	// swallow of CommitAll's ErrNoWorktree below, there is nothing to
	// report either. That swallow still wrote the failure into the
	// session's activity, so every autonomous research stage told the
	// reader its checkpoint had failed, for a condition the design
	// guarantees and no reader can act on.
	if s.Feature.Kind == domain.KindResearch {
		return nil
	}
	// A goal's branch takes only its cards' landings and gummi's own
	// catch-up merges. The goal's own sessions — its plan, review and
	// verify — write nothing that belongs there; what they leave in the
	// goal worktree is scratch (a binary built to run a check, a captured
	// stderr), and a checkpoint would commit it onto the branch that lands.
	if s.Feature.IsGoal() {
		return nil
	}
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
		if !errors.Is(err, worktree.ErrNoWorktree) {
			e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventCheckpointFailed, Err: err})
			return nil
		}
		// past the guard above this stage needs a worktree, so a missing
		// one is the total-loss case: report it back rather than letting
		// the run read as a clean finish.
		return err
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

// workStageCritique reports whether a critique on this stage is judging a
// diff — the ones that inherited the Review stage's job. The plan's
// critique judges a document and has no diff to be handed.
func workStageCritique(stage domain.Stage) bool {
	switch stage {
	case domain.StageImplement:
		return true
	}
	return false
}
