package agent

import (
	"context"
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

// Capabilities implements Agent. The Copilot SDK provides all four.
func (c *Copilot) Capabilities() Capabilities {
	return Capabilities{BYOK: true, Resume: true, UsageEvents: true, Interrupt: true}
}

// NewSession implements Agent.
func (c *Copilot) NewSession(ctx context.Context, opts SessionOpts) (Session, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("copilot agent is closed")
	}
	c.mu.Unlock()

	cfg := &copilot.SessionConfig{
		Model:            opts.Model,
		WorkingDirectory: opts.WorkDir,
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

	sess, err := c.client.CreateSession(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating copilot session: %w", err)
	}

	cs := &copilotSession{
		sdk:    sess,
		raw:    make(chan Event, 256),
		events: make(chan Event),
		stop:   make(chan struct{}),
	}
	go cs.forward()
	cs.unsub = sess.On(cs.onEvent)

	c.mu.Lock()
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

	mu     sync.Mutex
	closed bool
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
	case *copilot.AssistantMessageData:
		// Usage is metered from AssistantUsageData (the authoritative
		// per-call event) only; emitting it here too would double-count
		// whenever the CLI sends both. Providers that report tokens only
		// on the message and never send a usage event will undercount —
		// dedup by APICallID is an M3 (cost) refinement.
		s.emit(Event{Kind: EventMessage, Text: d.Content})
		return
	case *copilot.AssistantUsageData:
		u := Usage{Model: d.Model}
		if d.Cost != nil {
			u.Credits = *d.Cost
		}
		if d.OutputTokens != nil {
			u.OutputTokens = *d.OutputTokens
		}
		if d.InputTokens != nil {
			u.InputTokens = *d.InputTokens
		}
		out = Event{Kind: EventUsage, Usage: u}
	case *copilot.SessionUsageInfoData:
		// the SDK's live context-window occupancy: current tokens vs the
		// model's limit.
		out = Event{Kind: EventContext, Context: Context{Tokens: d.CurrentTokens, Limit: d.TokenLimit}}
	case *copilot.SessionIdleData:
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
