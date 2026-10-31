package workflowrecipe

import (
	"strings"
	"testing"
)

// A step may name its agent and model directly, and they survive a round trip.
func TestAStepNamesItsAgentAndModel(t *testing.T) {
	r, err := Parse([]byte("name: w\nsteps:\n  - id: a\n    role: reviewer\n    agent: Claude\n    model: claude-opus-5-5\n    prompt: look\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Steps[0].Step
	if s.Agent != "claude" || s.Model != "claude-opus-5-5" {
		t.Fatalf("agent %q model %q", s.Agent, s.Model)
	}
	if problems, _ := Check(r); len(problems) > 0 {
		t.Fatalf("problems = %v", problems)
	}
	out, err := MarshalEntries(r.Steps)
	if err != nil || !strings.Contains(string(out), "model: claude-opus-5-5") || !strings.Contains(string(out), "agent: claude") {
		t.Fatalf("marshal = %s, %v", out, err)
	}
}

// A malformed agent or model is a problem, a slot beside them a warning,
// and a step with no model of its own may name neither.
func TestAnAgentOrModelIsChecked(t *testing.T) {
	r, err := Parse([]byte("name: w\nsteps:\n  - id: a\n    role: reviewer\n    slot: x\n    agent: Bad Agent\n    model: \"a\\u00a0b\"\n    prompt: look\n"))
	if err != nil {
		t.Fatal(err)
	}
	problems, warnings := Check(r)
	all := strings.Join(problems, "\n")
	if len(problems) != 2 || !strings.Contains(all, `agent "bad agent" is not a backend name`) || !strings.Contains(all, "must be one model id with no spaces") {
		t.Fatalf("problems = %q", problems)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `its slot "x" is ignored`) {
		t.Fatalf("warnings = %q", warnings)
	}
	for _, step := range []string{
		"{id: a, run: x, agent: claude}",
		"{id: a, run: x, model: m}",
		"{id: a, pr: {act: create}, agent: claude}",
		"{id: a, watch: {until: merged}, model: m}",
		"{id: a, dispatch: {repo: r, title: t, envelope: 1, prompt: p}, agent: claude}",
	} {
		r, err := Parse([]byte("name: w\nsteps:\n  - " + step + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		problems, _ := Check(r)
		if !strings.Contains(strings.Join(problems, "\n"), "has no role, slot, agent, model, prompt or artifacts of its own") {
			t.Errorf("%s: problems = %q", step, problems)
		}
	}
}
