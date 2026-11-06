package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/skillpack"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// curatorFake answers the curator's one session with script's tool calls,
// and records what it was shown.
type curatorFake struct {
	*agent.Fake
	mu      sync.Mutex
	prompts []string
	opts    []agent.SessionOpts
}

func newCuratorFake(script []agent.Event) *curatorFake {
	cf := &curatorFake{Fake: agent.NewFake("")}
	cf.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, SkillDirs: true}
	cf.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		cf.mu.Lock()
		cf.prompts = append(cf.prompts, msg)
		cf.opts = append(cf.opts, opts)
		cf.mu.Unlock()
		return append(script,
			agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 0.5}},
			agent.Event{Kind: agent.EventMessage, Text: "wrote one skill"},
			agent.Event{Kind: agent.EventIdle})
	}
	return cf
}

func curatorEngine(t *testing.T, ag agent.Agent, c *config.Curator) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Curator: c, Workflows: true})
	t.Cleanup(func() { e.Close() })
	return e
}

func TestCuratorRunWritesThePackAndEverySessionGetsIt(t *testing.T) {
	ctx := context.Background()
	cf := newCuratorFake([]agent.Event{
		toolCall("1", curatorReadTool, map[string]any{"card": "FF-7"}),
		toolCall("2", skillWriteTool, map[string]any{
			"name": "make-test", "description": "Use when running this repository's tests.",
			"body": "Run `make test`; a bare `go test` misses the pin build tag.",
		}),
		toolCall("3", skillWriteTool, map[string]any{"name": "Bad Name", "description": "x", "body": "y"}),
	})
	e := curatorEngine(t, cf, &config.Curator{Budget: 100})
	mem := e.cfg.Workspace.SessionMemoryFile("FF-7")
	if err := os.MkdirAll(filepath.Dir(mem), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mem, []byte("go test needs -tags pin; use make test"), 0o600); err != nil {
		t.Fatal(err)
	}

	run, err := e.RunCurator(ctx, "person")
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != CuratorDone || !run.Changed() || strings.Join(run.Written, ",") != "make-test" {
		t.Fatalf("run = %+v", run)
	}
	if run.Credits != 0.5 {
		t.Errorf("run credits = %v, want the session's 0.5", run.Credits)
	}
	if !strings.Contains(cf.prompts[0], "FF-7") {
		t.Errorf("the kickoff does not name the card whose memory changed:\n%s", cf.prompts[0])
	}
	if o := cf.opts[0]; o.MaxCredits <= 0 || o.MaxCredits > 100 || o.WorkDir != filepath.Join(e.cfg.Workspace.GummiDir(), "curator") {
		t.Errorf("curator session opts: budget %v, workdir %s", o.MaxCredits, o.WorkDir)
	}
	runs, _ := e.CuratorRuns()
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("ledger = %+v", runs)
	}

	// Every later session on a backend that takes skill directories gets
	// the published skill: as gummi-make-test where the backend names
	// skills bare, so it never shadows a person's make-test.
	dirs := packDirsFor(ctx, e, cf, &domain.Feature{ID: "FD-1"}, "FD-1", false)
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "gummi-make-test" {
		t.Fatalf("a session gets %v", dirs)
	}
	namespaced := agent.NewFake("")
	namespaced.Caps = agent.Capabilities{SkillDirs: true, SkillNamespace: true}
	if got := packDirsFor(ctx, e, namespaced, &domain.Feature{ID: "FD-1"}, "FD-1", false); len(got) != 1 || filepath.Base(got[0]) != "make-test" {
		t.Errorf("a backend that namespaces the pack itself gets %v", got)
	}
	noSkills := agent.NewFake("")
	noSkills.Caps = agent.Capabilities{}
	if got := packDirsFor(ctx, e, noSkills, &domain.Feature{ID: "FD-1"}, "FD-1", false); got != nil {
		t.Errorf("a backend without SkillDirs got %v", got)
	}

	// Revert moves published back: the next session gets nothing.
	if err := e.SkillPack().Revert(ctx, run.Before, run.After, "revert"); err != nil {
		t.Fatal(err)
	}
	if got := packDirsFor(ctx, e, cf, &domain.Feature{ID: "FD-1"}, "FD-1", false); len(got) != 0 {
		t.Errorf("after the revert a session still gets %v", got)
	}

	// The run read FF-7: the next is not offered it until it changes.
	ct := &curatorTurn{e: e, run: &CuratorRun{}, seen: curatorSeenIn(runs)}
	if cards := ct.memoryCards(); len(cards) != 0 {
		t.Errorf("a card read and unchanged since is offered again: %v", cards)
	}
}

// A person pins and reverts what a real run wrote, by the id its own
// ledger row carries; a run that changed nothing or no run at all is
// refused in words.
func TestCuratorRunIsPinnedAndRevertedByItsID(t *testing.T) {
	ctx := context.Background()
	cf := newCuratorFake([]agent.Event{toolCall("1", skillWriteTool, map[string]any{
		"name": "make-test", "description": "Use when running this repository's tests.",
		"body": "Run `make test`; a bare `go test` misses the pin build tag.",
	})})
	e := curatorEngine(t, cf, &config.Curator{Budget: 100})
	run, err := e.RunCurator(ctx, "person")
	if err != nil || !run.Changed() {
		t.Fatalf("run = %+v, %v", run, err)
	}

	if err := e.PinSkill(ctx, "make-test", true); err != nil {
		t.Fatal(err)
	}
	v, err := e.CuratorView(ctx, time.Now())
	if err != nil || len(v.Skills) != 1 || !v.Skills[0].Pinned {
		t.Fatalf("after pin the view reads %+v, %v", v.Skills, err)
	}
	if err := e.PinSkill(ctx, "make-test", false); err != nil {
		t.Fatal(err)
	}

	if _, err := e.RevertCuratorRun(ctx, "no-such"); err == nil || !strings.Contains(err.Error(), "no curator run") {
		t.Errorf("revert of an unknown run = %v", err)
	}
	if _, err := e.RevertCuratorRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	v, _ = e.CuratorView(ctx, time.Now())
	if len(v.Skills) != 0 {
		t.Errorf("after the revert the pack holds %+v", v.Skills)
	}
	if len(v.Runs) != 1 || !v.Runs[0].Reverted || v.Runs[0].Revertable() {
		t.Errorf("after the revert the run reads %+v", v.Runs)
	}
	if _, err := e.RevertCuratorRun(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "already reverted") {
		t.Errorf("a second revert of the run = %v", err)
	}
	if got := packDirsFor(ctx, e, cf, &domain.Feature{ID: "FD-1"}, "FD-1", false); len(got) != 0 {
		t.Errorf("after the revert a session still gets %v", got)
	}

	idle := newCuratorFake(nil)
	e.cfg.Agents = singleAgent(idle)
	quiet, err := e.RunCurator(ctx, "person")
	if err != nil || quiet.Changed() || quiet.ID == run.ID {
		t.Fatalf("quiet run = %+v, %v", quiet, err)
	}
	if _, err := e.RevertCuratorRun(ctx, quiet.ID); err == nil || !strings.Contains(err.Error(), "changed nothing") {
		t.Errorf("revert of a run that changed nothing = %v", err)
	}
}

func TestCuratorPausesWhenTheMonthIsSpent(t *testing.T) {
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, &config.Curator{Budget: 100, Monthly: 100})
	if err := e.appendCuratorRun(CuratorRun{ID: "cr-old", Start: time.Now(), Credits: 80, Outcome: CuratorDone}); err != nil {
		t.Fatal(err)
	}
	run, err := e.RunCurator(context.Background(), "schedule")
	if err != nil || run.Outcome != CuratorNoBudget {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if len(cf.prompts) != 0 {
		t.Error("a curator with no budget left started a session")
	}
}

func TestCuratorTickRunsWhenAsked(t *testing.T) {
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, &config.Curator{Budget: 100, Schedule: "@weekly"})
	now := time.Now()
	if started, err := e.CuratorTick(context.Background(), now); err != nil || started {
		t.Fatalf("a fresh board ran the curator at once: %v %v", started, err)
	}
	if err := CuratorRunNow(e.cfg.Workspace); err != nil {
		t.Fatal(err)
	}
	if started, err := e.CuratorTick(context.Background(), now); err != nil || !started {
		t.Fatalf("run-now did not start a run: %v %v", started, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.CuratorRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reports := e.TakeCuratorReports()
	runs, _ := e.CuratorRuns()
	if len(runs) != 1 || runs[0].Why != "person" {
		t.Fatalf("ledger = %+v", runs)
	}
	if len(reports) != 0 {
		t.Errorf("a run that changed nothing raised a notice: %+v", reports)
	}
}

// A skill call counts as it happens, from any session: a freeform card's
// calls never reach the card-event log, and a stage's are pruned there.
func TestSkillUsageCountsAFreeformSessionsCalls(t *testing.T) {
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "Skill", Detail: "gummi-skills:make-test", CallID: "1"},
			{Kind: agent.EventToolCall, Tool: "Read", Detail: "/w/.gummi/state/skillpacks/0123abcd/make-test/SKILL.md", CallID: "2"},
			{Kind: agent.EventToolCall, Tool: "Read", Detail: "docs/SKILL.md", CallID: "3"},
			{Kind: agent.EventMessage, Text: "done"},
			{Kind: agent.EventIdle},
		}
	}}
	e := curatorEngine(t, ag, nil)
	ctx := context.Background()
	f := freeformCard(1, "uses skills")
	createFeature(t, e.cfg.Store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	u := e.SkillUsage(ctx)
	if u["make-test"].Count != 1 || u["make-test"].Views != 1 || len(u) != 1 {
		t.Fatalf("usage = %+v, want make-test invoked once and read once, nothing else", u)
	}
}

func TestSkillCalled(t *testing.T) {
	for _, c := range []struct {
		tool, detail, want string
		view               bool
	}{
		{"Skill", "gummi-skills:make-test", "make-test", false},
		{"skill", "gummi-make-test", "make-test", false},
		{"skill", "make-test", "", false}, // the person's own, on copilot or opencode
		{"Skill", "other-plugin:make-test", "", false},
		{"view", "/w/.gummi/state/skillpacks/0123abcd/make-test/SKILL.md", "make-test", true},
		{"view", "/w/.gummi/state/skillpacks/0123abcd/.prefixed/gummi-make-test/SKILL.md", "make-test", true},
		{"Read", "/tmp/gummi-claude-skills-1/gummi-skills/skills/make-test/SKILL.md", "make-test", true},
		{"read_file", ".gemini/config/skills/make-test/SKILL.md", "", false},
		{"Read", "docs/make-test/SKILL.md", "", false},
		{"Bash", "make test", "", false},
	} {
		if got, view := skillCalled(c.tool, c.detail); got != c.want || view != c.view {
			t.Errorf("skillCalled(%q, %q) = %q %v, want %q %v", c.tool, c.detail, got, view, c.want, c.view)
		}
	}
}

// gummi stops a run at its budget whether or not the backend does.
func TestCuratorStopsAtItsBudget(t *testing.T) {
	cf := newCuratorFake(nil)
	cf.Responder = func(agent.SessionOpts, string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 60}},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 60}},
			toolCall("1", skillWriteTool, map[string]any{"name": "late", "description": "Use when x.", "body": "y"}),
			{Kind: agent.EventIdle},
		}
	}
	e := curatorEngine(t, cf, &config.Curator{Budget: 100})
	run, err := e.RunCurator(context.Background(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != CuratorSpent || len(run.Written) != 0 {
		t.Fatalf("run = %+v, want it stopped at its budget before writing", run)
	}
}

// A run the board stopped mid-way leaves its row: its spend counts toward
// the month and what it wrote can be reverted as a run.
func TestARunCutShortKeepsItsRow(t *testing.T) {
	e := curatorEngine(t, newCuratorFake(nil), &config.Curator{Budget: 100})
	now := time.Now()
	for _, r := range []CuratorRun{
		{ID: "cr-1", Start: now, Outcome: CuratorRunning, Credits: 10},
		{ID: "cr-1", Start: now, Outcome: CuratorRunning, Credits: 40, Written: []string{"aa"}},
	} {
		if err := e.appendCuratorRun(r); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := e.CuratorRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ledger = %+v, %v", runs, err)
	}
	if r := runs[0]; r.Outcome != CuratorFailed || r.Credits != 40 || len(r.Written) != 1 {
		t.Errorf("a run cut short reads %+v", r)
	}
	if got := CuratorMonthSpent(runs, now); got != 40 {
		t.Errorf("month spent = %v, want the cut run's 40", got)
	}
}

// Reverting a run undoes its own commits only, not a pin a person made
// while it ran.
func TestRevertingARunLeavesWhatElseLandedMeanwhile(t *testing.T) {
	ctx := context.Background()
	e := curatorEngine(t, newCuratorFake(nil), &config.Curator{Budget: 100})
	pack := e.SkillPack()
	write := func(name, msg string) {
		t.Helper()
		if l, err := pack.Write(ctx, skillpack.Draft{Name: name, Description: "Use when " + name + ".", Body: "rule"}, nil, msg); err != nil || !l.OK() {
			t.Fatalf("write %s: %v %s", name, err, l)
		}
	}
	write("kept", "curator cr-0: write kept")
	before, _ := pack.Head(ctx)
	write("aa", curatorCommitPrefix("cr-1")+"write aa")
	if err := pack.Pin(ctx, "kept", true); err != nil {
		t.Fatal(err)
	}
	write("bb", curatorCommitPrefix("cr-1")+"write bb")
	after, _ := pack.Head(ctx)
	if err := e.appendCuratorRun(CuratorRun{ID: "cr-1", Start: time.Now(), Before: before, After: after, Outcome: CuratorDone}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RevertCuratorRun(ctx, "cr-1"); err != nil {
		t.Fatal(err)
	}
	skills, _ := pack.Skills(ctx, pack.Published(ctx))
	if _, ok := skills["aa"]; ok {
		t.Error("the run's skill aa survived its revert")
	}
	if _, ok := skills["bb"]; ok {
		t.Error("the run's skill bb survived its revert")
	}
	if !skills["kept"].Pinned {
		t.Error("reverting the run undid a person's pin made while it ran")
	}
}

// A run that failed after reading still read: the next is not offered
// the same cards again.
func TestAFailedRunsReadsCount(t *testing.T) {
	read := time.Now()
	seen := curatorSeenIn([]CuratorRun{{Outcome: CuratorFailed, ReadMemory: map[string]time.Time{"FF-1": read}}})
	if !seen.memory["FF-1"].Equal(read) {
		t.Errorf("a failed run's reads were dropped: %+v", seen.memory)
	}
}

// A fire missed while no board ran is caught up by the next board, as a
// schedule's is: the curator's clock does not restart with each board.
func TestAMissedCuratorFireIsCaughtUp(t *testing.T) {
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, &config.Curator{Budget: 100, Schedule: "@daily"})
	armed := filepath.Join(e.cfg.Workspace.StateDir(), "curator.armed")
	if err := os.MkdirAll(filepath.Dir(armed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(armed, []byte(time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	started, err := e.CuratorTick(context.Background(), time.Now())
	if err != nil || !started {
		t.Fatalf("a fire missed while no board ran was not caught up: %v %v", started, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.CuratorRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// A workflow card keeps no memory; the curator reads what it teaches off
// its events: the verdicts that asked for changes or failed, failed
// checks, stages sent back and answered questions, since the last run.
func TestCuratorReadsCardEvents(t *testing.T) {
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, &config.Curator{Budget: 100})
	ctx := context.Background()
	f := feature(4, "teach the curator", domain.StageImplement)
	quiet := feature(5, "nothing to learn", domain.StageImplement)
	for _, c := range []*domain.Feature{&f, &quiet} {
		if err := e.cfg.Store.CreateFeature(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-time.Hour)
	at := since.Add(time.Minute)
	ev := func(id domain.FeatureID, stage domain.Stage, kind, status string, payload any) state.CardEvent {
		at = at.Add(time.Second)
		raw, _ := json.Marshal(payload)
		return state.CardEvent{Feature: id, Stage: stage, Kind: kind, Status: status, At: at, Payload: string(raw)}
	}
	old := ev(f.ID, domain.StagePlan, state.EventAsk, "", state.AskPayload{Question: "an old question", Answer: "an old answer", By: "user"})
	old.At = since.Add(-time.Hour)
	evs := []state.CardEvent{
		old,
		ev(f.ID, domain.StageImplement, state.EventStageEnter, "", map[string]string{"role": "reviewer", "flavor": "critique"}),
		ev(f.ID, domain.StageImplement, state.EventMessage, "", map[string]string{"author": "assistant", "content": "The diff never runs the goldens.\nVERDICT: changes"}),
		ev(f.ID, domain.StageImplement, state.EventStageExit, "", map[string]string{"verdict": "changes"}),
		ev(f.ID, domain.StageVerify, state.EventTool, state.StatusFail, state.ToolPayload{Label: "check test: failed"}),
		ev(f.ID, domain.StageVerify, state.EventTool, state.StatusOK, state.ToolPayload{Label: "check test: passed"}),
		ev(f.ID, domain.StageVerify, state.EventStageEnter, "", map[string]string{"role": "reviewer", "flavor": "stage"}),
		ev(f.ID, domain.StageVerify, state.EventStageExit, "", map[string]string{"verdict": "fail"}),
		ev(f.ID, domain.StageVerify, state.EventGate, "", state.GatePayload{From: "verify", To: "implement", Actor: "auto"}),
		ev(f.ID, domain.StageImplement, state.EventAsk, "", state.AskPayload{Question: "Which port?", Answer: "8080, always", By: "user"}),
		ev(f.ID, domain.StageImplement, state.EventAsk, "", state.AskPayload{Question: "still open"}),
		ev(quiet.ID, domain.StageImplement, state.EventStageEnter, "", map[string]string{"role": "reviewer", "flavor": "critique"}),
		ev(quiet.ID, domain.StageImplement, state.EventStageExit, "", map[string]string{"verdict": "pass"}),
		ev(quiet.ID, domain.StagePlan, state.EventGate, "", state.GatePayload{From: "plan", To: "implement", Actor: "user"}),
	}
	for _, x := range evs {
		if err := e.cfg.Store.AppendEvent(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	ct := &curatorTurn{e: e, run: &CuratorRun{}, seen: curatorSeen{events: map[string]time.Time{
		string(f.ID): since, string(quiet.ID): since,
	}}}

	in := ct.inputs(ctx)
	want := "- " + string(f.ID) + " " + f.Name() + ": 1 critique or review asked for changes, 1 failed verify, " +
		"1 failing check, sent back once, 1 answered question"
	if !strings.Contains(in, want) {
		t.Errorf("curator_inputs does not list the card's events as\n%s\ngot:\n%s", want, in)
	}
	if strings.Contains(in, string(quiet.ID)) {
		t.Errorf("a card with a passing critique and a forward gate is listed:\n%s", in)
	}

	out, err := ct.dispatch(ctx, curatorEventsTool, json.RawMessage(`{"card":"`+string(f.ID)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"implement critique pass (reviewer) said changes: The diff never runs the goldens. VERDICT: changes",
		"verify stage pass (reviewer) said fail",
		"check test failed 1 of 2 runs",
		"sent back verify → implement by auto",
		`asked "Which port?"; user answered "8080, always"`,
		`asked "an old question"`, // the card's whole history, marked new or not
	} {
		if !strings.Contains(out, s) {
			t.Errorf("curator_events is missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "still open") {
		t.Errorf("an unanswered question is offered as a lesson:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		isOld := strings.Contains(line, "an old question")
		if isOld == strings.Contains(line, "(new)") && strings.HasPrefix(line, "- ") {
			t.Errorf("only events since the last run are marked new: %q", line)
		}
	}
	if !strings.Contains(curatorHint, curatorEventsTool) {
		t.Error("the hint does not name curator_events")
	}
	if _, err := ct.dispatch(ctx, curatorEventsTool, json.RawMessage(`{"card":"no-such"}`)); err == nil {
		t.Error("curator_events read a card that does not exist")
	}

	// Reading the card's events is recorded; a run that finished on it
	// is not offered the card again until it has new events.
	if _, ok := ct.run.ReadEvents[string(f.ID)]; !ok {
		t.Fatalf("the read was not recorded: %v", ct.run.ReadEvents)
	}
	ct.run.Outcome = CuratorDone
	next := &curatorTurn{e: e, run: &CuratorRun{}, seen: curatorSeenIn([]CuratorRun{*ct.run})}
	if cards, err := next.eventCards(ctx); err != nil || len(cards) != 0 {
		t.Errorf("a card whose events were read is offered again: %v, %v", cards, err)
	}
}

// The cutoff is each card's last read, not the last run's start: a run
// that read three cards of fifty leaves the rest new to the next, a run
// that read nothing moves nothing, and a card written after its read is
// offered again.
func TestCuratorOffersEveryCardItHasNotRead(t *testing.T) {
	ctx := context.Background()
	cf := newCuratorFake([]agent.Event{toolCall("1", curatorReadTool, map[string]any{"card": "FF-2"})})
	e := curatorEngine(t, cf, &config.Curator{Budget: 100})
	write := func(id, body string) {
		t.Helper()
		mem := e.cfg.Workspace.SessionMemoryFile(domain.FeatureID(id))
		if err := os.MkdirAll(filepath.Dir(mem), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mem, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("FF-1", "one")
	write("FF-2", strings.Repeat("x", 3000))
	write("FF-3", "three")
	offered := func() []string {
		runs, err := e.CuratorRuns()
		if err != nil {
			t.Fatal(err)
		}
		ct := &curatorTurn{e: e, run: &CuratorRun{}, seen: curatorSeenIn(runs)}
		var ids []string
		for _, c := range ct.memoryCards() {
			ids = append(ids, c.id)
		}
		slices.Sort(ids)
		return ids
	}

	// A finished run that read nothing moves no card; a failed one that
	// read FF-1 still read it.
	for _, r := range []CuratorRun{
		{ID: "cr-a", Outcome: CuratorDone, Start: time.Now()},
		{ID: "cr-b", Outcome: CuratorFailed, Start: time.Now(), ReadMemory: map[string]time.Time{"FF-1": time.Now()}},
	} {
		if err := e.appendCuratorRun(r); err != nil {
			t.Fatal(err)
		}
	}
	if got := offered(); !slices.Equal(got, []string{"FF-2", "FF-3"}) {
		t.Fatalf("after a run that read FF-1 the curator is offered %v", got)
	}

	run, err := e.RunCurator(ctx, "person")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.ReadMemory["FF-2"]; !ok || len(run.ReadMemory) != 1 {
		t.Fatalf("the run recorded reads %v, want FF-2", run.ReadMemory)
	}
	if !strings.Contains(cf.prompts[0], "FF-2 : 3 KB of memory") {
		t.Errorf("the kickoff does not size a card's memory:\n%s", cf.prompts[0])
	}
	if got := offered(); !slices.Equal(got, []string{"FF-3"}) {
		t.Fatalf("after reading FF-2 the curator is offered %v", got)
	}

	// The card's session writes again after the read: it is new again.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(e.cfg.Workspace.SessionMemoryFile("FF-2"), later, later); err != nil {
		t.Fatal(err)
	}
	if got := offered(); !slices.Equal(got, []string{"FF-2", "FF-3"}) {
		t.Fatalf("a card written after its read is not offered again: %v", got)
	}
}

// A curator model its backend does not offer is refused before it is
// saved; one it offers, no model, and a backend that cannot say pass.
func TestCheckCuratorModel(t *testing.T) {
	ctx := context.Background()
	ag := agent.NewFake("")
	ag.Models = []string{"m-small", "m-large"}
	e := curatorEngine(t, ag, nil)
	if err := e.CheckCuratorModel(ctx, &config.Curator{Backend: ag.Name(), Model: "m-tiny", Budget: 100}); err == nil {
		t.Error("a model the backend does not offer was accepted")
	}
	for _, model := range []string{"m-large", ""} {
		if err := e.CheckCuratorModel(ctx, &config.Curator{Backend: ag.Name(), Model: model, Budget: 100}); err != nil {
			t.Errorf("model %q refused: %v", model, err)
		}
	}
	quiet := agent.NewFake("")
	e = curatorEngine(t, quiet, nil)
	if err := e.CheckCuratorModel(ctx, &config.Curator{Backend: quiet.Name(), Model: "anything", Budget: 100}); err != nil {
		t.Errorf("a backend that cannot say refused a model: %v", err)
	}
}

// The grounding lint reads the scoped repository's base branch.
func TestMissingOnBase(t *testing.T) {
	ctx := context.Background()
	ws, store, _ := newRepo(t)
	pool, err := worktree.NewPool(ctx, ws.Root, ws.Root, nil, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Pool: pool, Workspace: ws})
	t.Cleanup(func() { e.Close() })
	got := e.missingOnBase(ctx, skillpack.ScopeDefault, []string{"README.md", "nope/x.go"})
	if !slices.Equal(got, []string{"nope/x.go"}) {
		t.Errorf("missing = %v, want only nope/x.go", got)
	}
}
