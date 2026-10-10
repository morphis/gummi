package webapi

import "time"

// Board is GET /api/board: the rail of cards and the header above it.
type Board struct {
	// Repo names the workspace; Head is the default repository's checked-
	// out branch.
	Repo string `json:"repo"`
	Head string `json:"head,omitempty"`
	// Name is what this gummi instance is called (config.yaml's name),
	// omitted when it has none.
	Name string `json:"name,omitempty"`
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
	// EventID is the event stream's last id as this read began: a page
	// opens GET /api/events?since=<it> and is told only what changed after
	// the read, rather than refetching everything it just read.
	EventID string `json:"eventId,omitempty"`
}

// ResumeOffer is the quit-resume question: the cards the last quit
// stopped, and when. Since is how long before this board started that
// was ("2h ago"), measured once; At is the moment itself, for a page that
// says how long ago it is now and keeps saying it.
type ResumeOffer struct {
	Cards []CardRef `json:"cards"`
	Since string    `json:"since"`
	At    time.Time `json:"at,omitzero"`
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

// Counts are how many cards need a person and how many are running.
type Counts struct {
	Needs   int `json:"needs"`
	Running int `json:"running"`
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
	// Objective is a freeform card's objective state (active, paused, met,
	// stuck, exhausted, capped, failed), empty when it has none: the row's
	// mark, coloured by it.
	Objective string `json:"objective,omitempty"`
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

// Settings is GET /api/settings and PUT /api/settings' answer: the
// workspace's own knobs. Name is what this instance is called, empty when
// it is unnamed; Repo is the workspace folder's name, which the page
// shows in its place until a name is set.
type Settings struct {
	Name string `json:"name"`
	Repo string `json:"repo"`
	// MaxName is the longest name the workspace accepts, in characters.
	MaxName int `json:"maxName"`
	// Credentials is what the workspace holds for GitHub, described and
	// never disclosed.
	Credentials Credentials `json:"credentials"`
}

// Credentials describes the GitHub token and SSH key stored for this
// workspace. Neither secret is ever in an answer: a token is named by its
// last characters, a key by its type, fingerprint and public half.
type Credentials struct {
	TokenSet  bool   `json:"tokenSet"`
	TokenHint string `json:"tokenHint,omitempty"`
	KeySet    bool   `json:"keySet"`
	KeyType   string `json:"keyType,omitempty"`
	// KeyFingerprint is the key's SHA256 fingerprint, as GitHub lists it.
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
	// KeyPublic is the key's authorized_keys line, for adding to GitHub.
	KeyPublic string `json:"keyPublic,omitempty"`
}

// SettingsRequest is PUT /api/settings' body. An empty Name clears the
// name.
type SettingsRequest struct {
	Name string `json:"name"`
}

// CredentialsRequest is PUT /api/settings/credentials' body. A field left
// out is left as it is; an empty one forgets what was stored.
type CredentialsRequest struct {
	GitHubToken *string `json:"githubToken,omitempty"`
	SSHKey      *string `json:"sshKey,omitempty"`
}
