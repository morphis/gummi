package webapi

// Board is GET /api/board: the rail of cards and the header above it.
type Board struct {
	// Repo names the workspace; Head is the default repository's checked-
	// out branch.
	Repo string `json:"repo"`
	Head string `json:"head,omitempty"`
	// Today is what the board spent since local midnight.
	Today Today `json:"today"`
	// Counts are the header's two numbers.
	Counts Counts `json:"counts"`
	// Viewers is who else has the board open.
	Viewers []Viewer `json:"viewers"`
	// Rows is every card, in the TUI board's order. The page groups them
	// by Status; it does not reorder within a group.
	Rows []Row `json:"rows"`
	// Resume is the question the TUI asks when it starts on cards its last
	// quit stopped: pick them back up, or not now. Nil when there is
	// nothing to offer. Answer it with POST /api/board/resume.
	Resume *ResumeOffer `json:"resume,omitempty"`
}

// ResumeOffer is the quit-resume question: the cards the last quit
// stopped, and how long ago ("2h ago").
type ResumeOffer struct {
	Cards []CardRef `json:"cards"`
	Since string    `json:"since"`
}

// ResumeRequest is POST /api/board/resume. Cards names the offered cards
// to pick back up (all of them is the TUI's "Resume all"); None declines
// the offer ("Not now"), which leaves every card parked where it stopped.
type ResumeRequest struct {
	Cards []string `json:"cards,omitempty"`
	None  bool     `json:"none,omitempty"`
}

// Today is the header's spend line.
type Today struct {
	Spent float64 `json:"spent"`
}

// Counts are how many cards need a person, how many are running, and how
// many are queued for a free lane. A queued card is never counted as
// running: nothing is working on it yet.
type Counts struct {
	Needs   int `json:"needs"`
	Running int `json:"running"`
	Queued  int `json:"queued,omitempty"`
}

// RowStatus is the one word the rail groups a card under.
type RowStatus string

// The rail's groups. A card is in exactly one: needs outranks running
// (the TUI's own precedence — a raised gate over a check still going),
// running outranks paused, and the rest follow the card's stage.
const (
	StatusNeeds   RowStatus = "needs"
	StatusRunning RowStatus = "running"
	StatusPaused  RowStatus = "paused"
	StatusIdle    RowStatus = "idle"
	// StatusWatching is a freeform card with no turn in flight but a watch
	// still open: the agent will speak up on its own.
	StatusWatching RowStatus = "watching"
	StatusTodo     RowStatus = "todo"
	StatusDone     RowStatus = "done"
)

// Row is one card on the rail.
type Row struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Stage is the workflow stage (domain.Stage's text).
	Stage  string    `json:"stage"`
	Status RowStatus `json:"status"`
	// Needs is set when the card is in the needs-you queue.
	Needs *RowNeeds `json:"needs,omitempty"`
	// Running is set while an agent is working on the card.
	Running *RowRunning `json:"running,omitempty"`
	// Spend is the card's credits so far (live while a session runs);
	// Envelope its budget, 0 when uncapped.
	Spend    float64 `json:"spend"`
	Envelope int     `json:"envelope"`
	// Context is the running session's context-window occupancy, when one
	// is live and its backend reports it. Nil otherwise — a card with no
	// live session, or whose backend never reports tokens, has none to
	// show.
	Context *AgentContext `json:"context,omitempty"`
	// Profile names the card's profile, when it has one.
	Profile string `json:"profile,omitempty"`
	// Repo names the card's managed repository; empty is the default.
	Repo string `json:"repo,omitempty"`
	// Autopilot marks a card whose gates cross unattended.
	Autopilot bool `json:"autopilot,omitempty"`
	// Elsewhere marks a card another gummi process is driving: this board
	// may watch it and not steer it.
	Elsewhere bool `json:"elsewhere,omitempty"`
	// Severity is a bug's severity.
	Severity string `json:"severity,omitempty"`
	// Stack and Goal place the card in a stack or under a goal.
	Stack *RowStack `json:"stack,omitempty"`
	Goal  *RowGoal  `json:"goal,omitempty"`
	// Waits names the cards this one's dependencies are waiting on.
	Waits []string `json:"waits,omitempty"`
	// Landed marks a card whose branch was squash-merged; PR is the linked
	// pull request's compact badge ("#123 open").
	Landed bool   `json:"landed,omitempty"`
	PR     string `json:"pr,omitempty"`
}

// NeedsKind is why a card is waiting on a person.
type NeedsKind string

// The needs-you kinds, matching the TUI's attention queue.
const (
	NeedsGate     NeedsKind = "gate"
	NeedsQuestion NeedsKind = "question"
	NeedsFailure  NeedsKind = "failure"
	NeedsBudget   NeedsKind = "budget"
)

// RowNeeds is a card's needs-you entry.
type RowNeeds struct {
	Kind NeedsKind `json:"kind"`
	// Color is the token the page tints the entry with ("warn", "err",
	// "info"): an escalated gate reads differently from a clean one.
	Color string `json:"color"`
	// Question is the entry's one line.
	Question string `json:"question"`
	// Word is the badge the rail shows ("design gate", "verify failed"),
	// said by the server so the page never guesses an outcome from a kind.
	Word string `json:"word,omitempty"`
}

// RowRunning is a working card's activity line.
type RowRunning struct {
	// Verb is what the agent is doing, in the TUI's words ("planning",
	// "running checks").
	Verb string `json:"verb"`
	// Autopilot marks a stretch autopilot is driving.
	Autopilot bool `json:"autopilot,omitempty"`
	// Pausing marks a pause asked for and not yet taken.
	Pausing bool `json:"pausing,omitempty"`
	// Why says what a queued card waits for, in the TUI's words ("queued
	// — the attended lane is busy with BG-001"); empty for a running one.
	Why string `json:"why,omitempty"`
}

// RowStack places a card in a stack: position Pos (0 at the bottom) of Of.
type RowStack struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Pos   int    `json:"pos"`
	Of    int    `json:"of"`
	Stale bool   `json:"stale,omitempty"`
}

// RowGoal names the goal a card belongs to.
type RowGoal struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
