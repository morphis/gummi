package threadfold

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

var runAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// deliverRecipe is the bundled deliver's shape: a design step, a build
// loop (an implementer, two reviews side by side, a verdict), and a
// validation.
func deliverRecipe() domain.Recipe {
	return domain.Recipe{Name: "deliver", Steps: []domain.Entry{
		{Step: &domain.Step{ID: "design", Role: domain.RoleContractor}},
		{Repeat: &domain.Repeat{ID: "build", Max: 3, Until: domain.Until{Verdict: "APPROVED", In: "review"}, Steps: []domain.Entry{
			{Step: &domain.Step{ID: "implement", Role: domain.RoleImplementer}},
			{Parallel: &domain.Parallel{ID: "reviews", Join: domain.JoinAllSettled, Steps: []domain.Step{
				{ID: "review-a", Role: domain.RoleReviewer}, {ID: "review-b", Role: domain.RoleReviewer},
			}}},
			{Step: &domain.Step{ID: "verdict", Role: domain.RoleReviewer}},
		}}},
		{Step: &domain.Step{ID: "validate", Role: domain.RoleReviewer}},
	}}
}

// step is a step instance at path, minute m into the run.
func step(path string, role domain.WorkflowRole, st domain.StepState, m int, spent float64, out string) domain.RunStep {
	id := path[strings.LastIndex(path, "/")+1:]
	return domain.RunStep{
		Session: "FF-001", Run: 2, Path: path, StepID: id, Card: domain.FeatureID("FF-1" + strings.ReplaceAll(path, "/", "")[:2]),
		Role: role, State: st, Spent: spent, Output: out, StartedAt: runAt.Add(time.Duration(m) * time.Minute),
	}
}

// runningRun is run 2 in build's second iteration: design done, the first
// iteration ended CHANGES, and the implementer of the second asks its
// person a question while a decision is open on it.
func runningRun() RunInput {
	return RunInput{
		Run: domain.Run{
			Session: "FF-001", N: 2, RecipeName: "deliver", Recipe: deliverRecipe(), Spec: 2, SpecRev: 2,
			State: domain.RunRunning, StartTree: "aaaa", CreatedAt: runAt, StartedAt: runAt,
			Until: map[string]domain.UntilJudgement{"build/1": {Met: false, Detail: "CHANGES · 2 findings: pruned logs come back out of order"}},
			Pending: &domain.RunDecision{
				ID: "run:FF-001:2:1", Kind: domain.RunDecideMax, Step: "build/2/implement",
				Question: "Run 2: the loop can't judge after build/2.", Options: []string{"Run one more iteration", "Continue", "Stop the run"},
			},
		},
		SpecTitle: "Resume pre-migration sessions",
		Files:     []string{"internal/state/migrate_turns.go", "internal/engine/migrate_test.go"},
		Steps: []domain.RunStep{
			step("design", domain.RoleContractor, domain.StepDone, 0, 40, "Design for the spec\n\nwrote design.md"),
			step("build/1/implement", domain.RoleImplementer, domain.StepDone, 5, 80, "implemented it"),
			step("build/1/reviews/review-a", domain.RoleReviewer, domain.StepDone, 9, 30, "findings"),
			step("build/1/reviews/review-b", domain.RoleReviewer, domain.StepDone, 9, 25, "findings"),
			step("build/1/verdict", domain.RoleReviewer, domain.StepDone, 12, 35, "CHANGES"),
			step("build/2/implement", domain.RoleImplementer, domain.StepAsking, 14, 0, ""),
		},
		Live: map[string]StepLive{"build/2/implement": {
			Spent: 100, Action: "Edit  internal/state/migrate_turns.go",
			Ask: &engine.Ask{Question: "Resume a pruned v40 session, or refuse it?", Options: []engine.AskOption{
				{Label: "Refuse with a sentence"}, {Label: "Resume with what's left (recommended)", Detail: "the log is ordered by event id"},
			}},
		}},
	}
}

func TestARunningRunFoldsToOneBlock(t *testing.T) {
	r := FoldRun(runningRun())
	if r.N != 2 || r.Recipe != "deliver" || r.SpecTitle != "Resume pre-migration sessions" || r.Spent != 310 {
		t.Fatalf("head %+v", r)
	}
	if !r.CanPause || !r.CanStop || r.CanResume || r.CanRevert {
		t.Fatalf("a running run with a step at work offers %+v", r)
	}
	strip := []string{}
	for _, e := range r.Strip {
		strip = append(strip, e.ID+":"+e.State)
	}
	if got := strings.Join(strip, " "); got != "design:done build:asking validate:waiting" {
		t.Fatalf("strip %s", got)
	}
	if b := r.Strip[1]; b.Kind != "repeat" || b.Iter != 2 || b.Max != 3 {
		t.Fatalf("build is not ↺ 2/3: %+v", b)
	}
	if len(r.Rows) != 3 {
		t.Fatalf("rows %+v", r.Rows)
	}
	design, iter, impl := r.Rows[0], r.Rows[1], r.Rows[2]
	if design.Line != "Design for the spec" || design.Role != "contractor" || design.Card == "" {
		t.Fatalf("design %+v", design)
	}
	if !iter.Folded || iter.Key != "build/1" || iter.Iter != 1 || iter.Spent != 170 ||
		iter.Steps != "implement · review-a ∥ review-b · verdict" || !strings.HasPrefix(iter.Verdict, "not met · CHANGES") {
		t.Fatalf("the finished iteration %+v", iter)
	}
	if impl.Key != "build/2/implement" || impl.Repeat != "build" || impl.Iter != 2 || impl.Line != "asks you" || impl.Spent != 100 || !impl.Waiting {
		t.Fatalf("the asking step %+v", impl)
	}
	q := impl.Question
	if q == nil || len(q.Options) != 2 || !q.Options[0].Recommended || !strings.HasPrefix(q.Options[0].Label, "Resume") {
		t.Fatalf("the question does not lead with its recommended answer: %+v", q)
	}
	if r.Pending == nil || r.Pending.Kind != "max" {
		t.Fatalf("pending %+v", r.Pending)
	}
	if r.Results != nil {
		t.Fatalf("an open run has no results: %+v", r)
	}
}

// The block's revision moves with what it draws, and only then.
func TestARunsRevMovesWithItsBlock(t *testing.T) {
	in := runningRun()
	a, b := FoldRun(in), FoldRun(in)
	if a.Rev == "" || a.Rev != b.Rev {
		t.Fatalf("the same run folds to two revisions: %q %q", a.Rev, b.Rev)
	}
	in.Live["build/2/implement"] = StepLive{Spent: 120, Action: "Bash  go test ./..."}
	if c := FoldRun(in); c.Rev == a.Rev {
		t.Fatal("a step's spend and action moved and the block's revision did not")
	}
}

func TestAnEndedRunFoldsToItsResults(t *testing.T) {
	in := runningRun()
	in.Run.State, in.Run.Pending, in.Run.EndedAt = domain.RunDone, nil, runAt.Add(72*time.Minute)
	in.Run.Report = "Run 2 (deliver) …"
	in.Run.Commits = []string{"9f8e7d6 feat(state): keep the turns"}
	in.Run.Results = []domain.ItemResult{{Item: "DW-1", Checked: true, Verdict: domain.ItemMet}, {Item: "DW-2", Verdict: domain.ItemUnjudged}}
	in.Run.Until["build/2"] = domain.UntilJudgement{Met: true, Detail: "APPROVED"}
	in.Steps[5].State, in.Steps[5].Output = domain.StepDone, "kept the turns"
	in.Steps = append(in.Steps,
		step("build/2/reviews/review-a", domain.RoleReviewer, domain.StepDone, 20, 30, "ok"),
		step("build/2/reviews/review-b", domain.RoleReviewer, domain.StepStopped, 20, 10, ""),
		step("build/2/verdict", domain.RoleReviewer, domain.StepDone, 22, 10, "APPROVED"),
		step("validate", domain.RoleReviewer, domain.StepDone, 25, 20, "MET: DW-1\nall good"))
	in.Steps[len(in.Steps)-1].Changed = []string{"internal/state/turns.go", "NOTES.md"}
	in.Steps[2].Changed = []string{"go.sum"} // build/1/reviews/review-a, folded into build ↺ 1
	in.Live = nil
	r := FoldRun(in)
	if r.CanPause || r.CanStop || r.CanResume || !r.CanRevert {
		t.Fatalf("an ended run offers %+v", r)
	}
	if !r.Report || len(r.Commits) != 1 || len(r.Results) != 2 || r.Results[0].Verdict != "met" {
		t.Fatalf("ended %+v", r)
	}
	keys := []string{}
	for _, row := range r.Rows {
		keys = append(keys, row.Key)
	}
	if got := strings.Join(keys, " "); got != "design build/1 build/2 validate" {
		t.Fatalf("rows %s", got)
	}
	if v := r.Rows[3]; v.Line != "all good" {
		t.Fatalf("validate's end summary skips its judgement lines: %q", v.Line)
	}
	// a reviewer that changed files is flagged beside its line, and only it
	if v := r.Rows[3]; v.Flag != "changed 2 files although it only reads" {
		t.Fatalf("validate's flag = %q", v.Flag)
	}
	if d := r.Rows[0]; d.Flag != "" {
		t.Fatalf("design is flagged: %q", d.Flag)
	}
	if b := r.Rows[1]; b.Flag != "a reading step in it changed 1 file" || r.Rows[2].Flag != "" {
		t.Fatalf("the folded iterations' flags = %q, %q", b.Flag, r.Rows[2].Flag)
	}
	if ReaderFlag(1) != "changed 1 file although it only reads" {
		t.Errorf("one file reads %q", ReaderFlag(1))
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.RequireEqual(t, append(b, '\n'))
}

// A run's item sits where the run was proposed, and is delivered on every
// page, since it changes without an event of its own.
func TestARunItemIsPlacedByTimeAndAlwaysDelivered(t *testing.T) {
	l := newLog()
	l.add(domain.StageOpen, state.EventPublish, "", state.PublishPayload{}, "")
	l.add(domain.StageOpen, state.EventPublish, "", state.PublishPayload{}, "")
	in := runningRun()
	in.Run.CreatedAt = l.evs[0].At.Add(30 * time.Second)
	items := Items(l.evs, Options{Runs: []RunInput{in}})
	if len(items) != 3 || items[1].T != ItemRun || items[1].Key != "run:2" || items[1].Seq != 1 {
		t.Fatalf("items %+v", items)
	}
	since := Since(items, 2)
	if len(since) != 1 || since[0].Key != "run:2" {
		t.Fatalf("a page past every event is not handed the run: %+v", since)
	}
}

// A folded iteration whose condition could not be judged says so, never
// "not met".
func TestAnUnjudgedIterationSaysSo(t *testing.T) {
	run := domain.Run{Until: map[string]domain.UntilJudgement{
		"build/1": {Unjudged: true, Detail: "the loop can't judge: review (review.json) was never written"},
	}}
	got := verdictWords(run, iteration{path: "build/1", n: 1})
	if got != "can't judge · the loop can't judge: review (review.json) was never written" {
		t.Fatalf("verdict = %q", got)
	}
}

// A step's row says what its own session is doing now, not what the store
// said at the run's last tick: a step that has just asked asks, one that
// has just been answered runs again, and an ended step a person is
// talking to is running. Without a session to read, the store's word
// stands; a planner's revised plan waits on the person whatever.
func TestAStepRowReadsItsOwnSession(t *testing.T) {
	ask := &engine.Ask{Question: "Plain or with a header?", Options: []engine.AskOption{{Label: "Plain"}}}
	cases := []struct {
		name    string
		stored  domain.StepState
		endTree string
		live    *StepLive
		state   string
		line    string
	}{
		{"asked since the last tick", domain.StepRunning, "", &StepLive{Busy: true, Ask: ask}, "asking", "asks you"},
		{"answered since the last tick", domain.StepAsking, "", &StepLive{Busy: true, Action: "Edit  made.txt"}, "running", "Edit made.txt"},
		{"coming up, nothing said yet", domain.StepRunning, "", &StepLive{}, "running", "working"},
		{"a person's follow-up", domain.StepDone, "", &StepLive{Busy: true, Action: "Bash  go test ./..."}, "running", "a follow-up · Bash go test ./..."},
		{"ended, quiet", domain.StepDone, "", &StepLive{}, "done", "wrote it"},
		{"no session to read", domain.StepAsking, "", nil, "asking", "asks you"},
		{"a revised plan", domain.StepAsking, "bbbb", &StepLive{}, "asking", "its revised plan waits on you"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := step("make", domain.RoleImplementer, c.stored, 0, 10, "wrote it")
			s.EndTree = c.endTree
			in := RunInput{
				Run: domain.Run{Session: "FF-001", N: 1, Recipe: domain.Recipe{Name: "one", Steps: []domain.Entry{
					{Step: &domain.Step{ID: "make", Role: domain.RoleImplementer}},
				}}, State: domain.RunRunning, CreatedAt: runAt, StartedAt: runAt},
				Steps: []domain.RunStep{s}, Live: map[string]StepLive{},
			}
			if c.live != nil {
				in.Live["make"] = *c.live
			}
			r := FoldRun(in)
			row := r.Rows[0]
			if row.State != c.state || row.Line != c.line {
				t.Fatalf("the row reads %s %q, want %s %q", row.State, row.Line, c.state, c.line)
			}
			// the strip reads a live step as its row does
			if c.stored.Live() && r.Strip[0].State != c.state {
				t.Errorf("the strip reads %s, the row %s", r.Strip[0].State, c.state)
			}
		})
	}
}

// endedRun is a run n of one step that ended in state st at minute end,
// proposed at minute at.
func endedRun(n int, st domain.RunState, at, end int) RunInput {
	return RunInput{
		Run: domain.Run{
			Session: "FF-001", N: n, RecipeName: "deliver", Spec: 2, State: st,
			CreatedAt: runAt.Add(time.Duration(at) * time.Minute), StartedAt: runAt.Add(time.Duration(at+1) * time.Minute),
			EndedAt: runAt.Add(time.Duration(end) * time.Minute),
			Results: []domain.ItemResult{
				{Item: "DW-1", Checked: true, Verdict: domain.ItemMet},
				{Item: "DW-2", Checked: true, Verdict: domain.ItemUnmet},
				{Item: "DW-3", Verdict: domain.ItemMet},
			},
		},
		SpecTitle: "Close the TUI's gaps",
		Files:     []string{"a.go", "b.go", "c.go"},
		Steps:     []domain.RunStep{step("make", domain.RoleImplementer, domain.StepDone, at+1, 292, "made it")},
	}
}

// An ended run folds to one line: its state's glyph, the recipe and run,
// a state word when it did not end done, its spec, its checked items met,
// its spend and the files it left.
func TestAnEndedRunSaysItselfInOneLine(t *testing.T) {
	cases := []struct {
		st         domain.RunState
		mark, line string
	}{
		{domain.RunDone, "✓", "deliver · run 2 · spec 2 “Close the TUI's gaps” · met 1/2 · $2.92 · 3 files"},
		{domain.RunPartial, "◐", "deliver · run 2 · partial · spec 2 “Close the TUI's gaps” · met 1/2 · $2.92 · 3 files"},
		{domain.RunStopped, "■", "deliver · run 2 · stopped · spec 2 “Close the TUI's gaps” · met 1/2 · $2.92 · 3 files"},
	}
	for _, c := range cases {
		r := FoldRun(endedRun(2, c.st, 0, 10))
		if r.Mark != c.mark || r.Line != c.line {
			t.Errorf("%s reads %q %q, want %q %q", c.st, r.Mark, r.Line, c.mark, c.line)
		}
	}
	if r := FoldRun(runningRun()); r.Line != "" || r.Mark != "" {
		t.Errorf("an open run has a line: %q", r.Line)
	}
}

// The run that ended last stays whole while it is the latest news; it
// folds once a person says something after it ended, or another run is
// proposed after it. Every older ended run is folded, and an open one
// never is.
func TestAnEndedRunFoldsOnceItIsNoLongerTheLatestNews(t *testing.T) {
	folded := func(rs []Run) string {
		var out []string
		for _, r := range rs {
			out = append(out, fmt.Sprintf("%d:%t", r.N, r.Folded))
		}
		return strings.Join(out, " ")
	}
	one, two := endedRun(1, domain.RunDone, 0, 10), endedRun(2, domain.RunPartial, 20, 30)
	if got := folded(FoldRuns([]RunInput{one, two}, time.Time{})); got != "1:true 2:false" {
		t.Errorf("nothing said since: %s", got)
	}
	// a line said before the run ended leaves it whole
	if got := folded(FoldRuns([]RunInput{one, two}, runAt.Add(25*time.Minute))); got != "1:true 2:false" {
		t.Errorf("said during the run: %s", got)
	}
	// one said after it folds it
	if got := folded(FoldRuns([]RunInput{one, two}, runAt.Add(31*time.Minute))); got != "1:true 2:true" {
		t.Errorf("said after the run: %s", got)
	}
	// so does another run proposed after it, still open
	three := runningRun()
	three.Run.N, three.Run.CreatedAt = 3, runAt.Add(40*time.Minute)
	got := FoldRuns([]RunInput{one, two, three}, time.Time{})
	if folded(got) != "1:true 2:true 3:false" {
		t.Errorf("a later proposal: %s", folded(got))
	}
	// folding moves the block's revision, so a face redraws it
	if whole := FoldRun(two); whole.Rev == got[1].Rev {
		t.Error("a folded run kept the whole block's revision")
	}
	// the run that ended last is the latest, whatever its number
	late := endedRun(1, domain.RunStopped, 0, 50)
	if got := folded(FoldRuns([]RunInput{late, two}, time.Time{})); got != "1:false 2:true" {
		t.Errorf("run 1 ended last: %s", got)
	}
	// the fold reaches the thread's items
	items := Items(nil, Options{Runs: []RunInput{one, two}, Said: runAt.Add(31 * time.Minute)})
	if len(items) != 2 || !items[0].Run.Folded || !items[1].Run.Folded {
		t.Errorf("the items do not carry the fold: %+v", items)
	}
}

// A person's line is what folds a run: a turn gummi sent the session on
// its own — a run's report, an objective's — is none.
func TestPersonSaidIsAPersonsLineAlone(t *testing.T) {
	at := func(m int) time.Time { return runAt.Add(time.Duration(m) * time.Minute) }
	tr := []engine.Message{
		{Author: engine.AuthorUser, Content: "go", At: at(1)},
		{Author: engine.AuthorAssistant, Content: "ok", At: at(2)},
		{Author: engine.AuthorUser, By: state.ActorWorkflow, Content: "Run 1 ended", At: at(3)},
		{Author: engine.AuthorUser, By: state.ActorObjective, Content: "carry on", At: at(4)},
	}
	if got := PersonSaid(tr); !got.Equal(at(1)) {
		t.Fatalf("said at %v, want minute 1", got)
	}
	tr = append(tr, engine.Message{Author: engine.AuthorUser, By: "web:ana", Content: "thanks", At: at(5)})
	if got := PersonSaid(tr); !got.Equal(at(5)) {
		t.Fatalf("a web viewer's line: said at %v", got)
	}
}

// A run stands among a conversation's turns before the first one written
// after it was proposed, never inside an activity row; after them all
// when nothing came since; and above them, among the settled items, when
// it predates every turn shown.
func TestARunIsPlacedAmongTheTurnsWhereItWasProposed(t *testing.T) {
	at := func(m int) time.Time { return runAt.Add(time.Duration(m) * time.Minute) }
	turns := []TurnMark{
		{At: at(1)},                 // you: make it
		{At: at(2), Activity: true}, // spec_start
		{At: at(3), Activity: true}, // workflow_run, the run proposed at minute 3.5
		{At: at(4), Activity: true}, // a thought after it
		{At: at(5)},                 // the agent: "I proposed run 1"
		{At: at(9)},                 // you: thanks
	}
	cases := []struct {
		at   time.Time
		want int
	}{
		{at(3).Add(30 * time.Second), 4}, // after the activity row it was proposed in
		{at(6), 5},                       // the person answered through decisions, then spoke
		{at(10), 6},                      // nothing came since: after every turn
		{runAt, -1},                      // before the conversation shown: the settled items'
	}
	for _, c := range cases {
		if got := RunPlace(turns, c.at); got != c.want {
			t.Errorf("a run at %v stands at %d, want %d", c.at.Sub(runAt), got, c.want)
		}
	}
	if got := RunPlace(nil, runAt); got != 0 {
		t.Errorf("no turns: %d", got)
	}
	// a transcript's tool calls and thoughts are activity
	marks := TranscriptMarks([]engine.Message{{Author: engine.AuthorTool}, {Author: engine.AuthorThinking}, {Author: engine.AuthorAssistant}})
	if !marks[0].Activity || !marks[1].Activity || marks[2].Activity {
		t.Errorf("marks %+v", marks)
	}
}

// In the settled thread a run sits among the log's items where it was
// proposed, before an item from after it, never stacked at the end.
func TestARunProposedInTheSettledHistorySitsThere(t *testing.T) {
	l := newLog()
	l.add(domain.StageOpen, state.EventPublish, "", state.PublishPayload{}, "")
	l.add(domain.StageOpen, state.EventPublish, "", state.PublishPayload{}, "")
	l.add(domain.StageOpen, state.EventPublish, "", state.PublishPayload{}, "")
	one, two := endedRun(1, domain.RunDone, 0, 1), endedRun(2, domain.RunDone, 0, 2)
	one.Run.CreatedAt = l.evs[0].At.Add(time.Second)
	two.Run.CreatedAt = l.evs[1].At.Add(time.Second)
	items := Items(l.evs, Options{Runs: []RunInput{two, one}})
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	if len(items) != 5 || items[1].Key != "run:1" || items[3].Key != "run:2" {
		t.Fatalf("items %s", strings.Join(keys, " "))
	}
}

// TestARunEndedBeforeTheConversationIsFolded: once /clear or /handoff
// starts the conversation over, a run that ended before it is still drawn,
// folded to its line, even the one that ended last; an open run, and one
// that ended since, stay whole.
func TestARunEndedBeforeTheConversationIsFolded(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cleared := t0.Add(time.Hour)
	tr := []engine.Message{
		{Author: engine.AuthorSystem, Content: "Cleared the conversation.", At: cleared},
	}
	if got := FoldSince(tr); !got.Equal(cleared) {
		t.Fatalf("FoldSince = %v, want the cleared note's %v", got, cleared)
	}
	left := []RunInput{
		{Run: domain.Run{N: 1, State: domain.RunDone, CreatedAt: t0, EndedAt: t0.Add(time.Minute)}},
		{Run: domain.Run{N: 2, State: domain.RunDone, CreatedAt: t0, EndedAt: t0.Add(2 * time.Minute)}},
	}
	for _, r := range FoldRuns(left, FoldSince(tr)) {
		if !r.Folded {
			t.Errorf("run %d ended before the conversation and is drawn whole", r.N)
		}
	}
	if !RunLeftBehind(domain.RunDone, t0.Add(time.Minute), cleared) || RunLeftBehind(domain.RunRunning, time.Time{}, cleared) {
		t.Error("RunLeftBehind misjudges an ended run before the conversation, or an open one")
	}
	ins := []RunInput{
		{Run: domain.Run{N: 1, State: domain.RunDone, CreatedAt: t0, EndedAt: t0.Add(time.Minute)}},
		{Run: domain.Run{N: 2, State: domain.RunRunning, CreatedAt: t0}},
		{Run: domain.Run{N: 3, State: domain.RunDone, CreatedAt: cleared, EndedAt: cleared.Add(time.Minute)}},
	}
	said := cleared.Add(30 * time.Second)
	tr = append(tr, engine.Message{Author: engine.AuthorUser, Content: "go on", At: said})
	var whole []int
	for _, r := range FoldRuns(ins, FoldSince(tr)) {
		if !r.Folded {
			whole = append(whole, r.N)
		}
	}
	if fmt.Sprint(whole) != "[2 3]" {
		t.Errorf("runs drawn whole = %v, want [2 3]", whole)
	}
	if got := FoldSince(tr); !got.Equal(said) {
		t.Errorf("FoldSince = %v, want the person's later line, %v", got, said)
	}
}

func TestFoldRunDrawsAWatch(t *testing.T) {
	impl := func(id string) domain.Step { return domain.Step{ID: id, Role: domain.RoleImplementer} }
	recipe := domain.Recipe{Name: "ship", Steps: []domain.Entry{
		{Step: &domain.Step{ID: "build", Role: domain.RoleImplementer}},
		{Step: &domain.Step{ID: "open", PR: &domain.PRAct{Act: "create"}}},
		{Step: &domain.Step{ID: "shepherd", Watch: &domain.Watch{On: map[domain.WatchEvent]domain.Handler{
			domain.EventChecksFailed: {Steps: []domain.Step{impl("fix")}},
			domain.EventComment:      {Steps: []domain.Step{impl("answer")}},
		}}}},
	}}
	w := &domain.WatchState{
		PR: domain.PullRequestRef{Repo: "me/widget", Number: 12, URL: "https://github.com/me/widget/pull/12"},
		Observed: &domain.WatchFacts{
			Head: "h", ChecksHead: "h", Threads: 2, KeptOut: 1, KeptOutByKind: map[string]int{"comment": 1},
			Checks: []domain.WatchCheck{{Name: "a", Bucket: "pending"}, {Name: "b", Bucket: "pending"}, {Name: "c", Bucket: "pending"}, {Name: "d", Bucket: "pass"}},
		},
		Passes: []domain.WatchPass{
			{N: 1, Event: domain.EventChecksFailed, Settled: "done", Pushed: true, PushedSHA: "abc1234def"},
			{N: 2, Event: domain.EventComment, Settled: "done", Pushed: true},
		},
	}
	watch := step("shepherd", "", domain.StepWatching, 6, 0, "")
	watch.Card, watch.Watch = "", w
	in := RunInput{
		Run: domain.Run{Session: "FF-001", N: 2, RecipeName: "ship", Recipe: recipe, State: domain.RunRunning, StartedAt: runAt},
		Steps: []domain.RunStep{
			step("build", domain.RoleImplementer, domain.StepDone, 0, 10, "built"),
			step("open", "", domain.StepDone, 5, 0, "opened"),
			watch,
			step("shepherd/1/fix", domain.RoleImplementer, domain.StepDone, 10, 5, "fixed"),
			step("shepherd/2/answer", domain.RoleImplementer, domain.StepDone, 15, 5, "answered"),
		},
	}
	r := FoldRun(in)
	const line = "waiting on the PR: 3 checks pending · 2 threads seen · 1 comment from outside the filter"
	var got []string
	var watchRow *StepRow
	for i, row := range r.Rows {
		got = append(got, row.ID+"|"+row.Event)
		if row.Watch != nil {
			watchRow = &r.Rows[i]
		}
	}
	if watchRow == nil || watchRow.Line != line || watchRow.State != "watching" || watchRow.Watch.Pending != 3 || watchRow.Watch.KeptOut != 1 {
		t.Fatalf("watch row = %+v", watchRow)
	}
	want := []string{"build|", "open|", "shepherd|", "pass 1|checks-failed", "fix|checks-failed", "pass 2|comment", "answer|comment"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	for _, row := range r.Rows {
		switch row.ID {
		case "pass 1":
			if row.Line != "pushed abc1234" {
				t.Errorf("pass 1 = %+v", row)
			}
		case "pass 2":
			if row.Line != "nothing to push" {
				t.Errorf("pass 2 = %+v", row)
			}
		case "answer":
			if row.Repeat != "" || row.Pass != 2 {
				t.Errorf("answer = %+v", row)
			}
		}
	}
	if r.Live == nil || r.Live.Mood != StripWatching || r.Live.Line != line {
		t.Errorf("strip = %+v", r.Live)
	}
	// a read that changes nothing drawn does not move the block's revision
	before := r.Rev
	w.Observed.ReadAt = w.Observed.ReadAt.Add(time.Minute)
	if again := FoldRun(in); again.Rev != before {
		t.Error("the revision moved on a read that changed nothing drawn")
	}
	w.Observed.Checks[0].Bucket = "pass"
	if again := FoldRun(in); again.Rev == before {
		t.Error("the revision did not move when a check settled")
	}
}

// TestWatchLineSaysWhatWasNotRead: feedback past the first 100 is named on
// the strip, never dropped silently.
func TestWatchLineSaysWhatWasNotRead(t *testing.T) {
	w := &domain.WatchState{Observed: &domain.WatchFacts{Head: "h", ChecksHead: "h", Unread: 3}}
	if got, want := WatchLine(w), "waiting on the PR: 3 items past the first 100 not read"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// TestFoldRunsRevIsTheFinishedBlocksHash: the block's Rev is taken once,
// after Folded and CanRetry are settled, so it moves with them.
func TestFoldRunsRevIsTheFinishedBlocksHash(t *testing.T) {
	ended := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ins := []RunInput{
		{Run: domain.Run{N: 1, State: domain.RunDone, EndedAt: ended, CreatedAt: ended.Add(-time.Hour)}},
		{Run: domain.Run{N: 2, State: domain.RunDone, EndedAt: ended.Add(time.Hour), CreatedAt: ended.Add(time.Minute)}},
	}
	out := FoldRuns(ins, time.Time{})
	if !out[0].Folded || out[1].Folded {
		t.Fatalf("folded = %v %v", out[0].Folded, out[1].Folded)
	}
	for i, r := range out {
		if r.Rev != runRev(r) {
			t.Errorf("run %d: Rev %q is not the hash of the finished block %q", i+1, r.Rev, runRev(r))
		}
	}
	if out[0].Rev == FoldRun(ins[0]).Rev {
		t.Error("a folded block keeps the Rev of its unfolded one")
	}
}
