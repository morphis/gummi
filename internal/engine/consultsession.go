package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// consultPermission is the fixed tool-call policy every consult session
// spawns with — allow-all, the same reasoning boardPermission's own doc
// comment gives (PermissionGuarded is refused outright by some adapters
// and hangs the others, since nothing in this codebase ever emits
// agent.EventPermission to answer it). A consult session's tool surface
// is read-only regardless, so "allow" here never risks a mutation.
const consultPermission = agent.PermissionAllowAll

// consultIdleTimeout is the golden value (Implementation notes): long
// enough to survive reading a diff or spec before a follow-up question,
// short enough that an abandoned conversation's subprocess doesn't
// outlive the rest of a multi-hour gummi run. Engine.consultIdleTimeout
// is seeded from this in New() and is what every ConsultSession actually
// reads, so a test can shrink the field and observe a real timer fire
// deterministically instead of waiting out the golden value.
const consultIdleTimeout = 20 * time.Minute

// ConsultSession is a card-scoped sibling of BoardSession: an
// engine.Session wrapped the same way, with the same absences — no
// lockCard, no attention-pool slot, no live-file binding, no checkpoint,
// no store row, no MaxCredits. It differs from BoardSession in exactly
// two ways: it is keyed by domain.FeatureID instead of being a workspace
// singleton, and its tool surface is the read-only three (card_status,
// card_spec, card_diff) instead of the full seven, each call implicitly
// scoped to its own bound card.
//
// One ConsultSession exists per card for this engine's whole lifetime
// (OpenConsult is idempotent per card) — but the backend it holds is not
// as long-lived: an idle timeout closes it after 20 minutes of no turns,
// and the next question respawns one, carrying the consult session's own
// transcript over. sess is therefore swapped, not fixed, across that
// session's lifetime; mu guards the swap.
type ConsultSession struct {
	engine *Engine
	id     domain.FeatureID
	// stage is the card's stage at OpenConsult time, stamped once and
	// used to attribute recorded spend (Store.RecordStageSpend) for as
	// long as this ConsultSession lives — a consult conversation outlives
	// any one stage, but the spend it costs still has to land somewhere.
	stage domain.Stage
	// rc/backend are resolved once, at OpenConsult, and reused by every
	// respawn — a card's profile does not change mid-conversation, so
	// there is nothing to re-resolve.
	rc      config.RoleConfig
	backend string

	mu   sync.Mutex
	sess *Session

	idleMu    sync.Mutex
	idleTimer *time.Timer
}

// OpenConsult starts (or reuses) the card's consult session. The first
// call for a card in this engine's lifetime spawns a backend, seeded
// from that card's live stage session's transcript (if one exists, in
// any state — done, paused, restored) so a finished autonomous stage's
// "best conversation available" carries into the read-only channel
// rather than being lost. Every later call — another question, reopening
// the card page — returns the identical *ConsultSession, mirroring
// OpenBoard's own "reopening while one is already live returns it as-is"
// rule.
func (e *Engine) OpenConsult(ctx context.Context, f domain.Feature) (*ConsultSession, error) {
	// Serialized end to end for the same reason boardMu exists: spawning
	// a backend is too slow to do under e.mu, so a check-then-act around
	// a released lock would let two concurrent callers for the same card
	// both see "not open yet" and both spawn one.
	e.consultMu.Lock()
	defer e.consultMu.Unlock()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("engine is closed")
	}
	if prior := e.consult[f.ID]; prior != nil {
		e.mu.Unlock()
		return prior, nil
	}
	priorStage := e.live[f.ID]
	e.mu.Unlock()

	rc, backend := e.resolveConsultRole(f.Profile)
	c := &ConsultSession{engine: e, id: f.ID, stage: f.Stage, rc: rc, backend: backend}

	// The one-directional seed: a card's stage session (of any state),
	// carried over exactly once, on this card's first-ever OpenConsult.
	// Every later respawn (ensureBackend, on idle-timeout) carries over
	// this ConsultSession's OWN transcript instead — it never re-reads
	// e.live, so the stage seed can never happen a second time.
	var seed []Message
	if priorStage != nil {
		seed = priorStage.Snapshot().Transcript
	}
	if err := c.spawn(ctx, seed); err != nil {
		return nil, err
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		c.stopBackend()
		return nil, errors.New("engine is closed")
	}
	e.consult[f.ID] = c
	e.mu.Unlock()
	e.send(Event{Feature: f.ID, Kind: EventUpdated})
	return c, nil
}

// Consult looks up a card's consult session without ever spawning one —
// the read path a render uses (thread.go's consultBlock) to show
// whatever exists without accidentally opening a backend just by
// visiting the card page.
func (e *Engine) Consult(id domain.FeatureID) *ConsultSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.consult[id]
}

// spawn starts a fresh backend for c, seeded with the given transcript
// (either the stage-session seed on first open, or this session's own
// accumulated history on a respawn). It installs the new *Session on c
// and starts its pump, but — unlike OpenConsult — never touches
// e.consult; the caller decides whether this is a first install or a
// respawn of an already-registered session.
func (c *ConsultSession) spawn(ctx context.Context, seed []Message) error {
	e := c.engine
	ag := e.agentFor(c.backend)
	if ag == nil {
		return errors.New("no agent configured for the consult role")
	}
	caps := ag.Capabilities()

	// Both halves of OpenBoard's two-way wiring, card-scoped: native
	// client tools (Tools, answered via dispatchConsultClientTool) for a
	// ClientTools backend, or a dedicated per-open MCP endpoint
	// (startConsultMCPEndpoint — never the workspace's own
	// StartWorkspaceMCPEndpoint or a stage session's own mcpSockPath, to
	// keep this session's tool surface pinned to the read-only three and
	// this one card) for an MCPTools backend. A backend with neither
	// capability gets no tools at all, the same "no tools" degradation
	// OpenBoard applies.
	var tools []agent.ToolDef
	var mcpPath string
	var mcpTeardown func()
	switch {
	case caps.ClientTools:
		tools = consultTools()
	case caps.MCPTools:
		path, teardown, err := e.startConsultMCPEndpoint(ctx, c.id)
		if err != nil {
			return fmt.Errorf("starting consult session's mcp endpoint: %w", err)
		}
		mcpPath = path
		mcpTeardown = teardown
	}

	sctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		Role:        agent.RoleConsult,
		Interactive: true,
		state:       StateInteractive,
		done:        make(chan struct{}),
		ctx:         sctx,
		cancel:      cancel,
		startedAt:   time.Now(),
	}
	sess.setSpawnInfo(ag.Name(), c.rc.Model, caps.ClientTools)
	if len(seed) > 0 {
		sess.transcript = append(sess.transcript, seed...)
	}

	agentSess, err := ag.NewSession(ctx, agent.SessionOpts{
		// The workspace root, never a worktree: a consult session has no
		// write access to earn one, and its one file-shaped answer
		// (card_diff) comes back as tool output text, not a checkout.
		WorkDir:        e.cfg.Workspace.Root,
		Role:           agent.RoleConsult,
		Model:          c.rc.Model,
		Permission:     consultPermission,
		Tools:          tools,
		OutputTokenMax: c.rc.OutputTokenMax,
		Provider:       c.rc.Provider,
		Think:          c.rc.Think,
		FeatureID:      string(c.id),
		MCPSockPath:    mcpPath,
		// No ArtifactPath, no MaxCredits: a consult session has no spec
		// prompt to seed (card_spec answers that on demand instead) and no
		// budget to enforce. Workspace is left false (the zero value) even
		// when mcpPath is set: an MCP-reaching backend here always dials in
		// --feature <id> mode (FeatureID above), never --workspace — this
		// is a card-scoped endpoint, not the board's.
	})
	if err != nil {
		if mcpTeardown != nil {
			mcpTeardown()
		}
		cancel()
		return fmt.Errorf("starting consult session: %w", err)
	}
	sess.setMCPTeardown(mcpTeardown)
	if !sess.attachAgent(agentSess) {
		_ = agentSess.Close()
		return errors.New("engine is closed")
	}
	sess.setState(StateInteractive)

	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()

	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pumpConsult(c, sess) }()
	c.armIdleTimer()
	return nil
}

// ensureBackend returns c's current backend, respawning one — carrying
// over c's own accumulated transcript — when the last one has idled out
// (or this is the very first turn after a respawn raced ahead of it).
// Session.Live() is the same predicate the thread composer uses for
// steer-vs-ask: it correctly reads false once onIdleTimeout has marked
// the stopped session StateDone, which is what makes the respawn trigger
// exactly when the backend is actually gone, never while a turn is still
// in flight.
func (c *ConsultSession) ensureBackend(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess.Live() {
		return sess, nil
	}
	var seed []Message
	if sess != nil {
		seed = sess.Snapshot().Transcript
	}
	if err := c.spawn(ctx, seed); err != nil {
		return nil, err
	}
	c.mu.Lock()
	sess = c.sess
	c.mu.Unlock()
	return sess, nil
}

// Send delivers a user turn to the card's consult session, respawning
// its backend first if the last one idled out. It mirrors BoardSession's
// own Send minus everything that doesn't apply here: no budget nudge (no
// budget), no persist (no store row backs a consult session — its spend
// still lands in the feature's own totals, via recordUsage, but the
// session itself has nothing to write).
func (c *ConsultSession) Send(ctx context.Context, msg string) error {
	sess, err := c.ensureBackend(ctx)
	if err != nil {
		return err
	}
	a := sess.agent()
	if a == nil {
		return errors.New("consult session has no live agent")
	}
	sess.appendUser(msg)
	sess.setBusy(true)
	c.armIdleTimer()
	c.engine.send(Event{Feature: c.id, Kind: EventUpdated})
	if err := a.Send(ctx, msg); err != nil {
		sess.setError(err)
		c.engine.send(Event{Feature: c.id, Kind: EventUpdated})
		return err
	}
	return nil
}

// Snapshot returns a render-safe copy of the consult session's current
// backend state (an empty Snapshot if none has ever spawned).
func (c *ConsultSession) Snapshot() Snapshot {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return Snapshot{}
	}
	return sess.Snapshot()
}

// Close permanently ends the card's consult session: stops its current
// backend, cancels its idle timer, and clears the engine's reference so
// a later OpenConsult for this card starts fresh rather than reusing a
// dead session (there is no resume across this — Out of scope's
// durability section — so a fresh OpenConsult after Close carries no
// seed at all, the same as a card asked about for the first time).
func (c *ConsultSession) Close() error {
	c.stopBackend()
	c.engine.mu.Lock()
	if c.engine.consult[c.id] == c {
		delete(c.engine.consult, c.id)
	}
	c.engine.mu.Unlock()
	return nil
}

// stopBackend stops c's current backend and cancels its idle timer,
// without touching e.consult — Engine.Close's own counterpart to Close,
// used when the caller has already cleared the map itself (under e.mu,
// for every entry at once) and only needs each session's process work
// torn down.
func (c *ConsultSession) stopBackend() {
	c.idleMu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	c.idleMu.Unlock()
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess != nil {
		sess.stop()
	}
}

// armIdleTimer (re)starts c's idle-close timer, called on spawn and on
// every turn sent or reply landing — the "resets on every reply" half of
// the Implementation notes' lifecycle. A non-positive
// Engine.consultIdleTimeout disables it (defensive; New() always seeds a
// positive value).
func (c *ConsultSession) armIdleTimer() {
	d := c.engine.consultIdleTimeout
	if d <= 0 {
		return
	}
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(d, c.onIdleTimeout)
}

// onIdleTimeout fires 20 minutes after the last turn: it closes only the
// current backend, marking it StateDone first so Session.Live() reads
// false afterward (mirroring how Pause's own setState-before-stop makes
// a stale, non-nil agent handle read correctly as not-live) — this is
// what makes the next Send's ensureBackend respawn rather than trying to
// use a closed adapter. The transcript is untouched: sess.stop() never
// clears it, so the respawn's seed still carries the full history.
func (c *ConsultSession) onIdleTimeout() {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return
	}
	sess.setState(StateDone)
	sess.stop()
}

// pumpConsult relays one consult backend's agent events into
// handleConsult and exits when that backend stops or its event channel
// closes — pumpBoard's shape, parametrized by the specific *Session this
// goroutine was started for, since c.sess can move on to a respawned one
// while this pump is still draining the old backend's final events.
func (e *Engine) pumpConsult(c *ConsultSession, sess *Session) {
	events := sess.agent().Events()
	for {
		select {
		case <-sess.done:
			return
		case ev, ok := <-events:
			if !ok {
				if !sess.finalizedState() {
					sess.setError(errSessionDied)
					e.send(Event{Feature: c.id, Kind: EventError, Err: errSessionDied})
					sess.stop()
				}
				return
			}
			e.handleConsult(c, sess, ev)
		}
	}
}

// handleConsult folds one backend event into the consult session's
// transcript/spend/context — handleBoard's shape, minus the arms a
// consult session has no use for (no budget threshold, no exhaustion: a
// ConsultSession's SessionOpts.MaxCredits is always 0, so overBudget can
// never trip and this never calls crossedThreshold/queueNudge/exhaust —
// they simply have no call site here, which is the whole of consult's
// cap-exemption invariant).
func (e *Engine) handleConsult(c *ConsultSession, sess *Session, ev agent.Event) {
	switch ev.Kind {
	case agent.EventTextDelta:
		sess.appendDelta(ev.Text)
	case agent.EventMessage:
		sess.finishAssistant(ev.Text)
	case agent.EventToolCall:
		sess.appendToolCall(ev.CallID, toolLine(ev))
	case agent.EventToolResult:
		if ev.Result != nil {
			sess.resolveToolResult(ev.CallID, ev.Result.OK, ev.Result.Output)
		}
	case agent.EventClientToolCall:
		e.dispatchConsultClientTool(c, sess, ev.ToolCall)
		return
	case agent.EventContext:
		sess.setContext(ev.Context)
	case agent.EventUsage:
		sess.addSpend(ev.Usage)
		// consult spend always reaches Store.AddSpend (via the shared
		// recordUsage helper both this and the stage pump call), but
		// never crossedThreshold/queueNudge/exhaust — see this method's
		// own doc comment.
		e.recordUsage(sess, c.id, c.stage, agent.RoleConsult, ev.Usage)
	case agent.EventIdle:
		sess.setBusy(false)
		c.armIdleTimer() // a reply landing resets the idle clock
	case agent.EventError:
		sess.setError(ev.Err)
	case agent.EventBudgetExhausted:
		// Not gummi's envelope — a consult session has none — but the
		// backend's own cap (see handleBoard's identical case for the
		// full reasoning: this is copilot's SessionLimitsExhausted,
		// reachable here the same way it is for a board session).
		sess.appendSystem("the backend reported its credit cap was reached — " +
			"this conversation cannot continue until the cap is raised on its side")
		sess.setBusy(false)
	default:
		return
	}
	e.send(Event{Feature: c.id, Kind: EventUpdated})
}

// dispatchConsultClientTool answers a consult session's client-tool call
// (the copilot-style path, for a ClientTools backend) by routing it
// through dispatchConsultTool, run on its own goroutine — card_diff
// shells out to git, and running it inline on the pump goroutine would
// stall every other event behind it (dispatchBoardClientTool's own
// comment has the full reasoning; it applies unchanged here).
func (e *Engine) dispatchConsultClientTool(c *ConsultSession, sess *Session, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	sess.appendToolCall(tc.ID, tc.Name)
	e.send(Event{Feature: c.id, Kind: EventUpdated})
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		result, err := e.dispatchConsultTool(sess.ctx, c.id, tc.Name, tc.Args)
		ok := err == nil
		if !ok {
			result = err.Error()
		}
		sess.resolveToolResult(tc.ID, ok, result)
		if r, isResolver := sess.agent().(agent.ToolResolver); isResolver {
			_ = r.Resolve(context.Background(), tc.ID, result)
		}
		e.send(Event{Feature: c.id, Kind: EventUpdated})
	}()
}
