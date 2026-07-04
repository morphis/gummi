// Package engine is gummi's M1 orchestrator: it binds a feature's
// current stage to a single active agent session (attention slot = 1,
// DESIGN §4.2), routes user turns, and streams the agent's activity to
// the UI as typed events. The scheduler, needs-attention queue, and
// multiple concurrent sessions are M2; this is the single-session core.
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
}

// Engine orchestrates the single active agent session.
type Engine struct {
	cfg Config
	// raw carries events from pump goroutines to the forwarder; events
	// is the UI-facing stream, owned solely by the forwarder so it can
	// close it exactly once without a send-on-closed race.
	raw     chan Event
	events  chan Event
	stopped chan struct{}

	mu     sync.Mutex
	active *Session
	closed bool
}

// New builds an engine. The caller owns cfg.Agent's lifetime.
func New(cfg Config) *Engine {
	if cfg.Permission == "" {
		cfg.Permission = agent.PermissionAllowAll
	}
	e := &Engine{
		cfg:     cfg,
		raw:     make(chan Event, 256),
		events:  make(chan Event),
		stopped: make(chan struct{}),
	}
	go e.forward()
	return e
}

// forward is the only writer to e.events: it relays raw events until
// the engine stops, then closes the stream.
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

// Events is the UI-facing stream. It stays open for the engine's life
// and closes on Close.
func (e *Engine) Events() <-chan Event { return e.events }

// Active returns the current session (nil if none). The returned value
// is the live session; read its state via Snapshot.
func (e *Engine) Active() *Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// Start binds the given feature's current stage to a new active
// session, replacing any existing one (attention slot = 1). Stages
// with no agent action (todo/done) are rejected.
func (e *Engine) Start(ctx context.Context, f domain.Feature) (*Session, error) {
	role, ok := roleForStage(f.Stage)
	if !ok {
		return nil, fmt.Errorf("stage %s has no agent action", f.Stage)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("engine is closed")
	}
	e.mu.Unlock()

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

	s := &Session{
		Feature:     f,
		Role:        role,
		Interactive: interactiveStage(f.Stage),
		agentSess:   sess,
		done:        make(chan struct{}),
	}

	// Swap in the new session, stopping the old one outside the lock.
	e.mu.Lock()
	old := e.active
	e.active = s
	e.mu.Unlock()
	if old != nil {
		old.stop()
	}

	go e.pump(s)
	e.send(Event{Feature: f.ID, Stage: f.Stage, Kind: EventStarted})
	return s, nil
}

// locate resolves the working directory and spec path for a feature's
// current stage. The interactive pre-worktree stages (brainstorm/spec)
// run in the main checkout against the draft; every later stage runs in
// the worktree and requires it to exist — running an implementer in the
// main checkout would corrupt the base repo, so that is an error, not a
// silent fallback.
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

// Send routes a user/orchestrator turn to the active session.
func (e *Engine) Send(ctx context.Context, msg string) error {
	s := e.Active()
	if s == nil {
		return errors.New("no active session")
	}
	s.appendUser(msg)
	s.setBusy(true)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
	if err := s.agentSess.Send(ctx, msg); err != nil {
		s.setError(err)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: err})
		return err
	}
	return nil
}

// Interrupt aborts the active session's in-flight turn.
func (e *Engine) Interrupt(ctx context.Context) error {
	s := e.Active()
	if s == nil {
		return errors.New("no active session")
	}
	return s.agentSess.Interrupt(ctx)
}

// Stop ends the active session (frees the attention slot).
func (e *Engine) Stop() {
	e.mu.Lock()
	s := e.active
	e.active = nil
	e.mu.Unlock()
	if s != nil {
		s.stop()
	}
}

// Close stops any active session and closes the event stream.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	s := e.active
	e.active = nil
	e.mu.Unlock()

	if s != nil {
		s.stop()
	}
	// Signal the forwarder, which closes the UI stream. Buffered events
	// still in flight are dropped — the engine is shutting down.
	close(e.stopped)
	return nil
}

// pump relays one session's agent events into the engine stream and
// accumulates the session's transcript/activity/spend. It exits when
// the session stops or its agent channel closes.
func (e *Engine) pump(s *Session) {
	for {
		select {
		case <-s.done:
			e.emitStopped(s)
			return
		case ev, ok := <-s.agentSess.Events():
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
	case agent.EventError:
		s.setError(ev.Err)
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventError, Err: ev.Err})
		return
	}
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: kind})
}

// emitStopped fires a single stopped event per session.
func (e *Engine) emitStopped(s *Session) {
	if s.markStopped() {
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventStopped})
	}
}

// send hands an event to the forwarder, applying backpressure on the
// 256-deep raw buffer rather than dropping — terminal events (idle,
// stopped, error, message) must not be lost. A closed engine unblocks
// via stopped, so this never leaks.
func (e *Engine) send(ev Event) {
	select {
	case e.raw <- ev:
	case <-e.stopped:
	}
}

func draftName(f domain.Feature) string {
	return string(f.ID) + "-" + f.Slug + ".md"
}
