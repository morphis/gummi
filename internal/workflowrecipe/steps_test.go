package workflowrecipe

import (
	"reflect"
	"strings"
	"testing"
)

const goodChild = "name: child\nsteps:\n  - id: work\n    role: implementer\n    prompt: do {{spec}}\n"

// indent puts text under a YAML block scalar's key.
func indent(text string) string {
	return "        " + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n        ") + "\n"
}

func gate(extra string) string {
	return "name: w\nsteps:\n  - id: check\n    run: ./check.sh\n    quietExit: 1\n" + extra
}

const goodDispatch = "  - id: bump\n    dispatch:\n      repo: myrepo\n      title: Bump to {{check.output}}\n      envelope: 5\n      recipe: fix-until-green\n      spec: |\n        Bump to {{check.output}}.\n"

func dispatchWith(fields string) string {
	return "name: w\nsteps:\n  - id: bump\n    dispatch:\n" + fields
}

// A recipe may hold a command step and a dispatch step, and Check refuses
// each shape the grammar rules out.
func TestCommandAndDispatchSteps(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"run with a role", "name: w\nsteps:\n  - {id: a, run: x, role: scout}\n", "no role, slot, agent, model, prompt or artifacts"},
		{"run with a slot", "name: w\nsteps:\n  - {id: a, run: x, slot: reviewer-alt}\n", "no role, slot, agent, model, prompt or artifacts"},
		{"run with a prompt", "name: w\nsteps:\n  - {id: a, run: x, prompt: hi}\n", "no role, slot, agent, model, prompt or artifacts"},
		{"run with artifacts", "name: w\nsteps:\n  - {id: a, run: x, artifacts: {n: n.md}}\n", "no role, slot, agent, model, prompt or artifacts"},
		{"dispatch with a role", dispatchWith("      {repo: r, title: t, envelope: 1, prompt: p}\n    role: scout\n"), "no role, slot, agent, model, prompt or artifacts"},
		{"run with an envelope", "name: w\nsteps:\n  - {id: a, run: x, envelope: 3}\n", "no envelope of its own"},
		{"quietExit 0", "name: w\nsteps:\n  - {id: a, run: x, quietExit: 0}\n", "from 1 to 255"},
		{"quietExit 256", "name: w\nsteps:\n  - {id: a, run: x, quietExit: 256}\n", "from 1 to 255"},
		{"quietExit on a dispatch", dispatchWith("      {repo: r, title: t, envelope: 1, prompt: p}\n    quietExit: 1\n"), "belongs to a command step"},
		{"quietExit on a model step", "name: w\nsteps:\n  - {id: a, role: scout, prompt: p, quietExit: 1}\n", "belongs to a command step"},
		{"quietExit after the first step", "name: w\nsteps:\n  - {id: a, run: x}\n  - {id: b, run: y, quietExit: 1}\n", "first step"},
		{"run and dispatch", dispatchWith("      {repo: r, title: t, envelope: 1, prompt: p}\n    run: x\n"), "not both"},
		{"repo is a template", dispatchWith("      {repo: '{{inputs.r}}', title: t, envelope: 1, prompt: p}\n"), "never a template"},
		{"repo is a path", dispatchWith("      {repo: ../r, title: t, envelope: 1, prompt: p}\n"), "not a path"},
		{"repo is empty", dispatchWith("      {title: t, envelope: 1, prompt: p}\n"), "names the repository"},
		{"envelope is zero", dispatchWith("      {repo: r, title: t, prompt: p}\n"), "envelope above zero"},
		{"title is empty", dispatchWith("      {repo: r, envelope: 1, prompt: p}\n"), "title"},
		{"recipe without a spec", dispatchWith("      {repo: r, title: t, envelope: 1, recipe: deliver}\n"), "gives its session the spec"},
		{"recipe with a prompt", dispatchWith("      {repo: r, title: t, envelope: 1, recipe: deliver, spec: s, prompt: p}\n"), "give it a spec, not a prompt"},
		{"no recipe without a prompt", dispatchWith("      {repo: r, title: t, envelope: 1}\n"), "opening prompt"},
		{"no recipe with a spec", dispatchWith("      {repo: r, title: t, envelope: 1, prompt: p, spec: s}\n"), "name a recipe"},
		{"frozen recipe is not YAML", dispatchWith("      repo: r\n      title: t\n      envelope: 1\n      spec: s\n      recipe: x\n      recipeYAML: \"steps: [\"\n"), "its frozen recipe"},
		{"frozen recipe fails Check", dispatchWith("      repo: r\n      title: t\n      envelope: 1\n      spec: s\n      recipe: x\n      recipeYAML: |\n" + indent("name: c\nsteps:\n  - {id: a, role: scout}\n")), "cannot run"},
		{"frozen recipe dispatches", dispatchWith("      repo: r\n      title: t\n      envelope: 1\n      spec: s\n      recipe: x\n      recipeYAML: |\n" + indent("name: c\nsteps:\n  - id: a\n    dispatch: {repo: q, title: t, envelope: 1, prompt: p}\n")), "cannot dispatch in turn"},
		{"unknown template in run", "name: w\nsteps:\n  - {id: a, run: 'echo {{nope.output}}'}\n", "nope"},
		{"run reads the diff", "name: w\nsteps:\n  - {id: a, run: 'cat {{run.diff}}'}\n", "run.diff"},
		{"unknown template in title", dispatchWith("      {repo: r, title: '{{nope.output}}', envelope: 1, prompt: p}\n"), "title {{nope.output}}"},
		{"unknown template in spec", dispatchWith("      {repo: r, title: t, envelope: 1, recipe: deliver, spec: '{{nope.output}}'}\n"), "spec {{nope.output}}"},
		{"unknown template in prompt", dispatchWith("      {repo: r, title: t, envelope: 1, prompt: '{{nope.output}}'}\n"), "prompt {{nope.output}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			problems, _ := Check(r)
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("problems = %q, want one saying %q", problems, tc.want)
			}
		})
	}

	t.Run("a gate and a dispatch check clean and round-trip", func(t *testing.T) {
		yml := gate(goodDispatch)
		r, err := Parse([]byte(yml))
		if err != nil {
			t.Fatal(err)
		}
		if problems, _ := Check(r); len(problems) > 0 {
			t.Fatalf("problems: %q", problems)
		}
		g, d := r.Steps[0].Step, r.Steps[1].Step
		if g.Run != "./check.sh" || g.QuietExit == nil || *g.QuietExit != 1 || !g.Agentless() || g.Kind() != "run" {
			t.Errorf("gate = %+v", g)
		}
		if d.Dispatch == nil || d.Dispatch.Repo != "myrepo" || d.Dispatch.Envelope != 500 || d.Dispatch.Recipe != "fix-until-green" ||
			d.Dispatch.Spec != "Bump to {{check.output}}.\n" || d.Kind() != "dispatch" {
			t.Errorf("dispatch = %+v", d)
		}
		out, err := Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(out)
		if err != nil {
			t.Fatalf("reparse: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(r, back) {
			t.Errorf("round trip changed the recipe:\n%+v\n%+v", r, back)
		}
	})

	t.Run("a frozen recipe passes", func(t *testing.T) {
		yml := dispatchWith("      repo: r\n      title: t\n      envelope: 1\n      spec: s\n      recipe: x\n      recipeYAML: |\n" + indent(goodChild))
		r, err := Parse([]byte(yml))
		if err != nil {
			t.Fatal(err)
		}
		if problems, _ := Check(r); len(problems) > 0 {
			t.Fatalf("problems: %q", problems)
		}
	})
}

// A value filled into a command stands as one shell word.
func TestFillShellQuotesEveryValue(t *testing.T) {
	v := Vars{Outputs: map[string]string{"check": "v1'; rm -rf /\n"}}
	got, err := FillShell("echo {{check.output}}", v)
	if err != nil || got != `echo 'v1'\''; rm -rf /'` {
		t.Fatalf("FillShell = %q, %v", got, err)
	}
	if got, _ := FillPlain("Bump to {{check.output}}", Vars{Outputs: map[string]string{"check": " v2\n"}}); got != "Bump to v2" {
		t.Errorf("FillPlain = %q", got)
	}
}

func TestAQuotedTemplateInACommandIsRefused(t *testing.T) {
	for cmd, bad := range map[string]bool{
		`echo {{spec}}`:       false,
		`echo '{{spec}}'`:     true,
		`echo "a {{spec}} b"`: true,
		`echo 'x' {{spec}}`:   false,
		`echo "\"" {{spec}}`:  false,
	} {
		if got := templateQuoted(cmd); got != bad {
			t.Errorf("templateQuoted(%q) = %v", cmd, got)
		}
	}
}

// TestADispatchEnvelopeIsInDollars: a recipe writes a dispatch's envelope
// the way a person types any budget, in dollars, and it is held in credits;
// a figure finer than a cent is refused, and it writes back as dollars.
func TestADispatchEnvelopeIsInDollars(t *testing.T) {
	for in, want := range map[string]int{"5": 500, "2.50": 250, "0.3": 30} {
		r, err := Parse([]byte(dispatchWith("      {repo: r, title: t, envelope: " + in + ", prompt: p}\n")))
		if err != nil {
			t.Fatalf("envelope %s: %v", in, err)
		}
		d := Steps(r)[0].Dispatch
		if d == nil || d.Envelope != want {
			t.Fatalf("envelope %s = %+v, want %d credits", in, d, want)
		}
		out, err := Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(out)
		if err != nil || Steps(back)[0].Dispatch.Envelope != want {
			t.Fatalf("envelope %s did not round-trip:\n%s", in, out)
		}
	}
	if _, err := Parse([]byte(dispatchWith("      {repo: r, title: t, envelope: 0.001, prompt: p}\n"))); err == nil || !strings.Contains(err.Error(), "finer than a cent") {
		t.Errorf("a sub-cent envelope parsed: %v", err)
	}
}
