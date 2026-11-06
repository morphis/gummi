package webapi

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
	// EventID is the event stream's last id as this read began: a page
	// opens GET /api/events?since=<it> and is told only what changed after
	// the read, rather than refetching everything it just read.
	EventID string `json:"eventId,omitempty"`
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
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Type is what kind of card this is in a person's words (Feature, Bug,
	// Research, Goal, Session, Agent): what the page says beside the title.
	// Kind stays the machine's word — a session and an agent share one.
	Type  string `json:"type"`
	Title string `json:"title"`
	// Stage is the workflow stage (domain.Stage's text).
	Stage  string    `json:"stage"`
	Status RowStatus `json:"status"`
	// Needs is set when the card is in the needs-you queue.
	Needs *RowNeeds `json:"needs,omitempty"`
	// Running is set while an agent is working on the card.
	Running *RowRunning `json:"running,omitempty"`
	// Word is an idle session's state in a few words ("ready to land",
	// "2 open comments", "nothing committed yet"), said by the server so the page
	// tells cards apart without guessing; "" for any other card or status.
	Word string `json:"word,omitempty"`
	// Spend is the card's credits so far, its workflow runs' steps
	// included (live while a session runs); Envelope its budget, 0 when
	// uncapped.
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
	// Home is set on an agent: where its home is, relative to the
	// workspace root (DESIGN §19.3d). Such a card is in no repository,
	// so its empty Repo does not mean the default.
	Home string `json:"home,omitempty"`
	// Elsewhere marks a card another gummi process is driving: this board
	// may watch it and not steer it.
	Elsewhere bool `json:"elsewhere,omitempty"`
	// Severity is a bug's severity.
	Severity string `json:"severity,omitempty"`
	// Stack places the card in a stack.
	Stack *RowStack `json:"stack,omitempty"`
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
	// DispatchedBy is the agent (its id) whose run minted this session;
	// DispatchedByTitle its title. Dispatched is, on an agent, how many of
	// the sessions it dispatched are still open (DESIGN §26.9).
	DispatchedBy      string `json:"dispatchedBy,omitempty"`
	DispatchedByTitle string `json:"dispatchedByTitle,omitempty"`
	Dispatched        int    `json:"dispatched,omitempty"`
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
	// Identity is who git writes this workspace's commits as.
	Identity Identity `json:"identity"`
	// CoAuthors are the people named in a Co-authored-by trailer on every
	// commit made in this workspace, each "Name <email>". Empty means no
	// trailer.
	CoAuthors []string `json:"coAuthors"`
	// MaxCoAuthors is how many the workspace accepts.
	MaxCoAuthors int `json:"maxCoAuthors"`
	// Workflows is whether sessions are offered workflows and specs when
	// they have no switch of their own; off by default.
	Workflows bool `json:"workflows"`
}

// WorkflowsRequest is PUT /api/settings/workflows' body: the workspace's
// setting.
type WorkflowsRequest struct {
	On bool `json:"on"`
}

// CoAuthorsRequest is PUT /api/settings/coauthors' body: the whole list,
// each entry "Name <email>". An empty list switches the trailer off.
type CoAuthorsRequest struct {
	CoAuthors []string `json:"coAuthors"`
}

// Identity is a git author: user.name and user.email as git resolves them
// in the workspace's repository, from whichever scope sets them. Either is
// empty where nothing does.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// IdentityRequest is PUT /api/settings/identity's body: the name and email
// to write to the repository's own git configuration. Both empty removes
// that setting and leaves the machine's.
type IdentityRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
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
	// Signing reports that commits made in this workspace are signed with
	// the stored key.
	Signing bool `json:"signing"`
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
	// GenerateSSHKey makes a fresh key on the host in place of any held;
	// it may not be sent together with SSHKey.
	GenerateSSHKey bool `json:"generateSshKey,omitempty"`
	// SignCommits switches signing commits with the stored key on or off;
	// on is refused with no key held.
	SignCommits *bool `json:"signCommits,omitempty"`
}
