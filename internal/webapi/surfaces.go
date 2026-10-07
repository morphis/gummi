package webapi

import (
	"time"

	"github.com/morphis/gummi/internal/engine"
)

// Outcome is the body of a surface write that ran: what the TUI's status
// band would have said about it. A write the board refused is a 409 whose
// Error carries the same sentence.
type Outcome struct {
	OK   bool   `json:"ok"`
	Text string `json:"text,omitempty"`
	// ID names what the write made or touched: a new goal's card id, a
	// stack's id.
	ID string `json:"id,omitempty"`
}

// Goals is GET /api/goals: every goal on the board, in the board's order.
type Goals struct {
	Goals []GoalSummary `json:"goals"`
}

// GoalSummary is one goal's rail row plus the progress its board row
// shows: done-when met, cards landed, budget spent.
type GoalSummary struct {
	Row
	// State is the goal page's one word: "todo", "agreeing the plan",
	// "running", "wrapping up", "ready for you" or "done".
	State    string `json:"state"`
	Met      int    `json:"met"`
	DoneWhen int    `json:"doneWhen"`
	// Landed and Cards count the goal's cards, dropped ones left out.
	Landed int `json:"landed"`
	Cards  int `json:"cards"`
	// Spent is the goal's whole spend, its cards' included.
	Spent   float64 `json:"spent"`
	Partial string  `json:"partial,omitempty"`
}

// Goal is GET /api/goals/{id}: the goal page. Report is the goal's
// hand-over exactly as `gummi status` prints it for a goal
// (engine.GoalReport is already the JSON shape the headless driver emits,
// snake_case included); the rest is what the TUI's goal page shows beside
// it.
type Goal struct {
	Report engine.GoalReport `json:"report"`
	// State is GoalSummary.State.
	State string `json:"state"`
	// Cards are the rail rows of the cards the goal is conducting.
	Cards []Row `json:"cards"`
	// Log is the lead's log, oldest first, the last 200 entries.
	Log      []GoalLogEntry `json:"log"`
	Notebook GoalNotebook   `json:"notebook"`
	// Actions is the goal's menu, offered when the TUI would offer it.
	// Each id is a POST /api/goals/{id}/actions/{action}.
	Actions []Action `json:"actions"`
}

// GoalLogEntry is one line of the lead's log.
type GoalLogEntry struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Card   string    `json:"card,omitempty"`
	// Ref is a decision's D-N, or what the entry answers.
	Ref    string `json:"ref,omitempty"`
	Item   string `json:"item,omitempty"`
	Detail string `json:"detail,omitempty"`
	By     string `json:"by,omitempty"`
}

// GoalNotebook is what the goal knows that no card owns (§17.10): its
// pinned reference documents and its findings.
type GoalNotebook struct {
	References []GoalReference `json:"references"`
	Findings   []GoalFinding   `json:"findings"`
}

// GoalReference is one reference document the plan was agreed against.
type GoalReference struct {
	Name    string `json:"name"`
	Changed bool   `json:"changed,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// GoalFinding is one thing that turned out to be true.
type GoalFinding struct {
	Ref      string `json:"ref"`
	Claim    string `json:"claim"`
	Evidence string `json:"evidence,omitempty"`
	Card     string `json:"card,omitempty"`
	Status   string `json:"status"`
}

// GoalCreateRequest is POST /api/goals: mint a goal the way `gummi goal`
// and the new-card form do. The goal is created in todo; Autopilot hands
// it to autopilot at once, which is what the form's start button offers.
type GoalCreateRequest struct {
	// Description is the objective.
	Description string `json:"description"`
	// Envelope is the goal's whole budget; nil takes the board's default.
	Envelope *int   `json:"envelope,omitempty"`
	Profile  string `json:"profile,omitempty"`
	// After continues another goal (a programme, §17.12).
	After string `json:"after,omitempty"`
	// References are paths to documents the plan is agreed against.
	References []string `json:"references,omitempty"`
	Autopilot  bool     `json:"autopilot,omitempty"`
}

// The goal actions, POST /api/goals/{id}/actions/{action}.
const (
	GoalActionNote      = "note"      // Text: a line for the lead
	GoalActionBudget    = "budget"    // Envelope: the new, higher envelope
	GoalActionSubstrate = "substrate" // Runs, Minutes: the substrate budget
	GoalActionTopUp     = "topup"     // raise by what the waiting card asked, carry on
	GoalActionStop      = "stop"      // finish now; comes back partial
	GoalActionSendBack  = "sendback"  // Text: notes for the lead
	GoalActionReverse   = "reverse"   // Ref (D-N), Why
	GoalActionLand      = "land"      // Text: the merge message ("" drafts one)
	GoalActionAbandon   = "abandon"   // close without landing; branch kept
)

// The inputs only a goal's actions ask for, beside card.go's.
const (
	// ActionNeedsDecision: pick one of the report's decisions for review
	// (Report.Decisions[].ref) and say why.
	ActionNeedsDecision ActionNeeds = "decision"
	// ActionNeedsSubstrate: a substrate budget, runs and minutes.
	ActionNeedsSubstrate ActionNeeds = "substrate"
)

// GoalActionRequest is POST /api/goals/{id}/actions/{action}.
type GoalActionRequest struct {
	// Text is a note, a send-back's notes, or a landing message.
	Text string `json:"text,omitempty"`
	// Envelope is a budget action's new envelope.
	Envelope *int `json:"envelope,omitempty"`
	// Ref and Why name the decision a reversal reverses, and why.
	Ref string `json:"ref,omitempty"`
	Why string `json:"why,omitempty"`
	// Runs and Minutes are a substrate action's new budget.
	Runs    *int `json:"runs,omitempty"`
	Minutes *int `json:"minutes,omitempty"`
}

// Stack is one entry of GET /api/stacks and the body of
// GET /api/stacks/{id}.
type Stack struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Repo string `json:"repo,omitempty"`
	// Base is the branch the bottom card forks from.
	Base    string        `json:"base,omitempty"`
	Members []StackMember `json:"members"`
	// Push is the `git push --force-with-lease` each branch a replay moved
	// needs; gummi never pushes (§18). On a restack's answer it is that
	// restack's; on a read it is the stack's latest replay walk — the
	// board's own automatic ticks included — so a page opened after the
	// replay still has the lines. Replayed and ReplayedAt name that walk's
	// cards and when it last moved one; all three are absent when this
	// process has replayed nothing on the stack.
	Push       []string  `json:"push,omitempty"`
	Replayed   []string  `json:"replayed,omitempty"`
	ReplayedAt time.Time `json:"replayedAt,omitzero"`
}

// StackMember is one card in a stack, bottom first.
type StackMember struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Pos    int    `json:"pos"`
	Stage  string `json:"stage"`
	Branch string `json:"branch,omitempty"`
	// Below is the card this one forks from, empty at the bottom.
	Below string `json:"below,omitempty"`
	// Tree marks a card that has cut its branch.
	Tree bool `json:"tree,omitempty"`
	// Adopted marks a branch gummi did not cut (DESIGN §10 D22): it
	// exists before the card has a worktree on it.
	Adopted bool `json:"adopted,omitempty"`
	Landed  bool `json:"landed,omitempty"`
	// HandedOff marks a card that closed without landing: its branch was
	// kept for someone else to push. It is not Landed, though the cards
	// above it stop forking from it all the same.
	HandedOff bool `json:"handedOff,omitempty"`
	// Stale marks a card sitting on commits that have since moved.
	Stale   bool `json:"stale,omitempty"`
	Running bool `json:"running,omitempty"`
	Dirty   bool `json:"dirty,omitempty"`
	// Blocker names the card that has to land before this one may.
	Blocker string `json:"blocker,omitempty"`
}

// Stacks is GET /api/stacks.
type Stacks struct {
	Stacks []Stack `json:"stacks"`
}

// StackRequest is the body of the stack writes: POST /api/stacks (Card
// at the bottom, Name optional, Cards stacked above it bottom first),
// POST /api/stacks/{id}/cards (Card at Pos), POST /api/stacks/{id}/move
// (Card to Pos) and POST /api/stacks/{id}/rename (Name). The two
// removals take no body: DELETE /api/stacks/{id}/cards/{card} takes a
// card out (`gummi stack rm`), DELETE /api/stacks/{id} deletes an empty
// stack.
type StackRequest struct {
	Name  string   `json:"name,omitempty"`
	Cards []string `json:"cards,omitempty"`
	Card  string   `json:"card,omitempty"`
	// Pos is a position, 0 at the bottom; nil means the top.
	Pos *int `json:"pos,omitempty"`
}

// Restack is POST /api/stacks/{id}/restack: the walk to a fixed point
// `gummi stack restack` forces, and the stack as it left it.
type Restack struct {
	Stack Stack `json:"stack"`
	// Replayed are the cards whose branches moved, in order.
	Replayed []string `json:"replayed"`
	// Conflict is set when a replay stopped on conflicts; that card's
	// branch is untouched.
	Conflict *StackConflict `json:"conflict,omitempty"`
	// Waiting says why the walk stopped short without a conflict.
	Waiting string `json:"waiting,omitempty"`
}

// StackConflict is a replay that hit conflicts.
type StackConflict struct {
	Card  string   `json:"card"`
	Files []string `json:"files"`
}

// IngestRequest is POST /api/ingest's JSON form: a document already in
// the workspace (Path, relative to it) or pasted (Markdown, saved under
// .gummi/ingest as Name). The multipart form carries the file under
// "file" and these fields beside it.
type IngestRequest struct {
	Path     string `json:"path,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	Name     string `json:"name,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Envelope *int   `json:"envelope,omitempty"`
}

// The states of an ingest run.
const (
	IngestRunning      = "running"
	IngestReview       = "review"
	IngestMaterialized = "materialized"
	IngestFailed       = "failed"
	IngestDiscarded    = "discarded"
)

// IngestRun is GET /api/ingest/{run} (and GET /api/ingest, the board's
// current one): a decomposition in flight or waiting for review. The board
// runs one at a time, as the TUI does. It is also the answer to the run's
// writes: POST /api/ingest (start one), POST /api/ingest/{run}/edit, POST
// /api/ingest/{run}/discard, and POST /api/ingest/{run}/approve (which
// mints the kept proposals and names them in Created; 409 when it
// failed).
type IngestRun struct {
	ID string `json:"id"`
	// State is one of the Ingest* states.
	State string `json:"state"`
	// Steps is the pass's progress lines so far: gummi's milestones and
	// the architect's tool calls. Commentary is what it is saying now.
	Steps      []IngestStep `json:"steps,omitempty"`
	Commentary string       `json:"commentary,omitempty"`
	Source     string       `json:"source,omitempty"`
	// Profile, Repo and Envelope are what the approved cards are made with.
	Profile   string           `json:"profile,omitempty"`
	Repo      string           `json:"repo,omitempty"`
	Envelope  int              `json:"envelope,omitempty"`
	Proposals []IngestProposal `json:"proposals,omitempty"`
	// Coverage counts the source→card map; Unmapped are the source
	// requirements no proposal covers — the review flags them loudly
	// (§11.2).
	Coverage *IngestCoverage `json:"coverage,omitempty"`
	Unmapped []string        `json:"unmapped,omitempty"`
	Error    string          `json:"error,omitempty"`
	// Created lists the cards an approved run minted.
	Created []CardRef `json:"created,omitempty"`
}

// IngestStep is one line of a running pass: a gummi milestone (note) or
// an architect tool call (tool).
type IngestStep struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// IngestCoverage counts how the source's requirements map.
type IngestCoverage struct {
	Mapped     int `json:"mapped"`
	OutOfScope int `json:"outOfScope"`
	Unmapped   int `json:"unmapped"`
}

// IngestProposal is one card an ingest proposes.
type IngestProposal struct {
	Index         int      `json:"index"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	OneLiner      string   `json:"oneLiner,omitempty"`
	SourceRefs    []string `json:"sourceRefs,omitempty"`
	DependsOn     []string `json:"dependsOn,omitempty"`
	Problem       string   `json:"problem,omitempty"`
	OpenQuestions []string `json:"openQuestions,omitempty"`
	Dropped       bool     `json:"dropped,omitempty"`
}

// The review's edits, IngestEditRequest.Op.
const (
	IngestEditRename   = "rename"   // Title
	IngestEditOneLiner = "oneLiner" // OneLiner
	IngestEditDrop     = "drop"
	IngestEditUndrop   = "undrop"
	// IngestEditMerge folds the proposal into the one above it.
	IngestEditMerge = "merge"
)

// IngestEditRequest is POST /api/ingest/{run}/edit: one of the TUI
// review's edits on one proposal before approval.
type IngestEditRequest struct {
	Index    int    `json:"index"`
	Op       string `json:"op"`
	Title    string `json:"title,omitempty"`
	OneLiner string `json:"oneLiner,omitempty"`
}

// Bugs is GET /api/bugs?repo=&label=&state=&limit=&in=: the issues a
// GitHub import would propose. repo is the GitHub owner/repo (empty lets
// gh read the checkout's remote), in the managed repository whose checkout
// gh runs in.
type Bugs struct {
	Source    string        `json:"source"`
	Proposals []BugProposal `json:"proposals"`
	// Skipped are issues already imported, by reference.
	Skipped []BugSkipped `json:"skipped,omitempty"`
	// Error is gh's failure, when the fetch failed: the list is empty and
	// the page says why rather than failing the request.
	Error string `json:"error,omitempty"`
}

// BugProposal is one issue offered as a bug card.
type BugProposal struct {
	Ref      string   `json:"ref"`
	Number   int      `json:"number,omitempty"`
	Title    string   `json:"title"`
	OneLiner string   `json:"oneLiner,omitempty"`
	Severity string   `json:"severity,omitempty"`
	State    string   `json:"state,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	// Author is who opened the issue, its login.
	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
}

// BugSkipped is an issue already on the board.
type BugSkipped struct {
	Ref   string `json:"ref"`
	Card  string `json:"card"`
	Title string `json:"title,omitempty"`
}

// BugsRequest is POST /api/bugs: materialize the chosen issues into todo,
// as `gummi bugs ingest` does. The issues are fetched again with the same
// filters and matched by Refs, so the server mints what gh says rather
// than what the page sent.
type BugsRequest struct {
	// Repo is the GitHub owner/repo, Label and State the filters the list
	// was fetched with; In is the managed repository gh runs in.
	Repo  string `json:"repo,omitempty"`
	Label string `json:"label,omitempty"`
	State string `json:"state,omitempty"`
	In    string `json:"in,omitempty"`
	// Limit is the list's own limit (0 is gh's default): an issue chosen
	// from a longer list than the default must still be on offer when the
	// import fetches again.
	Limit int `json:"limit,omitempty"`
	// TargetRepo is the managed repository the bug cards belong to.
	TargetRepo string   `json:"targetRepo,omitempty"`
	Refs       []string `json:"refs"`
	Profile    string   `json:"profile,omitempty"`
	Envelope   *int     `json:"envelope,omitempty"`
}

// BugsCreated is POST /api/bugs's answer.
type BugsCreated struct {
	Created []CardRef `json:"created"`
	// Missing are refs the fetch no longer offered: already on the board,
	// closed since, or filtered out.
	Missing []string `json:"missing,omitempty"`
}

// Doctor is GET /api/doctor: the readiness checklist `gummi doctor --json`
// prints. The deep run (`--deep`, a model turn per role) is POST
// /api/doctor?deep=1, since it spends.
type Doctor struct {
	Ready  bool          `json:"ready"`
	Checks []DoctorCheck `json:"checks"`
}

// DoctorCheck is one readiness item. Status is "ok", "warn" or "fail".
type DoctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// AgentContext is a context window's occupancy, in tokens.
type AgentContext struct {
	Tokens int64 `json:"tokens"`
	Limit  int64 `json:"limit"`
}

// PushKey is GET /api/push/key: the server's VAPID public key, base64url,
// for PushManager.subscribe's applicationServerKey.
type PushKey struct {
	Key string `json:"key"`
}

// PushSubscription is POST /api/push/subscribe's body — the browser's
// PushSubscription.toJSON(). DELETE /api/push/subscribe needs no body: a
// device has one subscription, and it drops that one.
type PushSubscription struct {
	Endpoint string   `json:"endpoint"`
	Keys     PushKeys `json:"keys"`
	// Expires is the browser's expirationTime: epoch milliseconds, or
	// null for a subscription that does not expire.
	Expires *float64 `json:"expirationTime,omitempty"`
}

// PushKeys are a subscription's encryption keys, base64url.
type PushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushPayload is what a push message carries to the service worker.
type PushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// URL is the page to open on click: "/#<card id>".
	URL string `json:"url"`
}
