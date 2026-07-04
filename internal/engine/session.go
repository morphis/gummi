package engine

import (
	"strings"
	"sync"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
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
)

// Event is one item in the engine's UI-facing stream.
type Event struct {
	Feature domain.FeatureID
	Stage   domain.Stage
	Kind    EventKind
	Err     error
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
)

// Message is one transcript turn.
type Message struct {
	Author    Author
	Content   string
	Streaming bool // true while assistant text is still arriving
}

// Snapshot is an immutable view of a session's state, safe to render.
type Snapshot struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool
	State       SessionState
	Transcript  []Message
	Activity    []string // recent tool-call lines
	Spend       agent.Usage
	Busy        bool // agent is mid-turn
	Err         error
}

// Session is one live agent conversation bound to a feature + stage.
type Session struct {
	Feature     domain.Feature
	Role        agent.Role
	Interactive bool

	done     chan struct{}
	stopOnce sync.Once

	mu         sync.Mutex
	agentSess  agent.Session // nil while queued
	state      SessionState
	transcript []Message
	activity   []string
	spend      agent.Usage
	busy       bool
	err        error
	stopped    bool
	finalized  bool // stopped; must not be persisted (may be dropped)
	heldSlot   bool // true between taking and releasing an attention slot
}

// Snapshot returns a render-safe copy of the session's state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Feature:     s.Feature,
		Role:        s.Role,
		Interactive: s.Interactive,
		State:       s.state,
		Transcript:  append([]Message(nil), s.transcript...),
		Activity:    append([]string(nil), s.activity...),
		Spend:       s.spend,
		Busy:        s.busy,
		Err:         s.err,
	}
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

// attachAgent binds an agent session and marks it running.
func (s *Session) attachAgent(a agent.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentSess = a
	s.state = StateRunning
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

func (s *Session) appendDelta(text string) {
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
func (s *Session) finishAssistant(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Author == AuthorAssistant && s.transcript[n-1].Streaming {
		s.transcript[n-1].Content = text
		s.transcript[n-1].Streaming = false
		return
	}
	s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: text})
}

func (s *Session) appendActivity(tool string) {
	// activity is stored newline-joined; keep labels single-line so they
	// round-trip through persistence intact.
	tool = strings.ReplaceAll(strings.ReplaceAll(tool, "\n", " "), "\r", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = append(s.activity, tool)
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
