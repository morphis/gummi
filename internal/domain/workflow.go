package domain

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// A workflow (DESIGN §25) is a recipe of steps a session runs, one run at
// a time, against one of its specs. Every step is a fresh-context agent
// working in the session's own tree; nothing is branched or landed, and
// gummi snapshots the tree at every step boundary. The types here are the
// recipe as its YAML spells it, the run and its step instances as the
// store keeps them, and a session's specs. Reading a recipe is
// internal/workflowrecipe's; what a run does next is internal/workflowpolicy's.

// WorkflowRole is a step's role, from a fixed catalogue (§25.4). Each role
// has a prompt preamble; the reading roles are told to change no files.
type WorkflowRole string

const (
	// RoleScout reads, reproduces and maps; it changes no files.
	RoleScout WorkflowRole = "scout"
	// RolePlanner plans; it may end with a gummi-workflow block that
	// replaces the run's remaining steps. It has a profile slot of its own.
	RolePlanner WorkflowRole = "planner"
	// RoleContractor writes the design, interfaces and stubs others build on.
	RoleContractor WorkflowRole = "contractor"
	// RoleImplementer builds.
	RoleImplementer WorkflowRole = "implementer"
	// RoleTester writes and extends tests.
	RoleTester WorkflowRole = "tester"
	// RoleScribe writes docs and reports.
	RoleScribe WorkflowRole = "scribe"
	// RoleReviewer reads the work against the spec; it changes no files.
	RoleReviewer WorkflowRole = "reviewer"
)

// WorkflowRoles is the catalogue, in the order a recipe is usually written.
var WorkflowRoles = []WorkflowRole{RoleScout, RolePlanner, RoleContractor, RoleImplementer, RoleTester, RoleScribe, RoleReviewer}

// Known reports whether r is in the catalogue.
func (r WorkflowRole) Known() bool {
	for _, k := range WorkflowRoles {
		if r == k {
			return true
		}
	}
	return false
}

// Reads reports whether r is a reading role: told to change no files. It
// has the full shell any step has — a reviewer runs the tests — so nothing
// holds it to that; a reading step that changes files is flagged, as
// evidence (RunStep.Changed), never refused.
func (r WorkflowRole) Reads() bool { return r == RoleScout || r == RoleReviewer }

// Writes reports whether a step of this role may change the tree. Every
// known role but the reading ones does.
func (r WorkflowRole) Writes() bool { return r.Known() && !r.Reads() }

// Recipe is a workflow as its YAML spells it (§25.4).
type Recipe struct {
	Name        string
	Description string
	// StaleTier marks a recipe whose YAML still names a tier, which gummi
	// no longer reads: a step's model comes from its role's profile slot.
	// Check warns about it; nothing else reads it.
	StaleTier bool
	// Inputs names what workflow_run fills in, each with a hint.
	Inputs map[string]string
	// InputDefaults holds the inputs a run may leave out, each with the
	// text it fills as when it is not given ("" for none). An input not
	// in it is required.
	InputDefaults map[string]string
	// Steps run in order.
	Steps []Entry
}

// Entry is one entry of a recipe's steps: exactly one of Step, Parallel or
// Repeat is set.
type Entry struct {
	Step     *Step
	Parallel *Parallel
	Repeat   *Repeat
}

// ID is the entry's id, whichever kind it is.
func (e Entry) ID() string {
	switch {
	case e.Step != nil:
		return e.Step.ID
	case e.Parallel != nil:
		return e.Parallel.ID
	case e.Repeat != nil:
		return e.Repeat.ID
	}
	return ""
}

// Step is one fresh-context agent's piece of a run.
type Step struct {
	ID     string
	Role   WorkflowRole
	Prompt string
	// Slot names the profile slot the step's model comes from; empty is
	// its role's own (Engine.stepRole has the fallbacks). A recipe names
	// one to run two steps of a role on different models — a second
	// reviewer on `reviewer-alt`, say.
	Slot string
	// Agent and Model name the backend and the model the step runs on
	// directly, instead of a profile slot. Either may stand alone: an Agent
	// with no Model runs that backend's default model; a Model with no Agent
	// runs on the backend the profile gives the step. Both win over Slot.
	Agent string
	Model string
	// StaleTier marks a step whose YAML still names a tier, ignored like
	// the recipe's.
	StaleTier bool
	// StaleEnvelope marks a step whose YAML still names an envelope, which
	// gummi no longer reads: a run binds to its session's budget and
	// estimates nothing (§25.12). Check warns about it; nothing else reads it.
	StaleEnvelope bool
	// Artifacts maps a name to a file name in the run's directory.
	Artifacts map[string]string
	// CaptureOutput, when false, keeps the step's final message out of the
	// run's outputs. Nil reads as true.
	CaptureOutput *bool
	// Run makes the step a command (DESIGN §26.3): it runs in the run's
	// tree with no role, no model and no prompt, and its standard output
	// is its output.
	Run string
	// QuietExit is the exit code with which a command step says "nothing
	// to do": the run ends quietly and leaves no row. Nil: none.
	QuietExit *int
	// Dispatch makes the step mint a session in a named repository (DESIGN
	// §26.4), with no role and no model of its own.
	Dispatch *Dispatch
	// PR makes the step gummi's own publish act on the session's pull
	// request (DESIGN §28.1): no role, no model, no prompt.
	PR *PRAct
	// Watch makes the step wait on the session's pull request without a
	// model, and run its handlers on what happens to it (DESIGN §28.2).
	Watch *Watch
}

// RunsOnSlot is the slot the step's model comes from: its Slot, or none
// when an Agent or Model it names wins over it.
func (s Step) RunsOnSlot() string {
	if s.Agent != "" || s.Model != "" {
		return ""
	}
	return s.Slot
}

// Captures reports whether the step's output is kept for later steps.
func (s Step) Captures() bool { return s.CaptureOutput == nil || *s.CaptureOutput }

// Agentless reports whether the step is gummi's own act — a command or a
// dispatch — rather than a fresh-context agent.
func (s Step) Agentless() bool {
	return s.Run != "" || s.Dispatch != nil || s.PR != nil || s.Watch != nil
}

// Kind names what the step is where a role would: its role, or "run" or
// "dispatch".
func (s Step) Kind() string {
	switch {
	case s.Run != "":
		return "run"
	case s.Dispatch != nil:
		return "dispatch"
	case s.PR != nil:
		return "pr"
	case s.Watch != nil:
		return "watch"
	}
	return string(s.Role)
}

// Dispatch is a dispatch step's session (DESIGN §26.4): minted in Repo,
// a literal the person allowed with the recipe, with an envelope of its
// own carved from the dispatching agent's. With a Recipe the filled Spec
// becomes the session's spec 1 and the recipe's run starts on it at once;
// without one the filled Prompt is the session's opening turn.
type Dispatch struct {
	Repo string
	// Repos are further repositories of the pool attached to the minted
	// session beside Repo (DESIGN §27), literals like Repo.
	Repos  []string
	Title  string
	Spec   string
	Prompt string
	// Recipe names the recipe the session runs; RecipeYAML is that recipe
	// frozen when the run (or the schedule) was proposed, which is what
	// the person allowed and what runs.
	Recipe     string
	RecipeYAML string
	Envelope   int
}

// Join is how a parallel block settles.
type Join string

const (
	// JoinAll fails as soon as one branch fails, and stops the others.
	JoinAll Join = "all"
	// JoinAllSettled waits for every branch, and fails only if all do.
	JoinAllSettled Join = "allSettled"
	// JoinAny settles when one branch succeeds, and stops the rest.
	JoinAny Join = "any"
)

// Known reports whether j is a join.
func (j Join) Known() bool { return j == JoinAll || j == JoinAllSettled || j == JoinAny }

// Parallel runs plain steps side by side in the same tree.
type Parallel struct {
	ID    string
	Join  Join
	Steps []Step
}

// OnMax is what a repeat does at its max without its stop condition.
type OnMax string

const (
	// OnMaxAbort ends the run as partial.
	OnMaxAbort OnMax = "abort"
	// OnMaxContinue goes on to the next entry.
	OnMaxContinue OnMax = "continue"
	// OnMaxPause asks the person: one more iteration, continue, or stop.
	OnMaxPause OnMax = "pause"
)

// Known reports whether o is an onMax; the empty one reads as pause.
func (o OnMax) Known() bool {
	switch o {
	case "", OnMaxAbort, OnMaxContinue, OnMaxPause:
		return true
	}
	return false
}

// Or returns o, or pause when o is empty: a loop that ran out asks the
// person unless its recipe said otherwise.
func (o OnMax) Or() OnMax {
	if o == "" {
		return OnMaxPause
	}
	return o
}

// Repeat runs its steps again until its stop condition holds, at most Max
// times.
type Repeat struct {
	ID    string
	Max   int
	Until Until
	OnMax OnMax
	Steps []Entry
}

// Until is a repeat's stop condition: exactly one of Checks, Verdict (with
// In) or Contains.
type Until struct {
	// Checks: every done-when check of the frozen spec passes in the tree.
	Checks bool
	// Verdict and In: the artifact In's JSON field "verdict" equals Verdict.
	Verdict string
	In      string
	// Contains: the iteration's last step's output contains the text.
	Contains string
}

// Until kinds, as Kind names them.
const (
	UntilChecks   = "checks"
	UntilVerdict  = "verdict"
	UntilContains = "contains"
)

// Kind names the condition, or "" when none or more than one is set.
func (u Until) Kind() string {
	var kinds []string
	if u.Checks {
		kinds = append(kinds, UntilChecks)
	}
	if u.Verdict != "" || u.In != "" {
		kinds = append(kinds, UntilVerdict)
	}
	if u.Contains != "" {
		kinds = append(kinds, UntilContains)
	}
	if len(kinds) != 1 {
		return ""
	}
	return kinds[0]
}

// RunState is where a run is in its life (§25.5).
type RunState string

const (
	// RunProposed waits for the person's answer to the proposal.
	RunProposed RunState = "proposed"
	// RunQueued is allowed, and waits for the open run to end.
	RunQueued RunState = "queued"
	// RunRunning is being conducted.
	RunRunning RunState = "running"
	// RunPaused is parked: a quit, a person's pause, or a spent pool. It
	// runs again only when a person resumes it or tops the session up.
	RunPaused RunState = "paused"
	// RunDone settled every step.
	RunDone RunState = "done"
	// RunPartial ended on a failed step or an aborted loop.
	RunPartial RunState = "partial"
	// RunStopped was stopped by the session agent or the person.
	RunStopped RunState = "stopped"
	// RunDenied was refused by the person.
	RunDenied RunState = "denied"
)

// RevertedToStart is Run.RevertedTo for a revert to the run's start.
const RevertedToStart = "start"

// Open reports whether the run is under way: it holds the session from
// landing (Engine.RunHolds), and a session has at most one.
func (s RunState) Open() bool { return s == RunRunning || s == RunPaused }

// Ended reports whether the run ran and will run no more.
func (s RunState) Ended() bool { return s == RunDone || s == RunPartial || s == RunStopped }

// StepState is where one step instance is.
type StepState string

const (
	// StepWaiting has a row but has not started.
	StepWaiting StepState = "waiting"
	StepRunning StepState = "running"
	// StepAsking waits on the person's answer to its ask_user.
	StepAsking StepState = "asking"
	StepDone   StepState = "done"
	// StepFailed: its backend errored, or its output's last line was
	// FAILED: <why>.
	StepFailed StepState = "failed"
	// StepStopped was interrupted: a stop, or a join that settled without it.
	StepStopped StepState = "stopped"
	// StepWatching is a watch step waiting on its pull request (DESIGN
	// §28.2). The faces say "waiting on the PR"; it is stored apart from
	// StepWaiting, which means a row not yet started.
	StepWatching StepState = "watching"
)

// Live reports whether the step's agent is at work or waiting on a person,
// or a watch is waiting on its pull request.
func (s StepState) Live() bool {
	return s == StepRunning || s == StepAsking || s == StepWatching
}

// Settled reports whether the step will do no more work in its run.
func (s StepState) Settled() bool { return s == StepDone || s == StepFailed || s == StepStopped }

// ItemVerdict is how a run's end found one done-when item (§25.6).
type ItemVerdict string

const (
	ItemMet   ItemVerdict = "met"
	ItemUnmet ItemVerdict = "unmet"
	// ItemUnjudged had no check and no MET:/UNMET: line; drawn as "–".
	ItemUnjudged ItemVerdict = "unjudged"
)

// ItemResult is one done-when item's result at a run's end.
type ItemResult struct {
	Item string // DW-n
	// Checked marks a result from the item's check command; otherwise it
	// came from the last step's MET:/UNMET: line, or there was none.
	Checked bool
	Verdict ItemVerdict
	Detail  string
	// Before is how the item's check found the tree before the run's first
	// step (the run's baseline, RunBaseline), set on a checked item at the
	// run's end; empty when there was no baseline for it.
	Before ItemVerdict `json:"Before,omitempty"`
}

// RunBaseline is the frozen spec's done-when checks run before a run's
// first step (DESIGN §25.5): a check that already passes, or that runs no
// tests, proves nothing about the run, and its baseline says so. Evidence
// for the session and the person, never a gate: nothing waits on what it
// finds.
type RunBaseline struct {
	// Tree is the snapshot of the session's tree the checks ran at, and
	// SpecRev the spec's revision whose items they were.
	Tree    string `json:"tree,omitempty"`
	SpecRev int    `json:"spec_rev,omitempty"`
	// Results holds each checked item's result; an item judged by a step
	// has none.
	Results []ItemResult `json:"results,omitempty"`
	// Running marks a baseline under way. Started marks one taken once the
	// run had started, in the tree its first step starts on.
	Running bool      `json:"running,omitempty"`
	Started bool      `json:"started,omitempty"`
	At      time.Time `json:"at,omitzero"`
	// Err says why the checks could not be run; the run goes on without.
	Err string `json:"err,omitempty"`
	// Told lists the items the session was told prove nothing, so a
	// baseline run again tells it only what is new.
	Told []string `json:"told,omitempty"`
}

// Problems lists the baseline's results that prove nothing about the run:
// a check that already passes, or one that ran no tests.
func (b *RunBaseline) Problems() []ItemResult {
	if b == nil {
		return nil
	}
	var out []ItemResult
	for _, r := range b.Results {
		if r.Verdict == ItemMet || r.Verdict == ItemUnjudged {
			out = append(out, r)
		}
	}
	return out
}

// Run is one execution of a workflow on a session.
type Run struct {
	Session FeatureID
	// N counts a session's runs from 1.
	N int
	// RecipeName is the recipe's name, or empty for an inline one.
	RecipeName string
	// Recipe is the copy taken when the run was proposed: what the person
	// allowed, and what runs, revised only by a planner's plan the person
	// allowed. RecipeYAML is the same copy as text.
	Recipe     Recipe
	RecipeYAML string
	Inputs     map[string]string
	// Spec is the spec's number on the session; SpecRev the revision the
	// run froze when it started (0 until then).
	Spec    int
	SpecRev int
	State   RunState
	// Spent is every step's spend together. A run estimates nothing: it
	// spends from the session's envelope, the one pool (§24.7a), until
	// that is spent.
	Spent float64
	// StartTree is the tree snapshot when the run started; EndTree the one
	// when its last step settled.
	StartTree string
	EndTree   string
	// StartHead is the session's HEAD when the run started: the commits
	// made while it ran are the ones after it, which its report names.
	StartHead string
	// StartIndex is the session's index when the run started, as a tree:
	// Revert run puts the index entries of the paths it puts back as they
	// were then. Empty on a run from before, or a checkout with no index.
	StartIndex string
	// Active is how long the run has worked, its pauses left out, up to
	// ResumedAt: when it last started or resumed running, zero while it is
	// not running. A face counts a running run's to now.
	Active    time.Duration
	ResumedAt time.Time
	// Commits are the commits made in the session's tree while the run ran
	// ("<short sha> <subject>", oldest last), recorded when it ended: gummi
	// commits nothing, so these are a step's or the session's own.
	Commits []string
	Results []ItemResult
	// Baseline is the done-when checks run before the run's first step, nil
	// until one has been asked for (§25.5).
	Baseline *RunBaseline
	// SavedAs is the workspace recipe a person saved this run's inline
	// recipe as (.gummi/workflows/<SavedAs>.yaml), empty until one does.
	SavedAs string
	// ReportPending is set in the write that ends the run and cleared once
	// its report has been written out and sent: a run found ended with it
	// still set lost the delivery to a crash, and is delivered again.
	ReportPending bool
	// RevertedTo is where a person last reverted the run's tree to:
	// RevertedToStart for the run's start, else the path of the step whose
	// start it was put back to. Empty until a revert. A run reverted to its
	// start has nothing of its own left in the tree to revert.
	RevertedTo string
	// Schedule is the schedule that started the run (DESIGN §26.2), empty
	// for a run a session proposed. A quiet end records no-change on it.
	Schedule ScheduleID
	// Until holds each repeat iteration's stop condition as the engine
	// judged it, keyed by the iteration's path (build/2).
	Until map[string]UntilJudgement
	// OnMax holds the person's answers to a repeat at its max, keyed by the
	// iteration's path: more, continue or stop.
	OnMax map[string]string
	// Report is the run's report, also written into its spec.
	Report string
	// Reason says why a run ended partial or stopped, or why it paused.
	Reason string
	// Stopping marks a workflow_stop or a person's stop not yet wound down.
	Stopping bool
	// OutOfBudget marks a run paused because the pool was spent: a raise of
	// the session's envelope resumes it.
	OutOfBudget bool
	// Steers holds workflow_steer notes, in order, for steps not yet started.
	Steers []string
	// Pending is the run's one open question to its person, nil when none.
	Pending *RunDecision
	// Feedback is the person's typed answer to the proposal, delivered to
	// the session agent as a turn; empty when there was none.
	Feedback string
	// Revision counts changes to the run, so a face redraws only on one.
	Revision  int
	CreatedAt time.Time
	StartedAt time.Time
	EndedAt   time.Time
	UpdatedAt time.Time
}

// UntilJudgement is one repeat iteration's stop condition, judged.
// Unjudged marks one gummi could not judge — the artifact its verdict is
// read from was never written, or a check ran no tests — which is put to
// the person rather than counted as not met.
type UntilJudgement struct {
	Met      bool   `json:"met"`
	Unjudged bool   `json:"unjudged,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// RunDecisionKind is what a run asks its person (§25.5, §25.7).
type RunDecisionKind string

const (
	// RunDecideProposal: allow this run? Allow, always allow the session's
	// runs, deny, or typed feedback.
	RunDecideProposal RunDecisionKind = "proposal"
	// RunDecidePlan: a planner step's revised plan, to allow or deny.
	RunDecidePlan RunDecisionKind = "plan"
	// RunDecideMax: a repeat reached its max without its condition and its
	// onMax is pause — one more iteration, continue, or stop.
	RunDecideMax RunDecisionKind = "max"
	// RunDecideUnjudged: a repeat's condition could not be judged after an
	// iteration — its artifact was never written, or a check ran no tests
	// — so the run paused: one more iteration, continue, or stop.
	RunDecideUnjudged RunDecisionKind = "unjudged"
	// RunDecideBudget: the session's pool is spent and the run paused.
	// Amount is the top-up suggested.
	RunDecideBudget RunDecisionKind = "budget"
	// RunDecideResume: gummi stopped while the run ran, and it was parked
	// when gummi came back. A parked run never resumes itself (§25.7): the
	// person resumes it or stops it.
	RunDecideResume RunDecisionKind = "resume"
	// RunDecideWatch: a watch step's question (DESIGN §28.4) — a loop on
	// the same event, a pass that changed nothing, a max, a trust filter
	// that could not be read, a push that failed, a quiet PR.
	RunDecideWatch RunDecisionKind = "watch"
)

// RunDecision is a run's one open question to its person. A step's own
// ask_user is not one: it is a question on the step's card.
type RunDecision struct {
	ID       string          `json:"id"`
	Kind     RunDecisionKind `json:"kind"`
	Question string          `json:"question"`
	Options  []string        `json:"options,omitempty"`
	// Step is the path of the step or repeat the decision is about.
	Step string `json:"step,omitempty"`
	// RecipeYAML is a revised plan's replacement steps, as the planner
	// wrote them; Warnings are Check's on the proposal or the revision.
	RecipeYAML string   `json:"recipe_yaml,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	// Amount is a budget decision's top-up, in credits.
	Amount int `json:"amount,omitempty"`
}

// RunStep is one step instance: a step of the recipe at a path, as in
// build/2/implement for iteration 2 of build. Its card is a hidden child
// of the session whose agent works in the session's checkout.
type RunStep struct {
	Session FeatureID
	Run     int
	Path    string
	StepID  string
	Card    FeatureID // empty until the step starts
	Role    WorkflowRole
	// Slot is the step's profile slot as its recipe named it; empty is its
	// role's own.
	Slot  string
	State StepState
	Spent float64
	// StartTree and EndTree are the snapshots at the step's start and end;
	// the step's own diff runs between them.
	StartTree string
	EndTree   string
	// Changed lists the paths a reading step changed between its start and
	// end snapshots, recorded when it ends: a reading role is told to
	// change no files, and one that did is flagged. Nil for a writing step
	// or a reading step that changed nothing.
	Changed []string
	// Prompt is the brief as filled in; Output the step's final message.
	Prompt string
	Output string
	// Reason says why a step failed or stopped.
	Reason    string
	StartedAt time.Time
	EndedAt   time.Time
	// Watch is a watch step's memory (DESIGN §28.2); nil on every other.
	Watch *WatchState
}

// StepPath joins path segments: a repeat's id and iteration, a parallel's
// id, and the step's id.
func StepPath(parts ...string) string { return strings.Join(parts, "/") }

// IterationPath is the path of a repeat's iteration under prefix.
func IterationPath(prefix, repeatID string, n int) string {
	return joinPrefix(prefix, repeatID+"/"+strconv.Itoa(n))
}

func joinPrefix(prefix, rest string) string {
	if prefix == "" {
		return rest
	}
	return prefix + "/" + rest
}

// SpecState is a session spec's state, derived from its runs (§25.3).
type SpecState string

const (
	// SpecDraft has never run.
	SpecDraft SpecState = "draft"
	// SpecRunning has a run open on it.
	SpecRunning SpecState = "running"
	// SpecMet: its last run's validation passed every checked item.
	SpecMet SpecState = "met"
	// SpecPartial is anything else.
	SpecPartial SpecState = "partial"
)

// SessionSpecPath is where a new spec's file goes, relative to the
// workspace root: .gummi/specs/<ID>/<n>-<slug>.md, the slug from its title
// (spec when the title yields none). A session's spec from before
// keeps its old path (Feature.SpecPath) as spec 1.
func SessionSpecPath(id FeatureID, n int, title string) string {
	slug, err := Slugify(title)
	if err != nil {
		slug = "spec"
	}
	return path.Join(".gummi", "specs", string(id), strconv.Itoa(n)+"-"+slug+".md")
}

// SessionSpec is one of a session's specs: one per piece of work.
type SessionSpec struct {
	Feature FeatureID
	// N counts a session's specs from 1.
	N     int
	Title string
	// Path is the spec's file, .gummi/specs/<ID>/<n>-<slug>.md (a spec
	// registered from before is at its old path).
	Path      string
	State     SpecState
	CreatedAt time.Time
}

// SpecStateOf derives a spec's state from the runs on it, in any order:
// running while one is open, draft until one has ended, then met when the
// latest ended run's results are all met (ResultsMet) and partial
// otherwise.
func SpecStateOf(runs []Run) SpecState {
	var last *Run
	for i := range runs {
		r := &runs[i]
		if r.State.Open() {
			return SpecRunning
		}
		if r.State.Ended() && (last == nil || r.N > last.N) {
			last = r
		}
	}
	switch {
	case last == nil:
		return SpecDraft
	case last.State != RunStopped && ResultsMet(last.Results):
		return SpecMet
	}
	return SpecPartial
}

// ResultsMet reports whether a run's end found its spec met: at least one
// item was checked, every checked item passed, and no item is unjudged —
// a check that ran no tests is no pass.
func ResultsMet(rs []ItemResult) bool {
	checked := 0
	for _, r := range rs {
		if r.Verdict == ItemUnjudged || r.Checked && r.Verdict != ItemMet {
			return false
		}
		if r.Checked {
			checked++
		}
	}
	return checked > 0
}

// QualifiedItem names a done-when item by its spec as well, as in S2/DW-1,
// for any view where two specs meet.
func QualifiedItem(spec int, item string) string { return fmt.Sprintf("S%d/%s", spec, item) }

// ParseQualifiedItem reads S<n>/DW-k back into the spec's number and the
// item's id.
func ParseQualifiedItem(s string) (spec int, item string, err error) {
	head, item, ok := strings.Cut(strings.TrimSpace(s), "/")
	n, convErr := strconv.Atoi(strings.TrimPrefix(head, "S"))
	if !ok || !strings.HasPrefix(head, "S") || convErr != nil || n < 1 || !doneWhenIDRe.MatchString(item) {
		return 0, "", fmt.Errorf("%q is not of the form S<n>/DW-<k>", s)
	}
	return n, item, nil
}

// SpecRevision is one saved state of a session's spec.
type SpecRevision struct {
	// Spec is the spec's number on its session (DESIGN §25.3); revisions
	// count from 1 per spec.
	Spec    int
	Rev     int
	Content string
	By      string
	At      time.Time
}

// RetryRefusal says why run cannot be retried, "" when it can: a person
// retrying a run that ended partial or stopped runs its failed and stopped
// steps again, keeping every step that finished (DESIGN §25.6). Only the
// session's newest run that started may be, while no other run is open or
// queued — a retry reopens it in the tree — and only one with something to
// run again: a step that failed or stopped, or a loop a person stopped.
// steps are run's; runs are the session's every run.
func RetryRefusal(run Run, steps []RunStep, runs []Run) string {
	if run.State != RunPartial && run.State != RunStopped {
		return fmt.Sprintf("run %d is %s: only a run that ended partial or stopped is retried", run.N, run.State)
	}
	if run.StartTree == "" {
		return fmt.Sprintf("run %d never started", run.N)
	}
	for _, o := range runs {
		switch {
		case o.N == run.N:
		case o.State.Open() || o.State == RunQueued:
			return fmt.Sprintf("run %d is %s: retry once it has ended", o.N, o.State)
		case o.N > run.N && o.StartTree != "":
			return fmt.Sprintf("run %d ran after it: only the newest run is retried", o.N)
		}
	}
	for _, s := range steps {
		if !IsRetriedPath(s.Path) && (s.State == StepFailed || s.State == StepStopped) {
			return ""
		}
	}
	for _, a := range run.OnMax {
		if a == "stop" {
			return ""
		}
	}
	return fmt.Sprintf("run %d has no failed or stopped step to run again", run.N)
}

// RetriedPath is where a retried step's earlier attempt is kept: its path
// with the attempt's number, as in build/2/implement@1. An entry's id never
// holds "@", so the path names no step of the recipe.
func RetriedPath(path string, attempt int) string {
	return fmt.Sprintf("%s@%d", path, attempt)
}

// IsRetriedPath reports whether path is an earlier attempt a retry kept
// (RetriedPath), which is never run again nor counted as a step to.
func IsRetriedPath(path string) bool { return strings.Contains(path, "@") }
