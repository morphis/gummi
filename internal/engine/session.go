package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/livelog"
)

// EventKind classifies an engine Event.
type EventKind string

const (
	// EventStarted fires when a session becomes active.
	EventStarted EventKind = "started"
	// EventUpdated signals the session's state changed (new delta,
	// tool call, or spend) — the UI should re-render from Snapshot.
	EventUpdated EventKind = "updated"
	// EventMessage fires when the agent completes a message.
	EventMessage EventKind = "message"
	// EventIdle fires when the agent finishes a turn and awaits input.
	EventIdle EventKind = "idle"
	// EventError fires on a session error (Err populated).
	EventError EventKind = "error"
	// EventStopped fires once when the session ends.
	EventStopped EventKind = "stopped"
	// EventBudget fires when a budget threshold is crossed (Threshold set).
	EventBudget EventKind = "budget"
	// EventExhausted fires when the session hit its credit cap.
	EventExhausted EventKind = "exhausted"
	// EventQuestion fires when the agent asks the user a question via the
	// ask_user client tool (Snapshot.PendingAsk populated).
	EventQuestion EventKind = "question"
	// EventAnnotations fires when the agent resolves a diff review comment
	// via the resolve_annotation client tool — an open diff surface should
	// re-read its annotations so the open-count burns down live.
	EventAnnotations EventKind = "annotations"
	// EventCheckpointFailed fires when checkpoint's CommitAll fails for any
	// reason other than ErrNoWorktree (Err populated). It is non-terminal:
	// the session and stage keep running.
	EventCheckpointFailed EventKind = "checkpoint_failed"
	// EventCardCreated fires when a card is minted or filed onto the open
	// board by a caller that holds an *Engine but doesn't otherwise touch
	// the session machinery — a goal's lead — so no other Event would
	// ever cover it. A UI surface should reload rows.
	EventCardCreated EventKind = "card_created"
	// EventGoal asks the driving loop to tick a goal (Feature is the goal):
	// it has work to conduct — it just entered implement, you added a note,
	// it was sent back or stopped.
	EventGoal EventKind = "goal"
)

// Event is one item in the engine's UI-facing stream.
type Event struct {
	// Feature is the card the event belongs to, and MAY BE EMPTY. The one
	// source that sends featureless events is a one-shot pass not bound
	// to a card (ingest, which sends EventError). A consumer
	// that looks a Feature up — Engine.Get, a row lookup, the attention
	// queue — must therefore establish it has one first. The empty case
	// is a normal value on this channel, not a bug upstream.
	Feature   domain.FeatureID
	Stage     domain.Stage
	Kind      EventKind
	Err       error
	Threshold int  // budget % for EventBudget
	Committed bool // EventExhausted: the stage's work was committed (not stranded)
}

// SessionState is a session's scheduling status.
type SessionState string

const (
	// StateInteractive is a chat session.
	StateInteractive SessionState = "interactive"
	// StateRunning is an autonomous session, from the moment it starts until
	// its turn ends or it is stopped. Live reports whether its agent is
	// attached yet.
	StateRunning SessionState = "running"
	// StatePaused is an autonomous session stopped by the user.
	StatePaused SessionState = "paused"
	// StateDone is an autonomous session that finished its turn.
	StateDone SessionState = "done"
)

// Author labels a transcript message.
type Author string

const (
	AuthorUser      Author = "user"
	AuthorAssistant Author = "assistant"
	// AuthorSystem labels gummi-authored turns (stage kickoffs): sent to
	// the agent like a user turn, rendered as gummi's own line.
	AuthorSystem Author = "system"
	// AuthorTool labels activity lines (tool calls, check results, budget
	// nudges) recorded as transcript entries so history keeps them in
	// order with the surrounding messages.
	AuthorTool Author = "tool"
	// AuthorThinking labels the agent's reasoning as its backend streamed
	// it. Shown, never replayed: it is how the agent got to a reply, not
	// part of the conversation (replayLine), nor of the card's record
	// (persist skips it).
	AuthorThinking Author = "thinking"
	// AuthorTasks is the agent's working checklist: one entry, rewritten
	// in place each time its task tool restates the list, its Content the
	// list as agent.FormatTasks writes it. Pinned rather than shown in the
	// flow, and like thinking never replayed nor mirrored to the record.
	AuthorTasks Author = "tasks"
)

// ToolStatus is an AuthorTool entry's known outcome. It stays
// ToolPending for gummi's own notes (nudges, checkpoints) and for
// backends that never report tool results.
type ToolStatus string

const (
	ToolPending ToolStatus = ""
	ToolOK      ToolStatus = "ok"
	ToolFail    ToolStatus = "fail"
)

// AttachmentRef is one image a user turn carried — the attachment store's
// id (what a later read serves from GET /api/attachments/{id}), the name
// it was uploaded under, its media type, and its size.
type AttachmentRef struct {
	ID        string
	Name      string
	MediaType string
	Size      int64
}

// Message is one transcript turn.
type Message struct {
	Author Author
	// By is who typed an AuthorUser line, as the store records a person
	// (state.PersonActor): set when the line came with an actor on its
	// context (WithActor), empty for the terminal's own.
	By        string
	Content   string
	Streaming bool // true while assistant text is still arriving

	// AuthorTool entries only: the call's outcome once the backend
	// reports it, and the captured output (bounded at the adapter).
	ToolStatus ToolStatus
	ToolOutput string

	// Tool and Detail are the call taken apart — the backend's name for
	// the tool, and the salient argument the activity line shows after
	// it. Content still holds the two joined for display; these exist so
	// the durable log can record a tool call as a record rather than as a
	// string a reader has to take back apart.
	//
	// Both are empty on gummi's own activity notes (budget nudges, check
	// results), which are lines rather than calls.
	Tool   string
	Detail string
	// CallID is the backend's tool-call id. Unlike pending below it
	// survives the outcome arriving, because the durable log correlates a
	// result to its call by exactly this value long after the call
	// settled.
	CallID string
	// pending marks an agent tool call still awaiting its outcome;
	// cleared on resolve. It is separate from CallID because "which call
	// is this" and "is it still open" are different questions, and
	// answering the second by erasing the first is what made the id
	// unavailable to everything downstream.
	pending bool
	// At and DoneAt are when the entry was appended and, for a tool call,
	// when its outcome arrived. They are the real times, as against the
	// time a mirror happened to run — the difference between a duration
	// that is measured and one that is invented.
	At     time.Time
	DoneAt time.Time

	// Role names the role that produced this message when it is NOT the
	// role of the session now holding it. Empty — the normal case — means
	// "whatever this session is", and a renderer falls back to the
	// session's own role. It is stamped only where a transcript crosses
	// sessions: OpenConsult seeds a consult session with the card's stage
	// transcript, and without this every seeded turn was relabelled, so a
	// stage reviewer's "VERDICT: pass" reappeared lower down the same card
	// page attributed to `consult` under the architect's model.
	Role agent.Role

	// AnsweredBy names who chose this user-authored turn's text when a
	// person did not type it: state.ActorAutopilot when the unattended
	// loop answered its own ask_user question. Stamped only by
	// appendUserAs — every plain typed send stays empty — and carried
	// through persistence, so the card-event mirror can tell the echo
	// of a machine-taken answer apart from a turn a person typed no
	// matter how many restarts the transcript survives. Empty on legacy
	// rows and every message that is not an ask echo.
	AnsweredBy string
	// Images are the attachments a user turn carried, in the order they
	// were sent. Empty for every turn without one.
	Images []AttachmentRef
}

// generation is this session generation's key: the discriminator that
// keeps its mirrored events unique (persist.go's dedupe prefix) and the
// session column of its realized spend (state.SpendSample.Session). Both
// have to agree on what "this run of this stage" is named, so there is
// one derivation and both read it.
func (s *Session) generation() string {
	return strconv.FormatInt(s.startedAt.UnixNano(), 10)
}

// The verdict floor's kinds: which deterministic floor holds a session's
// one floor slot. Recorded beside the floor and its reason because the
// reason string alone cannot say what re-derives the block — and one
// kind can. The promise floor re-reads the artifact it cited, so a plan
// corrected after the stamp clears the overrule at the next read
// instead of holding the card for a full verify re-run; the others are
// facts about a moment (a check that failed, a probe that probed,
// a document as it stood) and keep their semantics.
const (
	// FloorOmission: a bug's verify passed without exercising any [env:]
	// live check while a prerequisite probed present.
	FloorOmission = "env-omission"
	// FloorChecks: a live gummi-check failed during the verify run.
	FloorChecks = "checks"
	// FloorHygiene: the branch ships a build artifact (diff hygiene).
	FloorHygiene = "hygiene"
	// FloorPromise: a promise the plan made — an invariant verify never
	// answered, or a golden pinned by nothing on the branch — is unmet.
	FloorPromise = "promise"
	// FloorDocument: the research document floor failed.
	FloorDocument = "document"
)

// Snapshot is an immutable view of a session's state, safe to render.
type Snapshot struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool
	Critique    bool // this is a plan-critique pass, not the plan writer
	Rebase      bool // this is a rebase-resolve pass, not the stage's work
	// ReplacedRunning: this rebase pass replaced a stage session that
	// was still mid-turn — the hand-off interrupted it. False on every
	// other session, including a rebase dispatched over a parked card.
	ReplacedRunning bool
	State           SessionState
	AgentName       string // backend running this session ("copilot", "opencode", …)
	// AgentSessionID is the backend's own session id (agent.Identified),
	// pointing at its on-disk log; empty for backends without one.
	AgentSessionID string
	Model          string // model resolved at spawn (Spend.Model is the reported one)
	Transcript     []Message
	Activity       []string // recent tool-call lines
	Spend          agent.Usage
	SpentCredits   float64       // Spend as a credit-equivalent at the provider's rate
	Context        agent.Context // latest context-window occupancy
	Tasks          []agent.Task  // the agent's checklist, as it last stated it
	Queued         []string      // a freeform card's lines waiting for the turn in flight
	Watches        []string      // a freeform card's running gummi watches, "w1 · command"
	Busy           bool          // agent is mid-turn
	// Briefing is true while gummi's own handoff-brief turn is in flight
	// on a freeform card (freeformhandoff.go): the busy word names what is
	// happening rather than the bare "working".
	Briefing           bool
	PendingAsk         *Ask   // the agent's open ask_user question, if any
	Verdict            string // review verdict via submit_verdict, if submitted
	VerdictFloor       string // deterministic ceiling applied before returning the stage verdict
	VerdictFloorReason string // human-readable reason for the floor, if any
	// VerdictFloorKind names which floor holds the slot (FloorPromise,
	// FloorOmission, …). It is what lets a reader tell a floor it can
	// re-derive from the one it read at the idle the stage ended: a
	// promise floor re-runs against the artifact on read, so correcting
	// the plan clears the overrule without a new verify session.
	VerdictFloorKind string
	// Exhausted is true when this session stopped because the card's
	// envelope ran out, not because it finished. Both states persist as
	// StateDone, and only this tells them apart.
	Exhausted bool
	Err       error
	EnvProbes []envprobe.Result
	// StartedAt is the session's construction time, carried here as its
	// identity. A stage hosts several sessions in turn — the writer, its
	// critique, each replan round — and a reader that tracks a position
	// in Activity needs to know when the feed it is reading was replaced
	// by a different session's, rather than assuming one feed per stage.
	StartedAt time.Time
}

// Session is one live agent conversation bound to a feature + stage.
type Session struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool
	// Critique marks the plan-critique pass: a second, fresh-context
	// session on the Plan stage that reviews the written plan (role:
	// reviewer) instead of writing it. Set at construction, immutable.
	Critique bool
	// Rebase marks the rebase-resolve pass: an implementer session that
	// rebases the branch onto main and resolves the conflicts, borrowing
	// the current stage without doing its work. Set at construction,
	// immutable.
	Rebase bool
	// ReplacedRunning marks a rebase pass that was dispatched over a
	// stage session still mid-turn: the hand-off interrupted that run
	// (Engine.run's replacement path) rather than waiting for it to
	// drain. Set at construction, immutable. Settlement reads it to
	// re-run the stage the interrupt stopped — a rebase dispatched on a
	// parked card leaves nothing to re-run.
	ReplacedRunning bool
	// ReadOnly marks an autonomous research pass (investigate,
	// review-of-research) that must never mutate the main checkout. Set at
	// construction from researchReadOnly, immutable. The engine uses it to
	// refuse mutating client tools (spec_replace_section, spec_annotate)
	// even when a hand-crafted MCP call names them.
	ReadOnly bool

	// kickoffNote is extra content appended to an autonomous run's stage
	// kickoff — the user's review comments delivered via RunWith. Set at
	// construction, immutable after (like Feature/Role).
	kickoffNote string
	// answerForNextRun is an answer this stage's run could not take: the
	// run stopped on its budget with its question up, and the person
	// answered it there. Running the stage again is the top-up's call,
	// not the answer's, so the exchange waits here and rides the kickoff
	// of the next run of the same stage and pass (Engine.run). Written
	// under s.mu.
	answerForNextRun string
	// specComments is the artifact's open user comments, compiled when a
	// stage writer's run starts (Engine.openSpecComments) and appended to
	// its kickoff. Kept apart from kickoffNote so a restart re-reads the
	// artifact instead of replaying a stale list. Set before the kickoff
	// is sent, never after.
	specComments string
	// startedAt is when this session generation began. It is the
	// discriminator that keeps mirrored events unique per generation:
	// a stage that re-runs (a review bounce, a resumed card) gets a
	// fresh transcript numbered from zero again, so ord alone would
	// collide with the previous generation's events.
	startedAt time.Time

	done     chan struct{}
	stopOnce sync.Once
	// ctx is the session's lifecycle context: canceled by stop(), so any
	// in-flight subprocess it carries is killed the instant the session
	// finalizes rather than racing teardown.
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	agentSess      agent.Session // nil until the backend attaches
	agentName      string        // backend identity, for display
	agentSessionID string        // backend session id (agent.Identified), "" if none
	model          string        // model resolved at spawn
	specPath       string        // resolved spec/draft path (for ask_user capture)
	state          SessionState
	transcript     []Message
	// streamOpen and streamIdx name the transcript entry that is
	// currently streaming an assistant message and awaiting its
	// finishAssistant completion. finishAssistant used to find that
	// entry by checking whether it was still the *last* transcript
	// entry — but a tool-call activity line or an ask_user answer can
	// land in between a message's first delta and its completion, and
	// once that happens the streamed entry is no longer last. The old
	// check then read that as "nothing to finalize" and appended the
	// authoritative text as a brand new message, duplicating the
	// paragraph the reader had already seen straddling whatever got
	// appended in between. Tracking the index directly survives any
	// number of intervening appends. appendTool may still flip the
	// tracked entry's own Streaming flag off (closing its bubble for
	// the tool boundary, so a genuinely new paragraph after the tool
	// call opens its own bubble instead of silently extending this
	// one) without touching streamOpen/streamIdx: the entry is still
	// the one finishAssistant must finalize, it is just no longer
	// receiving deltas. streamIdx is meaningful only while streamOpen
	// is true. There is at most one in-progress streamed assistant
	// message at a time — one backend event stream drives a session —
	// so a single index suffices; this would need to become a stack or
	// a set if that ever stopped being true.
	streamOpen bool
	streamIdx  int
	activity   []string
	spend      agent.Usage
	context    agent.Context
	// ctxPeak is the highest context occupancy this session ever
	// reported, kept because the live figure is gone the moment the
	// session ends and how close a stage came to its window is often the
	// whole explanation for how that stage went.
	ctxPeak    agent.Context
	busy       bool
	pendingAsk *Ask
	verdict    string // review verdict from submit_verdict ("pass"/"changes")
	err        error
	stopped    bool
	finalized  bool // stopped; must not be persisted (may be dropped)

	budget     float64 // stage credit budget (0 = none)
	creditRate float64 // adapter's token→credit rate (0 = engine default)
	// cardSpent is the whole card's metered spend (credit-equivalent) as
	// the store knows it: seeded from the feature row when the session
	// spawns, then moved by exactly the figure recordUsage books against
	// that row, so it stays the store's number without re-reading it.
	// 0 means "never seeded" — a session restored from a snapshot that
	// has not been dispatched again — and callers fall back to their own
	// (possibly stale) copy of the row. Another session on the same card
	// (a consult, which books its spend against the same feature) moves
	// the row without moving this, so the figure can sit that much low
	// until the next reload; it is never high.
	cardSpent    float64
	threshold    int    // highest budget threshold crossed (%)
	pendingNudge string // budget nudge awaiting the next sent turn
	exhausted    bool   // hit the credit cap
	clientTools  bool   // resolved backend's ClientTools capability (spawn-time cache)
	failing      toolFailStreak

	// cardUnlock retires this session's hold on the workspace's per-card
	// lock (state.CardLocks), taken before the session was created so a
	// headless drive of the same card is refused rather than racing it.
	// It is idempotent and nil when card locking is off (the headless
	// driver, which holds the lock itself; tests).
	cardUnlock func()

	// live mirrors every transcript mutation to the card's live file so a
	// second gummi process can follow the run this one owns. It is bound
	// at construction and closed by stop; a nil Writer (no workspace
	// configured, or the file could not be opened) is a working no-op, so
	// no call site branches on it. Emits are non-blocking channel sends,
	// so holding s.mu across one costs nothing the UI can feel.
	live *livelog.Writer

	// envProbes holds the most recent env prerequisite probe results for
	// this session, produced at Verify kickoff and surfaced on Snapshot.
	envProbes []envprobe.Result

	// verdictFloor is a deterministic ceiling applied to the raw agent
	// verdict. Several floors share the one slot, last writer wins at the
	// verify idle: a live check failure (FloorChecks), a shipped build
	// artifact (FloorHygiene), a bug whose Verify finish omitted every
	// [env:] live check while a prerequisite probed present
	// (FloorOmission), an unmet plan promise (FloorPromise), and a
	// research document failing its floor (FloorDocument). They only ever
	// downgrade — Pass -> Blocked, or either -> Fail — and the downgrade
	// itself happens in verdict.SessionVerdict from the stamped floor.
	verdictFloor       string
	verdictFloorKind   string
	verdictFloorReason string
	// The promise floor's re-check bookkeeping, cached at stamp time so
	// the read path can re-run the check without re-entering locate or
	// the worktree manager: where the branch and the artifact live, and
	// the artifact's mtime+size signature as of the last check. An
	// unchanged signature serves a read as-is with no grep; a changed one
	// re-checks once and records the new signature. All three are cleared
	// by any other stamper (the slot is one) and left empty for floors
	// that never re-check. A session restored from persistence has the
	// paths re-armed by Restore and no signature, so its first read
	// re-checks once, then follows the guard.
	verdictFloorWorkDir  string
	verdictFloorSpecPath string
	verdictFloorSig      string

	// mcpTeardown releases the session's MCP inbound endpoint (closes the
	// listener, joins its goroutines, removes the socket file) exactly
	// once, hand-in-hand with stop (see setMCPTeardown).
	mcpTeardown func()

	// resolvers is the registered in-flight MCP tool-call waiters, keyed
	// by the engine-side call id (mcp-<n>). A registered call resolves
	// via its channel instead of the backend's ToolResolver, so a
	// non-ClientTools backend served over MCP gets its answer the same
	// way a native one does. Each entry is a buffered channel of capacity
	// 1 that DispatchClientTool selects on.
	resolvers map[string]chan string
	// resolverWait mirrors resolvers, recording which call's waiter has
	// entered its receive select (DispatchClientTool marks it live just
	// before blocking) and, crucially, which waiter has since given up
	// (the ctx.Done branch of that same select clears it without removing
	// the resolver). Answer consults it to tell a bridge call that is
	// still parked and will pick the answer up from one whose backend is
	// gone: a buffered channel alone cannot distinguish "delivered to a
	// live waiter" from "delivered into a buffer nobody reads", so the
	// flag has to change on the read side, not just when the resolver is
	// consumed.
	resolverWait map[string]bool

	// Outstanding estimated spend per model, awaiting a settle event
	// that reconciles it to the provider-metered figure: pendingTokenEst
	// is what the engine priced from raw tokens (no adapter cost at
	// all), pendingAdapterEst what the adapter estimated mid-turn.
	pendingTokenEst   map[string]float64
	pendingAdapterEst map[string]float64
}

// Snapshot returns a render-safe copy of the session's state.
//
// A promise-floor stamp is re-checked here, on the read path. The floor
// stamps once, at the idle that ends the stage, and nothing else in a
// live session ever revisits that verdict — so a plan corrected after
// the stamp stayed blocked until a fresh verify session ran, even though
// the condition is a pure read of the artifact and the branch. The check
// runs only when the artifact's signature has moved since the last check
// (a still-unmet card never greps per frame), runs outside s.mu — a git
// grep has no business under the session lock — and takes a short write
// afterwards that clears the stamp only if it still holds: a stamper
// that fired meanwhile wins.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	re := s.promiseRecheckArmedLocked()
	snap := s.snapshotLocked()
	s.mu.Unlock()
	if re != nil {
		s.settlePromiseFloor(re, &snap)
	}
	return snap
}

// snapshotLocked is the render-safe copy itself, taken with s.mu held.
func (s *Session) snapshotLocked() Snapshot {
	return Snapshot{
		Feature:            s.Feature,
		Role:               s.Role,
		Interactive:        s.Interactive,
		Critique:           s.Critique,
		Rebase:             s.Rebase,
		ReplacedRunning:    s.ReplacedRunning,
		State:              s.state,
		AgentName:          s.agentName,
		AgentSessionID:     s.agentSessionID,
		Model:              s.model,
		Transcript:         append([]Message(nil), s.transcript...),
		Activity:           append([]string(nil), s.activity...),
		Spend:              s.spend,
		SpentCredits:       s.spentForBudgetLocked(),
		Context:            s.context,
		Tasks:              s.tasksLocked(),
		Busy:               s.busy,
		PendingAsk:         s.pendingAsk,
		Verdict:            s.verdict,
		VerdictFloor:       s.verdictFloor,
		VerdictFloorKind:   s.verdictFloorKind,
		VerdictFloorReason: s.verdictFloorReason,
		Exhausted:          s.exhausted,
		Err:                s.err,
		EnvProbes:          append([]envprobe.Result(nil), s.envProbes...),
		StartedAt:          s.startedAt,
	}
}

// settlePromiseFloor applies one read-path re-check: it runs the bounded
// promises check against the paths cached at stamp time, then takes a
// short write that records the new signature and either lifts the stamp
// (the promises now check out — and the env-omission floor is re-derived
// before settling, so a clear does not erase a block that still holds)
// or refreshes the reason to what the artifact now says. A read error is
// no opinion and keeps the stamp. The copy is patched to the post-check
// state, so the read that did the work reports it.
func (s *Session) settlePromiseFloor(re *promiseRecheck, snap *Snapshot) {
	rep, ok := runPromiseRecheck(re)
	s.mu.Lock()
	cleared := false
	if s.verdictFloorKind == FloorPromise && s.verdictFloor == "blocked" &&
		s.verdictFloorReason == re.reason {
		// nobody re-stamped the slot while the check ran; a concurrent
		// stamper's reason would differ from the one checked here
		s.verdictFloorSig = artifactSignature(re.specPath)
		if ok && !rep.blocks() {
			s.verdictFloor, s.verdictFloorReason, s.verdictFloorKind = "", "", ""
			s.verdictFloorWorkDir, s.verdictFloorSpecPath, s.verdictFloorSig = "", "", ""
			cleared = true
		} else if ok {
			s.verdictFloorReason = rep.reason()
		}
	}
	s.patchFloorFields(snap)
	s.mu.Unlock()
	if !cleared {
		return
	}
	s.appendActivity(promiseFloorLiftedLine)
	// Re-derive the omission floor before settling: the floor is one slot
	// and the stampers are last-writer-wins at the same verify idle, so
	// the promise stamp may have been sitting on top of an env-omission
	// block that still holds. Re-derived from the session's env probes and
	// one read of the artifact — no grep — and nothing lands unsafe either
	// way: the omission gate re-reads at Advance.
	reStampOmissionFloor(s, re.specPath)
	s.mu.Lock()
	s.patchFloorFields(snap)
	s.mu.Unlock()
}

// patchFloorFields copies the session's current floor fields — and the
// activity feed, which a lift appends to — onto a snapshot built before
// the floor's state settled. Caller holds s.mu.
func (s *Session) patchFloorFields(snap *Snapshot) {
	snap.VerdictFloor = s.verdictFloor
	snap.VerdictFloorKind = s.verdictFloorKind
	snap.VerdictFloorReason = s.verdictFloorReason
	snap.Activity = append([]string(nil), s.activity...)
}

// kickoffMessage returns the autonomous stage kickoff, with the user's
// review comments appended when this run carries them (RunWith, or the
// artifact's open comments on a stage writer's run). A
// rebase session opens with its own go-ahead; its note carries the
// rebase target and expected conflicts (RunRebase).
func (s *Session) kickoffMessage() string {
	base := kickoff(s.Feature.Kind)
	if s.Rebase {
		base = rebaseKickoff
	}
	for _, extra := range []string{s.kickoffNote, s.specComments} {
		// RunWith from the spec view carries the same compiled list the
		// artifact yields: say it once.
		if extra != "" && !strings.Contains(base, extra) {
			base += "\n\n" + extra
		}
	}
	return base
}

// flavor recovers which autonomous pass this session runs (see runFlavor).
func (s *Session) flavor() runFlavor {
	switch {
	case s.Critique:
		return flavorCritique
	case s.Rebase:
		return flavorRebase
	}
	return flavorStage
}

// State returns the session's scheduling status.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Live reports whether this session has a backend genuinely attached and
// answering — the single predicate the thread composer uses to decide
// whether a plain line steers this session or is instead answered by the
// card's consult session. State alone is not enough: a session restored
// after a restart reports StateInteractive with no agent handle yet. A
// stale agent handle alone is not enough either: Pause leaves agentSess
// non-nil after closing the backend (stop closes the agent but never nils
// the field). Both together are what a genuinely steerable session needs,
// and checking both here — rather than at each call site — is what lets
// every non-live row in the Problem table (not-yet-attached, paused,
// restored, never-started, done) collapse onto one rule instead of being
// enumerated. A nil receiver (no session at all) reports false, so a
// caller can ask sessionFor(id).Live() without a separate nil check.
func (s *Session) Live() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning && s.state != StateInteractive {
		return false
	}
	return s.agentSess != nil
}

func (s *Session) setState(st SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
	s.live.Emit(livelog.Record{Kind: livelog.KindState, State: string(st)})
}

// agent returns the session's agent session (nil until the backend attaches).

func (s *Session) agent() agent.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentSess
}

// attachAgent binds an agent session and marks it running, reporting
// whether it did. It refuses (returning false) when the session was
// already finalized — a Pause/Drop/Close that landed while the backend
// was still starting — so the caller closes the just-created agent rather
// than leaving it running ungoverned with no way to stop it. Both this and
// stop() take s.mu, so the two serialize: whichever runs second sees the
// other's effect.
func (s *Session) attachAgent(a agent.Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return false
	}
	s.agentSess = a
	if id, ok := a.(agent.Identified); ok {
		s.agentSessionID = id.SessionID()
	}
	s.state = StateRunning
	return true
}

// setAgentSessionID restores a persisted backend session id (Restore
// has no live agent to ask).
func (s *Session) setAgentSessionID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSessionID = id
}

// clearAgent nils the stale agent handle stop() leaves behind (Live's own
// doc comment: "Pause leaves agentSess non-nil after closing the
// backend"). Without this, Attach's reuse check (prior.agent() != nil)
// mistakes a just-paused, already-closed session for one still worth
// reusing, and hands the caller a session whose backend is gone.
func (s *Session) clearAgent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSess = nil
}

// finishRunning atomically marks a running autonomous turn done, reporting
// whether it did (false if the session was paused or stopped meanwhile).
// Doing the check-and-set under one lock stops a racing Pause from being
// silently overwritten by the completing turn.
func (s *Session) finishRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return false
	}
	s.state = StateDone
	return true
}

func (s *Session) appendUser(text, by string) {
	s.appendUserImages(text, by, nil)
}

// appendUserImages is appendUser's image-carrying form: the refs a turn's
// images were resolved to land on the transcript entry itself, so the
// thread shows what was sent and a restart restores it (state.SessionMessage.Images).
func (s *Session) appendUserImages(text, by string, images []AttachmentRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorUser, Content: text, By: by, Images: images, At: time.Now()})
	s.err = nil
	s.live.Emit(livelog.Record{Kind: livelog.KindUser, Text: text})
}

// appendUserAs records a user-authored turn no person typed: the echo of
// an ask_user answer, stamped with who actually chose its text (by is
// state.ActorAutopilot when the unattended loop answered its own
// question). The transcript keeps reading as a conversation on restore;
// the stamp is what lets the card-event mirror tell this echo apart from
// a turn a person typed, on every save including ones after a restart.
func (s *Session) appendUserAs(text, by string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorUser, Content: text, AnsweredBy: by, At: time.Now()})
	s.err = nil
	s.live.Emit(livelog.Record{Kind: livelog.KindUser, Text: text})
}

func (s *Session) appendSystem(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorSystem, Content: text, At: time.Now()})
	s.err = nil
	s.live.Emit(livelog.Record{Kind: livelog.KindSystem, Text: text})
}

func (s *Session) appendDelta(text string) {
	if text == "" {
		return // nothing to add; don't open an empty streaming bubble
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live.Delta(text)
	// Extend the tracked entry only while it is still actually
	// streaming: appendTool closes that flag on a tool-call boundary on
	// purpose, so a delta arriving after one starts a fresh bubble
	// (see TestToolCallMidStreamClosesBubble) rather than resuming a
	// paragraph the reader has already seen rendered as finished.
	if s.streamOpen && s.transcript[s.streamIdx].Streaming {
		s.transcript[s.streamIdx].Content += text
		return
	}
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text, Streaming: true, At: time.Now()})
	s.streamOpen = true
	s.streamIdx = len(s.transcript) - 1
}

// appendThinking adds streamed reasoning. Consecutive chunks extend one
// entry; anything said or done in between starts the next. Like a tool
// call it closes a streaming reply: what follows thinking is a new one.
func (s *Session) appendThinking(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Author == AuthorThinking {
		s.transcript[n-1].Content += text
		return
	}
	if s.streamOpen {
		s.transcript[s.streamIdx].Streaming = false
		s.streamOpen = false
	}
	s.transcript = append(s.transcript, Message{Author: AuthorThinking, Content: text, At: time.Now()})
}

// setTasks records the agent's checklist, replacing the one it had.
func (s *Session) setTasks(ts []agent.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := agent.FormatTasks(ts)
	for i := range s.transcript {
		if s.transcript[i].Author == AuthorTasks {
			s.transcript[i].Content, s.transcript[i].At = body, time.Now()
			return
		}
	}
	s.transcript = append(s.transcript, Message{Author: AuthorTasks, Content: body, At: time.Now()})
}

// tasksLocked is the checklist setTasks last recorded, or nil.
func (s *Session) tasksLocked() []agent.Task {
	for i := range s.transcript {
		if s.transcript[i].Author == AuthorTasks {
			return agent.ParseTasks(s.transcript[i].Content)
		}
	}
	return nil
}

// finishAssistant finalizes the streaming assistant message with the
// authoritative full text, or appends a completed one if no deltas
// arrived (the common case for adapters that only emit whole messages).
// An empty completion — a tool-call or reasoning step that carries no
// prose, which agents emit many of per turn — adds no bubble: it just
// finalizes an in-progress streamed message (keeping its content) so the
// transcript never fills with blank assistant replies.
func (s *Session) finishAssistant(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// the empty completion is emitted too: it closes the follower's
	// streaming bubble exactly as it closes this transcript's.
	s.live.Emit(livelog.Record{Kind: livelog.KindMessage, Text: text})
	// Finalize by tracked index, not "the last transcript entry": an
	// activity line or an ask_user answer recorded between the first
	// delta and this completion — the whole point of streamOpen/
	// streamIdx — must not make this look like there was nothing to
	// finalize.
	streaming := s.streamOpen
	if strings.TrimSpace(text) == "" {
		if streaming {
			s.transcript[s.streamIdx].Streaming = false
			s.streamOpen = false
		}
		return
	}
	if streaming {
		s.transcript[s.streamIdx].Content = text
		s.transcript[s.streamIdx].Streaming = false
		s.streamOpen = false
		return
	}
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text, At: time.Now()})
}

// lastAssistant returns the most recent assistant message's content and
// its transcript index, or ("", -1) when there is none.
func (s *Session) lastAssistant() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.transcript) - 1; i >= 0; i-- {
		if s.transcript[i].Author == AuthorAssistant {
			return s.transcript[i].Content, i
		}
	}
	return "", -1
}

// replaceMessage overwrites a transcript entry's content (used to strip
// a parsed gummi-ask block out of the visible message).
func (s *Session) replaceMessage(i int, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= 0 && i < len(s.transcript) {
		s.transcript[i].Content = content
		s.live.Emit(livelog.Record{Kind: livelog.KindEdit, Text: content})
	}
}

// appendActivity records a gummi-authored note (nudges, checkpoint
// lines) as an AuthorTool entry with no outcome semantics.
func (s *Session) appendActivity(tool string) {
	s.appendTool(Message{Author: AuthorTool, Content: tool})
}

// appendToolCall records an agent tool invocation, keeping the backend
// call id so a later resolveToolResult can attach the outcome — and the
// tool's name and argument apart from the rendered line, so the durable
// log can record what was called rather than only how it was shown.
func (s *Session) appendToolCall(callID, line, tool, detail string) {
	s.appendTool(Message{
		Author: AuthorTool, Content: line,
		Tool: tool, Detail: detail, CallID: callID, pending: callID != "",
	})
}

// appendToolDone records a tool line whose outcome is already known —
// gummi-run checks, whose pass/fail and output exist before the entry.
func (s *Session) appendToolDone(line string, ok bool, output string) {
	st := ToolOK
	if !ok {
		st = ToolFail
	}
	s.appendTool(Message{Author: AuthorTool, Content: line, ToolStatus: st, ToolOutput: output})
}

// appendTool records a tool-call line twice: on the activity ticker
// (the dashboard's recent-lines feed) and as an AuthorTool transcript
// entry, so the full history keeps it ordered against the messages
// around it.
func (s *Session) appendTool(m Message) {
	// activity is stored newline-joined; keep labels single-line so they
	// round-trip through persistence intact.
	m.Content = strings.ReplaceAll(strings.ReplaceAll(m.Content, "\n", " "), "\r", " ")
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = append(s.activity, m.Content)
	// a tool call mid-stream also closes the streaming bubble: the next
	// delta belongs to a new one (the agent spoke, acted, spoke again).
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Author == AuthorAssistant && s.transcript[n-1].Streaming {
		s.transcript[n-1].Streaming = false
	}
	s.transcript = append(s.transcript, m)
	s.live.Emit(livelog.Record{
		Kind: livelog.KindTool, Text: m.Content, Call: m.CallID,
		OK: m.ToolStatus == ToolOK, Output: m.ToolOutput,
	})
}

// openWatches lists the background watches (agent.WatchTool) still pending
// that started after since, as the transcript names them, oldest first.
func (s *Session) openWatches(since time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.transcript {
		if m.Author == AuthorTool && m.pending && agent.WatchTool(m.Tool) && m.At.After(since) {
			out = append(out, m.Content)
		}
	}
	return out
}

// openWatch reports whether a background watch is still pending since then.
func (s *Session) openWatch(since time.Time) bool {
	return len(s.openWatches(since)) > 0
}

// resolveToolResult attaches a backend-reported outcome to the pending
// AuthorTool entry with the matching call id. Unknown ids are dropped
// (a result for a call gummi never displayed, e.g. one from before a
// restart). It returns the resolved call's tool and argument, both empty
// when nothing matched.
func (s *Session) resolveToolResult(callID string, ok bool, output string) (tool, detail string) {
	if callID == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.transcript) - 1; i >= 0; i-- {
		if !s.transcript[i].pending || s.transcript[i].CallID != callID {
			continue
		}
		s.transcript[i].pending = false
		s.transcript[i].DoneAt = time.Now()
		s.transcript[i].ToolStatus = ToolOK
		if !ok {
			s.transcript[i].ToolStatus = ToolFail
		}
		s.transcript[i].ToolOutput = output
		s.live.Emit(livelog.Record{Kind: livelog.KindResult, Call: callID, OK: ok, Output: output})
		return s.transcript[i].Tool, s.transcript[i].Detail
	}
	return "", ""
}

func (s *Session) addSpend(u agent.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spend.Credits += u.Credits
	s.spend.InputTokens += u.InputTokens
	s.spend.OutputTokens += u.OutputTokens
	if u.Model != "" {
		s.spend.Model = u.Model
	}
	s.live.Emit(livelog.Record{
		Kind: livelog.KindSpend, Credits: s.spend.Credits,
		InputTokens: s.spend.InputTokens, OutputTokens: s.spend.OutputTokens,
		Model: s.spend.Model,
	})
}

// setContext records the latest context-window occupancy (a known limit
// is sticky, so a later event that omits it doesn't blank the display).
func (s *Session) setContext(c agent.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.context.Tokens = c.Tokens
	if c.Limit > 0 {
		s.context.Limit = c.Limit
	}
	if c.Tokens > s.ctxPeak.Tokens {
		s.ctxPeak.Tokens = c.Tokens
	}
	if c.Limit > 0 {
		s.ctxPeak.Limit = c.Limit
	}
}

// contextPeak reports the session's high-water context occupancy.
func (s *Session) contextPeak() agent.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctxPeak
}

// crossedThreshold returns the highest new budget threshold this
// session's spend has crossed since the last call (0 if none), and the
// current spent credits. Advances the recorded threshold so each is
// reported once.
func (s *Session) crossedThreshold() (pct int, spent float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spent = s.spentForBudgetLocked()
	if s.budget <= 0 {
		return 0, spent
	}
	frac := spent / s.budget * 100
	crossed := 0
	for _, t := range budgetThresholds {
		if int(frac) >= t && t > s.threshold {
			crossed = t
		}
	}
	if crossed > 0 {
		s.threshold = crossed
	}
	return crossed, spent
}

// spentForBudgetLocked returns the session's spend as a credit-equivalent
// (credits for a metered backend, token-derived for a token-only one at
// the adapter's rate). Caller holds s.mu.
func (s *Session) spentForBudgetLocked() float64 {
	return domain.Spend{Credits: s.spend.Credits, InputTokens: s.spend.InputTokens, OutputTokens: s.spend.OutputTokens}.
		CreditEquivalentAt(s.creditRate)
}

// rate returns the adapter's token→credit rate for this session.
func (s *Session) rate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creditRate
}

func (s *Session) setByokRate(r float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creditRate = r
}

// creditEquivalent prices one usage event as credits at this session's
// adapter rate: the metered credits when the backend reports them, or a
// token-derived value when it reports only tokens. Persisting this keeps
// the feature's credit-denominated running total whole when its stages
// mix credits-metered and token-only backends.
func (s *Session) creditEquivalent(u agent.Usage) float64 {
	s.mu.Lock()
	rate := s.creditRate
	s.mu.Unlock()
	return domain.Spend{Credits: u.Credits, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}.
		CreditEquivalentAt(rate)
}

func (s *Session) setSpecPath(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specPath = p
}

// SpecPath returns the session's resolved spec/draft path.
func (s *Session) SpecPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specPath
}

func (s *Session) setPendingAsk(a *Ask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingAsk = a
	s.emitAskLocked(a)
}

// emitAskLocked mirrors the open question onto the live file. A watcher
// sees the question but can never answer it — the resolver channel lives
// in the owning process — so the follower renders it read-only.
func (s *Session) emitAskLocked(a *Ask) {
	r := livelog.Record{Kind: livelog.KindAsk}
	if a != nil {
		r.Call, r.Text = a.CallID, a.Question
	}
	s.live.Emit(r)
}

// trySetPendingAsk installs a as the open question only when none is
// pending, reporting whether it was installed. The engine holds one open
// ask at a time: overwriting would orphan the displaced call's blocked
// tool handler and hang the agent's turn for good.
func (s *Session) trySetPendingAsk(a *Ask) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingAsk != nil {
		return false
	}
	s.pendingAsk = a
	s.emitAskLocked(a)
	return true
}

func (s *Session) setVerdict(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdict = v
}

// setVerdictFloor stamps the verdict floor: kind names which floor holds
// the slot, v the ceiling it applies ("blocked"/"fail"), reason the
// sentence a reader acts on. It replaces any stamp in the slot, floor
// kind and re-check bookkeeping included — the slot is one and the
// stampers are last-writer-wins.
func (s *Session) setVerdictFloor(kind, v, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setVerdictFloorLocked(kind, v, reason)
}

// setVerdictFloorLocked is setVerdictFloor for a caller already holding
// s.mu.
func (s *Session) setVerdictFloorLocked(kind, v, reason string) {
	s.verdictFloor = v
	s.verdictFloorKind = kind
	s.verdictFloorReason = reason
	s.verdictFloorWorkDir = ""
	s.verdictFloorSpecPath = ""
	s.verdictFloorSig = ""
}

// setPromiseVerdictFloor stamps the promises floor and caches what its
// read-path re-check needs: the branch and artifact paths the check ran
// against, and the artifact's signature as of this check, so a later
// read re-runs it only when the artifact has moved.
func (s *Session) setPromiseVerdictFloor(v, reason, workDir, specPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setVerdictFloorLocked(FloorPromise, v, reason)
	s.verdictFloorWorkDir = workDir
	s.verdictFloorSpecPath = specPath
	s.verdictFloorSig = artifactSignature(specPath)
}

// cachePromiseFloorPaths arms a restored promise floor's re-check with
// where its branch and artifact live. No signature is recorded — the
// first read re-checks once, then follows the guard. A no-op for any
// other floor in the slot, and for a session whose floor has already
// been replaced.
func (s *Session) cachePromiseFloorPaths(workDir, specPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verdictFloorKind != FloorPromise {
		return
	}
	s.verdictFloorWorkDir = workDir
	s.verdictFloorSpecPath = specPath
}

// outlivePendingAsk cuts the open ask loose from the tool call that
// raised it, reporting whether there was one to cut. The question stays
// open under the same decision; what goes is the call id, so Answer stops
// looking for a blocked call to resolve and delivers the answer as a turn,
// and the call's bridge waiter, handed back so the caller can release it.
// The ask is replaced rather than edited: a snapshot taken earlier still
// holds the old one.
func (s *Session) outlivePendingAsk() (waiter chan string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingAsk == nil || s.pendingAsk.CallID == "" {
		return nil, false
	}
	cut := *s.pendingAsk
	waiter = s.resolvers[cut.CallID]
	delete(s.resolvers, cut.CallID)
	delete(s.resolverWait, cut.CallID)
	cut.CallID, cut.Outlived = "", true
	s.pendingAsk = &cut
	return waiter, true
}

// takesTurns reports whether a turn sent to this session reaches a
// backend that can act on it: Live, and its last turn did not end in an
// error, which on most backends means the process behind it is gone.
func (s *Session) takesTurns() bool {
	return s.Live() && s.errValue() == nil
}

// callLive reports whether the tool call behind callID is still waiting
// on its result: a bridge call parked on its waiter, or a native call on
// a backend that is still taking turns. A bridge call whose client gave
// up has deregistered as waiting; a call with neither is gone.
func (s *Session) callLive(callID string) bool {
	s.mu.Lock()
	_, bridged := s.resolvers[callID]
	waiting := s.resolverWait[callID]
	a := s.agentSess
	s.mu.Unlock()
	if bridged {
		return waiting
	}
	_, native := a.(agent.ToolResolver)
	return native && s.takesTurns()
}

// holdAnswerForNextRun parks an answer's re-entry turn for the stage's
// next run (see answerForNextRun).
func (s *Session) holdAnswerForNextRun(turn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answerForNextRun = turn
}

// takeAnswerForNextRun hands over a parked answer, once.
func (s *Session) takeAnswerForNextRun() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn := s.answerForNextRun
	s.answerForNextRun = ""
	return turn
}

// takePendingAsk clears and returns the open ask (nil if none), so the
// answer path resolves exactly one call.
func (s *Session) takePendingAsk() *Ask {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.pendingAsk
	s.pendingAsk = nil
	return a
}

// setSpawnInfo records which backend and model this session runs on, so
// the UI can say so before the first usage event arrives. clientTools
// caches the resolved backend's advertised ClientTools capability so
// callers that need to decide per-session (convention-ask fallback,
// tool registration) don't have to look the adapter up again.
func (s *Session) setSpawnInfo(agentName, model string, clientTools bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentName = agentName
	s.model = model
	s.clientTools = clientTools
}

// ClientTools reports whether this session's backend advertises native
// client-tool support. Stamped at spawn (setSpawnInfo).
func (s *Session) ClientTools() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientTools
}

// registerResolver stashes a waiter for an in-flight MCP tool-call and
// returns its channel. DispatchClientTool owns the call id; Resolve paths
// that answer the call take the channel via takeResolver.
func (s *Session) registerResolver(callID string) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolvers == nil {
		s.resolvers = map[string]chan string{}
	}
	ch := make(chan string, 1)
	s.resolvers[callID] = ch
	return ch
}

// takeResolver claims and removes a registered MCP call-waiter, reporting
// whether one existed. A take for an already-taken or never-registered id
// is a no-op returning ok=false, so a stale resolve can never fire twice.
func (s *Session) takeResolver(callID string) (chan string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.resolvers[callID]
	if ok {
		delete(s.resolvers, callID)
		delete(s.resolverWait, callID)
	}
	return ch, ok
}

// markResolverWaiting records that DispatchClientTool's receive select is
// live on callID's waiter. Answer uses it to tell a bridge call that is
// still blocked (and will pick the answer up) from one whose backend is
// gone: a buffered channel alone cannot falsify delivery.
func (s *Session) markResolverWaiting(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolverWait == nil {
		s.resolverWait = map[string]bool{}
	}
	s.resolverWait[callID] = true
}

// clearResolverWaiting records that callID's waiter has given up its
// receive select without being answered (DispatchClientTool's ctx.Done
// branch — the backend went away). It leaves the resolver registered so
// Answer's takeResolver still finds it; the cleared flag is what tells
// Answer the waiter is gone and the answer must not be dropped into a
// buffer nobody will read.
func (s *Session) clearResolverWaiting(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resolverWait, callID)
}

// clearResolversWaiting marks every registered MCP call-waiter as no
// longer receiving, used when the backend process dies mid-flight (pump's
// closed-event-stream path) so Answer treats any in-flight bridge call as
// gone even though the session was never explicitly stopped. It leaves the
// resolvers registered; takeResolver still finds them, and the cleared
// flags are what tell Answer not to drop the answer into a buffer nobody
// will read.
func (s *Session) clearResolversWaiting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.resolverWait {
		delete(s.resolverWait, id)
	}
}

// resolverWaiting reports whether callID's waiter is actively receiving.
func (s *Session) resolverWaiting(callID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolverWait[callID]
}

// resolverCount reports how many MCP call-waiters are still registered
// (test and lifecycle probes: there must be zero orphans after a session
// ends).
func (s *Session) resolverCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.resolvers)
}

// setMCPTeardown installs the session's MCP inbound-endpoint release
// function, or runs it inline when the session is already stopped. It is
// atomic with stop (both take s.mu): a teardown arriving after stop fired
// — the autonomous Pause/Drop/Close race where the backend is still
// spawning — runs immediately rather than being stashed and orphaned, so
// the accept-loop goroutine and the socket file never leak.
func (s *Session) setMCPTeardown(teardown func()) {
	if teardown == nil {
		return
	}
	s.mu.Lock()
	if s.finalized {
		s.mu.Unlock()
		teardown()
		return
	}
	s.mcpTeardown = teardown
	s.mu.Unlock()
}

// notePendingEst accumulates a usage sample's estimated credits against
// its model, split by origin: tokenEst was priced by the engine from raw
// tokens, adapterEst by the adapter from a realized rate. A later settle
// event for the model retires both (takePendingEst).
func (s *Session) notePendingEst(model string, tokenEst, adapterEst float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingTokenEst == nil {
		s.pendingTokenEst = map[string]float64{}
		s.pendingAdapterEst = map[string]float64{}
	}
	s.pendingTokenEst[model] += tokenEst
	s.pendingAdapterEst[model] += adapterEst
}

// takePendingEst returns and clears a model's outstanding estimates.
func (s *Session) takePendingEst(model string) (tokenEst, adapterEst float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenEst, adapterEst = s.pendingTokenEst[model], s.pendingAdapterEst[model]
	delete(s.pendingTokenEst, model)
	delete(s.pendingAdapterEst, model)
	return tokenEst, adapterEst
}

// isExhausted reports whether the session has hit its budget.
func (s *Session) isExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exhausted
}

// markExhausted records that the session hit its budget, returning true
// only the first time so the exhaustion checkpoint fires exactly once.
// Both trigger paths — gummi-side overspend and the CLI's (re-raisable)
// limits-exhausted event — funnel through here (cf. markStopped).
func (s *Session) markExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exhausted {
		return false
	}
	s.exhausted = true
	s.busy = false
	return true
}

// overBudget reports whether the session's spend has reached its budget —
// gummi-side enforcement that works for BYOK and small budgets the CLI
// cap can't cover. The exactly-once latch lives in markExhausted.
func (s *Session) overBudget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget > 0 && !s.exhausted && s.spentForBudgetLocked() >= s.budget
}

// Budget returns the session's stage budget (0 = none).
func (s *Session) Budget() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

func (s *Session) setBudget(b float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budget = b
}

// CardSpent returns the card's running total spend in credit-equivalent
// terms — the feature row's figure, kept live here so a render path can
// read it without touching the store (0 = never seeded).
func (s *Session) CardSpent() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cardSpent
}

// seedCardSpent records the card's spend as the store holds it at spawn.
func (s *Session) seedCardSpent(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cardSpent = v
}

// addCardSpent moves the card total by one booked usage sample. It takes
// the same signed figure recordUsage hands Store.AddSpend, and is called
// beside it, so the two never drift.
func (s *Session) addCardSpent(credits float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cardSpent += credits
}

// queueNudge stores a budget nudge to be prepended to the next turn the
// engine sends the agent (DESIGN §5.1 layer 2). It is appended to any
// already-pending nudge so multiple threshold crossings in one turn are
// all delivered in order.
func (s *Session) queueNudge(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingNudge != "" {
		s.pendingNudge += "\n"
	}
	s.pendingNudge += text
}

// takePendingNudge returns and clears the queued budget nudge text ("" if
// none). Called at the top of a turn-send path so the model sees the
// remaining-budget warning before the orchestrator's own message.
func (s *Session) takePendingNudge() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.pendingNudge
	s.pendingNudge = ""
	return text
}

// requeueNudge puts a nudge back at the FRONT of the pending text after
// a turn that consumed it was refused. Front, not back: the nudges are
// delivered in crossing order, and one taken for a turn that never
// happened is older than anything queued since.
func (s *Session) requeueNudge(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingNudge == "" {
		s.pendingNudge = text
		return
	}
	s.pendingNudge = text + "\n" + s.pendingNudge
}

// dropUnsentUser removes the user message the caller appended for a turn
// the backend then refused (agent.ErrBusy). An echo of a line the agent
// never received is worse than no echo at all: the reader sees their own
// sentence in the transcript, believes it delivered, and has no way to
// tell otherwise.
//
// It searches backwards for the newest user entry with exactly this
// content rather than assuming the entry is still last — the turn that
// refused this one is by definition still streaming, so its own output
// can and does land in between. Matching on content keeps that search
// honest: the same sentence sent twice means the newer copy is the
// undelivered one, which is the copy this removes.
func (s *Session) dropUnsentUser(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.transcript) - 1; i >= 0; i-- {
		if s.transcript[i].Author != AuthorUser || s.transcript[i].Content != text {
			continue
		}
		s.transcript = append(s.transcript[:i], s.transcript[i+1:]...)
		// a streamed entry after the removed one shifts down with it.
		if s.streamOpen && s.streamIdx > i {
			s.streamIdx--
		}
		return
	}
}

func (s *Session) setBusy(b bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = b
	s.live.Emit(livelog.Record{Kind: livelog.KindBusy, Busy: b})
}

// Busy reports whether the agent is mid-turn, without copying the
// transcript (cheap enough to call per card per frame).
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *Session) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	s.busy = false
}

// markStopped records the session as stopped, returning true the first
// time so the engine emits exactly one stopped event.
func (s *Session) markStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.stopped = true
	return true
}

// stop ends the session: it marks it finalized (so no late persist can
// resurrect it), signals the pump, and closes the agent session (if one
// was created). Idempotent.
func (s *Session) stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.finalized = true
		s.busy = false
		teardown := s.mcpTeardown
		s.mcpTeardown = nil
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		close(s.done)
		if a := s.agent(); a != nil {
			_ = a.Close()
		}
		if teardown != nil {
			teardown()
		}
		// the live file's last word: a follower learns the session ended
		// here rather than inferring it from a stream that went quiet.
		// Close flushes and joins the writer goroutine, so nothing is
		// half-written once stop returns.
		s.live.Emit(livelog.Record{Kind: livelog.KindStopped, Err: errText(s.errValue())})
		s.live.Close()
		// the agent is closed, so this session no longer drives the card:
		// let its hold on the card lock go. A successor session took its
		// own hold before this one stopped, so the card stays locked
		// across a replace and unlocks only when the last holder leaves.
		s.releaseCard()
	})
}

// bindLive attaches the session's live-file writer and replays whatever
// transcript it already carries, so a follower that joins mid-session — a
// restart-reattach carries the prior conversation over — sees the whole
// thing rather than only what arrives next. Called once, before the
// session's pump starts.
func (s *Session) bindLive(w *livelog.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = w
	for _, m := range s.transcript {
		switch m.Author {
		case AuthorUser:
			w.Emit(livelog.Record{Kind: livelog.KindUser, Text: m.Content})
		case AuthorSystem:
			w.Emit(livelog.Record{Kind: livelog.KindSystem, Text: m.Content})
		case AuthorAssistant:
			w.Emit(livelog.Record{Kind: livelog.KindMessage, Text: m.Content})
		case AuthorTool:
			w.Emit(livelog.Record{
				Kind: livelog.KindTool, Text: m.Content,
				OK: m.ToolStatus == ToolOK, Output: m.ToolOutput,
			})
		}
	}
	w.Emit(livelog.Record{Kind: livelog.KindState, State: string(s.state)})
}

// releaseCard retires this session's hold on the card lock. Safe to call
// on a session that never took one, and safe to call twice: the release
// CardLocks hands out is one-shot, so the death path and the teardown
// path can both call it.
func (s *Session) releaseCard() {
	if s.cardUnlock != nil {
		s.cardUnlock()
	}
}

// errValue reads the session's recorded error under the lock.
func (s *Session) errValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// errText renders an error for the wire, empty when there is none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// finalizedState reports whether the session has been stopped; a
// finalized session must not be persisted (it may have been dropped).
func (s *Session) finalizedState() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalized
}
