package engine

import (
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

	agentSess agent.Session
	done      chan struct{}
	stopOnce  sync.Once

	mu         sync.Mutex
	transcript []Message
	activity   []string
	spend      agent.Usage
	busy       bool
	err        error
	stopped    bool
}

// Snapshot returns a render-safe copy of the session's state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Feature:     s.Feature,
		Role:        s.Role,
		Interactive: s.Interactive,
		Transcript:  append([]Message(nil), s.transcript...),
		Activity:    append([]string(nil), s.activity...),
		Spend:       s.spend,
		Busy:        s.busy,
		Err:         s.err,
	}
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

// stop ends the session: it signals the pump and closes the agent
// session. Idempotent.
func (s *Session) stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		_ = s.agentSess.Close()
	})
}
