package webapi

import "time"

// A session's workflow runs on the page (DESIGN §25): the run's block in
// the thread (an Item of type "run"), a step opened beside it, the
// session's specs, the proposal's card, and Revert run. The block's words
// and states are internal/threadfold's; these are their wire shapes.

// ItemRun is a workflow run's block: Item.Run carries it. It is delivered
// on every thread page (it changes without an event of its own), and the
// page redraws it only when its Rev moved.
const ItemRun ItemType = "run"

// RunBlock is one run as its block draws it.
type RunBlock struct {
	N int `json:"n"`
	// Rev changes whenever anything else in the block does.
	Rev    string `json:"rev"`
	Recipe string `json:"recipe"`
	// Spec is the spec's number on the session, SpecTitle its title and
	// SpecRev the revision the run froze (0 until it starts).
	Spec      int    `json:"spec"`
	SpecTitle string `json:"specTitle,omitempty"`
	SpecRev   int    `json:"specRev,omitempty"`
	// State is proposed, queued, running, paused, done, partial, stopped
	// or denied; Reason says why it ended partial or stopped, or paused.
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Stopping bool   `json:"stopping,omitempty"`
	// Spent (live steps included), in credits, from the session's budget:
	// a run estimates nothing.
	Spent float64 `json:"spent"`
	// Started and Ended bound the run. Its elapsed time is the time it
	// worked, its pauses left out: ActiveMs, and while it runs the time
	// since Resumed.
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
	ActiveMs int64     `json:"activeMs,omitempty"`
	Resumed  time.Time `json:"resumed,omitzero"`
	// Phase is what gummi does on the run between its steps: baseline
	// (the checks before the first step), until (judging a loop), ending
	// (the end checks and the report).
	Phase string `json:"phase,omitempty"`
	// Strip is the recipe's top-level entries, each in its state.
	Strip []RunStripEntry `json:"strip"`
	// Rows is a row per step that has run or is running, a finished
	// repeat iteration folded to one.
	Rows []RunStepRow `json:"rows"`
	// Pending says the run waits on a decision: the session's pinned one.
	Pending *RunPending `json:"pending,omitempty"`
	// Baseline is the done-when checks run before the run's first step,
	// while it is proposed or queued, or open with no step yet.
	Baseline *RunBaseline `json:"baseline,omitempty"`
	// Files are what the run has changed so far, uncommitted (or what an
	// ended run left changed).
	Files []string `json:"files,omitempty"`
	// An ended run's result per done-when item, the commits made while it
	// ran, and whether its report is in its spec (the link opens the spec
	// on the Specs tab).
	Results []RunResult `json:"results,omitempty"`
	Commits []string    `json:"commits,omitempty"`
	Report  bool        `json:"report,omitempty"`
	// What the head and footer offer now.
	CanPause  bool `json:"canPause,omitempty"`
	CanResume bool `json:"canResume,omitempty"`
	CanStop   bool `json:"canStop,omitempty"`
	CanRevert bool `json:"canRevert,omitempty"`
	// CanWithdraw offers to take back a queued run before it starts (POST
	// …/runs/{n}/withdraw).
	CanWithdraw bool `json:"canWithdraw,omitempty"`
	// CanRetry offers to run an ended run's failed and stopped steps again
	// (POST …/runs/{n}/retry), keeping every step that finished.
	CanRetry bool `json:"canRetry,omitempty"`
	// CanForward offers to forward to the open run's watch the feedback its
	// trust filter kept out (POST .../runs/{n}/forward).
	CanForward bool `json:"canForward,omitempty"`
	// AlwaysAllowed, on the session's newest run, says a person always
	// allows its runs: Ask again (POST …/runs/{n}/askagain) takes it back.
	AlwaysAllowed bool `json:"alwaysAllowed,omitempty"`
	// Artifacts are what the recipe declares in the run's directory, each
	// read with GET …/runs/{n}/artifacts/{name}.
	Artifacts []RunArtifact `json:"artifacts,omitempty"`
	// CanSave offers Save as recipe… for a run whose recipe was written
	// inline (POST …/runs/{n}/save), SaveName the name it suggests; SavedAs
	// is the workspace recipe it was saved as, offered no more after.
	CanSave  bool   `json:"canSave,omitempty"`
	SaveName string `json:"saveName,omitempty"`
	SavedAs  string `json:"savedAs,omitempty"`
	// Live is the open run's live strip, drawn above the composer while
	// the run is running or paused; absent otherwise.
	Live *RunLive `json:"live,omitempty"`
	// Mark and Line are an ended run in one line, the way it folds in the
	// thread: its state's glyph and "deliver · run 2 · spec 2 “…” · met
	// 5/5 · $2.92 · 14 files". Folded says it is folded by rule — no longer
	// the latest news — and drawn as that line until a person unfolds it.
	Mark   string `json:"mark,omitempty"`
	Line   string `json:"line,omitempty"`
	Folded bool   `json:"folded,omitempty"`
}

// RunArtifact is one artifact a run's recipe declares: its name, its file
// in the run's directory, and whether it has been written.
type RunArtifact struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Exists bool   `json:"exists,omitempty"`
}

// RunArtifactText is GET /api/cards/{id}/runs/{n}/artifacts/{name}: an
// artifact's text.
type RunArtifactText struct {
	Name string `json:"name"`
	File string `json:"file"`
	Text string `json:"text"`
	// Cut says the text was cut to its first part.
	Cut bool `json:"cut,omitempty"`
}

// RunPlace is where one of the session's runs stands among a live
// conversation's turns (Conversation.Runs): before turn Before, or after
// every turn when Before is the number of turns. A run not placed there
// stands among the thread's settled items, where the thread put it.
type RunPlace struct {
	N      int `json:"n"`
	Before int `json:"before"`
}

// RunLive is an open run's live strip (DESIGN §25.9): one line above the
// composer, with Jump to the run's block beside what it offers.
type RunLive struct {
	// Name is the run in a line: "deliver · run 2".
	Name string `json:"name"`
	// Mood is running, asking (a step asks: Answer opens it), deciding
	// (the run's own decision is pinned above the composer), budget
	// (paused on a spent pool: Top up…) or paused (Resume).
	Mood string `json:"mood"`
	// Steps are the live steps: Open opens the one, or picks among
	// several.
	Steps []RunLiveStep `json:"steps,omitempty"`
	// Who names the active steps (joined by ∥); Action is the active
	// step's last action.
	Who    string `json:"who,omitempty"`
	Action string `json:"action,omitempty"`
	// Card is the step Open or Answer goes to, when there is one.
	Card string `json:"card,omitempty"`
	// Question is the asking step's question; Line what a strip that is
	// not running says.
	Question  string `json:"question,omitempty"`
	Line      string `json:"line,omitempty"`
	CanTopUp  bool   `json:"canTopUp,omitempty"`
	CanResume bool   `json:"canResume,omitempty"`
}

// RunLiveStep is one live step the strip names.
type RunLiveStep struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Card  string `json:"card,omitempty"`
	State string `json:"state"`
}

// RunStripEntry is a top-level entry of the recipe: a step, a parallel
// (its Branches) or a repeat (its Iter of Max).
type RunStripEntry struct {
	ID string `json:"id"`
	// Kind is "step", "parallel" or "repeat".
	Kind string `json:"kind"`
	// State is waiting, running, asking, done or failed.
	State    string          `json:"state"`
	Iter     int             `json:"iter,omitempty"`
	Max      int             `json:"max,omitempty"`
	Branches []RunStripEntry `json:"branches,omitempty"`
}

// RunStepRow is one row of the block: a step, or a finished iteration of
// a repeat folded to one (Folded, with Steps naming what ran in it and
// Verdict how its stop condition was judged).
type RunStepRow struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Repeat string `json:"repeat,omitempty"`
	Iter   int    `json:"iter,omitempty"`
	ID     string `json:"id"`
	Role   string `json:"role,omitempty"`
	// State is waiting, running, asking, done, failed or stopped, or
	// paused: at work when its run paused, stopped where it was.
	State string  `json:"state"`
	Spent float64 `json:"spent"`
	// Line is its last action while it works, its end summary after.
	Line string `json:"line,omitempty"`
	// Flag warns beside the line: a reading step that changed files.
	Flag string `json:"flag,omitempty"`
	// Card is the step's card: Open shows it, a message goes to it.
	Card    string `json:"card,omitempty"`
	Folded  bool   `json:"folded,omitempty"`
	Steps   string `json:"steps,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Waiting rings the row the run's open decision is about.
	Waiting bool `json:"waiting,omitempty"`
	// Started and Ended bound the step's time; Model is what its agent
	// runs on while it works.
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	Model   string    `json:"model,omitempty"`
	// Question is the step's own question, its recommended answer first,
	// answered with POST /api/cards/{id}/steps/{card}/answer.
	Question *StepQuestion `json:"question,omitempty"`
	// Watch summarises a watch step's PR (its Line is the strip's words);
	// Pass and Event place a row inside one of its passes, and on a pass's
	// header row name the event it ran for.
	Watch *RunWatch `json:"watch,omitempty"`
	Pass  int       `json:"pass,omitempty"`
	Event string    `json:"event,omitempty"`
}

// RunWatch is what a watch step shows of its pull request.
type RunWatch struct {
	PR      string `json:"pr"`
	URL     string `json:"url,omitempty"`
	Until   string `json:"until,omitempty"`
	Pending int    `json:"pending"`
	Threads int    `json:"threads"`
	KeptOut int    `json:"keptOut"`
	Passes  int    `json:"passes"`
	Held    bool   `json:"held,omitempty"`
}

// StepQuestion is a step's ask_user question. Call names it: an answer
// sends it back, and is refused once the step asks another.
type StepQuestion struct {
	Call     string           `json:"call,omitempty"`
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multi    bool             `json:"multi,omitempty"`
}

// QuestionOption is one answer a step's question offers.
type QuestionOption struct {
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// RunPending is the run's open decision as the block mentions it.
type RunPending struct {
	Kind     string   `json:"kind"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Step     string   `json:"step,omitempty"`
}

// RunResult is one done-when item at the run's end: met, unmet or
// unjudged, Checked when its check command decided it. Before is a
// checked item's verdict in the run's baseline, and Change its result
// before and after in words ("failed before the run, now passes").
type RunResult struct {
	Item    string `json:"item"`
	Verdict string `json:"verdict"`
	Checked bool   `json:"checked,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Before  string `json:"before,omitempty"`
	Change  string `json:"change,omitempty"`
}

// RunBaseline is a run's done-when checks run before its first step
// (DESIGN §25.5): still Running, or each checked item's result before the
// run. Evidence for the person, never a gate.
type RunBaseline struct {
	Running bool `json:"running,omitempty"`
	// Err says why the checks could not be run.
	Err   string            `json:"err,omitempty"`
	Items []RunBaselineItem `json:"items,omitempty"`
}

// RunBaselineItem is one checked item's baseline: its verdict, the line
// that says it ("DW-2 already passes before the run"), and Problem when it
// proves nothing about the run.
type RunBaselineItem struct {
	Item    string `json:"item"`
	Verdict string `json:"verdict"`
	Line    string `json:"line"`
	Problem bool   `json:"problem,omitempty"`
}

// StepFace is GET /api/cards/{id}/steps/{card}: a step opened beside the
// session's thread (DESIGN §25.10). Its transcript is the step card's own
// thread, read with GET /api/cards/{card}/thread and …/live; its diff is
// GET /api/cards/{id}/steps/{card}/diff (a Diff).
type StepFace struct {
	// Card is the step's card; Session the session it works for.
	Card    string `json:"card"`
	Session string `json:"session"`
	Run     int    `json:"run"`
	Recipe  string `json:"recipe"`
	Path    string `json:"path"`
	ID      string `json:"id"`
	// Crumb is the way back, outermost first: the session, the run, the
	// iteration, the step ("Session › deliver run 2 › build ↺ 2 › implement").
	Crumb []string `json:"crumb"`
	Role  string   `json:"role"`
	// Slot is the profile slot the step's recipe named; empty is its
	// role's own.
	Slot  string `json:"slot,omitempty"`
	Model string `json:"model,omitempty"`
	// State is the step's (waiting, running, asking, done, failed,
	// stopped); Reason why it failed or stopped.
	State   string    `json:"state"`
	Reason  string    `json:"reason,omitempty"`
	Spent   float64   `json:"spent"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	// Live marks a step whose agent is at work now.
	Live bool `json:"live,omitempty"`
	// Brief is the prompt as it was filled in; Artifacts what it reads and
	// writes in the run's directory.
	Brief     string         `json:"brief,omitempty"`
	Artifacts []StepArtifact `json:"artifacts,omitempty"`
	// Output is the step's final message, once it has one.
	Output string `json:"output,omitempty"`
	// Says is what a message to the step does now: a turn while it runs,
	// a follow-up once it has ended.
	Says string `json:"says"`
	// Question is the step's open question while it asks, answered with
	// POST /api/cards/{id}/steps/{card}/answer (the strip's Answer opens
	// the step on it).
	Question *StepQuestion `json:"question,omitempty"`
	// CanRevert offers to put the tree back to where the step started
	// (GET/POST …/runs/{n}/revert?step=<path>): its run is not running,
	// and nothing of it is at work.
	CanRevert bool `json:"canRevert,omitempty"`
}

// StepArtifact is one artifact a step's brief names: its name, its path
// in the run's directory, whether it is there yet, and whether the step
// writes it (declares it) or only reads it.
type StepArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Exists bool   `json:"exists,omitempty"`
	Writes bool   `json:"writes,omitempty"`
}

// StepSendRequest is POST /api/cards/{id}/steps/{card}/send: a person's
// message to a step's agent. Answers StepFace.
type StepSendRequest struct {
	Text string `json:"text"`
}

// StepAnswerRequest is POST /api/cards/{id}/steps/{card}/answer: the
// answer to a step's question, an option's label or the person's words.
type StepAnswerRequest struct {
	Answer string `json:"answer"`
	// Call is the question answered (StepQuestion.Call): the answer is
	// refused once the step asks another.
	Call string `json:"call,omitempty"`
}

// SpecList is GET /api/cards/{id}/specs: a session's specs, one per piece
// of work (DESIGN §25.3). Each spec is read with GET
// /api/cards/{id}/spec?spec=<n>.
type SpecList struct {
	Specs []SpecEntry `json:"specs"`
	// CanStart says a new spec may be started (POST …/spec/start).
	CanStart bool `json:"canStart,omitempty"`
}

// SpecEntry is one spec in the list.
type SpecEntry struct {
	N     int    `json:"n"`
	Title string `json:"title"`
	// State is draft, running, met or partial.
	State string `json:"state"`
	// Rev is its newest revision; Frozen the revision an open run froze.
	Rev    int `json:"rev"`
	Frozen int `json:"frozen,omitempty"`
	// DoneWhen counts its done-when items, Met those its last run met.
	DoneWhen int       `json:"doneWhen"`
	Met      int       `json:"met,omitempty"`
	Runs     []SpecRun `json:"runs,omitempty"`
}

// SpecRun is one run on a spec: newest last in a SpecEntry, newest first
// in a session's Spec.
type SpecRun struct {
	N      int     `json:"n"`
	Recipe string  `json:"recipe"`
	State  string  `json:"state"`
	Spent  float64 `json:"spent,omitempty"`
	// Mark is an ended run's state glyph; Met is how many of its checked
	// done-when items it met, of Checked; Ended is when it ended.
	Mark    string    `json:"mark,omitempty"`
	Met     int       `json:"met,omitempty"`
	Checked int       `json:"checked,omitempty"`
	Ended   time.Time `json:"ended,omitzero"`
}

// RevertPreview is GET /api/cards/{id}/runs/{n}/revert: every path that
// differs between the run's starting tree and the tree now, which a
// confirmed revert (POST, answering RevertDone) puts back.
type RevertPreview struct {
	Run   int      `json:"run"`
	Paths []string `json:"paths"`
	// Tree names the tree the preview was read from: the confirmed revert
	// sends it back (RevertRequest), and is refused when the tree has
	// changed since, so what is reverted is what the person was shown.
	Tree string `json:"tree"`
	// Note says what reverting also undoes.
	Note string `json:"note"`
	// Step is the step it reverts to before (?step=), "" for the run's
	// start.
	Step string `json:"step,omitempty"`
}

// RevertRequest is POST /api/cards/{id}/runs/{n}/revert: the confirmed
// revert, naming the preview's Tree.
type RevertRequest struct {
	Tree string `json:"tree"`
	// Step reverts to where that step started rather than the run.
	Step string `json:"step,omitempty"`
}

// SaveRecipeRequest is POST /api/cards/{id}/runs/{n}/save: a person
// saving the run's inline recipe as the workspace recipe Name
// (.gummi/workflows/<name>.yaml). Answers SaveRecipeDone; a name taken by
// a workspace or bundled recipe is refused.
type SaveRecipeRequest struct {
	Name string `json:"name"`
}

// SaveRecipeDone is a saved recipe: its name and its file, relative to
// the workspace.
type SaveRecipeDone struct {
	Run  int    `json:"run"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// RevertDone is a confirmed revert: the paths put back.
type RevertDone struct {
	Run   int      `json:"run"`
	Paths []string `json:"paths"`
}

// RunProposal is a run's proposal (or a revised plan) on its pinned
// decision (Decision.Proposal): what a person reads to allow it.
type RunProposal struct {
	Run int `json:"run"`
	// Kind is "proposal" or "plan".
	Kind        string `json:"kind"`
	Recipe      string `json:"recipe"`
	Description string `json:"description,omitempty"`
	Spec        int    `json:"spec"`
	SpecTitle   string `json:"specTitle,omitempty"`
	// SpecRev is the revision of the spec the run is judged against,
	// frozen when it was proposed.
	SpecRev int            `json:"specRev,omitempty"`
	Steps   []ProposalStep `json:"steps"`
	// Replaces is, on a revised plan, the run's remaining steps the plan
	// would replace: what would not run.
	Replaces []ProposalStep `json:"replaces,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	// Checks are the done-when check commands the run will execute.
	Checks []ProposalCheck `json:"checks,omitempty"`
	// Baseline is those checks run in the tree as it is, before the run.
	Baseline *RunBaseline `json:"baseline,omitempty"`
	// Preflight is the recipe's preflight command, which passed in the
	// tree when the run was proposed, and PreflightOutput what it printed.
	Preflight       string `json:"preflight,omitempty"`
	PreflightOutput string `json:"preflightOutput,omitempty"`
	// PoolLeft is what the session has left, when Bounded by an envelope:
	// the run spends from it, and estimates nothing.
	PoolLeft float64 `json:"poolLeft,omitempty"`
	Bounded  bool    `json:"bounded,omitempty"`
	// Dirty: the tree holds uncommitted work already.
	Dirty  bool   `json:"dirty,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// ProposalStep is one entry of a proposal's step tree, depth first.
type ProposalStep struct {
	ID    string `json:"id"`
	Depth int    `json:"depth"`
	// Kind is "step", "parallel" or "repeat".
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
	// Slot is the profile slot a step names, empty for its role's own;
	// Model what it runs on under the session's profile, in words.
	Slot   string `json:"slot,omitempty"`
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Max    int    `json:"max,omitempty"`
	Until  string `json:"until,omitempty"`
	OnMax  string `json:"onMax,omitempty"`
	Join   string `json:"join,omitempty"`
}

// ProposalCheck is one done-when item's check command.
type ProposalCheck struct {
	Item string `json:"item"`
	Says string `json:"says,omitempty"`
	Cmd  string `json:"cmd"`
}
