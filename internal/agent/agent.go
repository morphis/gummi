// Package agent is gummi's adapter layer over concrete coding agents.
// The interfaces (DESIGN §4.1) hide whether a session is backed by the
// GitHub Copilot SDK, a future opencode server, or the in-process fake
// used by tests. gummi's orchestrator speaks only this vocabulary.
package agent

import (
	"context"
	"encoding/json"
)

// Role is a named capability slot a profile maps to a concrete model.
type Role string

const (
	RoleArchitect   Role = "architect"
	RoleImplementer Role = "implementer"
	RoleReviewer    Role = "reviewer"
	RoleScribe      Role = "scribe"
	// RoleHelper attributes a backend's internal side-model spend (a
	// title/summary call the CLI makes on its own model) in the per-stage
	// breakdown, keeping it out of the working role's row.
	RoleHelper Role = "helper"
)

// Permission is the policy a session applies to tool calls. gummi's
// default is allow-all under the sandbox assumption (DESIGN §4.4).
type Permission string

const (
	// PermissionAllowAll auto-approves every tool call.
	PermissionAllowAll Permission = "allow-all"
	// PermissionGuarded surfaces each tool call for approval via the
	// needs-attention queue.
	PermissionGuarded Permission = "guarded"
)

// ToolDef declares a gummi-owned client tool exposed to the agent's
// model. The adapter surfaces each invocation as EventClientToolCall
// and blocks that call until the orchestrator answers via ToolResolver
// — the mechanism behind ask_user (a blocked call costs no tokens).
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is the tool's JSON Schema (a plain map, adapter-agnostic).
	Parameters map[string]any `json:"parameters,omitempty"`
}

// ToolCall is one in-flight client-tool invocation awaiting Resolve.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResolver is implemented by sessions that support client tools:
// Resolve completes the call surfaced as EventClientToolCall, feeding
// result back to the model as the tool's output.
type ToolResolver interface {
	Resolve(ctx context.Context, callID, result string) error
}

// Identified is implemented by sessions whose backend assigns a durable
// session id — e.g. the Copilot CLI, which keeps a full event log under
// ~/.copilot/session-state/<id>/. The orchestrator surfaces the id so
// the user can find that log after the session is gone.
type Identified interface {
	SessionID() string
}

// SessionOpts configures one agent session.
type SessionOpts struct {
	// WorkDir is the feature's worktree; the agent's cwd.
	WorkDir string
	// Role labels the session for the orchestrator and logs.
	Role Role
	// Model is the concrete model id, e.g. "gpt-5", "claude-sonnet-4.5".
	// The adapter routes it through whatever native provider config it
	// owns (Copilot's session, Claude Code's env, opencode's auth, the
	// headless child's own config).
	Model string
	// SystemHints are stage instructions appended to the agent's
	// system prompt (spec path, dev-guide, budget notes).
	SystemHints []string
	// Permission is the tool-call policy.
	Permission Permission
	// MaxCredits caps session spend (Copilot credits, 1 = $0.01); 0
	// means uncapped. Enforced by the adapter as a backstop.
	MaxCredits float64
	// Tools are gummi-owned client tools (ignored by adapters without
	// the ClientTools capability; the orchestrator gates on it).
	Tools []ToolDef
}

// Agent creates sessions and reports what its backend can do.
type Agent interface {
	// Name identifies the backend for status displays ("copilot",
	// "opencode", the headless command's basename).
	Name() string
	// NewSession starts a session in opts.WorkDir with a role config.
	NewSession(ctx context.Context, opts SessionOpts) (Session, error)
	// Capabilities reports optional features (resume, usage events, …)
	// so callers can degrade gracefully.
	Capabilities() Capabilities
	// CreditRate returns this adapter's token→credit rate for model, in
	// credits per 1k tokens. Zero means "engine, use your default / trust
	// this adapter's native metering": Copilot self-reports credits, so it
	// returns 0; the Claude Code CLI reports USD directly, so it too returns
	// 0. Adapters that route to a rate-less endpoint (headless pointed at a
	// local llama.cpp) return the operator-configured rate so budget math
	// prices non-native traffic correctly against the same credit envelope.
	CreditRate(model string) float64
	// Close releases any backend process the agent owns.
	Close() error
}

// Capabilities advertises optional adapter features.
type Capabilities struct {
	Resume      bool // session resume across restarts
	UsageEvents bool // per-turn usage/credit events
	Interrupt   bool // mid-turn interruption
	// ClientTools reports native support for SessionOpts.Tools: the
	// session surfaces invocations as EventClientToolCall and implements
	// ToolResolver. Adapters without it fall back to a prompt convention.
	ClientTools bool
}

// Session is one live agent conversation bound to a feature + stage.
type Session interface {
	// Send delivers a user/orchestrator turn. It returns when the turn
	// has been accepted, not when the agent is done; watch Events for
	// completion.
	Send(ctx context.Context, msg string) error
	// Events streams the agent's activity. The channel closes when the
	// session closes.
	Events() <-chan Event
	// Interrupt aborts the in-flight turn.
	Interrupt(ctx context.Context) error
	// Close ends the session and releases its resources.
	Close() error
}

// EventKind classifies a streamed Event.
type EventKind string

const (
	// EventTextDelta is an incremental chunk of assistant text.
	EventTextDelta EventKind = "text-delta"
	// EventReasoningDelta is an incremental chunk of the assistant's
	// reasoning ("thinking"). Display-only live progress: it is never
	// part of the reply text, so consumers that accumulate a reply must
	// not collect it.
	EventReasoningDelta EventKind = "reasoning-delta"
	// EventMessage is a complete assistant message.
	EventMessage EventKind = "message"
	// EventToolCall reports a tool invocation (name in Tool).
	EventToolCall EventKind = "tool-call"
	// EventToolResult reports a tool invocation finishing (Result
	// populated), correlated to its EventToolCall by CallID. Only some
	// backends emit it; without one a tool call's outcome stays unknown.
	EventToolResult EventKind = "tool-result"
	// EventClientToolCall reports the model invoking a gummi-owned client
	// tool (ToolCall populated). The call blocks until the orchestrator
	// answers via ToolResolver.Resolve.
	EventClientToolCall EventKind = "client-tool-call"
	// EventPermission reports a pending tool-call approval (guarded
	// mode); the orchestrator answers via the queue.
	EventPermission EventKind = "permission"
	// EventUsage carries per-turn spend (Usage populated).
	EventUsage EventKind = "usage"
	// EventContext reports the conversation's context-window usage
	// (Context populated): current tokens vs the model's limit.
	EventContext EventKind = "context"
	// EventIdle marks the agent finished its turn and awaits input.
	EventIdle EventKind = "idle"
	// EventBudgetExhausted reports the session hit its credit cap; the
	// in-flight response completes (soft stop) but no more turns run.
	EventBudgetExhausted EventKind = "budget-exhausted"
	// EventError reports a session error (Err populated).
	EventError EventKind = "error"
)

// Usage is the spend for one model call. Credits meter native provider
// billing (Copilot's AI credits, Claude Code's USD-derived credits);
// Tokens meter adapters that surface token counts only. Either may be zero.
type Usage struct {
	Credits float64
	// InputTokens counts the uncached input side: fresh input plus any
	// prompt-cache writes. Cache reads live in CachedTokens — adapters
	// whose upstream reports a cache-inclusive input count must split it,
	// so every metering path shares one convention and cumulative-minus-
	// settled deltas never go negative.
	InputTokens  int64
	OutputTokens int64
	// CachedTokens is the count read from the prompt cache (a subset of
	// the input side, billed cheaper); metering-only, kept for the
	// per-stage breakdown. Reasoning tokens are folded into OutputTokens
	// (billed as output), so there is no separate reasoning field.
	CachedTokens int64
	Model        string
	// Estimate marks Credits as adapter-derived (tokens × a realized
	// rate) rather than provider-metered: a live mid-turn figure a later
	// Settled event reconciles. Displays label it, and the reconciliation
	// clears it — it never lingers as real cost.
	Estimate bool
	// Settled marks a provider-metered reconciliation for Model: Credits
	// is the (signed) correction that brings the model's cumulative spend
	// to the provider's actual figure, superseding every estimate emitted
	// for it so far. An event with Settled set never carries tokens.
	Settled bool
	// Metered marks Credits as the provider's metered figure for this
	// sample, authoritative even at zero: the engine records it as-is and
	// must not re-price the tokens or book the sample as an estimate.
	// Credits-metering sessions (hosted Copilot) set it on every sample;
	// rate-less backends leave it unset so the engine's token-priced
	// fallback still covers them.
	Metered bool
	// Helper marks spend on a model the backend used internally (title
	// generation, summarization) rather than the session's working model —
	// real cost, but not the role's stage work. Metering attributes it to a
	// helper slot so a token-less helper call doesn't inflate or
	// mis-attribute the stage role's own row.
	Helper bool
}

// Context is the conversation's context-window occupancy: Tokens
// currently in the window against the model's Limit (0 = unknown).
type Context struct {
	Tokens int64
	Limit  int64
}

// ToolResult is the outcome of one tool execution (EventToolResult).
// Output is bounded at the source (boundTail) so a chatty tool can't
// blow up transcripts or the state db; failures keep a longer tail than
// successes because they are the forensic case.
type ToolResult struct {
	OK     bool
	Output string // captured output (failure message first when it failed)
}

// Event is one item in a session's activity stream.
type Event struct {
	Kind     EventKind
	Text     string      // text for deltas/messages
	Tool     string      // tool name for tool-call/permission events
	Detail   string      // tool-call salient argument (command, path, …); may be empty
	CallID   string      // backend tool-call id, pairing tool-result to tool-call
	ToolCall *ToolCall   // populated for EventClientToolCall
	Result   *ToolResult // populated for EventToolResult
	Usage    Usage       // populated for EventUsage
	Context  Context     // populated for EventContext
	Err      error       // populated for EventError
}
