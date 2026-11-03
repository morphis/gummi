package threadfold

// A session's workflow runs in its thread (DESIGN §25.9). Each run is one
// item, keyed run:<n>, folded from the run and its steps as the store
// keeps them (Options.Runs) — never from an event per step action — and
// updated in place: its Rev moves whenever anything the block draws does,
// so a face redraws the block only then. Both faces draw the block from
// this fold, and every word in it is said here.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// ItemRun is a workflow run's block.
const ItemRun ItemType = "run"

// RunInput is one of the session's runs as the fold reads it: the run and
// its step instances from the store, with what the store does not hold —
// the spec's title, the files the run has changed, and each live step's
// session — read by the caller.
type RunInput struct {
	Run   domain.Run
	Steps []domain.RunStep
	// SpecTitle is the run's spec's title.
	SpecTitle string
	// Files are the paths that differ between the run's starting tree and
	// its newest snapshot: what it has changed so far, or what it left.
	Files []string
	// Live is each live step's session, by its path, and the session of
	// an ended step that is working again (a person's follow-up).
	Live map[string]StepLive
	// Envelope is the session's envelope, in credits (0 uncapped): what a
	// strip paused on a spent pool says is spent.
	Envelope float64
	// Phase is what gummi itself is doing on the run between its steps
	// (engine.RunPhase): "baseline", "until", "ending", or "".
	Phase string
	// AlwaysAllowed says a person always allows the session's runs; the
	// session's newest run says so, with a way to ask again.
	AlwaysAllowed bool
	// Artifacts are the run's declared artifacts in its directory, and
	// whether each has been written.
	Artifacts []RunArtifact
}

// RunArtifact is one artifact a run's recipe declares.
type RunArtifact struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Exists bool   `json:"exists,omitempty"`
}

// StepLive is what a step's own session says while it works.
type StepLive struct {
	// Model is what the step's agent runs on.
	Model string
	// Busy says a turn is in flight on the step's card.
	Busy bool
	// Spent is the step card's spend so far, in credits.
	Spent float64
	// Action is the call the step's agent has in flight, or its last one,
	// as its tool row labels it ("Edit  internal/x.go").
	Action string
	// Ask is the step's open ask_user question, nil when none.
	Ask *engine.Ask
}

// Run is a run's block.
type Run struct {
	N int `json:"n"`
	// Rev changes whenever anything else in the block does: a face that
	// holds a block at the same Rev has nothing to redraw.
	Rev    string `json:"rev"`
	Recipe string `json:"recipe"`
	// Spec is the spec's number on the session, SpecTitle its title, and
	// SpecRev the revision the run froze (0 until it starts).
	Spec      int    `json:"spec"`
	SpecTitle string `json:"specTitle,omitempty"`
	SpecRev   int    `json:"specRev,omitempty"`
	// State is the run's (domain.RunState); Reason why it ended partial
	// or stopped, or why it paused. Stopping marks a stop not yet wound
	// down.
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Stopping bool   `json:"stopping,omitempty"`
	// Spent is what its steps have spent, live ones included, in credits:
	// a run estimates nothing, and spends from the session's budget.
	Spent float64 `json:"spent"`
	// Started and Ended bound the run. ActiveMs is how long it has
	// worked, its pauses left out, up to Resumed — when it last started
	// running, zero while it is not: a face counts a running run's
	// elapsed time as ActiveMs and the time since Resumed.
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
	ActiveMs int64     `json:"activeMs,omitempty"`
	Resumed  time.Time `json:"resumed,omitzero"`
	// Phase is what gummi does on the run between its steps: "baseline"
	// (the checks before the first step), "until" (judging a loop),
	// "ending" (the end checks and the report), or "".
	Phase string `json:"phase,omitempty"`
	// Strip is the recipe's top-level entries, each in its state.
	Strip []StripEntry `json:"strip"`
	// Rows is a row per step that has run or is running, in the order they
	// started, a finished repeat iteration folded to one.
	Rows []StepRow `json:"rows"`
	// Pending is the run's open decision (the proposal, a revised plan, a
	// loop at its max, the budget). The decision itself is the
	// session's pinned decision; the block only says one is pending.
	Pending *RunPending `json:"pending,omitempty"`
	// Baseline is the done-when checks run before the run's first step
	// (§25.5), drawn while the run is proposed or queued, or open with no
	// step yet; nil otherwise, and for a spec with no checks.
	Baseline *RunBaseline `json:"baseline,omitempty"`
	// Files are the paths the run has changed so far, uncommitted, or
	// those an ended run left changed.
	Files []string `json:"files,omitempty"`
	// Results, Commits and Report close an ended run: its result per
	// done-when item, the commits made while it ran, and whether its
	// report was written into its spec.
	Results []RunResult `json:"results,omitempty"`
	Commits []string    `json:"commits,omitempty"`
	Report  bool        `json:"report,omitempty"`
	// What a person may do to the run now: Pause, Resume and Stop it, and
	// Revert it (a run that started, with no step running).
	CanPause  bool `json:"canPause,omitempty"`
	CanResume bool `json:"canResume,omitempty"`
	CanStop   bool `json:"canStop,omitempty"`
	CanRevert bool `json:"canRevert,omitempty"`
	// CanWithdraw offers to take back a run allowed and queued behind the
	// open one, before it starts.
	CanWithdraw bool `json:"canWithdraw,omitempty"`
	// CanRetry offers to run an ended run's failed and stopped steps again
	// (domain.RetryRefusal), set where all the session's runs are folded.
	CanRetry bool `json:"canRetry,omitempty"`
	// CanForward offers to forward to the open run's watch what its trust
	// filter kept out (DESIGN §28.3).
	CanForward bool `json:"canForward,omitempty"`
	// AlwaysAllowed is set on the session's newest run while a person
	// always allows its runs: the block says so, with Ask again.
	AlwaysAllowed bool `json:"alwaysAllowed,omitempty"`
	// Artifacts are what the recipe declares in the run's directory — a
	// report, a design, a review — each to be read.
	Artifacts []RunArtifact `json:"artifacts,omitempty"`
	// CanSave offers to save the run's inline recipe as a workspace recipe
	// (a person's act), SaveName the name it suggests; SavedAs is the name
	// it was saved as, after which it is not offered again.
	CanSave  bool   `json:"canSave,omitempty"`
	SaveName string `json:"saveName,omitempty"`
	SavedAs  string `json:"savedAs,omitempty"`
	// Live is the open run's live strip, drawn above the composer; nil
	// for a run that is not running or paused.
	Live *RunStrip `json:"live,omitempty"`
	// Mark and Line are an ended run in one line, the way it folds in the
	// thread (§25.9): its state's glyph, and "deliver · run 2 · spec 2
	// “Close the TUI's gaps” · met 5/5 · $2.92 · 14 files". Empty on a run
	// that has not ended.
	Mark string `json:"mark,omitempty"`
	Line string `json:"line,omitempty"`
	// Folded says an ended run is folded to its Line by rule: it is no
	// longer the latest news (FoldRuns). A face draws it as the one line
	// until a person unfolds it; a person's unfolding is the face's own.
	Folded bool `json:"folded,omitempty"`
}

// StripEntry is one top-level entry of the recipe.
type StripEntry struct {
	ID string `json:"id"`
	// Kind is "step", "parallel" or "repeat".
	Kind string `json:"kind"`
	// State is waiting, running, asking, done or failed.
	State string `json:"state"`
	// A repeat's iteration under way (or last run) of its Max.
	Iter int `json:"iter,omitempty"`
	Max  int `json:"max,omitempty"`
	// A parallel's branches, joined by ∥ where it is drawn.
	Branches []StripEntry `json:"branches,omitempty"`
}

// StepRow is one row of the block: a step, or a finished iteration of a
// repeat folded to one.
type StepRow struct {
	// Key is the row's identity: the step's path, or the iteration's.
	Key  string `json:"key"`
	Path string `json:"path"`
	// Repeat and Iter place a row inside a repeat's iteration ("↺ 2").
	Repeat string `json:"repeat,omitempty"`
	Iter   int    `json:"iter,omitempty"`
	// ID is the step's id (the repeat's, on a folded iteration); Role its
	// role, empty on a folded iteration.
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
	// State is the step's (domain.StepState) as its own session has it
	// now: a live step asking while its question is open and running
	// otherwise, and an ended step running while a person's follow-up is
	// in flight. A folded iteration's is done, or failed when one of its
	// steps failed.
	State string  `json:"state"`
	Spent float64 `json:"spent"`
	// Line is the step's last action while it works, "asks you" while it
	// waits on the person, and its end summary once it has ended.
	Line string `json:"line,omitempty"`
	// Flag is what the row warns of, beside its line: a reading step that
	// changed files ("changed 2 files although it only reads"). Evidence
	// for the person, never a gate.
	Flag string `json:"flag,omitempty"`
	// Card is the step's card, the one Open shows and a message goes to.
	Card string `json:"card,omitempty"`
	// Folded marks a finished iteration: Steps names what ran in it
	// ("implement · review-a ∥ review-b · verdict") and Verdict how its
	// stop condition was judged.
	Folded  bool   `json:"folded,omitempty"`
	Steps   string `json:"steps,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Waiting marks the row the run's open decision is about.
	Waiting bool `json:"waiting,omitempty"`
	// Watch is a watch step's summary (DESIGN §28.7); Pass and Event place
	// a row inside one of its passes, and on a pass's own header row (ID
	// "pass 2") name the event it ran for.
	Watch *WatchRow `json:"watch,omitempty"`
	Pass  int       `json:"pass,omitempty"`
	Event string    `json:"event,omitempty"`
	// Started and Ended bound the step's time; Model is what its agent
	// runs on, while it works.
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	Model   string    `json:"model,omitempty"`
	// Question is the step's own open question, drawn under its row.
	Question *StepQuestion `json:"question,omitempty"`
}

// StepQuestion is a step's ask_user question, its recommended answer
// first.
type StepQuestion struct {
	// Call names the question: an answer names it back, and is refused
	// once the step asks another.
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

// RunPending is the run's open decision, as the block mentions it.
type RunPending struct {
	Kind     string   `json:"kind"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Step     string   `json:"step,omitempty"`
}

// RunResult is one done-when item at the run's end.
type RunResult struct {
	Item string `json:"item"`
	// Verdict is met, unmet or unjudged; Checked marks one its check
	// command decided.
	Verdict string `json:"verdict"`
	Checked bool   `json:"checked,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Before is a checked item's verdict in the run's baseline, and Change
	// its result before and after in words ("failed before the run, now
	// passes").
	Before string `json:"before,omitempty"`
	Change string `json:"change,omitempty"`
}

// RunBaseline is the run's baseline as its block draws it: still running,
// or each checked item before the run, those that prove nothing marked.
type RunBaseline struct {
	Running bool           `json:"running,omitempty"`
	Err     string         `json:"err,omitempty"`
	Items   []BaselineItem `json:"items,omitempty"`
}

// BaselineItem is one checked item's baseline: "DW-2 already passes
// before the run" (a Problem), "DW-1 fails before the run".
type BaselineItem struct {
	Item    string `json:"item"`
	Verdict string `json:"verdict"`
	Line    string `json:"line"`
	Problem bool   `json:"problem,omitempty"`
}

// BaselineWords is what a baseline still running says.
const BaselineWords = "running the done-when checks before the run…"

// foldBaseline is the run's baseline as its block draws it.
func foldBaseline(b *domain.RunBaseline) *RunBaseline {
	p := engine.ProposalBaselineOf(b)
	if p == nil {
		return nil
	}
	out := &RunBaseline{Running: p.Running, Err: Sanitize(firstLineOf(p.Err))}
	for _, it := range p.Items {
		out.Items = append(out.Items, BaselineItem{Item: it.Item, Verdict: string(it.Verdict), Line: it.Line, Problem: it.Problem})
	}
	return out
}

// runItems folds the session's runs into their items, the newest last.
// said is when a person last said something to the session (PersonSaid).
func runItems(runs []RunInput, said time.Time) []Item {
	out := make([]Item, 0, len(runs))
	for i, r := range FoldRuns(runs, said) {
		out = append(out, Item{Key: "run:" + strconv.Itoa(r.N), T: ItemRun, At: runs[i].Run.CreatedAt, Stage: domain.StageOpen, Run: &r})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Run.N < out[j].Run.N })
	return out
}

// FoldRuns folds each of a session's runs (FoldRun), in the order given,
// and folds an ended one to its line once it is no longer the latest
// news (§25.9). The run that ended most recently stays whole until a
// person says something to the session after it ended (said, PersonSaid)
// or another run is proposed after it ended; every other ended run is
// folded. An open run is never folded, nor one that never ran.
func FoldRuns(ins []RunInput, said time.Time) []Run {
	out := make([]Run, len(ins))
	latest := -1
	var latestAt time.Time
	latestN := 0
	all := make([]domain.Run, len(ins))
	for i, in := range ins {
		all[i] = in.Run
	}
	for i, in := range ins {
		out[i] = foldRun(in)
		if domain.RetryRefusal(in.Run, in.Steps, all) == "" {
			out[i].CanRetry = true
		}
		if !in.Run.State.Ended() {
			continue
		}
		at := in.Run.EndedAt
		if latest < 0 || at.After(latestAt) || (at.Equal(latestAt) && in.Run.N > latestN) {
			latest, latestAt, latestN = i, at, in.Run.N
		}
	}
	for i, in := range ins {
		if !in.Run.State.Ended() {
			continue
		}
		fold := i != latest || said.After(in.Run.EndedAt)
		for _, o := range ins {
			if o.Run.N != in.Run.N && o.Run.CreatedAt.After(in.Run.EndedAt) {
				fold = true
			}
		}
		if fold {
			out[i].Folded = true
		}
	}
	for i := range out {
		out[i].Rev = runRev(out[i])
	}
	return out
}

// PersonSaid is when a person last said something in a session's
// transcript: a line of theirs, never one gummi sent on its own (an
// objective's turn, a run's report or an old crew's), zero when none.
func PersonSaid(tr []engine.Message) time.Time {
	for i := len(tr) - 1; i >= 0; i-- {
		msg := tr[i]
		if msg.Author != engine.AuthorUser || msg.At.IsZero() {
			continue
		}
		switch TurnBy(msg.By) {
		case "gummi", "workflow", "crew":
			continue
		}
		return msg.At
	}
	return time.Time{}
}

// ConversationStart is when a session's conversation as it stands began:
// its first turn's moment. Zero when no turn carries one.
func ConversationStart(tr []engine.Message) time.Time {
	for _, msg := range tr {
		if !msg.At.IsZero() {
			return msg.At
		}
	}
	return time.Time{}
}

// FoldSince is the moment a session's runs that ended before it stop
// being the latest news (FoldRuns' said): the later of when a person last
// said something (PersonSaid) and when its conversation as it stands began
// (ConversationStart). A run a /clear or /handoff left behind with the
// conversation it belonged to is still drawn, folded to its line, so its
// steps and artifacts stay a click away.
func FoldSince(tr []engine.Message) time.Time {
	said, start := PersonSaid(tr), ConversationStart(tr)
	if start.After(said) {
		return start
	}
	return said
}

// RunLeftBehind says whether a run in state that ended at ended belongs to
// a conversation before the one that began at start: one FoldRuns folds
// once its said is FoldSince.
func RunLeftBehind(state domain.RunState, ended, start time.Time) bool {
	return !start.IsZero() && state.Ended() && ended.Before(start)
}

// TurnMark is one turn of a conversation as a run's place among the turns
// reads it: when it was written, and whether it sits inside an activity
// row (a tool call, a thought, the agent's task list), which a run's
// block never splits.
type TurnMark struct {
	At       time.Time
	Activity bool
}

// TranscriptMarks are a transcript's turns as RunPlace reads them.
func TranscriptMarks(tr []engine.Message) []TurnMark {
	out := make([]TurnMark, len(tr))
	for i, msg := range tr {
		out[i] = TurnMark{At: msg.At, Activity: msg.Author == engine.AuthorTool || msg.Author == engine.AuthorThinking}
	}
	return out
}

// RunPlace is where a run proposed at at stands among a conversation's
// turns, the one rule both faces place a run's block by (§25.9): before
// turn i, the first turn written after it that is no part of an activity
// row; len(turns) after every turn. It is -1 when the run predates every
// turn given: it stands among the thread's settled items, where Items
// places it, above the conversation.
func RunPlace(turns []TurnMark, at time.Time) int {
	seen := false
	for i, t := range turns {
		if t.At.IsZero() {
			continue
		}
		if !seen {
			seen = true
			if t.At.After(at) {
				return -1
			}
		}
		if !t.Activity && t.At.After(at) {
			return i
		}
	}
	return len(turns)
}

// endedLine is an ended run in one line: its glyph, and its recipe and
// number, its state's word when it did not end done, its spec, its checked
// items met, its spend and the files it left changed.
func endedLine(r Run) (string, string) {
	mark, word := "✓", ""
	switch domain.RunState(r.State) {
	case domain.RunPartial:
		mark, word = "◐", "partial"
	case domain.RunStopped:
		mark, word = "■", "stopped"
	}
	parts := []string{RunLine(r)}
	if word != "" {
		parts = append(parts, word)
	}
	spec := fmt.Sprintf("spec %d", r.Spec)
	if r.SpecTitle != "" {
		spec += " “" + cut(r.SpecTitle, 48) + "”"
	}
	parts = append(parts, spec)
	if met, of := RunMet(r); of > 0 {
		parts = append(parts, fmt.Sprintf("met %d/%d", met, of))
	}
	parts = append(parts, domain.FormatDollars(r.Spent))
	switch n := len(r.Files); n {
	case 0:
	case 1:
		parts = append(parts, "1 file")
	default:
		parts = append(parts, fmt.Sprintf("%d files", n))
	}
	return mark, strings.Join(parts, " · ")
}

// RunMet is how many of an ended run's checked done-when items it met, of
// how many were checked.
func RunMet(r Run) (met, of int) {
	for _, x := range r.Results {
		if !x.Checked {
			continue
		}
		of++
		if x.Verdict == string(domain.ItemMet) {
			met++
		}
	}
	return met, of
}

// placeRuns puts each run item into the thread where its run was
// proposed: before the first item from after that moment. A run item
// carries the Seq of the item before it, so paging never skips past an
// event (Since delivers every run item on every page).
func placeRuns(items, runs []Item) []Item {
	if len(runs) == 0 {
		return items
	}
	out := make([]Item, 0, len(items)+len(runs))
	var seq int64
	k := 0
	for _, it := range items {
		for k < len(runs) && runs[k].At.Before(it.At) {
			runs[k].Seq = seq
			out = append(out, runs[k])
			k++
		}
		out = append(out, it)
		seq = it.Seq
	}
	for ; k < len(runs); k++ {
		runs[k].Seq = seq
		out = append(out, runs[k])
	}
	return out
}

// FoldRun folds one run into its block.
func FoldRun(in RunInput) Run {
	r := foldRun(in)
	r.Rev = runRev(r)
	return r
}

// foldRun is FoldRun without the block's Rev, which FoldRuns computes
// once, after it has settled the fields it moves (Folded, CanRetry).
func foldRun(in RunInput) Run {
	run := in.Run
	r := Run{
		N: run.N, Recipe: runRecipe(run), Spec: run.Spec, SpecTitle: Sanitize(in.SpecTitle), SpecRev: run.SpecRev,
		State: string(run.State), Reason: Sanitize(run.Reason), Stopping: run.Stopping,
		Started: run.StartedAt, Ended: run.EndedAt, ActiveMs: run.Active.Milliseconds(), Resumed: run.ResumedAt,
		Files: in.Files, Strip: []StripEntry{}, Rows: []StepRow{},
	}
	if run.State.Open() {
		r.Phase = in.Phase
	}
	r.AlwaysAllowed = in.AlwaysAllowed
	for _, a := range in.Artifacts {
		r.Artifacts = append(r.Artifacts, RunArtifact{Name: Sanitize(a.Name), File: Sanitize(a.File), Exists: a.Exists})
	}
	steps := append([]domain.RunStep(nil), in.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i].StartedAt, steps[j].StartedAt
		switch {
		case a.IsZero() != b.IsZero():
			return !a.IsZero()
		case !a.Equal(b):
			return a.Before(b)
		}
		return steps[i].Path < steps[j].Path
	})
	live := false
	for _, s := range steps {
		r.Spent += stepSpent(s, in.Live)
		live = live || s.State.Live()
	}
	r.Spent = roundCredits(r.Spent)
	// the strip reads a live step as its row does (liveState); an ended
	// step's follow-up moves its row, not where the recipe stands
	now := append([]domain.RunStep(nil), steps...)
	for i, s := range now {
		if l, seen := in.Live[s.Path]; seen && s.State.Live() {
			now[i].State = liveState(s, l, seen)
		}
	}
	for _, en := range run.Recipe.Steps {
		r.Strip = append(r.Strip, stripEntry(run, en, "", now))
	}
	r.Rows = stepRows(run, steps, in.Live)
	if run.State == domain.RunPaused {
		// a paused run's steps have no backend: none of them works now,
		// whatever its row last said
		for i := range r.Rows {
			if row := &r.Rows[i]; !row.Folded && row.Watch == nil && domain.StepState(row.State).Live() && row.Line != "its revised plan waits on you" {
				row.State, row.Line = RowPaused, "stopped where it was · carries on when the run resumes"
			}
		}
	}
	if d := run.Pending; d != nil {
		r.Pending = &RunPending{Kind: string(d.Kind), Question: Sanitize(d.Question), Options: d.Options, Step: d.Step}
		for i := range r.Rows {
			if d.Step != "" && (r.Rows[i].Key == d.Step || r.Rows[i].Path == d.Step) {
				r.Rows[i].Waiting = true
			}
		}
	}
	switch {
	case run.State == domain.RunProposed, run.State == domain.RunQueued, run.State.Open() && len(r.Rows) == 0:
		r.Baseline = foldBaseline(run.Baseline)
	}
	if run.State.Ended() {
		for _, res := range run.Results {
			r.Results = append(r.Results, RunResult{
				Item: res.Item, Verdict: string(res.Verdict), Checked: res.Checked, Detail: Sanitize(firstLineOf(res.Detail)),
				Before: string(res.Before), Change: engine.ResultChange(res),
			})
		}
		for _, c := range run.Commits {
			r.Commits = append(r.Commits, Sanitize(c))
		}
		r.Report = run.Report != ""
	}
	// while gummi runs the end checks nothing a person does lands: the end
	// overwrites a pause, ignores a stop, and a revert would race the checks
	ending := r.Phase == "ending"
	r.CanPause = run.State == domain.RunRunning && !run.Stopping && !ending
	r.CanResume = run.State == domain.RunPaused && !run.Stopping
	r.CanStop = run.State.Open() && !run.Stopping && !ending
	// a run reverted to its start has nothing of its own left to put back
	r.CanRevert = run.StartTree != "" && !live && len(r.Files) > 0 && run.State != domain.RunRunning && r.Phase == "" &&
		run.RevertedTo != domain.RevertedToStart
	r.CanWithdraw = run.State == domain.RunQueued
	if run.State.Open() && !run.Stopping {
		for _, s := range in.Steps {
			if s.State == domain.StepWatching && s.Watch != nil && s.Watch.Observed != nil && len(s.Watch.Observed.KeptOutPrints) > 0 {
				r.CanForward = true
			}
		}
	}
	r.SavedAs = Sanitize(run.SavedAs)
	if r.CanSave = engine.RunSaveRefusal(run) == ""; r.CanSave {
		if workflowrecipe.CheckName(run.Recipe.Name) == nil {
			r.SaveName = run.Recipe.Name
		}
	}
	r.Live = liveStrip(in, r, now)
	if run.State.Ended() {
		r.Mark, r.Line = endedLine(r)
	}
	return r
}

// runRecipe is how the block names a run's recipe.
func runRecipe(run domain.Run) string {
	switch {
	case run.RecipeName != "":
		return Sanitize(run.RecipeName)
	case run.Recipe.Name != "":
		return Sanitize(run.Recipe.Name)
	}
	return "inline recipe"
}

// runRev is the block's revision: a hash of everything else it carries.
func runRev(r Run) string {
	r.Rev = ""
	b, _ := json.Marshal(r)
	h := fnv.New64a()
	_, _ = h.Write(b)
	return strconv.FormatUint(h.Sum64(), 36)
}

// stepSpent is a step's spend: its card's, live, while it works.
func stepSpent(s domain.RunStep, live map[string]StepLive) float64 {
	if l, ok := live[s.Path]; ok && s.State.Live() && l.Spent > s.Spent {
		return l.Spent
	}
	return s.Spent
}

// under reports whether path is prefix or sits beneath it.
func under(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }

// RowPaused is a step row's state while its run is paused: a step that was
// at work, stopped where it was, which carries on when the run resumes.
const RowPaused = "paused"

// stripEntry is one recipe entry at prefix, in the state its steps are in.
func stripEntry(run domain.Run, en domain.Entry, prefix string, steps []domain.RunStep) StripEntry {
	id := en.ID()
	path := id
	if prefix != "" {
		path = prefix + "/" + id
	}
	var mine []domain.RunStep
	for _, s := range steps {
		if under(s.Path, path) {
			mine = append(mine, s)
		}
	}
	e := StripEntry{ID: id, Kind: "step", State: entryState(mine)}
	switch {
	case en.Parallel != nil:
		e.Kind = "parallel"
		for _, s := range en.Parallel.Steps {
			e.Branches = append(e.Branches, stripEntry(run, domain.Entry{Step: &domain.Step{ID: s.ID}}, path, steps))
		}
	case en.Repeat != nil:
		e.Kind, e.Max = "repeat", en.Repeat.Max
		for _, s := range mine {
			if n, ok := iterationOf(s.Path, path); ok && n > e.Iter {
				e.Iter = n
			}
		}
		if e.State == "done" && !repeatOver(run, path, e.Iter, steps) {
			// its steps have settled, but the loop has not: its condition is
			// being judged, or its question waits on the person
			e.State = "running"
			if d := run.Pending; d != nil && under(d.Step, path) {
				e.State = "asking"
			}
		}
	}
	return e
}

// repeatOver reports whether the repeat at path, last run to iteration
// iter, is over: its condition met, the person's answer to go on or stop,
// an entry after it begun, or the run ended.
func repeatOver(run domain.Run, path string, iter int, steps []domain.RunStep) bool {
	if run.State.Ended() {
		return true
	}
	last := path + "/" + strconv.Itoa(iter)
	if j, ok := run.Until[last]; ok && j.Met {
		return true
	}
	if a := run.OnMax[last]; a == "continue" || a == "stop" {
		return true
	}
	// an entry after it begun: a step outside it started after the
	// earliest start in its last iteration
	var first time.Time
	seen := false
	for _, o := range steps {
		if under(o.Path, last) && (!seen || o.StartedAt.Before(first)) {
			first, seen = o.StartedAt, true
		}
	}
	if !seen {
		return false
	}
	for _, s := range steps {
		if !under(s.Path, path) && s.State != domain.StepWaiting && s.StartedAt.After(first) {
			return true
		}
	}
	return false
}

// iterationOf is the iteration of the repeat at prefix that path sits in.
func iterationOf(path, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(path, prefix+"/")
	if !ok {
		return 0, false
	}
	head, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(head)
	return n, err == nil
}

// entryState is the state of an entry from its steps' states: waiting
// until one starts, asking or running while any is live, then failed when
// none of them finished well, and done otherwise.
func entryState(steps []domain.RunStep) string {
	if len(steps) == 0 {
		return "waiting"
	}
	running, asking, done, settled := false, false, false, true
	for _, s := range steps {
		switch s.State {
		case domain.StepAsking:
			asking = true
		case domain.StepRunning:
			running = true
		case domain.StepDone:
			done = true
		}
		if !s.State.Settled() {
			settled = false
		}
	}
	switch {
	case asking:
		return "asking"
	case running:
		return "running"
	case !settled:
		return "waiting"
	case !done:
		return "failed"
	}
	return "done"
}

// iteration is one repeat iteration's path and the repeat it belongs to.
type iteration struct {
	path   string
	repeat domain.Repeat
	n      int
}

// iterations lists every repeat iteration a step of the run sits in, the
// innermost last.
func iterations(es []domain.Entry, prefix string, steps []domain.RunStep) []iteration {
	var out []iteration
	for _, en := range es {
		if en.Repeat == nil {
			continue
		}
		base := en.Repeat.ID
		if prefix != "" {
			base = prefix + "/" + base
		}
		seen := map[int]bool{}
		for _, s := range steps {
			if n, ok := iterationOf(s.Path, base); ok && !seen[n] {
				seen[n] = true
				it := iteration{path: base + "/" + strconv.Itoa(n), repeat: *en.Repeat, n: n}
				out = append(out, it)
				out = append(out, iterations(en.Repeat.Steps, it.path, steps)...)
			}
		}
	}
	return out
}

// finished reports whether an iteration has ended: every step in it
// settled, and its stop condition judged, a later iteration begun or the
// run over.
func finished(run domain.Run, it iteration, idx stepIndex) bool {
	f := idx.under[it.path]
	if f.n == 0 || f.unsettled {
		return false
	}
	later := idx.maxIter[strings.TrimSuffix(it.path, "/"+strconv.Itoa(it.n))] > it.n
	_, judged := run.Until[it.path]
	return judged || later || run.State.Ended()
}

// stepIndex is what the fold asks of a run's steps again and again, read
// in one pass over them, so a run of many steps folds in time linear in
// them rather than square.
type stepIndex struct {
	// under is, for every prefix of a step's path, how many steps sit
	// under it and whether any of them has not settled
	under map[string]underFacts
	// maxIter is, for every repeat's path, the highest iteration a step
	// sits in
	maxIter map[string]int
	// watch is the run's watch step's state, nil when there is none
	watch *domain.WatchState
	// passes is each watch pass's steps, oldest first, by pass number
	passes map[int][]domain.RunStep
}

type underFacts struct {
	n         int
	unsettled bool
}

func indexSteps(run domain.Run, steps []domain.RunStep) stepIndex {
	idx := stepIndex{under: map[string]underFacts{}, maxIter: map[string]int{}, passes: map[int][]domain.RunStep{}}
	w, hasWatch := run.Recipe.WatchStep()
	for i := range steps {
		s := steps[i]
		if hasWatch && s.Path == w.ID && s.Watch != nil && idx.watch == nil {
			idx.watch = s.Watch
		}
		for end := 0; end <= len(s.Path); end++ {
			if end < len(s.Path) && s.Path[end] != '/' {
				continue
			}
			prefix := s.Path[:end]
			f := idx.under[prefix]
			f.n++
			f.unsettled = f.unsettled || !s.State.Settled()
			idx.under[prefix] = f
			// a numeric segment is a repeat's iteration (or a watch's pass)
			if cut := strings.LastIndexByte(prefix, '/'); cut > 0 {
				if n, err := strconv.Atoi(prefix[cut+1:]); err == nil && n > idx.maxIter[prefix[:cut]] {
					idx.maxIter[prefix[:cut]] = n
				}
			}
		}
		if hasWatch {
			if rest, ok := strings.CutPrefix(s.Path, w.ID+"/"); ok {
				// a pass's steps sit under it, never at it
				if seg, _, inside := strings.Cut(rest, "/"); inside {
					if n, err := strconv.Atoi(seg); err == nil {
						idx.passes[n] = append(idx.passes[n], s)
					}
				}
			}
		}
	}
	return idx
}

// foldedInto is the folded iteration path sits under, if any: the folds
// are outermost iterations, so at most one prefix of path is among them.
func foldedInto(path string, fold map[string]iteration) (iteration, bool) {
	for end := 0; end <= len(path); end++ {
		if end < len(path) && path[end] != '/' {
			continue
		}
		if it, ok := fold[path[:end]]; ok {
			return it, true
		}
	}
	return iteration{}, false
}

// WatchRow is what a watch step shows of its PR.
type WatchRow struct {
	PR      string `json:"pr"`
	URL     string `json:"url,omitempty"`
	Until   string `json:"until,omitempty"`
	Pending int    `json:"pending"`
	Threads int    `json:"threads"`
	KeptOut int    `json:"keptOut"`
	Passes  int    `json:"passes"`
	Held    bool   `json:"held,omitempty"`
}

func foldWatch(w *domain.WatchState, until string) *WatchRow {
	row := &WatchRow{
		PR: fmt.Sprintf("%s#%d", w.PR.Repo, w.PR.Number), URL: w.PR.URL, Until: until,
		Passes: len(w.Passes), Held: w.Held,
	}
	if f := w.Observed; f != nil {
		row.Threads, row.KeptOut = f.Threads, f.KeptOut
		for _, c := range f.Checks {
			if c.Bucket == domain.WatchCheckPending {
				row.Pending++
			}
		}
	}
	return row
}

// WatchLine is a watching row's words, and the live strip's: what the
// watch has seen of the PR and how much the trust filter kept out.
func WatchLine(w *domain.WatchState) string {
	f := w.Observed
	if f == nil {
		return "waiting on the PR: not read yet"
	}
	var parts []string
	pending := 0
	for _, c := range f.Checks {
		if c.Bucket == domain.WatchCheckPending {
			pending++
		}
	}
	if pending > 0 {
		parts = append(parts, domain.WatchWords(pending, "check")+" pending")
	}
	if f.Threads > 0 {
		parts = append(parts, domain.WatchWords(f.Threads, "thread")+" seen")
	}
	if f.KeptOut > 0 {
		var kinds []string
		for _, k := range []string{"review", "thread", "comment"} {
			if n := f.KeptOutByKind[k]; n > 0 {
				kinds = append(kinds, domain.WatchWords(n, k))
			}
		}
		what := strings.Join(kinds, " and ")
		if what == "" {
			what = domain.WatchWords(f.KeptOut, "item")
		}
		parts = append(parts, what+" from outside the filter")
	}
	if f.Unread > 0 {
		parts = append(parts, domain.WatchWords(f.Unread, "item")+" past the first 100 not read")
	}
	if f.Unreadable != "" {
		parts = append(parts, "the trust filter could not be read")
	}
	line := "waiting on the PR: "
	if len(parts) == 0 {
		line += "green, no feedback"
		if hasBucketFail(f) {
			line = "waiting on the PR: a check failed"
		}
	} else {
		line += strings.Join(parts, " · ")
	}
	if w.Held {
		line += " · acts on nothing until you resume"
	}
	return line
}

func hasBucketFail(f *domain.WatchFacts) bool {
	for _, c := range f.Checks {
		if c.Bucket == domain.WatchCheckFail {
			return true
		}
	}
	return false
}

// passHeader is the folded row that heads one pass of a watch.
// passHeader is a watch pass's row; steps are the pass's own, oldest first.
func passHeader(watchPath string, p domain.WatchPass, steps []domain.RunStep) StepRow {
	line := "running"
	state := string(domain.StepRunning)
	switch {
	case p.Settled == "failed":
		line, state = "failed", string(domain.StepFailed)
	case p.PushError != "":
		line, state = "push failed: "+Sanitize(firstLineOf(p.PushError)), string(domain.StepFailed)
	case p.Pushed && p.PushedSHA != "":
		line, state = "pushed "+short(p.PushedSHA), string(domain.StepDone)
	case p.Pushed:
		line, state = "nothing to push", string(domain.StepDone)
	case p.Settled == "done":
		line = "pushing"
	}
	var ran []string
	prefix := watchPath + "/" + strconv.Itoa(p.N) + "/"
	for _, s := range steps {
		if rest, ok := strings.CutPrefix(s.Path, prefix); ok {
			id, _, _ := strings.Cut(rest, "/")
			if len(ran) == 0 || ran[len(ran)-1] != id {
				ran = append(ran, id)
			}
		}
	}
	return StepRow{
		Key: watchPath + "/" + strconv.Itoa(p.N), Path: watchPath + "/" + strconv.Itoa(p.N), ID: "pass " + strconv.Itoa(p.N),
		State: state, Line: line, Folded: true, Pass: p.N, Event: string(p.Event), Steps: strings.Join(ran, " · "),
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// stepRows draws the run's steps, oldest first, each finished outermost
// iteration of a repeat folded to one row where its first step stood.
func stepRows(run domain.Run, steps []domain.RunStep, live map[string]StepLive) []StepRow {
	idx := indexSteps(run, steps)
	fold := map[string]iteration{}
	for _, it := range iterations(run.Recipe.Steps, "", steps) {
		if !finished(run, it, idx) {
			continue
		}
		if _, inner := foldedInto(it.path, fold); !inner {
			fold[it.path] = it
		}
	}
	rows := []StepRow{}
	passed := map[int]bool{}
	placed := map[string]int{}
	changed := map[string]int{}
	for _, s := range steps {
		if s.State == domain.StepWaiting {
			continue
		}
		var into *iteration
		if it, ok := foldedInto(s.Path, fold); ok {
			into = &it
		}
		if into == nil {
			if w, n, ok := passOf(run, idx, s.Path); ok {
				if !passed[n] {
					passed[n] = true
					rows = append(rows, passHeader(w.path, w.state.Passes[n-1], idx.passes[n]))
				}
				row := stepRow(s, live)
				row.Pass, row.Event = n, string(w.state.Passes[n-1].Event)
				if row.Repeat == w.path {
					row.Repeat, row.Iter = "", 0
				}
				rows = append(rows, row)
				continue
			}
			row := stepRow(s, live)
			if s.Watch != nil {
				row.Watch = foldWatch(s.Watch, watchUntil(run, s.StepID))
				if s.State == domain.StepWatching {
					row.Line = WatchLine(s.Watch)
				}
			}
			rows = append(rows, row)
			continue
		}
		i, ok := placed[into.path]
		if !ok {
			i = len(rows)
			placed[into.path] = i
			rows = append(rows, StepRow{
				Key: into.path, Path: into.path, Repeat: into.repeat.ID, Iter: into.n, ID: into.repeat.ID,
				State: string(domain.StepDone), Folded: true, Verdict: verdictWords(run, *into),
			})
		}
		row := &rows[i]
		row.Spent = roundCredits(row.Spent + s.Spent)
		if s.State == domain.StepFailed {
			row.State = string(domain.StepFailed)
		}
		changed[into.path] += len(s.Changed)
	}
	for p, i := range placed {
		if n := changed[p]; n > 0 {
			rows[i].Flag = "a reading step in it " + strings.TrimSuffix(ReaderFlag(n), " although it only reads")
		}
	}
	for p, i := range placed {
		rows[i].Steps = iterationSteps(fold[p], idx)
		rows[i].Line = rows[i].Verdict
	}
	return rows
}

type watchAt struct {
	path  string
	state *domain.WatchState
}

// passOf reports whether the step at path is inside a pass of the run's
// watch, and which.
func passOf(run domain.Run, idx stepIndex, path string) (watchAt, int, bool) {
	w, ok := run.Recipe.WatchStep()
	if !ok || idx.watch == nil {
		return watchAt{}, 0, false
	}
	rest, under := strings.CutPrefix(path, w.ID+"/")
	if !under {
		return watchAt{}, 0, false
	}
	seg, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(seg)
	if err != nil {
		return watchAt{}, 0, false
	}
	if n >= 1 && n <= len(idx.watch.Passes) {
		return watchAt{path: w.ID, state: idx.watch}, n, true
	}
	return watchAt{}, 0, false
}

func watchUntil(run domain.Run, id string) string {
	if w, ok := run.Recipe.WatchStep(); ok && w.ID == id {
		return w.Watch.UntilOr()
	}
	return ""
}

// stepRow is one step's row.
func stepRow(s domain.RunStep, live map[string]StepLive) StepRow {
	row := StepRow{
		Key: s.Path, Path: s.Path, ID: s.StepID, Role: string(s.Role), State: string(s.State),
		Spent: roundCredits(stepSpent(s, live)), Card: string(s.Card), Started: s.StartedAt, Ended: s.EndedAt,
	}
	if row.ID == "" {
		row.ID = s.Path[strings.LastIndex(s.Path, "/")+1:]
	}
	row.Repeat, row.Iter = innerIteration(s.Path)
	l, seen := live[s.Path]
	state := liveState(s, l, seen)
	row.State = string(state)
	earlier := strings.Contains(s.Path, "@")
	switch {
	case state == domain.StepAsking && s.EndTree != "":
		row.Line = "its revised plan waits on you"
	case state == domain.StepAsking:
		row.Line = "asks you"
	case state == domain.StepRunning && s.State.Settled():
		row.Line = "a follow-up · " + actionWords(l.Action)
	case state.Live():
		row.Line = actionWords(l.Action)
	case state == domain.StepDone:
		row.Line = endSummary(s.Output)
	default:
		row.Line = Sanitize(firstLineOf(s.Reason))
	}
	if s.State.Live() && l.Ask != nil {
		row.Question = AskQuestion(l.Ask)
	}
	if seen && state.Live() {
		row.Model = Sanitize(l.Model)
	}
	row.Flag = ReaderFlag(len(s.Changed))
	if earlier {
		// a retried step's earlier attempt, kept beside the one run again
		row.Line = "an earlier attempt · " + row.Line
	}
	return row
}

// liveState is the state a step's row shows. The store's word on a live
// step moves between running and asking only when the run is next ticked,
// so a step that has just asked, or has just been answered, read as it was
// until then; its own session (seen) is the authority while it has one. A
// planner's revised plan waits on the person whatever its session says.
// An ended step whose card is working again — a person's follow-up — is
// running.
func liveState(s domain.RunStep, l StepLive, seen bool) domain.StepState {
	switch {
	case !seen:
		return s.State
	case s.State.Live() && s.EndTree == "":
		if l.Ask != nil {
			return domain.StepAsking
		}
		return domain.StepRunning
	case s.State.Settled() && l.Busy:
		return domain.StepRunning
	}
	return s.State
}

// ReaderFlag is a reading step's row flag for the n files it changed, ""
// when it changed none.
func ReaderFlag(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "changed 1 file although it only reads"
	}
	return fmt.Sprintf("changed %d files although it only reads", n)
}

// innerIteration is the repeat and iteration a step's path sits in, the
// innermost: build/2/implement → build, 2.
func innerIteration(path string) (string, int) {
	parts := strings.Split(path, "/")
	for i := len(parts) - 2; i > 0; i-- {
		if n, err := strconv.Atoi(parts[i]); err == nil {
			return parts[i-1], n
		}
	}
	return "", 0
}

// actionWords is a step's call in flight as its row says it.
func actionWords(label string) string {
	label = strings.Join(strings.Fields(Sanitize(label)), " ")
	if label == "" {
		return "working"
	}
	return cut(label, 110)
}

// endSummary is a step's output as its row ends on: its first line, past
// any line that only records an item's judgement.
func endSummary(out string) string {
	for _, l := range strings.Split(Sanitize(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "MET:") || strings.HasPrefix(l, "UNMET:") || strings.HasPrefix(l, "```") {
			continue
		}
		return cut(l, 110)
	}
	return "done"
}

// AskQuestion is a step's ask with its recommended answer first, as its
// row and its opened step both draw it.
func AskQuestion(a *engine.Ask) *StepQuestion {
	q := &StepQuestion{Call: a.CallID, Question: Sanitize(a.Question), Multi: a.MultiPick}
	rec := engine.RecommendedOption(a)
	first := 0
	for i, o := range a.Options {
		if o.Label == rec {
			first = i
			break
		}
	}
	for i, o := range a.Options {
		opt := QuestionOption{Label: Sanitize(o.Label), Detail: Sanitize(o.Detail)}
		if i == first {
			opt.Recommended = true
			q.Options = append([]QuestionOption{opt}, q.Options...)
			continue
		}
		q.Options = append(q.Options, opt)
	}
	return q
}

// verdictWords is how an iteration's stop condition was judged.
func verdictWords(run domain.Run, it iteration) string {
	j, ok := run.Until[it.path]
	if !ok {
		return ""
	}
	word := "not met"
	switch {
	case j.Met:
		word = "met"
	case j.Unjudged:
		word = "can't judge"
	}
	if d := firstLineOf(j.Detail); d != "" {
		return cut(Sanitize(word+" · "+d), 110)
	}
	return word
}

// iterationSteps names what ran in an iteration, in recipe order: steps
// joined by " · ", a parallel's branches by " ∥ ".
func iterationSteps(it iteration, idx stepIndex) string {
	ran := func(id string) bool { return idx.under[it.path+"/"+id].n > 0 }
	var parts []string
	for _, en := range it.repeat.Steps {
		switch {
		case en.Step != nil && ran(en.Step.ID):
			parts = append(parts, en.Step.ID)
		case en.Parallel != nil && ran(en.Parallel.ID):
			var b []string
			for _, s := range en.Parallel.Steps {
				b = append(b, s.ID)
			}
			parts = append(parts, strings.Join(b, " ∥ "))
		case en.Repeat != nil && ran(en.Repeat.ID):
			parts = append(parts, en.Repeat.ID+" ↺")
		}
	}
	return strings.Join(parts, " · ")
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// RunLine is a run in one line, the words a list of runs (a spec's, the
// TUI's crumb) names it by: "deliver · run 2".
func RunLine(r Run) string { return fmt.Sprintf("%s · run %d", r.Recipe, r.N) }

// IsRunNote reports whether a transcript entry is one of the lines a run
// puts into its session's transcript ("workflow: run 1 started"). The
// run's block says all of it, in place, so neither face draws them: a
// session's thread reads as its conversation with one block per run.
func IsRunNote(msg engine.Message) bool {
	return msg.Author == engine.AuthorTool && msg.Tool == "" && strings.HasPrefix(msg.Content, engine.RunNotePrefix)
}

// WithoutRunNotes is a transcript with its run notes left out (IsRunNote).
// It returns tr itself when it holds none.
func WithoutRunNotes(tr []engine.Message) []engine.Message {
	k := -1
	for i, msg := range tr {
		if IsRunNote(msg) {
			k = i
			break
		}
	}
	if k < 0 {
		return tr
	}
	out := append([]engine.Message(nil), tr[:k]...)
	for _, msg := range tr[k+1:] {
		if !IsRunNote(msg) {
			out = append(out, msg)
		}
	}
	return out
}

// RunStateWord is a run's state as its block's head says it.
func RunStateWord(r Run) string {
	if r.Stopping {
		return "stopping"
	}
	switch domain.RunState(r.State) {
	case domain.RunProposed:
		return "waits on your answer"
	case domain.RunQueued:
		return "queued behind the open run"
	case domain.RunDenied:
		return "not run"
	}
	return r.State
}

// PhaseWords is what gummi is doing on a run between its steps, as both
// faces say it; "" for no phase.
func PhaseWords(phase string) string {
	switch phase {
	case "baseline":
		return "running the done-when checks before the first step"
	case "until":
		return "judging whether the loop is done"
	case "ending":
		return "running the done-when checks and writing the report"
	}
	return ""
}

// RunWorked is how long a run has worked at now, its pauses left out: its
// active time, and the time since it last resumed while it runs. A run
// from before its clock was kept reads its whole span.
func RunWorked(r Run, now time.Time) time.Duration {
	if r.Started.IsZero() {
		return 0
	}
	d := time.Duration(r.ActiveMs) * time.Millisecond
	if !r.Resumed.IsZero() && now.After(r.Resumed) {
		d += now.Sub(r.Resumed)
	}
	if d == 0 && !r.Ended.IsZero() && r.Resumed.IsZero() && r.ActiveMs == 0 {
		return r.Ended.Sub(r.Started)
	}
	return d
}
