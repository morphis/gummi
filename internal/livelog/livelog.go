// Package livelog mirrors one card's live agent stream to an append-only
// NDJSON file so a *second* gummi process can watch it.
//
// The engine's own event stream (engine.Events) is in-process only: the
// backend CLI is a child of whichever gummi spawned it, and the transcript
// lives in that process's memory. A watcher — a second board, `gummi
// watch`, an orchestrating agent — has no handle on any of it. The state
// store is no substitute: it takes a whole-transcript snapshot once per
// turn, so it can say what happened but never what is happening.
//
// This package is the missing publish side. A Writer appends one JSON
// object per transcript event to the card's live file; Follow tails that
// file from another process. The file is a *view*, not a log of record:
// each new session truncates it, the store stays authoritative, and a
// write that fails is dropped rather than allowed to disturb the run.
package livelog

import "time"

// Kind classifies a Record. The set mirrors the transcript mutations a
// session performs, so a follower can rebuild the same view the owning
// process renders.
type Kind string

const (
	// KindSession heads a file: a new session took the card over. It
	// carries the stage/role/backend identity the header line renders.
	KindSession Kind = "session"
	// KindUser is a message the user sent the agent.
	KindUser Kind = "user"
	// KindSystem is a gummi-authored transcript note (kickoffs, notices).
	KindSystem Kind = "system"
	// KindDelta is a chunk of streaming assistant text. Deltas are
	// coalesced by the Writer, so one record is many backend chunks.
	KindDelta Kind = "delta"
	// KindMessage finalizes the streaming assistant message with the
	// authoritative full text (Text may repeat what deltas already
	// carried — it supersedes them, never appends).
	KindMessage Kind = "message"
	// KindEdit rewrites the last assistant message's content in place —
	// the convention-path ask, where gummi strips a gummi-ask block out
	// of the prose the model already streamed.
	KindEdit Kind = "edit"
	// KindTool is a tool-call line, with Call set when an outcome is
	// still to come. gummi's own notes on the activity ticker (budget
	// nudges, checkpoint lines) are KindTool too — the transcript makes
	// no distinction either, they are AuthorTool entries with no outcome.
	KindTool Kind = "tool"
	// KindResult attaches a tool call's outcome to its KindTool record.
	KindResult Kind = "result"
	// KindState reports the session's state (queued/running/paused/…).
	KindState Kind = "state"
	// KindBusy reports whether the agent is mid-turn, so a follower can
	// show the same thinking marker the owning board does.
	KindBusy Kind = "busy"
	// KindSpend reports the session's running spend total.
	KindSpend Kind = "spend"
	// KindAsk reports the agent's open ask_user question. A watcher can
	// see it but never answer it: only the owning process holds the
	// resolver channel.
	KindAsk Kind = "ask"
	// KindStopped ends a session's stream. The file stays on disk so a
	// watcher that arrives late still sees how the stage ended.
	KindStopped Kind = "stopped"
	// KindDropped reports Count records lost because the writer's queue
	// was full — the stream stays honest about its own gaps rather than
	// letting a follower read a truncated view as complete.
	KindDropped Kind = "dropped"
	// KindReset is synthesized by Follow (never written to disk) when the
	// file it tails is truncated under it: a new session took the card,
	// and everything the follower accumulated belongs to the old one.
	KindReset Kind = "reset"
)

// Record is one line of a live file: exactly one JSON object, no
// newlines inside it. Field names are terse because this file is written
// per streamed chunk; omitempty keeps unused fields off the wire.
type Record struct {
	Kind Kind      `json:"kind"`
	Time time.Time `json:"t"`

	// Feature/Stage/Role/Agent/Model identify the session. They are
	// stamped on KindSession and left off subsequent records, which
	// inherit the header's identity.
	Feature string `json:"id,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Role    string `json:"role,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Model   string `json:"model,omitempty"`

	// PID is the process that owns the session, stamped on KindSession so
	// a follower can tell a live writer from an abandoned file.
	PID int `json:"pid,omitempty"`

	// Text is the record's content: message/delta prose, a tool line, an
	// activity note, or the question on a KindAsk.
	Text string `json:"text,omitempty"`

	// State is the session state on KindState.
	State string `json:"state,omitempty"`
	// Busy is the agent's mid-turn flag on KindBusy.
	Busy bool `json:"busy,omitempty"`

	// Call correlates a KindResult with its KindTool; OK and Output carry
	// the outcome.
	Call   string `json:"call,omitempty"`
	OK     bool   `json:"ok,omitempty"`
	Output string `json:"out,omitempty"`

	// Err is set on a KindState/KindStopped that ended in failure.
	Err string `json:"err,omitempty"`

	// Spend fields on KindSpend: the session's running totals, not deltas.
	Credits      float64 `json:"credits,omitempty"`
	InputTokens  int64   `json:"in,omitempty"`
	OutputTokens int64   `json:"out_tokens,omitempty"`

	// Count is the number of lost records on KindDropped.
	Count int `json:"n,omitempty"`
}
