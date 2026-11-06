// Package engine is gummi's orchestrator: it runs each session's agent in
// its card's worktree (freeformsession.go), conducts a session's workflow
// runs — their steps in the session's own tree, the end checks and the
// report (workflow*.go, DESIGN §25) — fires schedules and heartbeats, ticks
// stacks, runs the skill curator, routes turns and streams typed
// activity to the faces.
//
// A session runs the moment it is asked to. Nothing caps how many run at
// once, and nothing queues one behind another: parallelism is the
// operator's decision. The per-card lock is what keeps two processes from
// driving the same card. A workflow card left in an old database is read,
// never run.
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
	"github.com/morphis/gummi/internal/attachment"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/skillpack"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

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

// errSessionDied is the error a session's pump records when its backend's
// event stream ends without the session having been stopped.
var errSessionDied = errors.New("agent session died without finishing")

// Config wires an engine to its backends. Model is the M1 stand-in for
// profiles: the fallback model when no profile applies. The default
// backend is the entry stored under the "" key in Agents (matched by
// agentFor's fallback).
type Config struct {
	// WatchPollEvery is how often a workflow watch reads its PR; zero is
	// the default (30s, backing off to 5m while nothing changes).
	WatchPollEvery time.Duration
	// Agents maps a backend name (matching a profile role's `backend:`)
	// to its adapter. The empty-string key "" designates the default
	// backend used when no profile applies or a role omits `backend:`.
	// buildAgents in cmd/gummi seeds both the default's Name() and ""
	// with the same adapter, so lookups by either always resolve.
	Agents map[string]agent.Agent
	// StartAgent starts a backend by name for a session that asks for one
	// this board did not start (DESIGN §19.8): the profiles decide which
	// backends a board launches, and a person may pick any installed agent
	// for a session. Nil means a session may only name a backend already
	// in Agents. Backends it starts are the engine's, closed by Close.
	StartAgent func(name string) (agent.Agent, error)
	Store      *state.Store
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
	// (§5.1 layer 3, see sessionBudget).
	StageBudget float64
	// TurnReserve is one agent turn's worth of credits, the floor for
	// every envelope-derived budget (0 = domain.TurnReserveCredits).
	// Enforcement runs between turns, so smaller caps cannot be held.
	TurnReserve float64
	// Instructions are absolute paths to extra instruction files appended
	// to the workspace environment card, in user-then-workspace order.
	Instructions []string
	// Curator is the skill curator's configuration (DESIGN §23.4); nil
	// means no curator runs. The pack itself is delivered either way.
	Curator *config.Curator
	// Triage reads a session's first request, before its agent hears it,
	// for whether it is work for a workflow recipe (triage.go). Off in a
	// zero Config; cmd/gummi switches it on unless config.yaml says
	// `triage: off`.
	Triage bool
	// Workflows is whether sessions are offered specs and workflow runs
	// when they have no switch of their own; off unless the workspace
	// says on (config.yaml `workflows: on`).
	Workflows bool
}

// Engine orchestrates all live sessions and the autonomous runs.
type Engine struct {
	cfg Config
	// workflowsOn is Config.Workflows, changed live by the settings dialog.
	workflowsOn atomic.Bool
	now         func() time.Time // injectable clock (spec-capture timestamps)

	// raw carries events from pump goroutines to the forwarder; events
	// is the UI-facing stream, owned solely by the forwarder.
	raw     chan Event
	events  chan Event
	stopped chan struct{}

	mu     sync.Mutex
	closed bool

	// consult holds every card's consult session, keyed by feature:
	// OpenConsult is idempotent per card for the engine's whole lifetime
	// (see consultsession.go), so once a card's entry exists here it is
	// reused, never replaced, until Close.
	consult map[domain.FeatureID]*ConsultSession
	// consultMu serializes OpenConsult end to end: spawning a backend is
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

	// freeform holds every freeform card's session, keyed by feature — the
	// same shape as consult, and for the same reason: OpenFreeform is
	// idempotent per card for the engine's whole lifetime, so a card page
	// reopened, or a diff surface sending its comments, reaches the one
	// session that card has. Unlike consult these sessions WRITE: each
	// holds its card's worktree, its per-card lock and its envelope (see
	// freeformsession.go).
	freeform map[domain.FeatureID]*FreeformSession
	// workflows is the workflow runs' runtime state (workflow.go).
	workflows *workflowRuntime
	// freeformClosed keeps the conversation of each freeform card whose
	// session ended (FreeformHistory), so a closed card still shows what
	// was said on it.
	freeformClosed map[domain.FeatureID]*Session
	// started holds the backends a session asked for that the board did
	// not start (sessionAgent), keyed by name, closed by Close.
	startedMu sync.Mutex
	started   map[string]agent.Agent
	// modelCatalog caches each backend's own answer about the models it
	// offers (SessionModelCatalog): the probes are subprocess and RPC
	// reads a picker may repeat, so a fresh answer is trusted for
	// modelCatalogTTL. Negative answers (a backend that cannot say, or a
	// probe that failed) are cached for the same span, so a picker does
	// not re-pay a slow failure on every open. An answer past its span is
	// still served while a refresh asks again in the background; catalogAsk
	// holds the one probe per backend under way, closed when it ends.
	// Probes run on catalogCtx, not on the read that started them, and
	// Close cancels it and waits (catalogWG): a probe's process group is
	// killed with the board rather than left to outlive it.
	catalogMu     sync.Mutex
	modelCatalog  map[string]modelCatalogEntry
	catalogAsk    map[string]chan struct{}
	catalogCtx    context.Context
	catalogCancel context.CancelFunc
	catalogWG     sync.WaitGroup
	// freeformMu serializes OpenFreeform end to end — consultMu's job, for
	// the identical check-then-act reason. Held by OpenFreeform and SwitchSessionModel, and
	// never while e.mu is also held.
	freeformMu sync.Mutex
	// freeformIdleTimeout bounds how long a freeform card's backend stays
	// spawned with no turns sent — consultIdleTimeout's twin, a field for
	// the same test reason.
	freeformIdleTimeout time.Duration
	// limitSlack is the wait past a usage limit's stated reset (limitresume.go).
	limitSlack time.Duration

	// wg tracks the pump and kickoff goroutines so Close can join them
	// before returning: a barrier for any filesystem touch (git subprocess
	// snapshots, persist writes) those goroutines may still be mid-way
	// through when teardown begins.
	wg sync.WaitGroup

	// ctx is the engine's own lifetime: a brief turn runs on it rather than
	// on the caller that asked for the brief, so it outlives the dialog or
	// request that started it. cancel ends it, and Close calls it. Only New
	// sets them: an Engine built as a literal has no lifetime for a brief
	// turn to run on, so the brief path is for engines from New alone.
	ctx    context.Context
	cancel context.CancelFunc
	// briefs joins the detached one-shot turns (goBrief: an objective's
	// audit, a workflow run's end), so Close waits for
	// every one before the freeform teardown saves the conversation and
	// drops the card lock.
	briefs sync.WaitGroup

	// mcpSeq is the atomic source of engine-side MCP call ids, so a
	// session's in-flight dispatches are unique and never collide with a
	// backend's own tool-call ids (disjoint namespaces).
	mcpSeq atomic.Uint64
	// answering holds one *sync.Mutex per card, so a card's answers are
	// taken one at a time (AnswerAs). Routing an answer can bring a
	// backend up, which takes a while, and a second answer arriving
	// meanwhile would find the question still open and bring up another.
	answering sync.Map

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

	// profiles is the profile set new sessions resolve against, re-read
	// from profiles.yaml when the file changes (profilereload.go);
	// profStamp is the file version it came from, profErr why the latest
	// edit was refused, profReloads how many edits were picked up. All
	// guarded by profMu.
	profMu      sync.Mutex
	profInit    bool
	profiles    config.Profiles
	profStamp   profileStamp
	profErr     string
	profReloads int

	// repoCards caches the repository orientation card per repository
	// root, computed at most once per root per Engine lifetime. A
	// workspace with several managed repos gets one card each; the map is
	// keyed by root rather than by card so a repo whose card comes out
	// empty is not recomputed on every session.
	repoCardMu sync.Mutex
	repoCards  map[string]string

	// pack is the workspace skill pack (skillpack.go); curator holds the
	// curator's run state (curator.go).
	packOnce sync.Once
	pack     *skillpack.Pack
	// usageMu serializes the skill usage file's read-modify-write, and
	// guards skillAsks.
	usageMu sync.Mutex
	// skillAsks is, per card, the pack skills its sessions used and
	// whether its next turn has asked about each yet (skillstage.go).
	skillAsks map[string]map[string]bool
	curator   curatorState
	// repoInstructions caches the managed repository's own instruction
	// card (AGENTS.md/CLAUDE.md, quoted) per repository root, under the
	// same mutex and with the same lifetime as repoCards.
	repoInstructions map[string]string

	// stackLocks serializes the restack walk per stack (stack.go's
	// stackLock), so two ticks never replay two members at once.
	stackLocksMu sync.Mutex
	stackLocks   map[domain.StackID]*sync.Mutex
	// treeLocks serializes finding or making a card's working directory
	// (locateFor's treeLock): a turn and a person's shell (WorkDir) may
	// both find it missing at once, and only one may make it.
	treeLocksMu sync.Mutex
	treeLocks   map[domain.FeatureID]*sync.Mutex
	// stackReplays holds each stack's latest replay walk (stack.go's
	// StackReplay), guarded by stackLocksMu: what a tick replayed and the
	// pushes it now needs, kept so a surface opened after the walk still
	// has the lines to show.
	stackReplays map[domain.StackID]*stackWalk
}

// New builds an engine from the config. The caller owns every agent's
// lifetime. A single-repository config (Worktrees) is wrapped into a
// one-repo pool so every per-card path resolves uniformly.
func New(cfg Config) *Engine {
	if cfg.Permission == "" {
		cfg.Permission = agent.PermissionAllowAll
	}
	pool := cfg.Pool
	if pool == nil && cfg.Worktrees != nil {
		pool = worktree.WrapSingle(cfg.Worktrees)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		ctx:            ctx,
		cancel:         cancel,
		cfg:            cfg,
		now:            time.Now,
		workflows:      newWorkflowRuntime(),
		raw:            make(chan Event, 256),
		events:         make(chan Event),
		stopped:        make(chan struct{}),
		consult:        map[domain.FeatureID]*ConsultSession{},
		freeform:       map[domain.FeatureID]*FreeformSession{},
		freeformClosed: map[domain.FeatureID]*Session{},
		pool:           pool,
	}
	e.workflowsOn.Store(cfg.Workflows)
	e.initProfiles()
	e.consultIdleTimeout = consultIdleTimeout
	e.freeformIdleTimeout = freeformIdleTimeout
	e.limitSlack = limitSlack
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

// RunRebaseStopped hands a rebase the board's own run could not finish to
// the card's session: files names the paths that conflicted, and reason
// is git's own account of a stop that was not a plain conflict
// (worktree.RebaseConflictError.Reason), put in the turn so the agent
// starts from what actually happened instead of looking for conflict
// markers that are not there.
func (e *Engine) RunRebaseStopped(ctx context.Context, f domain.Feature, files []string, reason string) error {
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return err
	}
	// the same rebase the board's r ran and saw stop on conflicts, against
	// the card's own base — which is not always what the checkout has out,
	// and for a card whose base was rewritten under it replays only the
	// card's own commits (worktree.rebaseTarget)
	cmd, err := wt.RebaseCommand(ctx, &f)
	if err != nil {
		return err
	}
	// A stage session live until this dispatch can dirty the worktree
	// after the board checked it (mid-turn edits) — and the plain
	// rebase refuses to start on dirt. Carry it the way the drift path
	// does: autostash it across, and say whose it is so it comes back
	// out instead of being read as fallout.
	carried := false
	if dirty, err := wt.Dirty(ctx, &f); err == nil && dirty {
		cmd += " --autostash"
		carried = true
	}
	base := wt.BaseRevFor(ctx, &f)
	if base == "HEAD" {
		base = wt.BaseBranch(ctx)
	}
	note := "Rebase this branch onto " + base + "'s current tip: run `" + cmd + "`."
	switch {
	case reason != "":
		note += "\nThe board's own run of it stopped: " + reason + "."
	case len(files) > 0:
		note += "\nExpect conflicts in: " + strings.Join(files, ", ") + "."
	}
	if carried {
		note += "\nThe worktree carries uncommitted work; it is this card's own. The --autostash in the command carries it across the rebase and puts it back — if applying the autostash conflicts at the end, resolve and keep it as the contract below says; never discard it."
	}
	// The hand-off is a turn in the card's own session, which the person
	// reads in the thread. Nothing judges the result — the card has no
	// gate that waits on it.
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		return err
	}
	return ff.Send(ctx, note)
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

// stampSpawnInfo records on the session which backend and model its
// profile/role resolve to — and the adapter's token→credit rate — so
// status displays (and interactive budget math) have them from the
// moment the session exists, not after the first usage event.
func (e *Engine) stampSpawnInfo(s *Session) {
	rc, backend := e.resolveRole(s.Feature.Profile, s.Role)
	a := e.agentFor(backend)
	if s.Feature.IsFreeform() {
		// A session names its own agent and model (DESIGN §19.8); asked
		// without starting one, since a restore spawns nothing.
		rc, backend = e.sessionRole(s.Feature)
		a = e.knownAgent(backend)
	}
	name := backend
	rate := 0.0
	clientTools := false
	if a != nil {
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

// scratchFilesDirFor returns the card's scratch-files directory, created
// best-effort — the shared derivation every session kind that stamps one
// uses (stage sessions, and the consult and freeform conversations a
// backend with a redirected-home requirement anchors there). Empty when
// the workspace has no root or creation fails: callers treat "" as "no
// anchor", never as an error.
func (e *Engine) scratchFilesDirFor(id domain.FeatureID) string {
	dir := e.cfg.Workspace.ScratchFilesDir(id)
	if dir == "" || e.cfg.Workspace.Root == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
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
	return e.locateFor(ctx, f, false)
}

// WorkDir is the directory a session's next turn works in — its worktree,
// its home or the main checkout — made now if it has none yet, exactly as
// that turn would make it. It is where a person's shell starts (the web
// face's Terminal tab), so a session that has not taken a turn yet still
// has somewhere to open one. Fork drift is no refusal here: a person's
// shell is one way to deal with it. Only an open session has one.
func (e *Engine) WorkDir(ctx context.Context, f domain.Feature) (string, error) {
	if !f.IsFreeform() || f.IsChild() {
		return "", fmt.Errorf("%s is not a session: it has no working directory of its own", f.Name())
	}
	if f.Stage == domain.StageDone {
		return "", fmt.Errorf("%s is closed: its working directory is gone", f.Name())
	}
	dir, _, err := e.locateFor(ctx, f, true)
	return dir, err
}

// treeLock is card id's lock on finding or making its working directory.
func (e *Engine) treeLock(id domain.FeatureID) *sync.Mutex {
	e.treeLocksMu.Lock()
	defer e.treeLocksMu.Unlock()
	if e.treeLocks == nil {
		e.treeLocks = map[domain.FeatureID]*sync.Mutex{}
	}
	m := e.treeLocks[id]
	if m == nil {
		m = &sync.Mutex{}
		e.treeLocks[id] = m
	}
	return m
}

// locateFor is locate, with allowDrift skipping the fork-drift refusal —
// for the rebase-resolve pass alone (newAgentSession).
func (e *Engine) locateFor(ctx context.Context, f domain.Feature, allowDrift bool) (workDir, specPath string, err error) {
	// Everything below may make the directory — an agent's home, a
	// session's worktree, one recovered from its branch — and each of
	// those is a check and then a write. A step's card locates its
	// session's tree under its own lock, and never the other way round.
	mu := e.treeLock(f.ID)
	mu.Lock()
	defer mu.Unlock()
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return "", "", err
	}
	root := e.pool.Root()
	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	// A legacy research card has no worktree and never runs (Engine.run
	// refuses it), so there is nothing to locate.
	if f.Kind == domain.KindResearch {
		return "", "", fmt.Errorf("%s is a retired research card, and it no longer runs", f.ID)
	}
	// A freeform card minted into the main checkout has no worktree to
	// ensure and no branch to cut: its working directory is the managed
	// checkout itself, shared with everything the person has there. That
	// is the choice its mint recorded — no branch, no worktree, no
	// protection (DESIGN §19) — so none of what follows applies to it:
	// there is no fork point to drift, and nothing here may create the
	// tree the card was minted to do without.
	if f.IsFreeform() && f.MainCheckout {
		return wt.RepoRoot(), "", nil
	}
	// A workflow step's card has no tree of its own: it works in its
	// session's checkout, beside the run's other steps (DESIGN §25.6).
	if dir, isStep, err := e.stepTreeDir(ctx, f); isStep {
		return dir, "", err
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
	// A freeform card's tree is the same total loss as a work stage's when
	// it vanishes — its branch carries every turn it has taken — but its
	// stage says nothing about whether it has ever had one, since it holds
	// StageOpen from mint. Its fork point does: stamped when the branch was
	// first cut, so a recorded one means there is a branch to recover from
	// and an empty one means this is simply the first turn.
	if !hadWT && f.IsFreeform() && f.ForkPoint != "" {
		if rerr := e.recoverMissingWorktree(ctx, wt, &f); rerr != nil {
			return "", "", fmt.Errorf("freeform card %s has no worktree and could not be recreated (%v); recreate .gummi/worktrees/%s from its branch manually", f.ID, rerr, f.ID)
		}
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
	if !allowDrift {
		if err := wt.AssertNoForkDrift(ctx, &f); err != nil {
			return "", "", err
		}
	}
	// A freeform card has no artifact to promote: there is no draft, no
	// workspace home, and no document any stage was told to read (DESIGN
	// §19). Everything above it still applies — it works in its own branch
	// worktree, and a rewrite of main under it is refused the same way —
	// because the worktree is the half of a card it keeps.
	if f.IsFreeform() {
		return workDir, "", nil
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

// Attachments returns the workspace's image store (internal/attachment),
// rooted under the gitignored .gummi an upload, a spec-anchored link, and
// a kickoff turn all resolve ids and paths through.
func (e *Engine) Attachments() *attachment.Store {
	return &attachment.Store{Dir: e.cfg.Workspace.AttachmentsDir()}
}

// checkImageCapable reports agent.ErrImagesUnsupported when s's live
// backend cannot take a Turn's images — it is not an agent.ImageSender or
// its advertised capability says no — without sending anything. The
// per-model check (copilot's vision flag) lives in that adapter's own
// SendTurn and surfaces the same error once a turn actually reaches it.
func (e *Engine) checkImageCapable(s *Session) error {
	a := s.agent()
	if _, ok := a.(agent.ImageSender); !ok {
		return fmt.Errorf("%s: %w", s.Feature.ID, agent.ErrImagesUnsupported)
	}
	if caps, ok := agent.CapabilitiesFor(s.Snapshot().AgentName); ok && !caps.Images {
		return fmt.Errorf("%s: %w", s.Feature.ID, agent.ErrImagesUnsupported)
	}
	return nil
}

// imagesSupporter is implemented by a backend whose per-model vision
// support SessionTakesImages checks live (copilot, via the CLI's own
// model list); every other capable backend answers for its whole adapter.
type imagesSupporter interface {
	SupportsImages(ctx context.Context, model string) (bool, error)
}

// SessionTakesImages reports whether the next line a card's composer
// sends can carry images — the composer's attach control reads this,
// unlike checkImageCapable (SendTurn's own gate), which stays backend-
// level only so a model-level refusal still surfaces as an error at the
// adapter rather than needing a live check on every send. The line
// reaches the session it would be routed to: a freeform card's own
// session (never in e.live — OpenFreeform's doc comment on
// FreeformSession.Session), a live stage session (the steer path), or —
// any other card in the workflow — its consult session.
//
// The consult path answers even before a session exists: an idle card's
// first prose line opens one (OpenConsult), and its attach control would
// otherwise be hidden exactly where a screenshot of the thing being asked
// about is most wanted. Nothing is spawned to answer; the backend the
// open would resolve answers structurally, and only the per-model part
// (copilot's vision flag) stays unknowable until the session is live — a
// refusal then surfaces at the send, with the line restored. The same
// structural answer covers a freeform card whose session nothing has
// opened yet — a restart before its first turn.
func (e *Engine) SessionTakesImages(ctx context.Context, id domain.FeatureID) bool {
	if ff := e.Freeform(id); ff != nil {
		if s := ff.Session(); s != nil {
			return e.imageCapable(ctx, s)
		}
	}
	f, err := e.feature(ctx, id)
	if err != nil {
		return false
	}
	if f.IsFreeform() {
		if f.Stage == domain.StageDone {
			// landed: the card's session is closed, and its open refuses
			// a closed card — the same floor OpenFreeform enforces
			return false
		}
		_, backend := e.sessionRole(f)
		return e.structurallyTakesImages(backend)
	}
	if f.Kind == domain.KindGoal {
		return false
	}
	if c := e.Consult(id); c != nil {
		if s := c.Session(); s != nil {
			return e.imageCapable(ctx, s)
		}
	}
	_, backend := e.resolveConsultRole(f.Profile)
	return e.structurallyTakesImages(backend)
}

// imageCapable is the live answer an existing session gives about its own
// next turn: the structural gate (an ImageSender backend whose advertised
// capabilities take images) plus, for an adapter that can answer per
// model (copilot, via the CLI's own cached model list), the model's.
func (e *Engine) imageCapable(ctx context.Context, s *Session) bool {
	if e.checkImageCapable(s) != nil {
		return false
	}
	if sup, ok := s.agent().(imagesSupporter); ok {
		can, err := sup.SupportsImages(ctx, s.Snapshot().Model)
		return err == nil && can
	}
	return true
}

// structurallyTakesImages is imageCapable for a session that does not
// exist yet — the backend the session's open would resolve answers
// structurally, without spawning anything. An agentFor fallback to the
// default (a named backend this host does not run) is a no: the open
// would refuse that name rather than silently switch.
func (e *Engine) structurallyTakesImages(backend string) bool {
	a := e.agentFor(backend)
	if a == nil {
		return false
	}
	if backend != "" && a.Name() != backend {
		return false
	}
	return a.Capabilities().Images
}

// deliverTurn dispatches msg (and any images) as the session's next turn
// to the backend. It is SendTurn's dispatch half with the transcript
// append already done — Answer's convention path uses the text-only form
// so an answer the transcript already recorded (and the durable decision
// log cites) is not appended a second time by the delivery that carries
// it.
func (e *Engine) deliverTurn(ctx context.Context, s *Session, msg string, images []AttachmentRef) error {
	if s.agent() == nil {
		return fmt.Errorf("%s is still starting", s.Feature.ID)
	}
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
	var err error
	if len(images) > 0 {
		turn := agent.Turn{Text: msg, Images: e.turnImages(images)}
		err = s.agent().(agent.ImageSender).SendTurn(ctx, turn)
	} else {
		err = s.agent().Send(ctx, msg)
	}
	if err != nil {
		// ErrBusy is the backend saying "not now", not "this session is
		// broken". Failing the run over it is how a second thought typed
		// while the spinner was up used to kill a stage — and the card
		// then reported "plan failed" while the same session went on
		// answering questions, because failRun leaves an interactive
		// session running and nothing ever clears the error it set.
		if errors.Is(err, agent.ErrBusy) || errors.Is(err, agent.ErrImagesUnsupported) {
			return err
		}
		s.setError(err)
		e.persist(s)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		return err
	}
	return nil
}

// turnImages resolves a turn's attachment refs to the absolute host paths
// an agent.ImageSender takes, via the workspace's attachment store. A ref
// whose file has since gone missing is dropped rather than failing the
// whole turn — the transcript still names it.
func (e *Engine) turnImages(refs []AttachmentRef) []agent.Image {
	store := e.Attachments()
	images := make([]agent.Image, 0, len(refs))
	for _, r := range refs {
		path, err := store.Path(r.ID)
		if err != nil {
			continue
		}
		images = append(images, agent.Image{Path: path, MediaType: r.MediaType})
	}
	return images
}

// kickoffTurn resolves the spec-anchored attachments source names (a
// stage's spec file content for a stage kickoff, the brief itself for a
// freeform one — a description attachment lands as a link in either) into
// what the kickoff carrying text should say and, when s's live backend can
// take them, the images it should carry natively alongside it.
//
// It never errors: a spec-named attachment must never be the reason a
// stage fails to start. A ref whose file has since gone missing is named
// as missing in the text either way; every other ref rides natively when
// the backend takes images, or is named by its absolute path in the text
// when it cannot — so a read-tool can still open it.
func (e *Engine) kickoffTurn(s *Session, text, source string) (string, []AttachmentRef) {
	refs := attachment.Refs(source)
	if len(refs) == 0 {
		return text, nil
	}
	store := e.Attachments()
	capable := e.checkImageCapable(s) == nil
	var images, textOnly []AttachmentRef
	for _, r := range refs {
		ref, err := store.Get(r.ID)
		if err != nil {
			textOnly = append(textOnly, AttachmentRef{ID: r.ID})
			continue
		}
		full := AttachmentRef{ID: ref.ID, Name: ref.Name, MediaType: ref.MediaType, Size: ref.Size}
		if capable {
			images = append(images, full)
		} else {
			textOnly = append(textOnly, full)
		}
	}
	if lines := e.imagePathLines(textOnly); lines != "" {
		text = text + "\n\n" + lines
	}
	return text, images
}

// imagePathLines renders one "Attached image: <path> (<media type>)" line
// per ref, naming a ref whose file has since gone missing as missing
// instead — the text a kickoff falls back to for an image it cannot (or,
// after a late per-model refusal, turned out not to be able to) deliver
// natively.
func (e *Engine) imagePathLines(refs []AttachmentRef) string {
	store := e.Attachments()
	var lines []string
	for _, r := range refs {
		ref, err := store.Get(r.ID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("Attached image missing: %s", r.ID))
			continue
		}
		path, err := store.Path(r.ID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("Attached image missing: %s", r.ID))
			continue
		}
		lines = append(lines, fmt.Sprintf("Attached image: %s (%s)", path, ref.MediaType))
	}
	return strings.Join(lines, "\n")
}

// Drop forgets a card's persisted session rows (on delete).
func (e *Engine) Drop(id domain.FeatureID) {
	e.persistDelete(id)
}

// TopUp durably raises a card's envelope after its session stopped on it —
// the "top up" answer to a budget stop (DESIGN §5.1 layer 3). The raise is
// persisted, so it survives a restart; RaisedEnvelope sizes it so the
// session has real multi-turn headroom rather than a sliver. Nothing is
// resumed here: the session carries on with its next turn.
func (e *Engine) TopUp(ctx context.Context, id domain.FeatureID) error {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if f.Budget.Envelope <= 0 {
		return nil
	}
	raised := f.Budget.RaisedEnvelope(f.Spend.CreditEquivalent())
	if int(raised) <= f.Budget.Envelope {
		return nil
	}
	if err := e.checkDispatchedRaise(ctx, f, int(raised)); err != nil {
		return err
	}
	f.Budget.Envelope = int(raised)
	if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
		return err
	}
	e.envelopeRaised(ctx, f)
	return nil
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
			return fmt.Errorf("%s: %s is below the %s floor (spend plus resume headroom)", id, domain.FormatDollars(float64(to)), domain.FormatDollars(float64(floor)))
		}
	}
	if err := e.checkDispatchedRaise(ctx, f, to); err != nil {
		return err
	}
	raised := to == 0 || to > f.Budget.Envelope
	f.Budget.Envelope = to
	if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
		return err
	}
	if raised {
		e.envelopeRaised(ctx, f)
	}
	return nil
}

// envelopeRaised tells a session's workflow run its pool grew (§24.7a).
func (e *Engine) envelopeRaised(ctx context.Context, f domain.Feature) {
	if f.IsFreeform() && !f.IsChild() {
		e.workflowEnvelopeRaised(ctx, f.ID)
	}
}

// Close stops every session and closes the event stream.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	if e.cancel != nil {
		e.cancel()
	}
	consults := make([]*ConsultSession, 0, len(e.consult))
	for _, c := range e.consult {
		consults = append(consults, c)
	}
	e.consult = map[domain.FeatureID]*ConsultSession{}
	freeforms := make([]*FreeformSession, 0, len(e.freeform))
	for _, ff := range e.freeform {
		freeforms = append(freeforms, ff)
	}
	e.freeform = map[domain.FeatureID]*FreeformSession{}
	e.mu.Unlock()

	e.stopCatalogProbes()
	for _, c := range consults {
		c.stopBackend()
	}
	// A brief turn canceled above writes its "did not answer" note and any
	// line it sends from the queue. Both must be on the record before the
	// teardown below saves the conversation and drops the card lock, so
	// the brief turns are joined here, not after it.
	e.briefs.Wait()
	// A freeform session's teardown saves the conversation and drops the
	// card lock with the backend — so another gummi process can drive the
	// card once this board is gone, and so the person who comes back finds
	// the conversation they left. It commits nothing: whatever the card's
	// worktree holds stays there, for the agent or the person to commit on
	// purpose.
	for _, ff := range freeforms {
		ff.stopWatches()
		ff.settle()
		ff.stopBackend()
	}
	// and the holds this engine kept on the sessions whose runs it
	// conducted (holdRuns)
	e.letGoAllRuns()
	// Join the pump and kickoff goroutines so no git subprocess or persist
	// write is still in flight against the workspace when Close returns.
	e.wg.Wait()
	e.closeStartedAgents()
	close(e.stopped)
	return nil
}

// goBrief runs fn detached on the engine's lifetime, joined by Close. It
// reports false once the engine is closing: the check and the join's Add
// share the lock Close sets closed under, so an Add never races the Wait.
func (e *Engine) goBrief(fn func()) bool {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	e.briefs.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.briefs.Done()
		fn()
	}()
	return true
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

// send hands an event to the forwarder, applying backpressure rather
// than dropping. A closed engine unblocks via stopped.
func (e *Engine) send(ev Event) {
	select {
	case e.raw <- ev:
	case <-e.stopped:
	}
}
