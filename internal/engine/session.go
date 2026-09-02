package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/sandbox"
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
	// EventTripwire fires when the agent made a clean→dirty transition on
	// the main checkout — new paths appear that weren't dirty before the
	// turn. The run aborts (DirtyPaths names the new paths).
	EventTripwire EventKind = "tripwire"
	// EventCheckpointFailed fires when checkpoint's CommitAll fails for any
	// reason other than ErrNoWorktree (Err populated). It is non-terminal:
	// the session and stage keep running.
	EventCheckpointFailed EventKind = "checkpoint_failed"
	// EventBoard signals the board session changed and its surface should
	// re-render from BoardSession.Snapshot. It is the one kind that
	// carries no Feature, because a board session is bound to the
	// workspace rather than to any card — see the note on Event.Feature.
	EventBoard EventKind = "board"
	// EventCardCreated fires when a card is minted by a caller that holds
	// an *Engine but doesn't otherwise touch the session machinery — the
	// workspace MCP endpoint's card_new — so no other Event would ever
	// cover it. A UI surface should reload rows.
	EventCardCreated EventKind = "card_created"
)

// Event is one item in the engine's UI-facing stream.
type Event struct {
	// Feature is the card the event belongs to, and MAY BE EMPTY. Two
	// sources send featureless events: a one-shot pass not bound to a
	// card (ingest, which sends EventError), and the board session
	// (EventBoard), which is bound to the workspace instead. A consumer
	// that looks a Feature up — Engine.Get, a row lookup, the attention
	// queue — must therefore establish it has one first. The empty case
	// is a normal value on this channel, not a bug upstream.
	Feature    domain.FeatureID
	Stage      domain.Stage
	Kind       EventKind
	Err        error
	Threshold  int      // budget % for EventBudget
	Committed  bool     // EventExhausted: the stage's work was committed (not stranded)
	DirtyPaths []string // EventTripwire: paths newly dirty on main after the turn (sorted)
}

// SessionState is a session's scheduling status.
type SessionState string

const (
	// StateInteractive is a chat session; it holds no attention slot.
	StateInteractive SessionState = "interactive"
	// StateQueued is an autonomous session waiting for a free slot.
	StateQueued SessionState = "queued"
	// StateRunning is an autonomous session holding a slot, agent working.
	StateRunning SessionState = "running"
	// StatePaused is an autonomous session stopped by the user; slot freed.
	StatePaused SessionState = "paused"
	// StateDone is an autonomous session that finished its turn; slot freed.
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

// Message is one transcript turn.
type Message struct {
	Author    Author
	Content   string
	Streaming bool // true while assistant text is still arriving

	// AuthorTool entries only: the call's outcome once the backend
	// reports it, and the captured output (bounded at the adapter).
	ToolStatus ToolStatus
	ToolOutput string
	callID     string // backend call id awaiting its result; cleared on resolve
}

// Snapshot is an immutable view of a session's state, safe to render.
type Snapshot struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool
	Critique    bool // this is a plan-critique pass, not the plan writer
	Rebase      bool // this is a rebase-resolve pass, not the stage's work
	State       SessionState
	AgentName   string // backend running this session ("copilot", "opencode", …)
	// AgentSessionID is the backend's own session id (agent.Identified),
	// pointing at its on-disk log; empty for backends without one.
	AgentSessionID     string
	Model              string // model resolved at spawn (Spend.Model is the reported one)
	Transcript         []Message
	Activity           []string // recent tool-call lines
	Spend              agent.Usage
	SpentCredits       float64       // Spend as a credit-equivalent at the provider's rate
	Context            agent.Context // latest context-window occupancy
	Busy               bool          // agent is mid-turn
	PendingAsk         *Ask          // the agent's open ask_user question, if any
	Verdict            string        // review verdict via submit_verdict, if submitted
	VerdictFloor       string        // deterministic ceiling applied before returning the stage verdict
	VerdictFloorReason string        // human-readable reason for the floor, if any
	Err                error
	EnvProbes          []envprobe.Result
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
	// ReadOnly marks an autonomous research pass (investigate,
	// review-of-research) that must never mutate the main checkout. Set at
	// construction from researchReadOnly, immutable. The engine uses it to
	// refuse mutating client tools (spec_replace_section, spec_annotate)
	// even when a hand-crafted MCP call names them.
	ReadOnly bool
	// pool is the attention pool (attended/autopilot) this session
	// competes in — decided once by lanePoolFor at construction from the
	// feature's gate-approval mode, and immutable after. It is what lets
	// freeSlot return a slot to the same pool the session took it from
	// (see heldSlot and releaseSlot).
	pool lanePool
	// kickoffNote is extra content appended to an autonomous run's stage
	// kickoff — the user's review comments delivered via RunWith. Set at
	// construction, immutable after (like Feature/Role).
	kickoffNote string
	// startedAt is when this session generation began. It is the
	// discriminator that keeps mirrored events unique per generation:
	// a stage that re-runs (a review bounce, a resumed card) gets a
	// fresh transcript numbered from zero again, so ord alone would
	// collide with the previous generation's events.
	startedAt time.Time

	done     chan struct{}
	stopOnce sync.Once
	// ctx is the session's lifecycle context: canceled by stop(), so any
	// in-flight git subprocess (the tripwire's dirty snapshots) is killed
	// the instant the session finalizes rather than racing teardown.
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	agentSess      agent.Session // nil while queued
	agentName      string        // backend identity, for display
	agentSessionID string        // backend session id (agent.Identified), "" if none
	model          string        // model resolved at spawn
	specPath       string        // resolved spec/draft path (for ask_user capture)
	state          SessionState
	transcript     []Message
	activity       []string
	spend          agent.Usage
	context        agent.Context
	busy           bool
	pendingAsk     *Ask
	verdict        string // review verdict from submit_verdict ("pass"/"changes")
	err            error
	stopped        bool
	finalized      bool         // stopped; must not be persisted (may be dropped)
	heldSlot       bool         // true between taking and releasing an attention slot
	budget         float64      // stage credit budget (0 = none)
	creditRate     float64      // adapter's token→credit rate (0 = engine default)
	threshold      int          // highest budget threshold crossed (%)
	pendingNudge   string       // budget nudge awaiting the next sent turn
	exhausted      bool         // hit the credit cap
	tripped        bool         // main-checkout tripwire fired; the run is dead
	clientTools    bool         // resolved backend's ClientTools capability (spawn-time cache)
	sandboxMode    sandbox.Mode // resolved confinement mode (stamped at spawn)

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

	// preTurnDirt is the set of paths dirty on the main checkout
	// immediately before the pending Send. The tripwire compares it
	// against the post-turn set at EventIdle; nil means no pre-turn
	// snapshot was taken this turn (see takePreTurn).
	preTurnDirt map[string]struct{}

	// envProbes holds the most recent env prerequisite probe results for
	// this session, produced at Verify kickoff and surfaced on Snapshot.
	envProbes []envprobe.Result

	// verdictFloor is a deterministic ceiling applied to the raw agent
	// verdict. Currently only "blocked" is used, and only for a bug whose
	// Verify finish omitted every [env:] live check while a prerequisite
	// probed present. It only ever downgrades Pass -> Blocked.
	verdictFloor       string
	verdictFloorReason string

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
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Feature:            s.Feature,
		Role:               s.Role,
		Interactive:        s.Interactive,
		Critique:           s.Critique,
		Rebase:             s.Rebase,
		State:              s.state,
		AgentName:          s.agentName,
		AgentSessionID:     s.agentSessionID,
		Model:              s.model,
		Transcript:         append([]Message(nil), s.transcript...),
		Activity:           append([]string(nil), s.activity...),
		Spend:              s.spend,
		SpentCredits:       s.spentForBudgetLocked(),
		Context:            s.context,
		Busy:               s.busy,
		PendingAsk:         s.pendingAsk,
		Verdict:            s.verdict,
		VerdictFloor:       s.verdictFloor,
		VerdictFloorReason: s.verdictFloorReason,
		Err:                s.err,
		EnvProbes:          append([]envprobe.Result(nil), s.envProbes...),
	}
}

// kickoffMessage returns the autonomous stage kickoff, with the user's
// review comments appended when this run carries them (RunWith). A
// rebase session opens with its own go-ahead; its note carries the
// rebase target and expected conflicts (RunRebase).
func (s *Session) kickoffMessage() string {
	base := kickoff
	if s.Rebase {
		base = rebaseKickoff
	}
	if s.kickoffNote == "" {
		return base
	}
	return base + "\n\n" + s.kickoffNote
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

// agent returns the session's agent session (nil while queued).
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

// takeSlot marks that this session holds an attention slot in its pool.
func (s *Session) takeSlot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heldSlot = true
}

// releaseSlot clears the slot flag, reporting whether it was held (so the
// engine decrements the running count exactly once, and never for a
// session — queued or interactive — that never took a slot) and which
// pool it was held in, so the engine returns the slot to that same pool
// and never the other one. pool is meaningful only when held is true.
func (s *Session) releaseSlot() (held bool, pool lanePool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held = s.heldSlot
	s.heldSlot = false
	return held, s.pool
}

func (s *Session) appendUser(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorUser, Content: text})
	s.err = nil
	s.live.Emit(livelog.Record{Kind: livelog.KindUser, Text: text})
}

func (s *Session) appendSystem(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorSystem, Content: text})
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
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Author == AuthorAssistant && s.transcript[n-1].Streaming {
		s.transcript[n-1].Content += text
		return
	}
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text, Streaming: true})
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
	n := len(s.transcript)
	streaming := n > 0 && s.transcript[n-1].Author == AuthorAssistant && s.transcript[n-1].Streaming
	if strings.TrimSpace(text) == "" {
		if streaming {
			s.transcript[n-1].Streaming = false
		}
		return
	}
	if streaming {
		s.transcript[n-1].Content = text
		s.transcript[n-1].Streaming = false
		return
	}
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text})
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
// call id so a later resolveToolResult can attach the outcome.
func (s *Session) appendToolCall(callID, line string) {
	s.appendTool(Message{Author: AuthorTool, Content: line, callID: callID})
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
		Kind: livelog.KindTool, Text: m.Content, Call: m.callID,
		OK: m.ToolStatus == ToolOK, Output: m.ToolOutput,
	})
}

// resolveToolResult attaches a backend-reported outcome to the pending
// AuthorTool entry with the matching call id. Unknown ids are dropped
// (a result for a call gummi never displayed, e.g. one from before a
// restart).
func (s *Session) resolveToolResult(callID string, ok bool, output string) {
	if callID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.transcript) - 1; i >= 0; i-- {
		if s.transcript[i].callID != callID {
			continue
		}
		s.transcript[i].callID = ""
		s.transcript[i].ToolStatus = ToolOK
		if !ok {
			s.transcript[i].ToolStatus = ToolFail
		}
		s.transcript[i].ToolOutput = output
		s.live.Emit(livelog.Record{Kind: livelog.KindResult, Call: callID, OK: ok, Output: output})
		return
	}
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

func (s *Session) setVerdictFloor(v, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdictFloor = v
	s.verdictFloorReason = reason
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

// setSandboxMode records the session's resolved confinement mode (warn /
// enforce / off), stamped once at spawn. The tripwire reads it to decide
// whether this run's arming state is on or off.
func (s *Session) setSandboxMode(m sandbox.Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxMode = m
}

// SandboxMode returns the session's resolved confinement mode. The zero
// value (unset) is treated as armed — only an explicit off disarms.
func (s *Session) SandboxMode() sandbox.Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandboxMode
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

// beginTurn records the set of paths dirty on main immediately before a
// Send, arming the tripwire's post-turn comparison.
func (s *Session) beginTurn(paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		set[p] = struct{}{}
	}
	s.preTurnDirt = set
}

// takePreTurn reads-and-clears the pre-turn dirt set, returning nil when
// no snapshot was taken this turn (a resumed session, a race, or a
// pre-turn snapshot error that skipped beginTurn) — the fault-open arm
// the tripwire must not mis-fire on.
func (s *Session) takePreTurn() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	pre := s.preTurnDirt
	s.preTurnDirt = nil
	return pre
}

// markTripped latches that the main-checkout tripwire fired, returning
// true only the first time so the abort (activity, state, event) happens
// exactly once. Mirrors markExhausted.
func (s *Session) markTripped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tripped {
		return false
	}
	s.tripped = true
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
