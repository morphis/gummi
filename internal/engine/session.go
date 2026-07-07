package engine

import (
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
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
)

// Event is one item in the engine's UI-facing stream.
type Event struct {
	Feature   domain.FeatureID
	Stage     domain.Stage
	Kind      EventKind
	Err       error
	Threshold int // budget % for EventBudget
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

// Message is one transcript turn.
type Message struct {
	Author    Author
	Content   string
	Streaming bool // true while assistant text is still arriving
}

// Snapshot is an immutable view of a session's state, safe to render.
type Snapshot struct {
	Feature      domain.Feature
	Role         agent.Role
	Interactive  bool
	State        SessionState
	AgentName    string         // backend running this session ("copilot", "opencode", …)
	Model        string         // model resolved at spawn (Spend.Model is the reported one)
	Provider     agent.Provider // BYOK endpoint; zero means native routing
	Transcript   []Message
	Activity     []string // recent tool-call lines
	Spend        agent.Usage
	SpentCredits float64       // Spend as a credit-equivalent at the provider's rate
	Context      agent.Context // latest context-window occupancy
	Busy         bool          // agent is mid-turn
	PendingAsk   *Ask          // the agent's open ask_user question, if any
	Verdict      string        // review verdict via submit_verdict, if submitted
	Err          error
}

// Session is one live agent conversation bound to a feature + stage.
type Session struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool
	// kickoffNote is extra content appended to an autonomous run's stage
	// kickoff — the user's review comments delivered via RunWith. Set at
	// construction, immutable after (like Feature/Role).
	kickoffNote string

	done     chan struct{}
	stopOnce sync.Once

	mu         sync.Mutex
	agentSess  agent.Session // nil while queued
	agentName  string        // backend identity, for display
	model      string        // model resolved at spawn
	provider   agent.Provider
	specPath   string // resolved spec/draft path (for ask_user capture)
	state      SessionState
	transcript []Message
	activity   []string
	spend      agent.Usage
	context    agent.Context
	busy       bool
	pendingAsk *Ask
	verdict    string // review verdict from submit_verdict ("pass"/"changes")
	err        error
	stopped    bool
	finalized  bool    // stopped; must not be persisted (may be dropped)
	heldSlot   bool    // true between taking and releasing an attention slot
	budget     float64 // stage credit budget (0 = none)
	byokRate   float64 // provider token→credit rate (0 = default)
	threshold  int     // highest budget threshold crossed (%)
	exhausted  bool    // hit the credit cap
}

// Snapshot returns a render-safe copy of the session's state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Feature:      s.Feature,
		Role:         s.Role,
		Interactive:  s.Interactive,
		State:        s.state,
		AgentName:    s.agentName,
		Model:        s.model,
		Provider:     s.provider,
		Transcript:   append([]Message(nil), s.transcript...),
		Activity:     append([]string(nil), s.activity...),
		Spend:        s.spend,
		SpentCredits: s.spentForBudgetLocked(),
		Context:      s.context,
		Busy:         s.busy,
		PendingAsk:   s.pendingAsk,
		Verdict:      s.verdict,
		Err:          s.err,
	}
}

// kickoffMessage returns the autonomous stage kickoff, with the user's
// review comments appended when this run carries them (RunWith).
func (s *Session) kickoffMessage() string {
	if s.kickoffNote == "" {
		return kickoff
	}
	return kickoff + "\n\n" + s.kickoffNote
}

// State returns the session's scheduling status.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) setState(st SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
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
	s.state = StateRunning
	return true
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

// takeSlot marks that this session holds an attention slot.
func (s *Session) takeSlot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heldSlot = true
}

// releaseSlot clears the slot flag, returning whether it was held (so
// the engine decrements the running count exactly once, and never for a
// session — queued or interactive — that never took a slot).
func (s *Session) releaseSlot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.heldSlot
	s.heldSlot = false
	return held
}

func (s *Session) appendUser(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorUser, Content: text})
	s.err = nil
}

func (s *Session) appendSystem(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = append(s.transcript, Message{Author: AuthorSystem, Content: text})
	s.err = nil
}

func (s *Session) appendDelta(text string) {
	if text == "" {
		return // nothing to add; don't open an empty streaming bubble
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	}
}

// appendActivity records a tool-call line twice: on the activity ticker
// (the dashboard's recent-lines feed) and as an AuthorTool transcript
// entry, so the full history keeps it ordered against the messages
// around it.
func (s *Session) appendActivity(tool string) {
	// activity is stored newline-joined; keep labels single-line so they
	// round-trip through persistence intact.
	tool = strings.ReplaceAll(strings.ReplaceAll(tool, "\n", " "), "\r", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = append(s.activity, tool)
	// a tool call mid-stream also closes the streaming bubble: the next
	// delta belongs to a new one (the agent spoke, acted, spoke again).
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Author == AuthorAssistant && s.transcript[n-1].Streaming {
		s.transcript[n-1].Streaming = false
	}
	s.transcript = append(s.transcript, Message{Author: AuthorTool, Content: tool})
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
// (credits for hosted, token-derived for BYOK at the provider's rate).
// Caller holds s.mu.
func (s *Session) spentForBudgetLocked() float64 {
	return domain.Spend{Credits: s.spend.Credits, InputTokens: s.spend.InputTokens, OutputTokens: s.spend.OutputTokens}.
		CreditEquivalentAt(s.byokRate)
}

func (s *Session) setByokRate(r float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byokRate = r
}

// creditEquivalent prices one usage event as credits at this session's
// provider rate: the metered credits for hosted usage, or a token-derived
// value for BYOK (which reports tokens, never credits). Persisting this
// keeps the feature's credit-denominated running total whole when its
// stages mix hosted and BYOK providers.
func (s *Session) creditEquivalent(u agent.Usage) float64 {
	s.mu.Lock()
	rate := s.byokRate
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
	return true
}

func (s *Session) setVerdict(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdict = v
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

// setSpawnInfo records which backend, model, and provider this session
// runs on, so the UI can say so before the first usage event arrives.
func (s *Session) setSpawnInfo(agentName, model string, provider agent.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentName = agentName
	s.model = model
	s.provider = provider
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

func (s *Session) setBusy(b bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = b
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
		s.mu.Unlock()
		close(s.done)
		if a := s.agent(); a != nil {
			_ = a.Close()
		}
	})
}

// finalizedState reports whether the session has been stopped; a
// finalized session must not be persisted (it may have been dropped).
func (s *Session) finalizedState() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalized
}
