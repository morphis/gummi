package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// A freeform card is a coding agent that happens to be a card (DESIGN
// §19): no stages, no gates, no critique, no verify — and a worktree, a
// branch, an envelope, a thread and the diff surface, which is every
// structural thing a card has.
//
// This file is its session, and it is deliberately assembled out of the
// two sessions that already exist rather than out of a stage run:
//
//   - From ConsultSession: keyed per card, idempotent to open, and a
//     backend that idles out after 20 minutes and respawns carrying its
//     own transcript. A freeform conversation outlives any one backend.
//   - From an engine.Session: folded so every surface renders it with
//     the same Snapshot machinery, and outside every stage mechanism —
//     no scheduling (a human-paced conversation is never rationed against
//     autonomous STAGES), no gate, no verdict, no advance.
//
// What it has that neither of them does is the reason it needed its own
// file: it WRITES. So it takes the card's worktree as its cwd, the card's
// per-card lock for as long as it lives, the card's envelope as a real
// cap. What it does NOT do is commit for the agent: every commit on a
// freeform card's branch is one the agent made on purpose, so what a turn
// leaves loose stays in the worktree until somebody means to keep it.
//
// What it deliberately does NOT have is a gate, a
// verdict, a round cap or a kickoff. The corrective-round cap exists to
// stop an unattended loop from spinning; here the human is the loop, and
// the envelope is the only bound.

// freeformIdleTimeout bounds how long a freeform card's backend stays
// spawned with no turns sent. It is consultIdleTimeout's twin and for the
// same reasons (long enough to read a diff and write comments before the
// next turn, short enough that an abandoned conversation's subprocess
// does not outlive the session). Engine.freeformIdleTimeout is seeded
// from it so a test can shrink it.
const freeformIdleTimeout = 20 * time.Minute

// FreeformSession is one freeform card's whole working life: the agent
// conversation, the worktree it writes in, the lock that keeps a second
// gummi out of it, and the envelope it spends against.
//
// One exists per card for this engine's lifetime (OpenFreeform is
// idempotent per card). The backend it holds is not as long-lived: the
// idle timeout closes it and the next turn respawns one carrying this
// session's own transcript, so sess is swapped rather than fixed and mu
// guards the swap.
type FreeformSession struct {
	engine *Engine
	id     domain.FeatureID
	// rc/backend are resolved at OpenFreeform and reused by every respawn.
	// They change only through SwitchSessionModel, which swaps them under
	// mu and stops the backend so the next turn respawns on the new pair.
	rc      config.RoleConfig
	backend string

	mu   sync.Mutex
	sess *Session
	// workDir is the card's worktree, known once a backend has spawned;
	// Commands reads the project's command files from it.
	workDir string
	// compacts is the spawned backend's Capabilities.Compact: whether
	// Commands offers /compact.
	compacts bool
	// gummi's own watches (freeformwatch.go): running, and ended with an
	// exit still to report. They outlive any one backend.
	watches      []*freeformWatch
	watchEnded   []*freeformWatch
	watchSeq     int
	watchWG      sync.WaitGroup
	watchStop    chan struct{}
	watchStopped bool
	watchSent    time.Time
	// queue holds the lines sent while a turn was in flight, oldest
	// first, guarded by mu. They go to the agent together as the next
	// turn once this one ends (drainQueue) — including a turn that ended
	// because it was interrupted, which is how "stop, and do this
	// instead" is said — and until then each can be taken back.
	queue []queuedTurn

	// writeMu serializes one card's memory writes (freeformmemory.go):
	// an MCP backend may issue two memory_write calls in parallel —
	// mcpsock dispatches each in its own goroutine — and a rename-based
	// replace racing an append would drop the append onto the replaced
	// inode. Native backends are pump-serialized and never race; this
	// is for the ones that can.
	writeMu sync.Mutex

	// lockMu guards release, this engine's hold on the card's per-card
	// lock. The hold spans a BACKEND's life, not the conversation's: a
	// backend is what drives the card, and while one exists the worktree
	// may hold work that is not committed yet, which is what a `gummi
	// merge` or a second board arriving must be excluded from.
	//
	// It is deliberately not the conversation's life. A freeform card's
	// session outlives its backend — across the idle timeout, and now
	// across a restart (restoreFreeformLocked) — and a lock held for all of
	// that would mean a board left open overnight blocks every CLI landing
	// of every freeform card on it, including ones nobody has touched.
	// Between turns there is no backend; what the worktree still holds
	// uncommitted is guarded by Remove's refusal of a dirty tree, not by
	// this lock.
	lockMu  sync.Mutex
	release func()

	idleMu    sync.Mutex
	idleTimer *time.Timer
}

// OpenFreeform starts (or reuses) a freeform card's session, taking the
// card's per-card lock and ensuring its worktree on the way. Every later
// call — another turn, the diff surface delivering comments, reopening
// the card page — returns the identical *FreeformSession.
//
// It refuses a card that is not freeform: a card in the workflow is
// driven by its stages, and handing one a session with no stage, no
// verdict and no gate would be a second way to run it that skips all
// three.
func (e *Engine) OpenFreeform(ctx context.Context, f domain.Feature) (*FreeformSession, error) {
	if !f.IsFreeform() {
		return nil, fmt.Errorf("%s is a %s card: it runs through its stages, not as a freeform session", f.ID, f.Kind)
	}
	if f.Stage == domain.StageDone {
		return nil, fmt.Errorf("%s is closed; reopen it before sending it a turn", f.ID)
	}
	// Serialized end to end for the reason consultMu exists: spawning a
	// backend is too slow to hold e.mu across, so a check-then-act around
	// a released lock would let two callers for the same card both see
	// "not open yet" and both spawn one — and here that would also mean
	// two writers in one worktree.
	e.freeformMu.Lock()
	defer e.freeformMu.Unlock()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("engine is closed")
	}
	if prior := e.freeform[f.ID]; prior != nil {
		e.mu.Unlock()
		return prior, nil
	}
	e.mu.Unlock()

	rc, backend := e.sessionRole(f)
	ff := &FreeformSession{engine: e, id: f.ID, rc: rc, backend: backend}

	if err := ff.spawn(ctx, nil, ""); err != nil {
		return nil, err
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		ff.stopBackend()
		return nil, errors.New("engine is closed")
	}
	e.freeform[f.ID] = ff
	e.mu.Unlock()
	e.send(Event{Feature: f.ID, Stage: domain.StageOpen, Kind: EventStarted})
	return ff, nil
}

// NoteClosedFreeform adds a line from gummi to a closed freeform card's
// conversation — what became of its work, once that is known — so a reader
// of the closed card sees it beneath the last turn. The row is updated
// too, so the line is there after a restart.
func (e *Engine) NoteClosedFreeform(id domain.FeatureID, text string) {
	e.mu.Lock()
	sess := e.freeformClosed[id]
	e.mu.Unlock()
	if sess == nil {
		return
	}
	sess.appendSystem(text)
	e.persistClosed(sess)
}

// FreeformHistory is the conversation of a freeform card whose session has
// ended, for a reader that wants to show what was said on a closed card.
// ok is false for a card with no ended session in this engine.
func (e *Engine) FreeformHistory(id domain.FeatureID) (Snapshot, bool) {
	e.mu.Lock()
	sess := e.freeformClosed[id]
	e.mu.Unlock()
	if sess == nil {
		return Snapshot{}, false
	}
	return sess.Snapshot(), true
}

// Session returns ff's current backend session, or nil if none has ever
// spawned — AnswerAs's and the UI's hook into a freeform card's own
// session, which e.live never holds (OpenFreeform's doc comment).
func (ff *FreeformSession) Session() *Session {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.sess
}

// Freeform looks up a card's freeform session without ever spawning one —
// the read path a render or a delivery uses (the diff surface's request
// changes) to reach whatever exists without opening a backend as a side
// effect of asking.
func (e *Engine) Freeform(id domain.FeatureID) *FreeformSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.freeform[id]
}

// spawn starts a fresh backend for ff, seeded with the given transcript
// (nothing on the first open, this session's own history on a respawn).
// It installs the new *Session on ff and starts its pump, but never
// touches e.freeform: the caller decides whether this is a first install
// or a respawn of an already-registered session.
func (ff *FreeformSession) spawn(ctx context.Context, seed []Message, resumeID string) error {
	e := ff.engine
	ff.mu.Lock()
	rc, backend := ff.rc, ff.backend
	ff.mu.Unlock()
	ag, err := e.sessionAgent(backend)
	if err != nil {
		return fmt.Errorf("%s's session: %w", ff.id, err)
	}
	// The card lock, for as long as this backend exists. A second gummi
	// driving the card is excluded from here until the backend stops, and
	// the refusal names that rather than a git failure further in.
	if err := ff.takeLock(); err != nil {
		return fmt.Errorf("%s is being driven elsewhere: %w", ff.id, err)
	}
	f, err := e.feature(ctx, ff.id)
	if err != nil {
		ff.dropLock()
		return err
	}
	// The worktree, ensured here: this is what "gets its worktree/branch"
	// means, and it is the same locate() every stage goes through, so the
	// branch is cut the same way and a rewrite of main under it is refused
	// the same way. A freeform card comes back with no artifact path.
	workDir, _, err := e.locate(ctx, f)
	if err != nil {
		ff.dropLock()
		return err
	}
	ff.mu.Lock()
	ff.workDir = workDir
	ff.compacts = ag.Capabilities().Compact
	ff.mu.Unlock()

	sctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		Feature:     f,
		Role:        agent.RoleImplementer,
		Interactive: true,
		state:       StateInteractive,
		done:        make(chan struct{}),
		ctx:         sctx,
		cancel:      cancel,
		startedAt:   time.Now(),
	}
	sess.setSpawnInfo(ag.Name(), rc.Model, ag.Capabilities().ClientTools)
	sess.setByokRate(ag.CreditRate(rc.Model))
	if len(seed) > 0 {
		sess.transcript = append(sess.transcript, seed...)
	}

	// The envelope, as a real cap: recomputed on every respawn from what
	// the card has left, so a long conversation's later backends are
	// capped by what is actually still there rather than by what was left
	// when it opened. This is the one floor a freeform card keeps.
	budget := e.stageBudget(f, sess.rate())
	sess.setBudget(budget)
	e.seedCardSpend(sess)

	hints := e.freeformHints(ctx, f, workDir, budget, ag)
	// The context a person comes back to. Two ways, and which one applies
	// is the backend's to decide, not ours to guess:
	//
	//   - A backend that can continue its OWN conversation is asked to
	//     (ResumeID below, from the row this session was restored from).
	//     Full fidelity, nothing replayed, nothing paid for twice.
	//   - One that cannot — no Resume capability (headless/BYOK), or no
	//     recorded conversation to continue — is handed the conversation as
	//     text instead. Without this the transcript on screen would be a
	//     record the model does not share, and the first turn after a
	//     restart would answer as though nothing had been said.
	if replay := freeformReplayHint(seed, ag.Capabilities().Resume && resumeID != ""); replay != "" {
		hints = append(hints, replay)
	}

	// gummi's tools here are ask_user and resolve_annotation — the same
	// pair any other coding-agent session would have for a card with no
	// artifact and no gate, and the diff comments that steer this one
	// (DESIGN §6.1: resolve_annotation's open count burns down live). They
	// reach the model by whichever of the two routes the backend supports,
	// exactly as a stage's tools do: natively, or over this card's own
	// inbound MCP endpoint. A backend with neither falls back to the
	// fenced-block convention for ask_user, same as a stage session's.
	var tools []agent.ToolDef
	var mcpPath string
	var mcpTeardown func()
	if caps := ag.Capabilities(); caps.ClientTools || caps.MCPTools {
		tools = stageTools(domain.StageOpen, flavorStage, nil)
		if h := toolHint(domain.StageOpen, flavorStage); h != "" {
			hints = append(hints, h)
		}
		// gummi's own watch, for a backend with no Monitor of its own
		var extra []agent.ToolDef
		if !caps.NativeWatch {
			extra = []agent.ToolDef{watchTool(), unwatchTool()}
			tools = append(tools, extra...)
			hints = append(hints, freeformWatchHint)
		}
		// Project memory (freeformmemory.go): the workspace's global
		// memory — read-only to the session — and the session's own
		// files under .gummi/memory, inlined at spawn by memoryCard
		// and filled as the session works. Offered on every freeform
		// backend with a tool route — independent of whether it has
		// a Monitor of its own.
		extra = append(extra, memoryReadTool(), memoryWriteTool())
		tools = append(tools, memoryReadTool(), memoryWriteTool())
		hints = append(hints, freeformMemoryHint)
		path, teardown, merr := e.startMCPEndpoint(ctx, f, flavorStage, extra...)
		if merr != nil {
			cancel()
			return merr
		}
		mcpPath, mcpTeardown = path, teardown
	} else {
		hints = append(hints, askConventionHint)
	}

	agentSess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:        workDir,
		Role:           agent.RoleImplementer,
		Model:          rc.Model,
		SystemHints:    hints,
		Permission:     e.cfg.Permission,
		MaxCredits:     budget * capHeadroom,
		Tools:          tools,
		OutputTokenMax: rc.OutputTokenMax,
		MCPSockPath:    mcpPath,
		FeatureID:      string(ff.id),
		SkillDirs:      e.skillDirsFor(ag, backendLabel(backend)),
		// No ArtifactPath: there is no document.
		//
		// ResumePath and ResumeID are how a freeform conversation survives
		// a restart on a backend that keeps its own: the path is stable per
		// card (one session, for the card's whole life, so there is no
		// flavor to distinguish), and the id is whatever the last backend
		// reported, restored with the row. A backend that cannot resume
		// ignores both and reads the replay hint instead.
		ResumePath: resumeSessionPath(e.cfg.Workspace, ff.id, agent.RoleImplementer, flavorStage),
		ResumeID:   resumeID,
	})
	if err != nil {
		if mcpTeardown != nil {
			mcpTeardown()
		}
		cancel()
		ff.dropLock()
		return fmt.Errorf("starting %s's freeform session: %w", ff.id, err)
	}
	sess.setMCPTeardown(mcpTeardown)
	if !sess.attachAgent(agentSess) {
		_ = agentSess.Close()
		if mcpTeardown != nil {
			mcpTeardown()
		}
		cancel()
		ff.dropLock()
		return errors.New("engine is closed")
	}
	sess.setState(StateInteractive)
	e.trackAgentPID(ff.id, agentSess)

	ff.mu.Lock()
	ff.sess = sess
	ff.mu.Unlock()

	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pumpFreeform(ff, sess) }()
	ff.armIdleTimer()
	return nil
}

// freeformHints is the whole system-prompt stack a freeform card's
// session gets (DESIGN §19). It is deliberately short, and what is
// missing from it is the design:
//
//   - No stage contract, because there is no stage. Nothing here tells
//     the agent to converge on a plan, fill a section, or emit a verdict.
//   - No artifact, because a freeform card has none. A stage session is
//     told where its document is and to write its progress into it; this
//     one is told the opposite — the thread is the record.
//   - No gate. A stage session is told what its gate will demand; this
//     one has nothing to earn, and saying so is what keeps it from
//     inventing ceremony nobody asked for.
//
// What it keeps is everything about the machine it is standing in: the
// operator's environment, the repository and its own instructions, the
// worktree boundary, the budget, and how the person will steer it.
func (e *Engine) freeformHints(ctx context.Context, f domain.Feature, workDir string, budget float64, ag agent.Agent) []string {
	var hints []string
	if card := e.environmentCard(); card != "" {
		hints = append(hints, card)
	}
	if mgr, err := e.mgr(ctx, &f); err == nil && mgr != nil {
		if card := e.repoInstructionsCard(mgr.RepoRoot()); card != "" {
			hints = append(hints, card)
		}
		if card := e.repoCard(mgr.RepoRoot()); card != "" {
			hints = append(hints, card)
		}
	}
	// Project memory — the workspace's global memory and this card's
	// session memory — is the read-at-the-start half; it rides only a
	// backend that has a tool route, because its own text promises the
	// tools that fill it.
	if caps := ag.Capabilities(); caps.ClientTools || caps.MCPTools {
		if card := e.memoryCard(f.ID); card != "" {
			hints = append(hints, card)
		}
	}
	hints = append(hints, repoInstructionsPrecedenceFreeform, freeformContractHint(f, workDir))
	if budget > 0 {
		hints = append(hints, budgetHintFreeform(budget))
	}
	// Any diff comments still open — the reader's, from before this
	// backend existed. A respawned session must inherit them, or a
	// conversation that idled out between "request changes" and the fix
	// would lose the request entirely.
	hints = append(hints, e.diffReviewHints(ctx, f.ID, ag.Capabilities().ClientTools)...)
	return hints
}

// feature reads the card's authoritative row, so a respawn sees the
// envelope a top-up raised and the branch state a landing changed rather
// than the copy the session opened with. Without a store (tests, a
// transient engine) there is nothing to read and the caller's copy is all
// there is, which is why this returns an error rather than silently
// handing back a zero Feature.
func (e *Engine) feature(ctx context.Context, id domain.FeatureID) (domain.Feature, error) {
	if e.cfg.Store == nil {
		return domain.Feature{}, fmt.Errorf("no store: cannot resolve %s", id)
	}
	return e.cfg.Store.GetFeature(ctx, id)
}

// ensureBackend returns ff's current backend, respawning one — carrying
// over ff's own accumulated transcript — when the last one has idled out.
// Session.Live() is the predicate that makes the respawn trigger exactly
// when the backend is gone and never while a turn is in flight.
func (ff *FreeformSession) ensureBackend(ctx context.Context) (*Session, error) {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	spent := sess != nil && sess.isExhausted()
	if sess != nil && sess.Live() && !spent {
		return sess, nil
	}
	// An exhausted session is respawned rather than refused — but only once
	// the envelope actually has room, since the respawn recomputes its cap
	// from what is left. This is what makes "raise the envelope to carry
	// on" true: the exhaustion latch is per backend, so a topped-up card
	// gets a fresh one instead of a conversation that refuses every turn
	// for the rest of the board's life.
	if spent {
		f, ferr := ff.engine.feature(ctx, ff.id)
		if ferr != nil {
			return nil, ferr
		}
		if ff.engine.stageBudget(f, sess.rate()) <= 0 {
			return nil, fmt.Errorf("%s has spent its envelope of %d credits; raise it to carry on", ff.id, f.Budget.Envelope)
		}
	}
	var seed []Message
	var resumeID string
	if sess != nil {
		snap := sess.Snapshot()
		seed = snap.Transcript
		// The conversation the last backend was keeping, so a backend that
		// can pick its own up is asked to. It survives a restart because
		// the row carries it (restoreFreeformLocked).
		resumeID = snap.AgentSessionID
	}
	if spent && sess.Live() {
		// A spent backend is still running — exhaustFreeform stops the turn,
		// not the process — so it has to be stopped before its replacement
		// exists. Otherwise its pump outlives the session it was started
		// for, and Engine.Close, which joins every pump, waits forever.
		sess.setState(StateDone)
		sess.stop()
	}
	if err := ff.spawn(ctx, seed, resumeID); err != nil {
		return nil, err
	}
	ff.mu.Lock()
	sess = ff.sess
	ff.mu.Unlock()
	return sess, nil
}

// Send delivers one turn to the card's freeform session, respawning its
// backend first if the last one idled out.
//
// It is the single entry point for every kind of feedback a freeform card
// takes — a person's prose in the thread, and the diff surface's compiled
// review comments — because they are the same thing to the agent: a turn.
// That is the whole of what replaces a stage's kickoff, its verdict
// grammar and its bounce edges.
func (ff *FreeformSession) Send(ctx context.Context, msg string) error {
	return ff.SendTurn(ctx, msg, nil)
}

// SendTurn is Send's image-carrying form: a turn with images is delivered
// natively, or refused with agent.ErrImagesUnsupported before anything is
// appended or recorded.
//
// A turn sent while the agent is still on the last one is queued rather
// than refused: the person said it, and the agent should hear it next.
func (ff *FreeformSession) SendTurn(ctx context.Context, msg string, images []AttachmentRef) error {
	sess, err := ff.ensureBackend(ctx)
	if err != nil {
		return err
	}
	a := sess.agent()
	if a == nil {
		return fmt.Errorf("%s's freeform session has no live agent", ff.id)
	}
	// A line typed while a question is open is an answer waiting to be
	// given, not a turn: the agent is blocked on the question, and a line
	// delivered as a turn would reach it without closing the decision, so
	// the card would keep asking for the answer it already has. Refused
	// before anything is echoed or recorded, as Engine.SendTurn does; the
	// answer goes through the question itself.
	if sess.Snapshot().PendingAsk != nil {
		return fmt.Errorf("%s is waiting on your answer: %w", ff.id, agent.ErrBusy)
	}
	if len(images) > 0 {
		if err := ff.engine.checkImageCapable(sess); err != nil {
			return err
		}
	}
	if sess.Busy() {
		ff.enqueue(ctx, msg, images)
		return nil
	}
	sess.appendUserImages(msg, actorOf(ctx), images)
	ff.engine.persist(sess)
	sess.setBusy(true)
	ff.armIdleTimer()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	// the transcript keeps the line as typed; the agent hears what a
	// "/name" in it stands for
	cmds := ff.Commands()
	wire := expandProjectCommands(cmds, ff.WorkDir(), msg)
	var sendErr error
	if c, ok := a.(agent.Compactor); ok && len(images) == 0 && isCompactLine(cmds, msg) {
		sendErr = c.Compact(ctx)
	} else if len(images) > 0 {
		sendErr = a.(agent.ImageSender).SendTurn(ctx, agent.Turn{Text: wire, Images: ff.engine.turnImages(images)})
	} else {
		sendErr = a.Send(ctx, wire)
	}
	if sendErr != nil {
		if errors.Is(sendErr, agent.ErrBusy) || errors.Is(sendErr, agent.ErrImagesUnsupported) {
			// Same undo Engine.SendTurn gives a live steer: a refusal that
			// only surfaces this late (a busy backend, or copilot's
			// per-model vision check, which checkImageCapable's structural
			// gate can't see) must leave no echo and nothing durably
			// recorded — persist rewrites the whole session row, so
			// re-running it after the drop erases the turn from what a
			// restart would restore.
			sess.dropUnsentUser(msg)
			ff.engine.persist(sess)
			if errors.Is(sendErr, agent.ErrBusy) {
				// busy by the backend's own reckoning rather than ours: the
				// same "not now" the check above answers by queueing
				ff.enqueue(ctx, msg, images)
				return nil
			}
			ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
			return sendErr
		}
		sess.setError(sendErr)
		ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventError, Err: sendErr})
		return sendErr
	}
	return nil
}

// queuedTurn is one line waiting for the turn in flight to end. ctx keeps
// who said it (WithActor) but not the request it arrived on, which is
// long finished by the time the line is sent.
type queuedTurn struct {
	ctx    context.Context
	text   string
	images []AttachmentRef
}

func (ff *FreeformSession) enqueue(ctx context.Context, msg string, images []AttachmentRef) {
	ff.mu.Lock()
	ff.queue = append(ff.queue, queuedTurn{ctx: context.WithoutCancel(ctx), text: msg, images: images})
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

// Commands is the project's command files as the card's branch has them
// now, read afresh each time so an edit to one is picked up by the next
// turn, then /compact when the backend can compact. Empty until the
// session's first backend has found its worktree.
func (ff *FreeformSession) Commands() []ProjectCommand {
	ff.mu.Lock()
	workDir, compacts := ff.workDir, ff.compacts
	ff.mu.Unlock()
	cmds := LoadProjectCommands(workDir)
	if compacts {
		if _, _, clash := FindProjectCommand(cmds, "/"+compactCommand.Name); !clash {
			cmds = append(cmds, compactCommand)
		}
	}
	return cmds
}

// WorkDir is the card's worktree, "" until a backend has spawned.
func (ff *FreeformSession) WorkDir() string {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.workDir
}

// Queued is the lines waiting for the turn in flight, oldest first.
func (ff *FreeformSession) Queued() []string {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	out := make([]string, len(ff.queue))
	for i, q := range ff.queue {
		out[i] = q.text
	}
	return out
}

// Unqueue takes back the i'th waiting line before it is sent, returning
// its text so a caller can hand it back to the composer to be edited. ok
// is false when it is no longer waiting — sent already, or never there.
func (ff *FreeformSession) Unqueue(i int) (text string, ok bool) {
	ff.mu.Lock()
	if i < 0 || i >= len(ff.queue) {
		ff.mu.Unlock()
		return "", false
	}
	text = ff.queue[i].text
	ff.queue = append(ff.queue[:i], ff.queue[i+1:]...)
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	return text, true
}

// drainQueue sends everything waiting as one turn, the way it reads to a
// person: several things said while the agent was busy, each its own
// paragraph. It runs off the pump, since a send can block on the backend
// the pump is draining. Not while a question is open: that is waiting on
// an answer, and a queued line is not one.
func (ff *FreeformSession) drainQueue(sess *Session) {
	if sess.Snapshot().PendingAsk != nil {
		return
	}
	ff.mu.Lock()
	q := ff.queue
	ff.queue = nil
	ff.mu.Unlock()
	if len(q) == 0 {
		return
	}
	texts := make([]string, len(q))
	var images []AttachmentRef
	for i, t := range q {
		texts[i] = t.text
		images = append(images, t.images...)
	}
	go func() {
		// an error is the session's own and already on it (SendTurn)
		_ = ff.SendTurn(q[0].ctx, strings.Join(texts, "\n\n"), images)
	}()
}

// Kickoff sends the card's brief as the session's first turn, and does
// nothing at all if the conversation has already started.
//
// It exists because a freeform card's description is the task: the person
// typed it into the creation dialog and expects work to begin, not to have
// to retype it into the thread. The brief is also in the session's system
// prompt (freeformContractHint), which is what orients a RESPAWNED backend
// — this is the turn that actually starts the first one.
//
// Idempotent by the transcript rather than by a flag, so every path that
// might start a card (creating it, opening its page, delivering the first
// diff comments to a card nobody ever talked to) can call it without
// checking first, and only the first one costs anything.
func (ff *FreeformSession) Kickoff(ctx context.Context) error {
	return ff.KickoffWith(ctx, "")
}

// KickoffWith is Kickoff with the person's own opening message, verbatim:
// the text a session was started with, which the card itself cannot hold
// (it keeps a line-sized title and one-liner, domain.SplitFreeform). A
// session started from its first message wants every line of that message
// as its first turn, not the title it was shortened to. Empty falls back
// to the card's title and one-liner, which is all a card minted elsewhere
// (the TUI's form, a restart before the first turn) has to go on.
func (ff *FreeformSession) KickoffWith(ctx context.Context, opening string) error {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess != nil && len(sess.Snapshot().Transcript) > 0 {
		return nil
	}
	f, err := ff.engine.feature(ctx, ff.id)
	if err != nil {
		return err
	}
	brief := strings.TrimSpace(opening)
	if brief == "" {
		brief = strings.TrimSpace(f.Title + "\n\n" + f.OneLiner)
	}
	if brief == "" {
		return nil
	}
	// The brief is both the turn and the document a description
	// attachment's link landed in, so it is scanned for its own links —
	// unlike a stage kickoff, which scans its spec separately from its
	// (unrelated) boilerplate text.
	live, err := ff.ensureBackend(ctx)
	if err != nil {
		return err
	}
	text, images := ff.engine.kickoffTurn(live, brief, brief)
	return ff.SendTurn(ctx, text, images)
}

// InterruptFreeform stops a freeform card's turn in flight. It is
// Engine.Interrupt's counterpart for a session that is not in e.live, and
// it keeps the backend: the conversation continues, this turn does not.
// Whatever the turn wrote before it was stopped stays in the worktree,
// uncommitted, for the next turn to carry on from.
func (e *Engine) InterruptFreeform(ctx context.Context, id domain.FeatureID) error {
	ff := e.Freeform(id)
	if ff == nil {
		return fmt.Errorf("%s has no freeform session to interrupt", id)
	}
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("%s has no live turn", id)
	}
	if a := sess.agent(); a != nil {
		if err := a.Interrupt(ctx); err != nil {
			return err
		}
	}
	sess.setBusy(false)
	sess.appendActivity("stopped mid-turn by the reader")
	e.persist(sess)
	e.send(Event{Feature: id, Stage: domain.StageOpen, Kind: EventUpdated})
	return nil
}

var errCommitBusy = errors.New("the session is mid-turn; commit once this turn ends")

// CommitFreeform commits everything in a freeform card's worktree to its
// branch with the person's own message: the one way a freeform card's work
// becomes a commit without the agent making it. It reports whether there
// was anything to commit.
//
// It refuses while a turn is in flight, because a commit taken then would
// catch the turn half-written.
func (e *Engine) CommitFreeform(ctx context.Context, id domain.FeatureID, message string) (bool, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return false, errors.New("a commit needs a message")
	}
	ff := e.Freeform(id)
	if ff != nil && ff.Busy() {
		return false, errCommitBusy
	}
	f, err := e.feature(ctx, id)
	if err != nil {
		return false, err
	}
	if !f.IsFreeform() {
		return false, fmt.Errorf("%s is a %s card: its stages commit its work", id, f.Kind)
	}
	if f.MainCheckout {
		return false, fmt.Errorf("%s runs in the main checkout: there is no branch to commit to — commit your work there yourself", id)
	}
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return false, err
	}
	committed, err := wt.CommitAll(ctx, &f, message)
	if err != nil || !committed || ff == nil {
		return committed, err
	}
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess != nil {
		subject, _, _ := strings.Cut(message, "\n")
		sess.appendActivity("committed by the reader: " + subject)
		e.persist(sess)
	}
	e.send(Event{Feature: id, Stage: domain.StageOpen, Kind: EventUpdated})
	return true, nil
}

// Busy reports whether the card's agent is mid-turn, without copying the
// conversation the way Snapshot does.
func (ff *FreeformSession) Busy() bool {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	return sess != nil && sess.Busy()
}

// Watching reports whether the card has a watch open, gummi's or the
// backend's own, whether or not a turn is in flight. A watch is not work
// in progress, so it is not Busy; it is what says the card will speak up
// on its own.
func (ff *FreeformSession) Watching() bool {
	if len(ff.Watches()) > 0 {
		return true
	}
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	return sess != nil && sess.openWatch(time.Now().Add(-freeformWatchMax))
}

// CardSpent is the card's running spend as the session has booked it
// (Session.CardSpent), or 0 with no backend up.
func (ff *FreeformSession) CardSpent() float64 {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return 0
	}
	return sess.CardSpent()
}

// Snapshot returns a render-safe copy of the session's current backend
// state (an empty Snapshot if none has ever spawned).
func (ff *FreeformSession) Snapshot() Snapshot {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return Snapshot{}
	}
	snap := sess.Snapshot()
	snap.Queued = ff.Queued()
	for _, w := range ff.Watches() {
		snap.Watches = append(snap.Watches, w.ID+" · "+w.Command)
	}
	// the backend's own watch (Claude Code's Monitor) is listed beside
	// gummi's, so a reader sees it outside the activity row it was folded
	// into; bounded like the idle timer's check, so a watch whose end was
	// never reported does not stay listed forever
	snap.Watches = append(snap.Watches, sess.openWatches(time.Now().Add(-freeformWatchMax))...)
	return snap
}

// Close ends the card's freeform session: it stops the current backend,
// cancels the idle timer, drops the card lock, and clears the engine's
// reference so a later OpenFreeform starts fresh.
//
// It saves the conversation first. It commits nothing: what the last turn
// left in the worktree stays there, uncommitted, for somebody to commit on
// purpose.
func (ff *FreeformSession) Close() error {
	ff.stopWatches()
	ff.settle()
	ff.stopBackend()
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	ff.engine.mu.Lock()
	if ff.engine.freeform[ff.id] == ff {
		delete(ff.engine.freeform, ff.id)
	}
	// the conversation stays readable once the card has ended: it is the
	// record of the work its branch holds (FreeformHistory)
	if sess != nil && len(sess.Snapshot().Transcript) > 0 {
		ff.engine.freeformClosed[ff.id] = sess
	}
	ff.engine.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventStopped})
	return nil
}

// settle saves the conversation, which is what a person comes back to
// beside the tree: the tree carries what the turns wrote, the row carries
// what was said about it and the backend conversation to continue. Close
// does this on its way out and Engine.Close calls it directly — the two
// teardown paths differ in what else they tidy, not in whether the
// conversation survives. It deliberately commits nothing.
func (ff *FreeformSession) settle() {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return
	}
	ff.engine.persist(sess)
}

// stopBackend stops ff's current backend and cancels its idle timer,
// without touching e.freeform or the card lock — Engine.Close's
// counterpart to Close, for the caller that has already cleared the map
// itself.
func (ff *FreeformSession) stopBackend() {
	ff.idleMu.Lock()
	if ff.idleTimer != nil {
		ff.idleTimer.Stop()
		ff.idleTimer = nil
	}
	ff.idleMu.Unlock()
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess != nil {
		sess.stop()
	}
	// The lock goes with the backend: with none running, nothing here is
	// driving the card.
	ff.dropLock()
}

// takeLock acquires this engine's hold on the card's per-card lock, or
// reports why it could not. Idempotent: a session that already holds it
// (a respawn inside one board's life) keeps the one hold rather than
// nesting a second, so every acquire has exactly one release.
func (ff *FreeformSession) takeLock() error {
	ff.lockMu.Lock()
	defer ff.lockMu.Unlock()
	if ff.release != nil {
		return nil
	}
	release, err := ff.engine.lockCard(ff.id)
	if err != nil {
		return err
	}
	ff.release = release
	return nil
}

// dropLock releases the hold if this session has one. Safe to call from
// every teardown path, and from one that never took it.
func (ff *FreeformSession) dropLock() {
	ff.lockMu.Lock()
	defer ff.lockMu.Unlock()
	if ff.release == nil {
		return
	}
	ff.release()
	ff.release = nil
}

// armIdleTimer (re)starts the idle-close timer, called on spawn and on
// every turn sent or reply landing.
func (ff *FreeformSession) armIdleTimer() {
	d := ff.engine.freeformIdleTimeout
	if d <= 0 {
		return
	}
	ff.idleMu.Lock()
	defer ff.idleMu.Unlock()
	if ff.idleTimer != nil {
		ff.idleTimer.Stop()
	}
	ff.idleTimer = time.AfterFunc(d, ff.onIdleTimeout)
}

// freeformWatchMax bounds how long an open watch keeps an idle backend
// alive: the backstop for an end the backend never reports (Claude Code
// reports one, as a task_notification; nothing guarantees every end does).
const freeformWatchMax = 6 * time.Hour

// onIdleTimeout closes only the current backend, marking it StateDone
// first so Session.Live() reads false and the next Send respawns. The
// transcript is untouched, and the card lock is kept: the worktree and
// branch are still this session's, and nothing else may drive the card
// just because its backend went to sleep.
//
// A busy backend is not idle: the clock is armed when a turn is sent, and
// a turn can run past it, so a turn still in flight re-arms the timer
// rather than having its backend killed under it. The idle span counts
// from the turn's end.
func (ff *FreeformSession) onIdleTimeout() {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return
	}
	// A watch the agent left running (agent.WatchTool) lives in this
	// backend: stopping it would end the watch silently while the page
	// still says "watching". So the backend stays up while one is open,
	// bounded so a watch whose end was never reported cannot hold it
	// forever.
	if sess.Busy() || sess.openWatch(time.Now().Add(-freeformWatchMax)) {
		ff.armIdleTimer()
		return
	}
	sess.setState(StateDone)
	sess.stop()
	// A card whose backend has idled out is not being driven, so it stops
	// excluding the verbs that would drive it. The next turn respawns and
	// takes the lock again.
	ff.dropLock()
}

// pumpFreeform relays one freeform backend's agent events into
// handleFreeform and exits when that backend stops or its event channel
// closes — pumpConsult's shape, parametrized by the specific *Session
// this goroutine was started for, since ff.sess can move on to a
// respawned one while this pump is still draining the old backend's
// final events.
func (e *Engine) pumpFreeform(ff *FreeformSession, sess *Session) {
	events := sess.agent().Events()
	for {
		select {
		case <-sess.done:
			return
		case ev, ok := <-events:
			if !ok {
				if !sess.finalizedState() {
					sess.setError(errSessionDied)
					e.send(Event{Feature: ff.id, Kind: EventError, Err: errSessionDied})
					sess.stop()
				}
				return
			}
			e.handleFreeform(ff, sess, ev)
		}
	}
}

// handleFreeform folds one backend event into the freeform session —
// handleConsult's shape, plus the envelope a session that spends needs.
func (e *Engine) handleFreeform(ff *FreeformSession, sess *Session, ev agent.Event) {
	switch ev.Kind {
	case agent.EventTextDelta:
		sess.appendDelta(ev.Text)
	case agent.EventReasoningDelta:
		sess.appendThinking(ev.Text)
	case agent.EventMessage:
		sess.finishAssistant(ev.Text)
		// Saved as it is said, not only at the end: a board that dies
		// mid-turn must not lose the reply it had already streamed, and this
		// row is the only place a freeform card's conversation lives.
		e.persist(sess)
	case agent.EventToolCall:
		sess.appendToolCall(ev.CallID, toolLine(ev), ev.Tool, ev.Detail)
	case agent.EventToolResult:
		if ev.Result != nil {
			sess.resolveToolResult(ev.CallID, ev.Result.OK, ev.Result.Output)
		}
	case agent.EventClientToolCall:
		e.dispatchFreeformClientTool(ff, sess, ev.ToolCall)
		return
	case agent.EventContext:
		sess.setContext(ev.Context)
	case agent.EventTasks:
		sess.setTasks(ev.Tasks)
	case agent.EventTurnStarted:
		// the backend woke by itself (a watch fired): the session is
		// working until that turn's idle, so a line sent meanwhile queues
		// and the faces say so
		sess.setBusy(true)
		ff.armIdleTimer()
	case agent.EventUsage:
		sess.addSpend(ev.Usage)
		e.recordUsage(sess, ff.id, domain.StageOpen, agent.RoleImplementer, ev.Usage)
		if sess.overBudget() {
			e.exhaustFreeform(ff, sess)
		}
	case agent.EventIdle:
		// No commit here: a turn ending is not a reason to commit. The agent
		// commits what it means to keep; the rest stays in the worktree.
		sess.setBusy(false)
		// convention-path ask (a backend without client tools): a
		// gummi-ask block in the final message becomes a pending question
		// instead of a finished turn — maybeConventionAsk's own doc
		// (asktool.go), unchanged by which kind of session it is reading.
		if e.maybeConventionAsk(sess) {
			e.persist(sess)
			ff.armIdleTimer()
			e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventQuestion})
			return
		}
		// a turn that ended with its question still open has not finished:
		// the card stays parked on the question, not on a reply that never
		// came (askOutlivedItsCall's own doc).
		e.askOutlivedItsCall(sess)
		e.persist(sess)
		ff.armIdleTimer() // a reply landing resets the idle clock
		ff.drainQueue(sess)
	case agent.EventError:
		sess.setError(ev.Err)
	case agent.EventBudgetExhausted:
		// The BACKEND's own cap, not gummi's envelope (handleBoard's
		// identical case has the full reasoning). Nothing gummi can raise
		// answers this one.
		sess.appendSystem("the backend reported its credit cap was reached — " +
			"this conversation cannot continue until the cap is raised on its side")
		sess.setBusy(false)
	default:
		return
	}
	e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

// exhaustFreeform is the envelope running out on a freeform card. It
// says what would unblock it — and that is
// all it does, because there is nothing here to park: no stage to leave
// mid-flight, no gate to raise, no lane slot to free. The conversation
// simply refuses further turns (Send checks Exhausted) until the person
// raises the envelope.
func (e *Engine) exhaustFreeform(ff *FreeformSession, sess *Session) {
	if !sess.markExhausted() {
		return
	}
	sess.appendSystem("this card has spent its envelope — its work is left as it is in its worktree; " +
		"raise the envelope to carry on")
	sess.setBusy(false)
	e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventExhausted})
}

// dispatchFreeformClientTool answers a freeform session's client-tool
// call by routing it through the same handleClientTool every stage
// session uses — so resolve_annotation behaves identically here, which is
// the point: the review loop on a freeform card is the one that already
// exists, not a second implementation of it.
//
// Inline on the pump goroutine, unlike dispatchConsultClientTool, which
// hands its call to a goroutine because card_diff shells out to git and
// would stall every event behind it. Every tool reachable here —
// resolve_annotation, the watch pair, the memory pair — resolves at once
// (store and file writes, no human in the loop), so it is dispatched the
// way a stage session's is.
func (e *Engine) dispatchFreeformClientTool(ff *FreeformSession, sess *Session, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	sess.appendToolCall(tc.ID, tc.Name, tc.Name, "")
	e.handleClientTool(sess, tc)
	e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

// restoreFreeformLocked rebuilds a freeform card's session from its
// persisted row, so the conversation a person left is the conversation
// they come back to. The caller holds e.mu (Engine.Restore does).
//
// It deliberately starts NO backend and takes NO card lock. A board that
// opens with eight freeform cards on it must not spawn eight agents and
// hold eight locks for cards nobody has touched yet: the session carries
// its transcript, and the next turn is what spawns a backend (ensureBackend
// treats a session with no agent exactly as it treats one whose backend
// idled out). Until then the card costs nothing and blocks nothing.
//
// The backend's own conversation id comes back with the row, so a backend
// that can continue its own conversation is asked to (SessionOpts.ResumeID);
// one that cannot is handed the transcript instead (freeformReplayHint).
func (e *Engine) restoreFreeformLocked(f domain.Feature, snap state.SessionSnapshot) {
	rc, backend := e.sessionRole(f)
	sess := restoredFreeformSession(f, snap)
	e.stampSpawnInfo(sess)
	e.freeform[f.ID] = &FreeformSession{
		engine: e, id: f.ID, rc: rc, backend: backend, sess: sess,
	}
}

// restoreClosedFreeformLocked keeps a closed freeform card's conversation
// readable after a restart: its row outlives the card, and a session that
// ended (landed, handed off, continued as a spec) is still the record of
// what was said and done on that branch. Nothing about it can run again.
func (e *Engine) restoreClosedFreeformLocked(f domain.Feature, snap state.SessionSnapshot) {
	sess := restoredFreeformSession(f, snap)
	sess.cancel()
	e.freeformClosed[f.ID] = sess
}

// restoredFreeformSession rebuilds a freeform session from its row, with no
// backend: the transcript, activity, spend and the backend's conversation
// id, exactly as they were persisted.
func restoredFreeformSession(f domain.Feature, snap state.SessionSnapshot) *Session {
	sctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		Feature:     f,
		Role:        agent.RoleImplementer,
		Interactive: true,
		state:       StateInteractive,
		done:        make(chan struct{}),
		ctx:         sctx,
		cancel:      cancel,
		startedAt:   restoredStart(snap.StartedAt),
	}
	for _, m := range snap.Transcript {
		sess.transcript = append(sess.transcript, Message{
			Author: Author(m.Author), Content: m.Content,
			ToolStatus: ToolStatus(m.ToolStatus), ToolOutput: m.ToolOutput,
			AnsweredBy: m.AnsweredBy,
			Images:     engineImages(m.Images),
		})
	}
	sess.activity = append(sess.activity, snap.Activity...)
	sess.spend = usageFrom(snap)
	sess.exhausted = snap.Exhausted
	sess.setAgentSessionID(snap.AgentSession)
	return sess
}

// restoredStart parses a persisted generation stamp, falling back to now
// for a legacy row that carries none.
func restoredStart(stamp string) time.Time {
	if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return at
	}
	return time.Now()
}

// freeformReplayBudget bounds how much of a conversation is replayed into
// a backend that cannot continue its own. It is a character budget rather
// than a turn count because what matters is the prompt it becomes: a long
// card can hold hundreds of turns, and a replay that grows without limit
// turns every turn after a restart into the most expensive one of the
// card. The newest turns are the ones kept — the work in front of the
// person is what the next turn is about, and the branch's diff carries
// everything older.
const freeformReplayBudget = 8000

// freeformReplayHint is the conversation so far, as text, for a backend
// that cannot continue its own. Empty when there is nothing to replay or
// when the backend was handed a conversation id to resume instead —
// replaying on top of a native resume would state the same turns twice,
// once as history and once as the model's own memory of them.
func freeformReplayHint(seed []Message, resuming bool) string {
	if resuming || len(seed) == 0 {
		return ""
	}
	// Walked newest-first and reversed, so what gets dropped when the
	// budget runs out is the OLDEST turn rather than whatever happened to
	// come last.
	var kept []string
	used := 0
	for i := len(seed) - 1; i >= 0; i-- {
		line := replayLine(seed[i])
		if line == "" {
			continue
		}
		if used+len(line) > freeformReplayBudget && len(kept) > 0 {
			kept = append(kept, "  […] earlier turns are not replayed; the branch's diff carries what they wrote.")
			break
		}
		used += len(line)
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return ""
	}
	slices.Reverse(kept)
	return "This card's conversation so far, which you are continuing. It happened in an\n" +
		"earlier session of yours — treat it as your own work, not as someone else's\n" +
		"report of it, and do not redo what it already did:\n\n" +
		strings.Join(kept, "\n\n")
}

// replayLine renders one transcript entry for the replay: what was SAID,
// and only that.
//
// Tool lines are left out, and the pty drive is why. The transcript keeps
// them for the reader, but two kinds of line live there — the backend's own
// calls, and gummi's activity notes (a budget nudge, "stopped mid-turn by
// the reader") — and a restored transcript cannot tell them apart, because
// the persisted row carries each line's text without the tool name that
// would. Replayed indiscriminately they came out as "you ran: …", which is
// not something the session did.
//
// Nothing is lost by dropping them. What a tool DID is in the worktree the
// session is standing in and in the branch's diff; what it was for is in
// the reply beside it, which is replayed. The conversation is the context;
// the tree is the state.
func replayLine(m Message) string {
	body := strings.TrimSpace(m.Content)
	if body == "" {
		return ""
	}
	switch m.Author {
	case AuthorUser:
		return "  them: " + body
	case AuthorAssistant:
		return "  you: " + body
	case AuthorSystem:
		// gummi's own notes on the conversation — a rewind, a switch of
		// model — are part of what the next backend has to know
		return "  gummi: " + body
	}
	return ""
}
