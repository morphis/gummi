package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	copilot "github.com/github/copilot-sdk/go"
	copilotrpc "github.com/github/copilot-sdk/go/rpc"
)

// cliMinCredits is the CLI's minimum accepted session credit cap; below
// it the CLI rejects session.create, so gummi enforces the budget itself.
const cliMinCredits = 30

// settleTimeout bounds the usage.getMetrics RPC at idle; past it the
// turn settles from the message fallback instead of stalling the idle.
const settleTimeout = 10 * time.Second

// Copilot is an Agent backed by the GitHub Copilot CLI via the official
// Go SDK. It runs one CLI server process (the client) and opens one SDK
// session per gummi Session.
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
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true, ClientTools: true}
}

// CreditRate implements Agent: Copilot self-reports per-model AI-credit
// spend via the SDK's usage events, so the engine must not re-price its
// tokens. Zero here disables the token-priced fallback for hosted sessions.
func (c *Copilot) CreditRate(string) float64 { return 0 }

// NewSession implements Agent.
func (c *Copilot) NewSession(ctx context.Context, opts SessionOpts) (Session, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("copilot agent is closed")
	}
	c.mu.Unlock()

	// A ReadOnly research session runs in the main checkout with no
	// worktree. This backend has no structural read-only cage
	// (ReadOnlyEnforce is false), so refuse rather than silently run
	// read-write — the engine gate is the first line, this is the second
	// so a stray direct call cannot drop the deny.
	if opts.ReadOnly {
		return nil, errors.New("copilot backend cannot enforce a read-only research session; " +
			"point this role at `claude` or `opencode`, or accept that autonomous research cannot run on copilot")
	}

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
	// credits); below that floor, the orchestrator enforces the budget
	// itself. So only pass the CLI cap when it clears the floor — it's an
	// extra backstop.
	if opts.MaxCredits >= cliMinCredits {
		credits := opts.MaxCredits
		cfg.SessionLimits = &copilot.SessionLimitsConfig{MaxAiCredits: &credits}
	}

	cs := &copilotSession{
		workdir: opts.WorkDir,
		// hosted sessions are credits-metered by the CLI; their usage
		// samples are authoritative as-is.
		metered:      true,
		raw:          make(chan Event, 256),
		events:       make(chan Event),
		stop:         make(chan struct{}),
		pending:      map[string]chan string{},
		pendingUsage: map[string]Usage{},
		meteredCalls: map[string]struct{}{},
		settled:      map[string]Usage{},
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
	cs.getMetrics = sess.RPC.Usage.GetMetrics
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
	sdk     *copilot.Session
	workdir string // opts.WorkDir, for repo-relative tool-call details
	metered bool   // hosted (credits-metered) session; stamped on usage events
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

	// Fallback metering for calls the CLI doesn't cover with an
	// AssistantUsageData event (streamed calls, depending on CLI version):
	// completed messages stash their per-call token counts here, the
	// authoritative usage event (when it does come) claims its call, and
	// the idle settle flushes what remains if the metrics RPC fails.
	pendingUsage map[string]Usage    // apiCallID → usage gleaned from messages
	meteredCalls map[string]struct{} // apiCallIDs already covered by AssistantUsageData

	// settled is the per-model usage already emitted to the engine — by
	// prior idle settles, AssistantUsageData events, or fallback flushes.
	// The idle settle emits the CLI's cumulative session.usage.getMetrics
	// figures minus this, so every path stays additive and none double-
	// counts another; a fallback estimate is corrected by the next
	// successful settle rather than standing forever.
	settled map[string]Usage // model → cumulative usage emitted

	// getMetrics fetches the CLI's cumulative usage (the sdk RPC in
	// production; a stub in tests). nil settles from the fallback stash.
	getMetrics func(context.Context) (*copilotrpc.UsageGetMetricsResult, error)
}

// addSettled folds one emitted usage sample into the per-model settled
// total. Caller holds s.mu.
func (s *copilotSession) addSettled(u Usage) {
	if s.settled == nil {
		s.settled = map[string]Usage{}
	}
	t := s.settled[u.Model]
	t.Model = u.Model
	t.Credits += u.Credits
	t.InputTokens += u.InputTokens
	t.CachedTokens += u.CachedTokens
	t.OutputTokens += u.OutputTokens
	s.settled[u.Model] = t
}

// cumulativeUsage flattens one model's getMetrics entry into gummi's
// usage vocabulary: credits from nano-AIU (1e9 nano-AIU = 1 AI credit,
// the CLI's billing unit), and tokens in Usage's convention — cache
// reads split out of the input side, cache writes kept in it (the SDK's
// inputTokens aggregates fresh + cache reads + cache writes; tokenDetails
// carries the full split). Reasoning tokens are already inside
// outputTokens — verified against GitHub's published per-token rates —
// so they are not re-added.
func cumulativeUsage(model string, m copilotrpc.UsageMetricsModelMetric) Usage {
	u := Usage{Model: model}
	if m.TotalNanoAiu != nil {
		u.Credits = *m.TotalNanoAiu / 1e9
	}
	if td, ok := m.TokenDetails["input"]; ok {
		u.InputTokens = td.TokenCount + m.TokenDetails["cache_write"].TokenCount
		u.CachedTokens = m.TokenDetails["cache_read"].TokenCount
		u.OutputTokens = m.TokenDetails["output"].TokenCount
		return u
	}
	u.InputTokens = m.Usage.InputTokens - m.Usage.CacheReadTokens
	u.CachedTokens = m.Usage.CacheReadTokens
	u.OutputTokens = m.Usage.OutputTokens
	return u
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

// settleIdle meters the finished turn and then reports idle. Primary
// source is the CLI's cumulative session.usage.getMetrics RPC — the
// authoritative per-model AI-credit (nano-AIU) and token accounting the
// CLI keeps but doesn't reliably push as assistant.usage events to SDK
// sessions. Each model emits its cumulative figure minus what was
// already settled, so credits arrive provider-metered instead of the
// engine's tokens×rate estimate. When the RPC fails (older CLI, timeout)
// the turn falls back to the stashed per-message output-token counts, and
// the next successful settle corrects the difference.
func (s *copilotSession) settleIdle() {
	var res *copilotrpc.UsageGetMetricsResult
	var err error
	if s.getMetrics != nil {
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		defer cancel()
		res, err = s.getMetrics(ctx)
	}
	var evs []Usage
	if err == nil && res != nil && len(res.ModelMetrics) > 0 {
		models := make([]string, 0, len(res.ModelMetrics))
		for m := range res.ModelMetrics {
			models = append(models, m)
		}
		sort.Strings(models) // deterministic emission order
		s.mu.Lock()
		if s.settled == nil {
			s.settled = map[string]Usage{}
		}
		// the authoritative figures supersede this turn's fallback stash
		s.pendingUsage = map[string]Usage{}
		s.meteredCalls = map[string]struct{}{}
		for _, model := range models {
			cum := cumulativeUsage(model, res.ModelMetrics[model])
			prev := s.settled[model]
			d := Usage{
				Model:        model,
				Credits:      cum.Credits - prev.Credits,
				InputTokens:  cum.InputTokens - prev.InputTokens,
				CachedTokens: cum.CachedTokens - prev.CachedTokens,
				OutputTokens: cum.OutputTokens - prev.OutputTokens,
			}
			s.settled[model] = cum
			if d.Credits != 0 || d.InputTokens != 0 || d.CachedTokens != 0 || d.OutputTokens != 0 {
				evs = append(evs, d)
			}
		}
		s.mu.Unlock()
	} else {
		evs = s.takePendingUsage()
		s.mu.Lock()
		for _, u := range evs {
			s.addSettled(u)
		}
		s.mu.Unlock()
	}
	for _, u := range evs {
		// hosted samples are authoritative as-is — even a zero-credit
		// delta must not be re-priced from tokens downstream.
		u.Metered = s.metered
		s.emit(Event{Kind: EventUsage, Usage: u})
	}
	s.emit(Event{Kind: EventIdle})
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

// SessionID implements Identified: the CLI's session id, under which it
// keeps the full event log (~/.copilot/session-state/<id>/events.jsonl).
func (s *copilotSession) SessionID() string { return s.sdk.SessionID }

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
		args, _ := d.Arguments.(map[string]any)
		out = Event{Kind: EventToolCall, Tool: d.ToolName, Detail: toolDetail(s.workdir, args), CallID: d.ToolCallID}
	case *copilot.ToolExecutionCompleteData:
		out = Event{Kind: EventToolResult, CallID: d.ToolCallID, Result: toolResult(d)}
	case *copilot.AssistantMessageData:
		// Usage is metered from AssistantUsageData (the authoritative
		// per-call event) when the CLI sends one; not every call gets one
		// (streamed calls, depending on CLI version), so the message's own
		// token count is stashed and flushed at idle for calls still
		// missing theirs — deduped by APICallID so both paths never
		// double-count.
		s.emit(Event{Kind: EventMessage, Text: d.Content})
		s.stashMessageUsage(d)
		return
	case *copilot.AssistantUsageData:
		u := Usage{Model: d.Model, Metered: s.metered}
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
		// The event's inputTokens aggregates cache reads; split them out to
		// match cumulativeUsage's convention — the two feed one settled
		// ledger, and mixing conventions there sent the idle settle's
		// cumulative-minus-settled delta hugely negative on cache-heavy
		// sessions. Clamp in case a CLI version reports fresh-only input.
		if d.CacheReadTokens != nil {
			u.CachedTokens = *d.CacheReadTokens
			u.InputTokens -= *d.CacheReadTokens
			if u.InputTokens < 0 {
				u.InputTokens = 0
			}
		}
		s.markUsageMetered(d.APICallID)
		// count this emission toward the model's settled total so the
		// idle settle's cumulative-minus-settled delta excludes it.
		s.mu.Lock()
		s.addSettled(u)
		s.mu.Unlock()
		out = Event{Kind: EventUsage, Usage: u}
	case *copilot.SessionUsageInfoData:
		// the SDK's live context-window occupancy: current tokens vs the
		// model's limit.
		out = Event{Kind: EventContext, Context: Context{Tokens: d.CurrentTokens, Limit: d.TokenLimit}}
	case *copilot.SessionIdleData:
		// Settle the turn's spend before reporting idle (the engine
		// persists at idle). The settle's RPC round-trip runs off the
		// SDK's event-dispatch goroutine — blocking dispatch on a
		// request/response cycle risks deadlock — so it owns emitting
		// the trailing idle after the usage events.
		go s.settleIdle()
		return
	case *copilot.SessionLimitsExhaustedRequestedData:
		out = Event{Kind: EventBudgetExhausted, Usage: Usage{Credits: d.UsedAiCredits}}
	case *copilot.SessionErrorData:
		out = Event{Kind: EventError, Err: fmt.Errorf("%s: %s", d.ErrorType, d.Message)}
	default:
		return
	}
	s.emit(out)
}

// toolResult converts an SDK completion into gummi's ToolResult: the
// failure message (if any) leads, followed by the tool's output —
// DetailedContent (the SDK's full for-display text) when present, else
// Content (the concise model-facing text) — tail-bounded at the source.
func toolResult(d *copilot.ToolExecutionCompleteData) *ToolResult {
	var parts []string
	if d.Error != nil && d.Error.Message != "" {
		parts = append(parts, d.Error.Message)
	}
	if d.Result != nil {
		text := d.Result.Content
		if d.Result.DetailedContent != nil && *d.Result.DetailedContent != "" {
			text = *d.Result.DetailedContent
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return &ToolResult{OK: d.Success, Output: boundTail(strings.Join(parts, "\n"), d.Success)}
}

// Output caps for captured tool results: failures keep a longer tail
// (they are what post-hoc debugging needs), successes a short one.
const (
	toolOutputCapOK   = 4 << 10
	toolOutputCapFail = 16 << 10
)

// boundTail truncates output to its status's cap, keeping the tail —
// errors and final verdicts land at the end of a command's output.
func boundTail(s string, ok bool) string {
	limit := toolOutputCapFail
	if ok {
		limit = toolOutputCapOK
	}
	if len(s) <= limit {
		return s
	}
	// cut on a rune boundary so the marker never splits a character
	cut := len(s) - limit
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…(truncated)\n" + s[cut:]
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
