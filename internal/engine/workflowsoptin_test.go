package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// optinSession opens a session on a fake that records, for each backend
// start, the tools and hints it was given. The engine has workflows as
// asked (the workspace setting).
func optinSession(t *testing.T, workflows bool) (ff *FreeformSession, e *Engine, tools, hints func() string) {
	t.Helper()
	var mu sync.Mutex
	var ts, hs string
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true, Resume: true}
	ag.OnNewSession = func(opts agent.SessionOpts) {
		mu.Lock()
		defer mu.Unlock()
		var names []string
		for _, td := range opts.Tools {
			names = append(names, td.Name)
		}
		ts, hs = strings.Join(names, " "), strings.Join(opts.SystemHints, "\n")
	}
	ws, store, wt := newRepo(t)
	e = New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Workflows: workflows})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "tidy the parser")
	createFeature(t, store, f)
	var err error
	if ff, err = e.OpenFreeform(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	get := func(p *string) func() string {
		return func() string { mu.Lock(); defer mu.Unlock(); return *p }
	}
	return ff, e, get(&ts), get(&hs)
}

func hasWorkflowOrSpecTool(tools string) bool {
	for _, n := range strings.Fields(tools) {
		if workflowOrSpecTool(n) {
			return true
		}
	}
	return false
}

func commandNames(cmds []ProjectCommand) string {
	var n []string
	for _, c := range cmds {
		n = append(n, c.Name)
	}
	return strings.Join(n, " ")
}

func TestWorkflowsOffSessionHearsNothing(t *testing.T) {
	ctx := context.Background()
	ff, e, tools, hints := optinSession(t, false)
	say(t, ff, "hello")
	if hasWorkflowOrSpecTool(tools()) {
		t.Errorf("tools = %s", tools())
	}
	h := hints()
	for _, bad := range []string{sessionSpecHint, "workflow_run", "workflow-recipes"} {
		if strings.Contains(h, bad) {
			t.Errorf("hints mention %q", bad)
		}
	}
	if strings.Contains(commandNames(ff.Commands()), "workflow") {
		t.Errorf("commands = %s", commandNames(ff.Commands()))
	}
	cf := newCuratorFake(nil)
	f := &domain.Feature{ID: ff.id}
	if names := baseNames(e.skillDirsFor(ctx, cf, f, string(ff.id), false)); names != "gummi-publishing-prs" {
		t.Errorf("skills = %q", names)
	}
}

func TestWorkflowsOnSessionGetsThem(t *testing.T) {
	ctx := context.Background()
	ff, e, tools, hints := optinSession(t, true)
	say(t, ff, "hello")
	for _, n := range []string{specStartToolName, workflowRunToolName} {
		if !strings.Contains(tools(), n) {
			t.Errorf("tools lack %s: %s", n, tools())
		}
	}
	if !strings.Contains(hints(), sessionSpecHint) {
		t.Error("hints lack the spec hint")
	}
	if !strings.Contains(commandNames(ff.Commands()), "workflow") {
		t.Errorf("commands = %s", commandNames(ff.Commands()))
	}
	cf := newCuratorFake(nil)
	if names := baseNames(e.skillDirsFor(ctx, cf, &domain.Feature{ID: ff.id}, string(ff.id), false)); !strings.Contains(names, "workflows") {
		t.Errorf("skills = %q", names)
	}
}

func TestSessionWorkflowsOverridesGlobal(t *testing.T) {
	ctx := context.Background()
	for _, global := range []bool{false, true} {
		_, e, _, _ := optinSession(t, global)
		id := freeformCard(1, "x").ID
		if e.WorkflowsOn(ctx, id) != global {
			t.Errorf("unset follows the global %v", global)
		}
		on, off := true, false
		if err := e.SetSessionWorkflows(ctx, id, &on, ""); err != nil || !e.WorkflowsOn(ctx, id) {
			t.Errorf("override on: %v", err)
		}
		if err := e.SetSessionWorkflows(ctx, id, &off, ""); err != nil || e.WorkflowsOn(ctx, id) {
			t.Errorf("override off: %v", err)
		}
		if err := e.SetSessionWorkflows(ctx, id, nil, ""); err != nil || e.WorkflowsOn(ctx, id) != global {
			t.Errorf("cleared: %v", err)
		}
	}
}

func TestWorkflowsOffSkipsTriage(t *testing.T) {
	// the title is one the person chose: nothing for triage to name
	ff, _, worked, triaged := triageSessionTitled(t, true, false, "TRIAGE: deliver — big\nNAME: Whatever", "add spec states")
	say(t, ff, bigAsk)
	if len(triaged()) != 0 {
		t.Fatalf("triage read the request: %q", triaged())
	}
	if w := worked(); len(w) != 1 || w[0] != bigAsk {
		t.Fatalf("the agent was sent %q", w)
	}
}

func TestWorkflowsOffTriageOnlyNames(t *testing.T) {
	ff, e, worked, triaged := triageSessionTitled(t, true, false, "TRIAGE: deliver — big\nNAME: Session spec states", domain.DeriveTitle(bigAsk))
	say(t, ff, bigAsk)
	asks := triaged()
	if len(asks) != 1 || strings.Contains(asks[0], "deliver") || strings.Contains(asks[0], "TRIAGE") || !strings.Contains(asks[0], "NAME:") {
		t.Fatalf("triage was shown %q", asks)
	}
	if f, _ := e.feature(context.Background(), ff.id); f.Title != "Session spec states" {
		t.Errorf("title = %q", f.Title)
	}
	for _, w := range worked() {
		if strings.Contains(w, "workflow recipe") {
			t.Errorf("agent got a recipe note: %q", w)
		}
	}
	if strings.Contains(transcriptOf(ff), "workflow") {
		t.Errorf("thread names a workflow: %s", transcriptOf(ff))
	}
}

func TestWorkflowToolsRefuseWhenOff(t *testing.T) {
	e, s, f := specToolsSession(t)
	ctx := context.Background()
	if got := specCall(t, e, s, "a", specStartToolName, `{"title":"x"}`); !strings.Contains(got, "created spec") {
		t.Fatalf("on: %q", got)
	}
	off := false
	// a stale tool list: the switch is flipped under a live backend
	if err := e.cfg.Store.SetSessionWorkflows(ctx, f.ID, &off); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ name, args string }{
		{specStartToolName, `{"title":"y"}`},
		{specListToolName, `{}`},
		{workflowRunToolName, `{"recipe":"deliver"}`},
	} {
		if got := specCall(t, e, s, string(rune('b'+i)), tc.name, tc.args); got != workflowsOffRefusal {
			t.Errorf("%s answered %q", tc.name, got)
		}
	}
	if specs, _ := e.SessionSpecs(ctx, f); len(specs) != 1 {
		t.Errorf("%d specs", len(specs))
	}
}

func TestSetSessionWorkflowsRestartsTheBackend(t *testing.T) {
	ctx := context.Background()
	ff, e, tools, _ := optinSession(t, true)
	say(t, ff, "hello")
	off, on := false, true
	if err := e.SetSessionWorkflows(ctx, ff.id, &off, "Sam"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transcriptOf(ff), "Sam turns workflows off for this session") {
		t.Errorf("thread: %s", transcriptOf(ff))
	}
	say(t, ff, "again")
	if hasWorkflowOrSpecTool(tools()) {
		t.Errorf("tools after off = %s", tools())
	}
	if err := e.SetSessionWorkflows(ctx, ff.id, &on, ""); err != nil {
		t.Fatal(err)
	}
	say(t, ff, "once more")
	if !strings.Contains(tools(), workflowRunToolName) {
		t.Errorf("tools after on = %s", tools())
	}
	// a closed card or a missing one is refused
	if err := e.SetSessionWorkflows(ctx, "FF-999", &on, ""); err == nil {
		t.Error("a missing card was accepted")
	}
	_ = errors.Is(ErrWorkflowsBusy, ErrWorkflowsBusy)
}

func TestSetWorkflowsDefaultIsLive(t *testing.T) {
	ctx := context.Background()
	_, e, _, _ := optinSession(t, false)
	id := freeformCard(1, "x").ID
	e.SetWorkflowsDefault(true)
	if !e.WorkflowsOn(ctx, id) || !e.WorkflowsDefault() {
		t.Error("not live")
	}
	off := false
	_ = e.SetSessionWorkflows(ctx, id, &off, "")
	if e.WorkflowsOn(ctx, id) {
		t.Error("override lost to the global")
	}
}

func TestSkillStageHintNamesNoWorkflowSkillsWhenOff(t *testing.T) {
	h := skillStageHintFor(false)
	if strings.Contains(h, "workflow") || !strings.Contains(h, "publishing-prs") {
		t.Errorf("hint: %s", h)
	}
}

func TestWorkflowsOffAgentHearsNoWorkflow(t *testing.T) {
	wf := newWFFake()
	var mu sync.Mutex
	var hints, tools string
	wf.OnNewSession = func(opts agent.SessionOpts) {
		mu.Lock()
		defer mu.Unlock()
		hints = strings.Join(opts.SystemHints, "\n")
		var n []string
		for _, td := range opts.Tools {
			n = append(n, td.Name)
		}
		tools = strings.Join(n, " ")
	}
	e, _, f, ff := watchEngine(t, wf, 2000)
	off := false
	if err := e.SetSessionWorkflows(context.Background(), f.ID, &off, ""); err != nil {
		t.Fatal(err)
	}
	say(t, ff, "hello")
	mu.Lock()
	defer mu.Unlock()
	for _, n := range []string{scheduleProposeToolName, sessionStatusToolName, sessionSendToolName} {
		if !strings.Contains(tools, n) {
			t.Errorf("tools lack %s: %s", n, tools)
		}
	}
	if hasWorkflowOrSpecTool(tools) {
		t.Errorf("tools = %s", tools)
	}
	for _, bad := range []string{"workflow_run", "dispatch step", "you dispatch one"} {
		if strings.Contains(hints, bad) {
			t.Errorf("hints mention %q", bad)
		}
	}
}

func TestAStepCardKeepsWorkflowSkillsWithWorkflowsOff(t *testing.T) {
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, nil)
	e.SetWorkflowsDefault(false)
	ctx := context.Background()
	names := baseNames(e.skillDirsFor(ctx, cf, &domain.Feature{ID: "FF-1"}, "FF-1", false))
	if names != "gummi-publishing-prs" {
		t.Errorf("a session with workflows off gets %q", names)
	}
	step := &domain.Feature{ID: "FF-2", Child: domain.ChildLink{Seq: 1}}
	names = baseNames(e.skillDirsFor(ctx, cf, step, "FF-2", false))
	if names != "gummi-publishing-prs,gummi-workflow-recipes,gummi-workflows" {
		t.Errorf("a step card gets %q", names)
	}
}

func TestADispatchedRunSessionIsOfferedWorkflows(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := watchEngine(t, wf, 2000)
	e.SetWorkflowsDefault(false)
	ctx := context.Background()
	recipe := "name: watch\ntier: cheap\nsteps:\n  - { id: check, run: \"echo v2\" }\n" +
		"  - id: bump\n    dispatch:\n      repo: target\n      title: \"Bump\"\n      envelope: 0.30\n" +
		"      recipe: childwork\n      spec: |\n        Bump.\n"
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: recipe, Spec: 1})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	ds, err := store.Dispatched(ctx, f.ID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("dispatches %+v %v", ds, err)
	}
	if !e.WorkflowsOn(ctx, ds[0].Card) {
		t.Error("the dispatched session was not offered workflows")
	}
}
