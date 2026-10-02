package webapi

// Card is GET /api/cards/{id}: the card page's head, its pinned decision
// and what else can be done to it.
type Card struct {
	Row
	// Branch is the card's git branch; Base what it forks from and lands
	// on. Adopted marks a branch gummi did not cut (DESIGN §10 D22).
	Branch  string `json:"branch,omitempty"`
	Base    string `json:"base,omitempty"`
	Adopted bool   `json:"adopted,omitempty"`
	// Scratch marks a card that works in a throwaway scratch tree and
	// never gets a branch (a research card): Branch and Base are empty,
	// because there is nothing to land and nowhere to land it.
	Scratch bool `json:"scratch,omitempty"`
	// OneLiner is the card's short summary.
	OneLiner string `json:"oneLiner,omitempty"`
	// Decision is the one open decision the page pins, ranked the way the
	// TUI ranks it; nil when nothing is waiting on a person.
	Decision *Decision `json:"decision,omitempty"`
	// DecisionsMore counts the other open decisions behind it.
	DecisionsMore int `json:"decisionsMore"`
	// Actions is the card's menu: every non-decision action the TUI offers
	// on it right now.
	Actions []Action `json:"actions"`
	// Composer says what a line typed into the card's composer would do.
	Composer Composer `json:"composer"`
	// Session is the agent and model a session (a freeform card) runs on,
	// resolved the way its next turn will resolve them; nil on a card in
	// the workflow, whose stages take theirs from its profile.
	Session *SessionModel `json:"session,omitempty"`
	// Files is where the page may open the card's worktree files from;
	// nil while the card has no worktree on this machine.
	Files *Files `json:"files,omitempty"`
}

// Files maps a card's worktree onto the server: a file at Dir/<path> is
// served at URL<path>. An agent names the files it writes by their
// absolute path, and the page links a path under Dir to the copy at URL.
// URL carries its own key rather than relying on the device cookie: the
// file is served sandboxed, as a page of no origin, and the cookie does
// not reach the stylesheets and scripts it loads beside it.
type Files struct {
	Dir string `json:"dir"`
	URL string `json:"url"`
}

// DecisionKind classifies an open decision.
type DecisionKind string

// The decision kinds. They match the durable decision_open record's kinds,
// plus confirm for a TUI confirmation a web intent has to put to a person.
const (
	DecisionGate     DecisionKind = "gate"
	DecisionAsk      DecisionKind = "ask"
	DecisionVerify   DecisionKind = "verify"
	DecisionConflict DecisionKind = "conflict"
	DecisionBudget   DecisionKind = "budget"
	DecisionIdle     DecisionKind = "idle"
	DecisionConfirm  DecisionKind = "confirm"
	// DecisionFailure is a stage that failed (the session errored, or the
	// backend could not serve it); DecisionClosed a card that has ended,
	// with the answers that remain (a follow-up bug, adopting it back).
	// Neither is a durable record; both are what the TUI's card page pins.
	DecisionFailure DecisionKind = "failure"
	DecisionClosed  DecisionKind = "closed"
)

// Anchor names the tab a decision is about, which the page shows beside it.
type Anchor string

// The anchors.
const (
	AnchorSpec   Anchor = "spec"
	AnchorDiff   Anchor = "diff"
	AnchorThread Anchor = "thread"
)

// Decision is an open decision with its answers. Options are regenerated
// on every read and never stored (DESIGN §10 decision 18).
type Decision struct {
	// Ref identifies the decision: an ask's decision id, or a workflow
	// decision's record id. An answer names it back.
	Ref      string       `json:"ref"`
	Kind     DecisionKind `json:"kind"`
	Question string       `json:"question"`
	// Word is the decision's one-word name as the page heads it ("design
	// gate", "verify passed", "working"): said by the server, which knows
	// the outcome behind a kind, so the page never derives it.
	Word string `json:"word,omitempty"`
	// Tone tints it: "ok", "warn", "err" or "info"; empty takes the
	// stage's own hue (a clean gate).
	Tone   string `json:"tone,omitempty"`
	Anchor Anchor `json:"anchor"`
	// Against is the revision the decision was raised on (§20.1). An
	// answer sends Against.Token back and is refused with 409 if the card
	// has moved since.
	Against Against  `json:"against"`
	Options []Option `json:"options"`
	// Multi marks a question that takes several of its options at once:
	// the answer names them comma-separated ("0,2").
	Multi bool `json:"multi,omitempty"`
}

// Against is a decision's revision: an opaque token for the server, and a
// label for a person ("spec a1b2c3d", "verify run 3 on 9f8e7d6").
type Against struct {
	Token string `json:"token"`
	Label string `json:"label"`
}

// Option is one answer to a decision.
type Option struct {
	// ID is what an answer names; it is stable only within one read. A
	// workflow answer's id is its action ("advance", "bounce"); an ask's
	// option is its index ("0", "1"; several comma-separated for a
	// multi-pick question) and its chat row "chat"; the confirm chip's are
	// "go" and "keep".
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Words marks an option that takes a note ("send back with …").
	Words bool `json:"words"`
	// Relabel is the label to show once words have been typed, when the
	// option reads differently with a note than without.
	Relabel string `json:"relabel,omitempty"`
	// Chat marks the "chat about this" answer to an ask: it opens a
	// conversation instead of answering.
	Chat bool `json:"chat,omitempty"`
	// Danger marks an answer that discards or cannot be undone.
	Danger bool `json:"danger,omitempty"`
	// CarriesComments marks an answer that takes the card's unresolved
	// diff comments with it (a send-back from a failed verify).
	CarriesComments bool `json:"carriesComments,omitempty"`
}

// ActionNeeds is the input an action asks for before it runs.
type ActionNeeds string

// The inputs an action can need. The page collects the value and sends it
// in ActionRequest; nothing is asked for in a follow-up dialog.
const (
	ActionNeedsMessage ActionNeeds = "message"
	ActionNeedsNumber  ActionNeeds = "number"
	ActionNeedsProfile ActionNeeds = "profile"
	ActionNeedsCards   ActionNeeds = "cards"
	// ActionNeedsConfirm: the action asks a yes before it acts. The page
	// collects nothing first: it sends the action bare, shows the question
	// the "confirm" question answers with, and sends its token back
	// (AnswerRequest.Confirm).
	ActionNeedsConfirm ActionNeeds = "confirm"
	// ActionNeedsRepo: the repository picker (ActionRequest.Repo).
	ActionNeedsRepo ActionNeeds = "repo"
	// ActionNeedsMode: the autopilot switch's mode (ActionRequest.Mode).
	ActionNeedsMode ActionNeeds = "mode"
	// ActionNeedsText: one line of text (ActionRequest.Message), such as a
	// pull request's URL or number; empty is an answer too when Detail
	// says so.
	ActionNeedsText ActionNeeds = "text"
	// ActionNeedsModel: a session's agent and model (ActionRequest.Backend
	// and ActionRequest.Model), picked from Form.Sessions or typed.
	ActionNeedsModel ActionNeeds = "model"
	// ActionNeedsSpec: writing a spec from a session asks for the spec's
	// title (ActionRequest.Message), its profile (ActionRequest.Profile,
	// from Choices) and its budget (ActionRequest.Number).
	ActionNeedsSpec ActionNeeds = "spec"
)

// Action is one entry in a card's menu.
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Key is the TUI's key for it, shown as a hint.
	Key    string `json:"key,omitempty"`
	Danger bool   `json:"danger,omitempty"`
	// Needs names the input the page must collect first; empty runs at once.
	Needs  ActionNeeds `json:"needs,omitempty"`
	Detail string      `json:"detail,omitempty"`
	// Default is the input's suggested value: the landing message the
	// verify gate drafted, the current envelope, the current dependencies
	// (comma-separated ids).
	Default string `json:"default,omitempty"`
	// Choices are the values a profile or repository picker offers.
	Choices []Choice `json:"choices,omitempty"`
}

// Route is where a composer line goes.
type Route string

// The composer routes, in the TUI's own terms.
const (
	RouteSteer    Route = "steer"
	RouteConsult  Route = "consult"
	RouteFreeform Route = "freeform"
	RouteGoalNote Route = "goalnote"
	RouteAnswer   Route = "answer"
	RouteVerb     Route = "verb"
	RouteBlocked  Route = "blocked"
	// RouteMenu: the line names an action the card has but is not one of
	// its answers right now ("/rebase" at a gate). The TUI opens its menu
	// filtered by the word; the page opens the card's actions.
	RouteMenu Route = "menu"
	// RouteRead: a line typed at a stop that no answer takes words for. It
	// is sent as a line, and the board reads it to place it (a routed
	// re-entry, or a message) exactly as the TUI's enter does — it is never
	// the highlighted answer.
	RouteRead Route = "read"
)

// Composer is the card's composer state, and the answer to POST
// /api/cards/{id}/composer (a SendRequest's Text): what sending that line
// would do, asked as a person types. That route changes nothing.
type Composer struct {
	// Says is the line under the composer that tells a person what sending
	// will do ("steers the implementer mid-turn").
	Says  string `json:"says"`
	Route Route  `json:"route"`
}

// AnswerRequest is POST /api/cards/{id}/answer.
type AnswerRequest struct {
	Ref    string `json:"ref"`
	Option string `json:"option"`
	Words  string `json:"words,omitempty"`
	// Against is the Decision.Against.Token the answer was given against.
	Against string `json:"against"`
	// Confirm answers the confirmations the answer's flow raises on the
	// way (the TUI's y). It holds the token of each confirmation the
	// person was shown and said yes to, space-separated, exactly as a
	// "confirm" question handed it out (Error.Confirm). A token is bound to the
	// question it was issued with — the dialog, the card and the
	// question's whole text — and answers that one question once: a
	// question that reads differently now (a goal that has grown a card, a
	// check list that changed) is asked again, and a second question in
	// the same flow gets a token of its own. There is no bare "yes": a
	// page never confirms a question before the server has asked it.
	Confirm string `json:"confirm,omitempty"`
}

// The words Error.Error carries for a write that did not go through
// and says why in a way the page answers specially.
//
// Two kinds of answer wear them, and they are told apart by status:
//
//   - A refusal is a 409: the card moved, someone answered first, the
//     agent is busy (the line handed back). Any other 409 is a refusal
//     too, and Error.Error is the board's own sentence for it.
//   - A question is a 202 Accepted (StatusQuestion): the request was
//     understood and nothing was done, because its flow stopped at
//     something only the person can answer — an input it needs, a
//     confirmation to give, a line that reads as a new card. Nothing is
//     wrong, so it is not an HTTP error; the body is the same Error
//     shape, and the request sent again with the answer goes on (see
//     IsQuestion).
const (
	ConflictMoved    = "moved"
	ConflictAnswered = "answered"
	ConflictBusy     = "busy"
	// ConflictNeeds (a question): the flow stopped at an input the
	// request carried no answer for. Needs names the input, Text the
	// question; ask the person and send the request again with it.
	ConflictNeeds = "needs"
	// ConflictConfirm (a question): the flow stopped at a confirmation.
	// Text is the question, Confirm its token; the yes is the request
	// sent again with that token (AnswerRequest.Confirm).
	ConflictConfirm = "confirm"
	// ConflictNewCard (a question): the line reads as separate work. Text
	// is the line; the page opens the new-card form seeded with it.
	ConflictNewCard = "newcard"
)

// StatusQuestion is the status a question is answered with: 202
// Accepted — understood, not done, answer this. A question is ordinary
// control flow, and a 4xx would have every browser log it as a failed
// load on every landing, confirmation and hand-off.
const StatusQuestion = 202

// IsQuestion reports whether an Error.Error word is a question (answered
// with StatusQuestion) rather than a refusal (409).
func IsQuestion(word string) bool {
	switch word {
	case ConflictNeeds, ConflictConfirm, ConflictNewCard:
		return true
	}
	return false
}

// SendRequest is POST /api/cards/{id}/send: one composer line.
type SendRequest struct {
	Text string `json:"text"`
	// Against is the pinned decision's Against.Token as the page showed
	// it: a line sent against a card that has moved since is refused with
	// 409 "moved" rather than routed at a stop nobody saw. Required while
	// the card pins a decision.
	Against string `json:"against,omitempty"`
}

// SendResponse says where the line went and returns the card as it now
// stands.
type SendResponse struct {
	Route Route `json:"route"`
	Card  Card  `json:"card"`
}

// ActionRequest is POST /api/cards/{id}/actions/{action}: the input the
// action's Needs asked for.
type ActionRequest struct {
	Message string   `json:"message,omitempty"`
	Number  *int     `json:"number,omitempty"`
	Profile string   `json:"profile,omitempty"`
	Cards   []string `json:"cards,omitempty"`
	// Confirm is the token(s) of the confirmation(s) the server asked on an
	// earlier try ("confirm" question) and the person said yes to (AnswerRequest.Confirm).
	Confirm string `json:"confirm,omitempty"`
	// Repo is the repository picker's answer.
	Repo string `json:"repo,omitempty"`
	// Mode is the autopilot switch's answer: "autopilot" or "attended".
	// Empty takes the one the card's menu entry names.
	Mode string `json:"mode,omitempty"`
	// Backend and Model are the model switch's answer: the agent and model
	// a session runs on from its next turn.
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	// Against is the pinned decision's Against.Token as the page showed
	// it: an action is refused with 409 "moved" if the card moved since.
	// On a card that pins a decision it is required, except for the
	// actions that do not answer it (DecisionIndependentActions).
	Against string `json:"against,omitempty"`
}

// DecisionIndependentActions are the card actions that neither answer nor
// move past a pinned decision, so they run without an Against: the menu
// entries that change what the card waits for, what it may spend, which
// profile or repository it runs under and which pull request it is linked
// to (and, for a session, which model it runs on and committing its
// worktree), and the ones that copy
// or remove it (each of those asks its own
// question first). Every other action — a crossing, a landing, a
// send-back, a run, a pause, a rebase, a hand-off, the autopilot switch —
// and every composer line (SendRequest) sent while a decision is pinned
// must carry the token the page showed, and is refused with 409 "moved"
// without one. An action that is also one of the pinned decision's
// options is never independent.
var DecisionIndependentActions = []string{
	"deps", "profile", "envelope", "repo", "prlink", "prunlink", "prpull",
	"duplicate", "delete", "clean", "model", "commit",
}
