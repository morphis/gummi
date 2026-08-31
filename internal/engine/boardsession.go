package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/agent"
)

// boardPermission is the fixed tool-call policy every board session
// spawns with. It is never PermissionGuarded, and not merely as a matter
// of taste: claude and zz reject PermissionGuarded outright from
// NewSession (see claudecode.go, zz.go — TestClaudeCodeRejectsGuarded,
// TestZZRefusesGuarded), and copilot/opencode/codex accept it but nothing
// in this codebase ever emits agent.EventPermission (grep confirms the
// EventKind is declared and documented but never produced by any
// adapter) — so a guarded board session on one of those three would
// accept the option, then hang forever on its first tool call with no
// event to answer. Allow-all is what every stage session already runs
// under by default (DESIGN §4.4's sandbox assumption); a board session
// just never offers the alternative that doesn't work yet.
const boardPermission = agent.PermissionAllowAll

// BoardOpts configures a board session's spawn. There is no domain.Feature
// here to resolve a profile from the way a card's stage session does
// (newAgentSession), so Profile lets a caller still pick one; empty falls
// back to the workspace's default profile — resolveRole's own fallback,
// unchanged.
type BoardOpts struct {
	Profile string
}

// BoardSession is a workspace-scoped agent conversation: bound to the
// board rather than to any card, so it can act on every card through the
// same seven tools a hosted agent reaches over the workspace MCP socket
// (mcpworkspace.go) — but running in this process, folded into an
// engine.Session the same accumulator methods a card's chat uses, so a UI
// surface renders it with the same Snapshot machinery.
//
// It deliberately sits outside every card-scoped mechanism: no card lock
// (it drives no single card), no live-file binding (nothing to follow —
// a board conversation isn't a stage run another gummi process needs to
// watch), no checkpoint (there is no worktree to commit), no persisted
// store row, and no scheduler slot (the attended/autopilot lanes ration
// contention between CARDS; a board session competes with nothing there).
type BoardSession struct {
	engine *Engine
	sess   *Session
}

// OpenBoard starts (or reuses) the engine's board session. Only one
// exists at a time — reopening while one is already live returns it
// as-is, mirroring Attach's "a live agent session for this stage is
// reused" rule for card chats, so a second caller (a UI reattach after
// e.g. a reconnect) never spawns a second backend process behind the
// user's back.
func (e *Engine) OpenBoard(ctx context.Context, opts BoardOpts) (*BoardSession, error) {
	// Serialized end to end, so the nil-check below and the install at the
	// bottom are one atomic decision. Without this the reuse promise in
	// the doc comment above is only true for callers that never overlap:
	// two concurrent opens would both see no board, both spawn a backend,
	// and the loser's caller would receive a session that replaceBoard had
	// already stopped — a live-looking handle whose Send goes to a closed
	// process. See Engine.boardMu.
	e.boardMu.Lock()
	defer e.boardMu.Unlock()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("engine is closed")
	}
	if prior := e.board; prior != nil {
		e.mu.Unlock()
		return prior, nil
	}
	e.mu.Unlock()

	rc, backend := e.resolveBoardRole(opts.Profile)
	ag := e.agentFor(backend)
	if ag == nil {
		return nil, errors.New("no agent configured for the board role")
	}
	caps := ag.Capabilities()

	// gummi's board tools reach the model exactly one of two ways,
	// mirroring newAgentSession's stage-tool wiring: native client tools
	// (Tools, answered via the adapter's own ToolResolver — see
	// dispatchBoardClientTool) for a backend that supports them, or the
	// inbound workspace MCP endpoint for one that reaches gummi's tools
	// that way instead. A backend with neither capability gets no tools
	// at all: there is no prompt-convention fallback here the way
	// askConventionHint covers ask_user for card sessions (that trick is
	// specific to one tool's fenced-block convention, not a general
	// substitute for seven board verbs) — it can still converse, just
	// without acting on the board.
	var tools []agent.ToolDef
	var mcpPath string
	var mcpTeardown func()
	// Exclusive by construction, not by coincidence: no adapter advertises
	// both capabilities today (capabilities.go), but nothing in the type
	// system says one cannot, and wiring both would hand the model the
	// same seven tools twice — once natively and once through a spawned
	// `gummi __mcp --workspace` child. Native client tools win, being the
	// cheaper path (no child process, no socket).
	switch {
	case caps.ClientTools:
		tools = workspaceTools()
	case caps.MCPTools:
		path, teardown, err := e.StartWorkspaceMCPEndpoint()
		if err != nil {
			return nil, fmt.Errorf("starting board session's mcp endpoint: %w", err)
		}
		mcpPath = path
		mcpTeardown = teardown
	}

	// The session's lifecycle context is bound to nothing (mirrors
	// Attach): canceled by sess.stop(), not by the caller's ctx going
	// away, so the initial NewSession call stays on the caller's
	// cancellation semantics while everything after (client-tool
	// dispatches, in particular) survives that ctx going away and is
	// only ever torn down by this session's own stop.
	sctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		Role:        agent.RoleBoard,
		Interactive: true,
		state:       StateInteractive,
		done:        make(chan struct{}),
		ctx:         sctx,
		cancel:      cancel,
		startedAt:   time.Now(),
	}
	sess.setSpawnInfo(ag.Name(), rc.Model, caps.ClientTools)

	agentSess, err := ag.NewSession(ctx, agent.SessionOpts{
		// The workspace root, never a worktree: a board session belongs
		// to the whole backlog, not to any one card's checkout.
		WorkDir:        e.cfg.Workspace.Root,
		Role:           agent.RoleBoard,
		Model:          rc.Model,
		Permission:     boardPermission,
		Tools:          tools,
		OutputTokenMax: rc.OutputTokenMax,
		Provider:       rc.Provider,
		Think:          rc.Think,
		MCPSockPath:    mcpPath,
		// Workspace tells an MCP-reaching adapter's `gummi __mcp` child to
		// dial in --workspace mode rather than --feature <id> — there is
		// no feature id for a board session to hand it.
		Workspace: mcpPath != "",
		// No ArtifactPath, no FeatureID, no MaxCredits: a board session
		// has no spec to read, no card to attribute spend to, and no
		// budget to enforce.
	})
	if err != nil {
		if mcpTeardown != nil {
			mcpTeardown()
		}
		cancel()
		return nil, fmt.Errorf("starting board session: %w", err)
	}
	sess.setMCPTeardown(mcpTeardown)
	// s is not yet reachable by Close (not installed as e.board yet), so
	// attachAgent can't be racing a finalize here; the bool is checked for
	// symmetry with Attach/startAutonomous.
	if !sess.attachAgent(agentSess) {
		_ = agentSess.Close()
		return nil, errors.New("engine is closed")
	}
	// attachAgent always marks the session Running (it doesn't know which
	// caller it's serving); a board session is interactive like a card
	// chat, so restore that state the same way Attach does right after
	// its own attachAgent call.
	sess.setState(StateInteractive)

	b := &BoardSession{engine: e, sess: sess}
	if !e.replaceBoard(b) {
		sess.stop() // engine closed during startup: don't leave the agent live
		return nil, errors.New("engine is closed")
	}
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.pumpBoard(b) }()
	e.send(Event{Kind: EventBoard})
	return b, nil
}

// replaceBoard installs b as the engine's board session, stopping any
// prior one — the board-session counterpart to replace (card sessions).
// It reports false without installing when the engine has since closed,
// so a session created concurrently with Close isn't left live past
// shutdown; the caller then stops the orphan itself.
func (e *Engine) replaceBoard(b *BoardSession) bool {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	old := e.board
	e.board = b
	e.mu.Unlock()
	if old != nil {
		old.sess.stop()
	}
	return true
}

// Send delivers a user turn to the board session. It mirrors
// Engine.Send's card-chat path minus everything that doesn't apply here:
// no pending budget nudge (no budget), no persist (no store row backs a
// board session), no pre-turn tripwire snapshot (no worktree to dirty).
func (b *BoardSession) Send(ctx context.Context, msg string) error {
	a := b.sess.agent()
	if a == nil {
		return errors.New("board session has no live agent")
	}
	b.sess.appendUser(msg)
	b.sess.setBusy(true)
	b.engine.send(Event{Kind: EventBoard})
	if err := a.Send(ctx, msg); err != nil {
		b.sess.setError(err)
		b.engine.send(Event{Kind: EventBoard})
		return err
	}
	return nil
}

// Interrupt aborts the board session's in-flight turn.
func (b *BoardSession) Interrupt(ctx context.Context) error {
	if a := b.sess.agent(); a != nil {
		return a.Interrupt(ctx)
	}
	return nil
}

// Snapshot returns a render-safe copy of the board session's state.
func (b *BoardSession) Snapshot() Snapshot {
	return b.sess.Snapshot()
}

// Close ends the board session: stops the agent and tears down its MCP
// endpoint (if any), and clears the engine's reference so a later
// OpenBoard starts fresh rather than reusing a dead session. Idempotent:
// sess.stop's sync.Once absorbs a second call to this method (or a
// concurrent Engine.Close), and clearing e.board is a no-op once it no
// longer points at this session.
func (b *BoardSession) Close() error {
	b.sess.stop()
	b.engine.mu.Lock()
	if b.engine.board == b {
		b.engine.board = nil
	}
	b.engine.mu.Unlock()
	return nil
}

// pumpBoard relays the board session's agent events into handleBoard and
// exits when the session stops or its agent channel closes. It is the
// board counterpart to Engine.pump, with the same stop path (a select on
// sess.done) so Close can join it via e.wg without deadlocking.
func (e *Engine) pumpBoard(b *BoardSession) {
	events := b.sess.agent().Events()
	for {
		select {
		case <-b.sess.done:
			return
		case ev, ok := <-events:
			if !ok {
				// finalizedState is true here exactly when stop() already
				// ran (it sets s.finalized before doing anything else,
				// including closing the agent that closes this channel):
				// that means WE closed this session (BoardSession.Close,
				// Engine.Close, or replaceBoard's stop-the-old-one), so
				// the channel closing is expected, not a death. Skip the
				// error in that case — the alternative (checking
				// b.sess.done instead) can't distinguish the two: closing
				// the agent races closing done in the very same select,
				// so either branch can fire first regardless of which one
				// "caused" the shutdown.
				if !b.sess.finalizedState() {
					b.sess.setError(errSessionDied)
					e.send(Event{Kind: EventBoard})
					b.sess.stop() // finalize: closes done, releases the mcp endpoint
				}
				return
			}
			e.handleBoard(b, ev)
		}
	}
}

// handleBoard folds one backend event into the board session's
// transcript/spend/context and emits EventBoard so a UI surface knows to
// re-render from Snapshot. It answers to a fraction of Engine.handle's
// arms: a board session has no budget to enforce, no worktree to
// checkpoint or tripwire, no stage to advance, and no attention slot to
// free, so every card-scoped tail of Engine.handle (persist, exhaust,
// tripwire, stageReceipt, gate verdicts) simply has nothing to fold into
// here.
func (e *Engine) handleBoard(b *BoardSession, ev agent.Event) {
	switch ev.Kind {
	case agent.EventTextDelta:
		b.sess.appendDelta(ev.Text)
	case agent.EventMessage:
		b.sess.finishAssistant(ev.Text)
	case agent.EventToolCall:
		b.sess.appendToolCall(ev.CallID, toolLine(ev))
	case agent.EventToolResult:
		if ev.Result != nil {
			b.sess.resolveToolResult(ev.CallID, ev.Result.OK, ev.Result.Output)
		}
	case agent.EventClientToolCall:
		// Dispatched on its own goroutine (see dispatchBoardClientTool's
		// comment) and emits its own EventBoard once it resolves —
		// nothing has changed on this session synchronously, so return
		// before the shared emit below rather than sending a no-op event.
		e.dispatchBoardClientTool(b, ev.ToolCall)
		return
	case agent.EventContext:
		b.sess.setContext(ev.Context)
	case agent.EventUsage:
		// Accumulate into the snapshot for display only: no store write
		// (a board session belongs to no feature row to record spend
		// against), no budget threshold, no gummi-side exhaustion — a
		// board chat has no envelope to run out of.
		b.sess.addSpend(ev.Usage)
	case agent.EventIdle:
		b.sess.setBusy(false)
	case agent.EventError:
		b.sess.setError(ev.Err)
	case agent.EventBudgetExhausted:
		// Not gummi's envelope — a board session has none — but the
		// BACKEND's own cap, which is a different fact and a reachable
		// one: copilot emits this from SessionLimitsExhaustedRequestedData
		// when the CLI's session credit limit is reached, and copilot is a
		// board backend (it is the ClientTools one). A card session routes
		// this to e.exhaust, which checkpoints a worktree and raises a
		// top-up gate — neither of which exists here. What must not happen
		// is the silent drop this used to be: the conversation simply
		// stopped answering and nothing on screen said why. So say it, in
		// gummi's own voice, and clear busy so the surface stops showing a
		// turn that is never coming back.
		b.sess.appendSystem("the backend reported its credit cap was reached — " +
			"this conversation cannot continue until the cap is raised on its side")
		b.sess.setBusy(false)
	default:
		// Reasoning deltas and permission events (never offered — see
		// boardPermission) carry no state a board session tracks.
		return
	}
	e.send(Event{Kind: EventBoard})
}

// dispatchBoardClientTool answers a board session's client-tool call —
// the copilot-style path (SessionOpts.Tools), for a backend whose
// Capabilities().ClientTools is true — by routing it through
// dispatchBoardTool, the same board-level tool dispatcher the workspace
// MCP endpoint uses for a hosted agent dialing in over the socket
// (mcpworkspace.go). It runs on its own goroutine, tracked by e.wg like
// every other engine goroutine that can touch the filesystem: card_diff
// shells out to git, and running it on the pump goroutine would stall
// every other event (deltas, tool results, the next tool call) behind it
// until the subprocess returns — DispatchClientTool's card-scoped
// counterpart gets away with running inline because its callers (the MCP
// bridge) are already off the pump goroutine themselves; this one isn't.
func (e *Engine) dispatchBoardClientTool(b *BoardSession, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	b.sess.appendToolCall(tc.ID, tc.Name)
	e.send(Event{Kind: EventBoard})
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		// b.sess.ctx, not context.Background(): canceled the instant the
		// board session stops, so a card_diff mid-shell-out is killed
		// rather than racing (or outliving) teardown.
		result, err := e.dispatchBoardTool(b.sess.ctx, tc.Name, tc.Args)
		ok := err == nil
		if !ok {
			result = err.Error()
		}
		b.sess.resolveToolResult(tc.ID, ok, result)
		if r, isResolver := b.sess.agent().(agent.ToolResolver); isResolver {
			_ = r.Resolve(context.Background(), tc.ID, result)
		}
		e.send(Event{Kind: EventBoard})
	}()
}
