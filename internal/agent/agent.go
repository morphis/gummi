// Package agent is gummi's adapter layer over concrete coding agents.
// The interfaces (DESIGN §4.1) hide whether a session is backed by the
// GitHub Copilot SDK, a future opencode server, or the in-process fake
// used by tests. gummi's orchestrator speaks only this vocabulary.
package agent

import "context"

// Role is a named capability slot a profile maps to a concrete model.
type Role string

const (
	RoleArchitect   Role = "architect"
	RoleImplementer Role = "implementer"
	RoleReviewer    Role = "reviewer"
	RoleScribe      Role = "scribe"
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

// Provider is an OpenAI-compatible BYOK endpoint (llama.cpp, vLLM,
// hosted). An empty Provider means the adapter's native routing
// (Copilot-hosted models).
type Provider struct {
	// Type is "openai", "azure", or "anthropic". Empty ⇒ "openai".
	Type string
	// BaseURL is the endpoint, e.g. http://127.0.0.1:8080/v1.
	BaseURL string
	// APIKeyEnv names the environment variable holding the key; the
	// adapter reads it at session start. The key itself is never
	// stored on SessionOpts, so it can't leak into state or logs.
	APIKeyEnv string
}

// SessionOpts configures one agent session.
type SessionOpts struct {
	// WorkDir is the feature's worktree; the agent's cwd.
	WorkDir string
	// Role labels the session for the orchestrator and logs.
	Role Role
	// Model is the concrete model id, e.g. "gpt-5", "claude-sonnet-4.5",
	// or a BYOK model name. Required when Provider is set.
	Model string
	// SystemHints are stage instructions appended to the agent's
	// system prompt (spec path, dev-guide, budget notes).
	SystemHints []string
	// Provider selects a BYOK endpoint; empty means native routing.
	Provider Provider
	// Permission is the tool-call policy.
	Permission Permission
	// MaxCredits caps session spend (Copilot credits, 1 = $0.01); 0
	// means uncapped. Enforced by the adapter as a backstop.
	MaxCredits float64
}

// Agent creates sessions and reports what its backend can do.
type Agent interface {
	// NewSession starts a session in opts.WorkDir with a role config.
	NewSession(ctx context.Context, opts SessionOpts) (Session, error)
	// Capabilities reports optional features (BYOK, resume, usage
	// events) so callers can degrade gracefully.
	Capabilities() Capabilities
	// Close releases any backend process the agent owns.
	Close() error
}

// Capabilities advertises optional adapter features.
type Capabilities struct {
	BYOK        bool // per-session OpenAI-compatible providers
	Resume      bool // session resume across restarts
	UsageEvents bool // per-turn usage/credit events
	Interrupt   bool // mid-turn interruption
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
	// EventMessage is a complete assistant message.
	EventMessage EventKind = "message"
	// EventToolCall reports a tool invocation (name in Tool).
	EventToolCall EventKind = "tool-call"
	// EventPermission reports a pending tool-call approval (guarded
	// mode); the orchestrator answers via the queue.
	EventPermission EventKind = "permission"
	// EventUsage carries per-turn spend (Usage populated).
	EventUsage EventKind = "usage"
	// EventIdle marks the agent finished its turn and awaits input.
	EventIdle EventKind = "idle"
	// EventError reports a session error (Err populated).
	EventError EventKind = "error"
)

// Usage is the spend for one model call. Credits meter Copilot-hosted
// usage; Tokens meter BYOK. Either may be zero.
type Usage struct {
	Credits      float64
	InputTokens  int64
	OutputTokens int64
	Model        string
}

// Event is one item in a session's activity stream.
type Event struct {
	Kind  EventKind
	Text  string // text for deltas/messages
	Tool  string // tool name for tool-call/permission events
	Usage Usage  // populated for EventUsage
	Err   error  // populated for EventError
}
