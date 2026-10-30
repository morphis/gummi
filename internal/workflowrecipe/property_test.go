package workflowrecipe

// Property tests of filling a step's templates and reading a planner's
// block, driven by hegel (https://hegel.dev). A step's output is text an
// agent wrote, and these are the boundaries it crosses: into a shell
// command (FillShell, checked against a real sh), into the next step's
// prompt (Fill and Wrap), and — for a planner — into the run's plan
// (ParsePlanBlock). Values are drawn from shell metacharacters, gummi's
// own delimiters and templates, code fences and newlines.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

const propCases = 2000

func props(t *testing.T, n int, fn func(*hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(n), hegel.WithDatabase(""))
}

var propPieces = []string{
	"ok", "a b", " ", "\n", "\t", "'", `"`, `\`, "$", "$(id)", "`id`", "${HOME}", ";", "&&", "|", ">", "<",
	"<<<", "<<", "<", ">>>", "end gummi-output step=", `<<<end gummi-output step="a">>>`, `"a"`, "gummi-output", "{{spec}}", "{{a.output}}", "{{", "}}",
	"*", "?", "~", "#", "!", "%", "é", "```", "```go", "- id: x", ":",
}

func drawText(ht *hegel.T) string {
	ps := hegel.Draw(ht, hegel.Lists(hegel.SampledFrom(propPieces)).MaxSize(12))
	return strings.Join(ps, "")
}

// TestPropertyAFilledCommandPassesAnOutputAsOneWord: an output filled
// into a command as a word of its own reaches the program as exactly one
// argument holding the output (trimmed), whatever shell syntax it holds.
// A shell word cannot hold a NUL, so none is drawn.
func TestPropertyAFilledCommandPassesAnOutputAsOneWord(t *testing.T) {
	props(t, 500, func(ht *hegel.T) {
		out := drawText(ht)
		cmd, err := FillShell(`printf '%s|' {{a.output}}`, Vars{Outputs: map[string]string{"a": out}})
		if err != nil {
			ht.Fatalf("FillShell: %v", err)
		}
		var got, stderr bytes.Buffer
		c := exec.Command("sh", "-c", cmd)
		c.Stdout, c.Stderr = &got, &stderr
		if err := c.Run(); err != nil {
			ht.Fatalf("sh -c %q: %v: %s", cmd, err, stderr.String())
		}
		if want := strings.TrimSpace(out) + "|"; got.String() != want {
			ht.Fatalf("sh -c %q printed %q, want %q", cmd, got.String(), want)
		}
	})
}

// TestPropertyAWrappedOutputCannotCloseItsWrapping: whatever an output
// holds, its wrapping has one opening and one closing delimiter — the
// output cannot end its own wrapping and speak as gummi.
func TestPropertyAWrappedOutputCannotCloseItsWrapping(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		out := drawText(ht)
		w := Wrap("a", out)
		if n := strings.Count(w, "<<<end gummi-output"); n != 1 {
			ht.Fatalf("Wrap(%q) holds %d closing delimiters:\n%s", out, n, w)
		}
		if n := strings.Count(w, "<<<gummi-output"); n != 1 {
			ht.Fatalf("Wrap(%q) holds %d opening delimiters:\n%s", out, n, w)
		}
	})
}

// TestPropertyAFilledValueIsNotFilledAgain: a value is filled in as it
// stands — a template inside an output, an input or the spec is text, not
// filled in turn — by Fill, FillPlain and FillShell alike.
func TestPropertyAFilledValueIsNotFilledAgain(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		val := "{{spec}}" + drawText(ht)
		v := Vars{Spec: "SPEC", Outputs: map[string]string{"a": val}, Inputs: map[string]string{"x": val}}
		for name, fill := range map[string]func(string, Vars) (string, error){"Fill": Fill, "FillPlain": FillPlain, "FillShell": FillShell} {
			for _, tmpl := range []string{"{{a.output}}", "{{inputs.x}}"} {
				got, err := fill(tmpl, v)
				if err != nil {
					ht.Fatalf("%s(%q): %v", name, tmpl, err)
				}
				if strings.Contains(got, "SPEC") {
					ht.Fatalf("%s(%q) filled a template inside the value %q: %q", name, tmpl, val, got)
				}
			}
		}
	})
}

// TestPropertyAPlanBlockReadsItsPromptsAsWritten: a planner's block whose
// prompts hold code fences, quotes and colons — written as YAML block
// scalars, as a planner writes a long prompt — reads back with each
// prompt as written.
func TestPropertyAPlanBlockReadsItsPromptsAsWritten(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 3))
		var b strings.Builder
		var want []string
		for i := range n {
			lines := hegel.Draw(ht, hegel.Lists(hegel.SampledFrom([]string{
				"look at the code", "```go", "```", "x := 1", "- not a list", "a: b", `"quoted"`, "# heading", "{{spec}}",
			})).MinSize(1).MaxSize(6))
			fmt.Fprintf(&b, "- id: s%d\n  role: implementer\n  prompt: |-\n", i+1)
			for _, l := range lines {
				b.WriteString("    " + l + "\n")
			}
			want = append(want, strings.Join(lines, "\n"))
		}
		out := "Here is the plan.\n\n```gummi-workflow\n" + b.String() + "```\n"
		entries, err := ParsePlanBlock(out)
		if err != nil {
			ht.Fatalf("ParsePlanBlock:\n%s\n%v", out, err)
		}
		var got []string
		for _, e := range entries {
			if e.Step == nil {
				ht.Fatalf("entry %+v is not a step", e)
			}
			got = append(got, e.Step.Prompt)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			ht.Fatalf("plan:\n%s\nprompts read back %q, want %q", out, got, want)
		}
	})
}
