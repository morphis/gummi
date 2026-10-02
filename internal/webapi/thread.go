package webapi

import "time"

// Thread is GET /api/cards/{id}/thread?after=<seq>: the card's folded
// conversation, oldest first. With after set, only items whose Seq is newer
// come back. An item can grow after it was delivered — a tool group gains
// calls, a stage divider gets its verdict and credits when the stage exits
// — and its Seq moves forward when it does, so the page upserts by Key and
// never appends blindly.
type Thread struct {
	Items []Item `json:"items"`
	// Live is the card's running session, when one is.
	Live *Live `json:"live,omitempty"`
	// LastSeq is the newest item's Seq: the page's next after=.
	LastSeq int64 `json:"lastSeq"`
}

// ItemType is what one thread item draws as.
type ItemType string

// The thread's item types.
const (
	// ItemStage opens a stage segment: "plan · architect · gpt-5".
	ItemStage ItemType = "stage"
	// ItemMessage is the agent's message.
	ItemMessage ItemType = "message"
	// ItemYou is a person's line: a steer, a consult, a note.
	ItemYou ItemType = "you"
	// ItemTools is a run of tool calls folded into one block.
	ItemTools ItemType = "tools"
	// ItemReceipt is a crossing's receipt: approved, sent back, landed.
	ItemReceipt ItemType = "receipt"
	// ItemVerify is a verify run's checks.
	ItemVerify ItemType = "verify"
	// ItemDecision is a decision raised (and, once answered, its answer).
	ItemDecision ItemType = "decision"
	// ItemNote is gummi's own narration: a pause, a hand-off.
	ItemNote ItemType = "note"
	// ItemStretch marks where an autopilot stretch opened or closed, with
	// its label and tally.
	ItemStretch ItemType = "stretch"
)

// Item is one entry in a card's thread.
type Item struct {
	// Key identifies the item for its whole life; Seq is the event log's
	// sequence number of its newest event, and moves when the item grows.
	Key  string    `json:"key"`
	Seq  int64     `json:"seq"`
	T    ItemType  `json:"t"`
	Time time.Time `json:"time"`
	// Stage, Role, Model and Flavor place the item in the workflow; Flavor
	// is the pass kind (work, critique, rebase).
	Stage  string `json:"stage,omitempty"`
	Role   string `json:"role,omitempty"`
	Model  string `json:"model,omitempty"`
	Flavor string `json:"flavor,omitempty"`
	// Author labels a message or you item the way the TUI's thread does:
	// "you", "gummi", or the role that spoke.
	Author string `json:"author,omitempty"`
	// Text is markdown. The page renders it with its own small, safe
	// renderer; the server never sends HTML.
	Text string `json:"text,omitempty"`
	// By names the person behind a you, receipt or decision item.
	By string `json:"by,omitempty"`
	// Attachments are the images a you item's turn carried, in order; the
	// page renders them as thumbnails linking to GET /api/attachments/{id}.
	Attachments []AttachmentRef `json:"attachments,omitempty"`
	// Via says how a person's line reached the agent: "steered" or
	// "answer".
	Via string `json:"via,omitempty"`
	// Exited, Verdict and Credits close a stage divider once its stage
	// exited (a verify item carries Exited and Verdict too); Outcome is
	// the divider's mark, "ok", "fail" or "" for neutral.
	Exited  bool    `json:"exited,omitempty"`
	Verdict string  `json:"verdict,omitempty"`
	Credits float64 `json:"credits,omitempty"`
	Outcome string  `json:"outcome,omitempty"`
	// Label and Tally describe a stretch marker ("autopilot", "3 gates").
	// Edge is "open" or "close"; a closing marker says How the stretch
	// ended and the Reason its closing event gave. Mode is the autopilot
	// mode the stretch ran in.
	Label   string     `json:"label,omitempty"`
	Tally   string     `json:"tally,omitempty"`
	Edge    string     `json:"edge,omitempty"`
	How     string     `json:"how,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Mode    string     `json:"mode,omitempty"`
	Tools   []ToolCall `json:"tools,omitempty"`
	Receipt *Receipt   `json:"receipt,omitempty"`
	Checks  []CheckRun `json:"checks,omitempty"`
	// Decision is the decision a decision item raised, with Answer set once
	// someone answered it.
	Decision *ThreadDecision `json:"decision,omitempty"`
}

// ToolCall is one tool call in a tools item.
type ToolCall struct {
	Tool   string `json:"tool"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Status is "ok" or "fail" once the call reported an outcome;
	// "running" for the call a live session has in flight; "watching" for
	// a backend's own persistent background watch (Claude Code's Monitor
	// tool) still outstanding, which never reports one while it runs and
	// may outlive the turn that started it; "" for a logged call that
	// never reported one.
	Status string `json:"status"`
	Ms     int64  `json:"ms,omitempty"`
	// Output is kept for a failed call only: the tail that says why.
	Output string `json:"output,omitempty"`
}

// Receipt is a crossing's outcome line.
type Receipt struct {
	// Kind is the event the receipt records: "gate", "ask", "park",
	// "autopilot" (a mode change) or "decision" (superseded unanswered).
	Kind string `json:"kind,omitempty"`
	OK   bool   `json:"ok"`
	Text string `json:"text"`
	By   string `json:"by,omitempty"`
}

// CheckRun is one gummi-check's result. Cmd is joined from the spec's
// gummi-checks by name, since a verify record carries only the name.
type CheckRun struct {
	Name   string `json:"name"`
	Cmd    string `json:"cmd,omitempty"`
	OK     bool   `json:"ok"`
	Ms     int64  `json:"ms,omitempty"`
	Output string `json:"output,omitempty"`
	// Status is gummi's own word for the result: "pass", "FAIL (exit
	// 1)", "FAIL (pre-existing)", "TIMEOUT (killed by deadline)"…
	Status string `json:"status,omitempty"`
}

// ThreadDecision is a decision as the thread records it.
type ThreadDecision struct {
	// ID is the decision's ref, the one an answer names.
	ID       string       `json:"id,omitempty"`
	Kind     DecisionKind `json:"kind"`
	Question string       `json:"question"`
	Answer   string       `json:"answer,omitempty"`
	By       string       `json:"by,omitempty"`
}

// Live is GET /api/cards/{id}/live: what is happening on the card right
// now that its log does not hold yet — the TUI thread's live block.
//
// A stage session writes its turns to the card's log once per turn, so
// while one runs, the thread's items stop at the divider of the stage it
// is running (Thread leaves the rest out) and Turns carries the session's
// transcript in their place; Streaming is the message still arriving.
// Session names the run, so a page can tell the transcript it holds was
// replaced by another run's. Consult and Freeform are the card's other
// conversations, which are never in its log; Elsewhere is a run another
// gummi process owns.
type Live struct {
	Busy bool `json:"busy"`
	// Verb is the busy word ("thinking", "editing main.go").
	Verb  string    `json:"verb,omitempty"`
	Since time.Time `json:"since,omitzero"`
	// Streaming is the message the agent is writing right now, markdown.
	Streaming string `json:"streaming,omitempty"`
	// Tool is the call in flight.
	Tool *ToolCall `json:"tool,omitempty"`
	// Spent is the session's credits so far.
	Spent float64 `json:"spent"`
	// State is the engine's scheduling state (queued, running, paused…).
	State string `json:"state,omitempty"`
	// Stage, Role and Model head the live session's block; Session is
	// when it started, its identity.
	Stage   string    `json:"stage,omitempty"`
	Role    string    `json:"role,omitempty"`
	Model   string    `json:"model,omitempty"`
	Session time.Time `json:"session,omitzero"`
	// Turns is the live session's settled transcript, oldest first, at
	// most the newest LiveTurns of it.
	Turns []Turn `json:"turns,omitempty"`
	// Err is the session's failure, when it failed.
	Err       string        `json:"err,omitempty"`
	Consult   *Conversation `json:"consult,omitempty"`
	Freeform  *Conversation `json:"freeform,omitempty"`
	Elsewhere *Elsewhere    `json:"elsewhere,omitempty"`
}

// LiveTurns bounds how much of a live transcript one response carries,
// and LiveText how much of any one turn: a runaway session must not make
// every open page download its whole history on every delta.
const (
	LiveTurns = 200
	LiveText  = 32 << 10
)

// Turn is one entry of a live transcript.
type Turn struct {
	// Author is "you", "gummi", "tool" or the role that spoke.
	Author string `json:"author"`
	// By names the person behind a "you" turn, when it carried a name.
	By   string `json:"by,omitempty"`
	Text string `json:"text,omitempty"`
	// Tool is set on a tool call's turn.
	Tool *ToolCall `json:"tool,omitempty"`
}

// Conversation is a session that is not a stage run: the card's consult
// (questions asked beside the stage, read-only) or a freeform card's
// working session.
type Conversation struct {
	Busy bool `json:"busy"`
	// Role is who speaks for the agent side, as the thread's finished
	// turns name it ("implementer" on a freeform card), so the turn being
	// written is headed the same way as the ones already written.
	Role string `json:"role,omitempty"`
	// Verb is the busy word the TUI shows under it.
	Verb      string `json:"verb,omitempty"`
	Streaming string `json:"streaming,omitempty"`
	Turns     []Turn `json:"turns,omitempty"`
	// Sending is a line on its way that the session does not hold yet.
	Sending string `json:"sending,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Elsewhere is a run of the card another gummi process owns: read-only
// here, answered where it runs.
type Elsewhere struct {
	PID     int       `json:"pid"`
	Stage   string    `json:"stage,omitempty"`
	Role    string    `json:"role,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Model   string    `json:"model,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	Updated time.Time `json:"updated,omitzero"`
	Busy    bool      `json:"busy"`
	// Watching is set when this board tails the run's live file, which
	// is what fills Live's Turns and Streaming for it; Note is the TUI's
	// footer line about it.
	Watching bool   `json:"watching,omitempty"`
	Note     string `json:"note,omitempty"`
}
