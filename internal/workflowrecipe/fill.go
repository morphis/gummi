package workflowrecipe

import (
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// Vars is what a step's prompt is filled from when it starts (§25.4).
type Vars struct {
	// Spec is the frozen spec revision's text.
	Spec string
	// Inputs are the inputs given at workflow_run.
	Inputs map[string]string
	// Artifacts maps an artifact's name to its absolute path in the run's
	// directory.
	Artifacts map[string]string
	// Previous and PreviousOutput are the step before's id and output.
	Previous       string
	PreviousOutput string
	// Outputs maps a step's id to its output — inside a repeat, its latest
	// iteration's (LatestOutputs).
	Outputs map[string]string
	// RunDiff is the path of the file holding the diff from the run's
	// starting tree to now.
	RunDiff string
	// Event is the event a watch handler's pass was started for, as a text
	// of facts; WatchEvent is the same text where a goto pass re-runs the
	// recipe's own steps (DESIGN §28.5).
	Event      string
	WatchEvent string
}

// Fill fills a prompt's templates. A step's output is wrapped in
// delimiters naming its step (Wrap), so text from one step cannot pose as
// instructions to the next. A template naming nothing Vars holds is an
// error, as is one naming no template at all; a step that has left no
// output (a failed branch of a settled parallel) fills as a note saying so.
func Fill(prompt string, v Vars) (string, error) {
	return fill(prompt, v, func(r ref) string {
		switch {
		case r.step != "":
			return Wrap(r.step, r.text)
		case r.input == "watch.event" && strings.TrimSpace(r.text) == "":
			return ""
		case r.input != "":
			return WrapInput(r.input, r.text)
		}
		return r.text
	})
}

// FillPlain fills the templates of text that is data, not instruction —
// a dispatch's title, spec or prompt — with each step's output as it
// stands, trimmed and with no delimiters around it.
func FillPlain(text string, v Vars) (string, error) {
	return fill(text, v, func(r ref) string { return strings.TrimSpace(r.text) })
}

// FillShell fills a command's templates. Every value stands as one
// single-quoted shell word, so an earlier step's output cannot add to the
// command it is filled into: write a template as a word of its own, never
// inside quotes.
func FillShell(command string, v Vars) (string, error) {
	return fill(command, v, func(r ref) string {
		return "'" + strings.ReplaceAll(strings.TrimSpace(r.text), "'", `'\''`) + "'"
	})
}

// ref is one resolved template: its text, and the id of the step it is the
// output of or the name of the input it is (each empty for anything else).
type ref struct{ step, input, text string }

func fill(text string, v Vars, render func(ref) string) (string, error) {
	var err error
	out := templateRe.ReplaceAllStringFunc(text, func(m string) string {
		r, e := v.resolve(templateRe.FindStringSubmatch(m)[1])
		if e != nil {
			if err == nil {
				err = e
			}
			return ""
		}
		return render(r)
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

func (v Vars) resolve(name string) (ref, error) {
	switch name {
	case "spec":
		return ref{text: v.Spec}, nil
	case "run.diff":
		if v.RunDiff == "" {
			return ref{}, fmt.Errorf("{{run.diff}}: no diff file was written")
		}
		return ref{text: v.RunDiff}, nil
	case "event":
		return ref{input: "event", text: v.Event}, nil
	case "watch.event":
		return ref{input: "watch.event", text: v.WatchEvent}, nil
	case "previous.output":
		if v.Previous == "" {
			return ref{}, fmt.Errorf("{{previous.output}}: no step ran before this one")
		}
		return ref{step: v.Previous, text: v.PreviousOutput}, nil
	}
	head, tail, ok := strings.Cut(name, ".")
	switch {
	case !ok:
	case head == "inputs":
		s, given := v.Inputs[tail]
		if !given {
			return ref{}, fmt.Errorf("{{%s}}: input %q was not given", name, tail)
		}
		return ref{input: tail, text: s}, nil
	case head == "artifacts":
		s, declared := v.Artifacts[tail]
		if !declared {
			return ref{}, fmt.Errorf("{{%s}}: no artifact %q in the run", name, tail)
		}
		return ref{text: s}, nil
	case tail == "output":
		return ref{step: head, text: v.Outputs[head]}, nil
	}
	return ref{}, fmt.Errorf("{{%s}} is not a template", name)
}

const (
	openMark       = "<<<gummi-output step=%q>>>"
	closeMark      = "<<<end gummi-output step=%q>>>"
	openInputMark  = "<<<gummi-input name=%q>>>"
	closeInputMark = "<<<end gummi-input name=%q>>>"
)

// Wrap puts a step's output between delimiters naming the step. A
// delimiter-like run of "<<<" inside the output is broken up, so an output
// cannot close its own wrapping and speak as gummi. One pass is not
// enough: it breaks a run of five into "<< <<<", whose second half still
// opens a delimiter, so the break repeats until no "<<<" is left.
func Wrap(step, output string) string {
	body := output
	for strings.Contains(body, "<<<") {
		body = strings.ReplaceAll(body, "<<<", "<< <")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		body = fmt.Sprintf("(step %s left no output)", step)
	}
	return fmt.Sprintf(openMark+"\n%s\n"+closeMark, step, body, step)
}

// WrapInput puts a run's input between delimiters naming it, as Wrap does
// a step's output: an input is text from outside the run — a bug report
// as it was filed — and must not pose as instructions to the step.
func WrapInput(name, text string) string {
	body := strings.TrimSpace(strings.ReplaceAll(text, "<<<", "<< <"))
	if body == "" {
		body = fmt.Sprintf("(input %s was left empty)", name)
	}
	return fmt.Sprintf(openInputMark+"\n%s\n"+closeInputMark, name, body, name)
}

// LatestOutputs maps each step id to the output of its latest instance,
// steps given in the order they started, so inside a repeat a step's id
// names its latest iteration. A step whose recipe keeps its output out
// (captureOutput: false) is left out by the caller.
func LatestOutputs(steps []domain.RunStep) map[string]string {
	out := map[string]string{}
	for _, s := range steps {
		if s.State == domain.StepDone || s.Output != "" {
			out[s.StepID] = s.Output
		}
	}
	return out
}
