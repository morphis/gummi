package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
)

// cliMinCredits is the CLI's minimum accepted session credit cap; below
// it the CLI rejects session.create, so gummi enforces the budget itself.
const cliMinCredits = 30

// Copilot is an Agent backed by the GitHub Copilot CLI via the official
// Go SDK. It runs one CLI server process (the client) and opens one SDK
// session per gummi Session. BYOK providers are passed per session, so
// per-role model routing needs no extra processes.
type Copilot struct {
	client   *copilot.Client
	mu       sync.Mutex
	closed   bool
	sessions []*copilotSession
}

// CopilotOptions configures the backing CLI process.
type CopilotOptions struct {
	// CLIPath overrides the copilot binary location (else PATH or the
	// COPILOT_CLI_PATH env var the SDK honors).
	CLIPath string
	// LogLevel is the CLI log level ("none".."debug"); empty ⇒ default.
	LogLevel string
	// Env is the environment for the CLI process. Empty inherits the
	// parent. gummi passes a scrubbed allowlist here for child-process
	// hygiene (threat list).
	Env []string
}

// NewCopilot starts the CLI server and returns a ready Agent. The
// caller must Close it to stop the process.
func NewCopilot(ctx context.Context, opts CopilotOptions) (*Copilot, error) {
	var conn copilot.RuntimeConnection
	if opts.CLIPath != "" {
		conn = copilot.StdioConnection{Path: opts.CLIPath}
	}
	client := copilot.NewClient(&copilot.ClientOptions{
		Connection: conn,
		LogLevel:   opts.LogLevel,
		Env:        opts.Env,
	})
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting copilot CLI: %w", err)
	}
	return &Copilot{client: client}, nil
}

// Name implements Agent.
func (c *Copilot) Name() string { return "copilot" }

// Capabilities implements Agent. The Copilot SDK provides all of these.
func (c *Copilot) Capabilities() Capabilities {
	return Capabilities{BYOK: true, Resume: true, UsageEvents: true, Interrupt: true, ClientTools: true}
}

// NewSession implements Agent.
func (c *Copilot) NewSession(ctx context.Context, opts SessionOpts) (Session, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("copilot agent is closed")
	}
	c.mu.Unlock()

	// Deltas are opt-in: with Streaming unset the runtime defaults to
	// non-streaming and assistant.message_delta / assistant.reasoning_delta
	// events never arrive — sessions would look frozen until the first
	// complete message.
	streaming := true
	cfg := &copilot.SessionConfig{
		Model:            opts.Model,
		WorkingDirectory: opts.WorkDir,
		Streaming:        &streaming,
	}
	if len(opts.SystemHints) > 0 {
		cfg.SystemMessage = &copilot.SystemMessageConfig{
			Content: strings.Join(opts.SystemHints, "\n\n"),
		}
	}
	// Allow-all is gummi's default (sandbox assumption). In guarded
	// mode we leave OnPermissionRequest nil so requests surface as
	// events for the needs-attention queue.
	if opts.Permission != PermissionGuarded {
		cfg.OnPermissionRequest = copilot.PermissionHandler.ApproveAll
	}
	// The CLI enforces a floor on its own session cap (currently 30
	// credits) and only meters GitHub-hosted usage; below the floor, or
	// for BYOK, the orchestrator enforces the budget itself. So only
	// pass the CLI cap when it clears the floor — it's an extra backstop.
	if opts.MaxCredits >= cliMinCredits {
		credits := opts.MaxCredits
		cfg.SessionLimits = &copilot.SessionLimitsConfig{MaxAiCredits: &credits}
	}
	if opts.Provider.BaseURL != "" {
		if opts.Model == "" {
			return nil, errors.New("model is required when a BYOK provider is set")
		}
		p := &copilot.ProviderConfig{
			Type:    opts.Provider.Type,
			BaseURL: opts.Provider.BaseURL,
		}
		// The key is read from the environment at session start and
		// never persisted on gummi's own structs (threat list).
		if opts.Provider.APIKeyEnv != "" {
			p.APIKey = os.Getenv(opts.Provider.APIKeyEnv)
		}
		cfg.Provider = p
	}

	cs := &copilotSession{
		raw:          make(chan Event, 256),
		events:       make(chan Event),
		stop:         make(chan struct{}),
		pending:      map[string]chan string{},
		pendingUsage: map[string]Usage{},
		meteredCalls: map[string]struct{}{},
	}
	// Register gummi's client tools with a handler that surfaces the call
	// as an event and blocks until the orchestrator resolves it. The
	// handler runs on the SDK's goroutine; blocking it holds the model's
	// turn open (what ask_user needs) without spending tokens.
	for _, td := range opts.Tools {
		cfg.Tools = append(cfg.Tools, copilot.Tool{
			Name:           td.Name,
			Description:    td.Description,
			Parameters:     td.Parameters,
			SkipPermission: true, // gummi asking gummi; no approval prompt
			Handler:        cs.toolHandler(td.Name),
		})
	}

	sess, err := c.client.CreateSession(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating copilot session: %w", err)
	}
	cs.sdk = sess
	go cs.forward()
	cs.unsub = sess.On(cs.onEvent)

	// Re-check closed under the lock: Close may have run during the
	// CreateSession RPC above, snapshotting c.sessions before this session
	// existed. Without the re-check its forward goroutine would leak and its
	// Events() channel would never close. (Headless does the same.)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = cs.Close()
		return nil, errors.New("copilot agent is closed")
	}
	c.sessions = append(c.sessions, cs)
	c.mu.Unlock()
	return cs, nil
}

// Close implements Agent. It closes every outstanding session (so their
// Events channels close and consumers unblock) before stopping the CLI.
func (c *Copilot) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()

	for _, s := range sessions {
		_ = s.Close()
	}
	return c.client.Stop()
}

type copilotSession struct {
	sdk *copilot.Session
	// raw carries events from the SDK's event goroutine to the
	// forwarder; events is the consumer-facing stream, owned solely by
	// the forwarder so it can close it exactly once.
	raw    chan Event
	events chan Event
	stop   chan struct{}
	unsub  func()

	mu      sync.Mutex
	closed  bool
	pending map[string]chan string // client-tool callID → answer channel

	// Fallback metering for streamed BYOK calls, which never get an
	// AssistantUsageData event (CLI gap): completed messages stash their
	// per-call token counts here, the authoritative usage event (when it
	// does come) claims its call, and idle flushes what remains.
	pendingUsage map[string]Usage    // apiCallID → usage gleaned from messages
	meteredCalls map[string]struct{} // apiCallIDs already covered by AssistantUsageData
}

// stashMessageUsage records a completed message's token count as the
// fallback usage for its API call, unless the authoritative usage event
// already covered that call. Multiple messages from one call carry the
// same per-response count, so the latest wins rather than summing.
func (s *copilotSession) stashMessageUsage(d *copilot.AssistantMessageData) {
	if d.OutputTokens == nil || d.APICallID == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meteredCalls[*d.APICallID]; ok {
		return
	}
	u := Usage{OutputTokens: *d.OutputTokens}
	if d.Model != nil {
		u.Model = *d.Model
	}
	s.pendingUsage[*d.APICallID] = u
}

// markUsageMetered notes that apiCallID got its authoritative usage
// event and discards any fallback stashed for it.
func (s *copilotSession) markUsageMetered(apiCallID *string) {
	if apiCallID == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meteredCalls[*apiCallID] = struct{}{}
	delete(s.pendingUsage, *apiCallID)
}

// takePendingUsage drains the fallback usage still unclaimed at idle.
// Both maps reset per turn — usage events always land within their turn,
// and per-call IDs never repeat, so carrying them over only leaks.
func (s *copilotSession) takePendingUsage() []Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Usage, 0, len(s.pendingUsage))
	for _, u := range s.pendingUsage {
		out = append(out, u)
	}
	s.pendingUsage = map[string]Usage{}
	s.meteredCalls = map[string]struct{}{}
	return out
}

// toolHandler builds an SDK handler for a client tool: it emits an
// EventClientToolCall and blocks until Resolve (or session teardown)
// delivers a result. Blocking here holds the model's turn open, which
// is exactly the ask_user semantics — the turn resumes with the answer.
func (s *copilotSession) toolHandler(name string) copilot.ToolHandler {
	return func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
		callID := inv.ToolCallID
		ans := make(chan string, 1)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return copilot.ToolResult{TextResultForLLM: "cancelled — proceed with your best judgment"}, nil
		}
		s.pending[callID] = ans
		s.mu.Unlock()

		args, _ := json.Marshal(inv.Arguments)
		s.emit(Event{Kind: EventClientToolCall, ToolCall: &ToolCall{ID: callID, Name: name, Args: args}})

		select {
		case result := <-ans:
			return copilot.ToolResult{TextResultForLLM: result}, nil
		case <-s.stop:
			return copilot.ToolResult{TextResultForLLM: "cancelled — proceed with your best judgment"}, nil
		}
	}
}

// Resolve completes a pending client-tool call with result, resuming
// the model's blocked turn. Unknown or already-resolved calls are a
// no-op (a late Resolve after teardown must not panic).
func (s *copilotSession) Resolve(_ context.Context, callID, result string) error {
	s.mu.Lock()
	ans, ok := s.pending[callID]
	delete(s.pending, callID)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending tool call %q", callID)
	}
	ans <- result
	return nil
}

func (s *copilotSession) Events() <-chan Event { return s.events }

// forward is the only writer to s.events: it relays raw events until
// the session stops, then closes the stream. Concentrating ownership in
// one goroutine removes any send-on-closed race.
func (s *copilotSession) forward() {
	defer close(s.events)
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.raw:
			select {
			case s.events <- e:
			case <-s.stop:
				return
			}
		}
	}
}

// onEvent translates SDK events into gummi's vocabulary and forwards
// them. Unmapped event types are dropped (the stream is advisory).
func (s *copilotSession) onEvent(ev copilot.SessionEvent) {
	var out Event
	switch d := ev.Data.(type) {
	case *copilot.AssistantMessageDeltaData:
		out = Event{Kind: EventTextDelta, Text: d.DeltaContent}
	case *copilot.AssistantReasoningDeltaData:
		out = Event{Kind: EventReasoningDelta, Text: d.DeltaContent}
	case *copilot.ToolExecutionStartData:
		out = Event{Kind: EventToolCall, Tool: d.ToolName}
	case *copilot.AssistantMessageData:
		// Usage is metered from AssistantUsageData (the authoritative
		// per-call event) when the CLI sends one; streamed BYOK calls
		// don't get one (CLI gap), so the message's own token count is
		// stashed and flushed at idle for calls still missing theirs —
		// deduped by APICallID so both paths never double-count.
		s.emit(Event{Kind: EventMessage, Text: d.Content})
		s.stashMessageUsage(d)
		return
	case *copilot.AssistantUsageData:
		u := Usage{Model: d.Model}
		// Cost precedence: the authoritative CAPI figure (nano-AIU → credits)
		// when present, else the Experimental Cost field, else 0 (a BYOK call
		// with neither — priced from tokens by the engine's credit-equivalent).
		switch {
		case d.CopilotUsage != nil && d.CopilotUsage.TotalNanoAiu > 0:
			u.Credits = d.CopilotUsage.TotalNanoAiu / 1e9
		case d.Cost != nil:
			u.Credits = *d.Cost
		}
		if d.OutputTokens != nil {
			u.OutputTokens = *d.OutputTokens
		}
		// Reasoning tokens are billed as output; fold them in so token
		// counts don't undercount on reasoning models.
		if d.ReasoningTokens != nil {
			u.OutputTokens += *d.ReasoningTokens
		}
		if d.InputTokens != nil {
			u.InputTokens = *d.InputTokens
		}
		if d.CacheReadTokens != nil {
			u.CachedTokens = *d.CacheReadTokens
		}
		s.markUsageMetered(d.APICallID)
		out = Event{Kind: EventUsage, Usage: u}
	case *copilot.SessionUsageInfoData:
		// the SDK's live context-window occupancy: current tokens vs the
		// model's limit.
		out = Event{Kind: EventContext, Context: Context{Tokens: d.CurrentTokens, Limit: d.TokenLimit}}
	case *copilot.SessionIdleData:
		// meter calls whose authoritative usage event never came before
		// reporting idle, so budget accounting sees the whole turn.
		for _, u := range s.takePendingUsage() {
			s.emit(Event{Kind: EventUsage, Usage: u})
		}
		out = Event{Kind: EventIdle}
	case *copilot.SessionLimitsExhaustedRequestedData:
		out = Event{Kind: EventBudgetExhausted, Usage: Usage{Credits: d.UsedAiCredits}}
	case *copilot.SessionErrorData:
		out = Event{Kind: EventError, Err: fmt.Errorf("%s: %s", d.ErrorType, d.Message)}
	default:
		return
	}
	s.emit(out)
}

// emit hands an event to the forwarder, applying backpressure
// (blocking on the 256-deep raw buffer) rather than dropping — terminal
// and accounting events (idle, usage, error) must not be lost. The SDK
// delivers events serially, so blocking here throttles the SDK, which
// is intended. A closed session unblocks via stop, so this never leaks.
func (s *copilotSession) emit(e Event) {
	select {
	case s.raw <- e:
	case <-s.stop:
	}
}

func (s *copilotSession) Send(ctx context.Context, msg string) error {
	_, err := s.sdk.Send(ctx, copilot.MessageOptions{Prompt: msg})
	if err != nil {
		return fmt.Errorf("sending to copilot: %w", err)
	}
	return nil
}

func (s *copilotSession) Interrupt(ctx context.Context) error {
	return s.sdk.Abort(ctx)
}

func (s *copilotSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// Stop new SDK events, signal the forwarder (which closes the
	// consumer channel) and unblocks any emit, then disconnect.
	if s.unsub != nil {
		s.unsub()
	}
	close(s.stop)
	return s.sdk.Disconnect()
}
