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
	"path/filepath"
	"sync"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/worktree"
)

// kickoff is the go-ahead sent to start an autonomous stage; the stage
// hints already tell the agent what to do.
const kickoff = "Proceed with this stage per your instructions and the spec."

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
}

// Engine orchestrates all live sessions and the autonomous run queue.
type Engine struct {
	cfg       Config
	maxActive int

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
		raw:       make(chan Event, 256),
		events:    make(chan Event),
		stopped:   make(chan struct{}),
		live:      map[domain.FeatureID]*Session{},
	}
	go e.forward()
	return e
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
	if s, ok := e.live[f.ID]; ok && s.Feature.Stage == f.Stage {
		e.mu.Unlock()
		return s, nil // reuse the live session
	}
	e.mu.Unlock()

	sess, err := e.newAgentSession(ctx, f, role)
	if err != nil {
		return nil, err
	}
	s := &Session{Feature: f, Role: role, Interactive: true, state: StateInteractive, done: make(chan struct{})}
	s.attachAgent(sess)
	s.setState(StateInteractive)

	e.replace(f.ID, s)
	go e.pump(s)
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventStarted})
	return s, nil
}

// Run enqueues an autonomous stage for a feature and fills any free
// slot. A no-op if the feature is already queued or running.
func (e *Engine) Run(f domain.Feature) error {
	role, ok := roleForStage(f.Stage)
	if !ok {
		return fmt.Errorf("stage %s has no agent action", f.Stage)
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
		if st := old.state; st == StateRunning || st == StateQueued {
			e.mu.Unlock()
			return nil // already scheduled
		}
	}
	s := &Session{Feature: f, Role: role, state: StateQueued, done: make(chan struct{})}
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
		if s == nil || s.state != StateQueued {
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
	sess, err := e.newAgentSession(context.Background(), s.Feature, s.Role)
	if err != nil {
		s.setError(err)
		s.setState(StatePaused)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		e.freeSlot(s)
		return
	}
	s.attachAgent(sess)
	go e.pump(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStarted})

	s.appendUser(kickoff)
	s.setBusy(true)
	if err := sess.Send(context.Background(), kickoff); err != nil {
		s.setError(err)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		e.freeSlot(s)
	}
}

// newAgentSession builds an agent session for a feature's stage.
func (e *Engine) newAgentSession(ctx context.Context, f domain.Feature, role agent.Role) (agent.Session, error) {
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, err
	}
	sess, err := e.cfg.Agent.NewSession(ctx, agent.SessionOpts{
		WorkDir:     workDir,
		Role:        role,
		Model:       e.cfg.Model,
		SystemHints: stageHints(f, specPath),
		Provider:    e.cfg.Provider,
		Permission:  e.cfg.Permission,
	})
	if err != nil {
		return nil, fmt.Errorf("starting %s session: %w", role, err)
	}
	return sess, nil
}

// locate resolves the working directory and spec path for a feature's
// stage. Interactive pre-worktree stages run in the main checkout
// against the draft; later stages require the worktree.
func (e *Engine) locate(ctx context.Context, f domain.Feature) (workDir, specPath string, err error) {
	root := e.cfg.Worktrees.Root()
	if interactiveStage(f.Stage) {
		return root, filepath.Join(e.cfg.Workspace.DraftsDir(), draftName(f)), nil
	}
	hasWT, err := e.cfg.Worktrees.Exists(ctx, &f)
	if err != nil {
		return "", "", err
	}
	if !hasWT {
		return "", "", fmt.Errorf("feature %s at stage %s has no worktree; approve the spec to create one first", f.ID, f.Stage)
	}
	workDir = filepath.Join(root, f.WorktreePath())
	return workDir, filepath.Join(workDir, f.SpecPath()), nil
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
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})
	if err := a.Send(ctx, msg); err != nil {
		s.setError(err)
		e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventError, Err: err})
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

// replace installs a session for a feature, stopping any prior one.
func (e *Engine) replace(id domain.FeatureID, s *Session) {
	e.mu.Lock()
	old := e.live[id]
	e.removeFromQueue(id)
	e.live[id] = s
	e.mu.Unlock()
	if old != nil {
		old.stop()
		e.freeSlot(old)
	}
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

func (e *Engine) handle(s *Session, ev agent.Event) {
	kind := EventUpdated
	switch ev.Kind {
	case agent.EventTextDelta:
		s.appendDelta(ev.Text)
	case agent.EventMessage:
		s.finishAssistant(ev.Text)
		kind = EventMessage
	case agent.EventToolCall:
		s.appendActivity(ev.Tool)
	case agent.EventUsage:
		s.addSpend(ev.Usage)
	case agent.EventIdle:
		s.setBusy(false)
		kind = EventIdle
		// an autonomous turn completing frees the slot
		if !s.Interactive && s.State() == StateRunning {
			s.setState(StateDone)
			e.freeSlot(s)
		}
	case agent.EventError:
		s.setError(ev.Err)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: ev.Err})
		return
	}
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: kind})
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

func draftName(f domain.Feature) string {
	return string(f.ID) + "-" + f.Slug + ".md"
}
