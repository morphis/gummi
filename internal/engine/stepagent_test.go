package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// An unknown agent is refused wherever a run may start the step: a repeat's
// body, a parallel's, and the recipe a dispatch froze.
func TestAnUnknownAgentIsRefusedInNestedSteps(t *testing.T) {
	bad := domain.Step{ID: "b", Role: domain.RoleReviewer, Agent: "claud"}
	es := []domain.Entry{
		{Repeat: &domain.Repeat{ID: "loop", Steps: []domain.Entry{{Step: &bad}}}},
		{Parallel: &domain.Parallel{ID: "par", Steps: []domain.Step{bad}}},
		{Step: &domain.Step{ID: "d", Dispatch: &domain.Dispatch{
			Recipe: "x", RecipeYAML: "name: x\nsteps:\n  - {id: c, role: scout, agent: claud, prompt: p}\n",
		}}},
	}
	p := agentProblems(es)
	if len(p) != 3 || !strings.HasPrefix(p[2], "dispatch d: step c: unknown agent") {
		t.Fatalf("problems = %q", p)
	}
	// a misspelt name is Check's to report, once
	if p := agentProblems([]domain.Entry{{Step: &domain.Step{ID: "a", Agent: "Bad Agent"}}}); len(p) != 0 {
		t.Fatalf("problems = %q", p)
	}
}

// A model alone runs on the backend the profile gives the step, and one
// that backend cannot run is refused before the run starts.
func TestAModelAloneIsCheckedAgainstItsProfilesBackend(t *testing.T) {
	e := New(Config{Agents: map[string]agent.Agent{"": agent.NewFake("ok")}, Profiles: config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{"p": {
			"reviewer":    {Backend: "opencode", Model: "openai/gpt-5"},
			"implementer": {Backend: "claude"},
		}},
	}})
	t.Cleanup(func() { e.Close() })
	step := func(role domain.WorkflowRole, agentName, model string) []domain.Entry {
		return []domain.Entry{{Step: &domain.Step{ID: "s", Role: role, Agent: agentName, Model: model}}}
	}
	if p := e.modelProblems("p", step(domain.RoleReviewer, "", "claude-opus-5-5")); len(p) != 1 || !strings.Contains(p[0], "provider/model") {
		t.Errorf("a claude model on opencode: %q", p)
	}
	if p := e.modelProblems("p", step(domain.RoleImplementer, "", "claude-opus-5-5")); len(p) != 0 {
		t.Errorf("a claude model on claude: %q", p)
	}
	// an agent it names is agentProblems' to judge
	if p := e.modelProblems("p", step(domain.RoleReviewer, "claude", "claude-opus-5-5")); len(p) != 0 {
		t.Errorf("a named agent: %q", p)
	}
	// a backend gummi has no adapter for in its list (the fake) is not judged
	if p := e.modelProblems("p", step(domain.RoleScout, "", "anything")); len(p) != 0 {
		t.Errorf("the default fake: %q", p)
	}
}

// A proposal naming an unknown agent, even inside a loop, is refused.
func TestAProposalNamingAnUnknownAgentIsRefused(t *testing.T) {
	e, _, f, _ := wfEngine(t, newWFFake())
	_, err := e.WorkflowPropose(context.Background(), f.ID, WorkflowRequest{RecipeYAML: "name: w\nsteps:\n" +
		"  - id: loop\n    repeat:\n      max: 2\n      until: { contains: OK }\n      steps:\n        - {id: a, role: scout, agent: claud, prompt: look}\n"})
	if err == nil || !strings.Contains(err.Error(), `unknown agent "claud"`) {
		t.Fatalf("err = %v", err)
	}
}

// A step that names its agent and model runs on them: its card records
// both, and that agent answers it.
func TestAStepRunsOnTheAgentAndModelItNames(t *testing.T) {
	wf := newWFFake()
	wf.on("look", func(_, _ string, _ int) wfReply { return wfReply{text: "seen", credits: 1} })
	e, store, f, _ := wfEngineWith(t, wf, map[string]agent.Agent{"": wf, "fake": wf, "claude": wf})
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: w\nsteps:\n" +
		"  - {id: look, role: scout, agent: claude, model: claude-opus-5-5, slot: reviewer-alt, prompt: look}\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	s := stepPaths(t, e, f.ID, run.N)["look"]
	if s.State != domain.StepDone {
		t.Fatalf("step %+v", s)
	}
	if s.Slot != "" {
		t.Errorf("the step records slot %q, which its agent and model override", s.Slot)
	}
	card, err := store.GetFeature(context.Background(), s.Card)
	if err != nil {
		t.Fatal(err)
	}
	if card.SessionBackend != "claude" || card.SessionModel != "claude-opus-5-5" {
		t.Errorf("the step's card runs %q on %q", card.SessionModel, card.SessionBackend)
	}
	if wf.count("look") != 1 {
		t.Errorf("the agent answered the step %d times", wf.count("look"))
	}
}
