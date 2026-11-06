package driver

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// triageRecipe reads a bug report and its optional logs, and writes its
// assessment as an artifact.
const triageRecipe = `name: triage
description: Triage a bug report against the tree
inputs:
  report: the bug report, as filed
  logs: {hint: logs attached to the report, required: false}
  version: {hint: the version it was seen on, default: latest}
steps:
  - id: reproduce
    role: scout
    prompt: |
      Triage the report below on {{inputs.version}}.

      {{inputs.report}}

      {{inputs.logs}}
  - id: assess
    role: scribe
    prompt: |
      Write the triage into {{artifacts.triage}}.

      {{reproduce.output}}
    artifacts: { triage: triage.md }
`

// A drive given a workflow and its inputs runs it with no session turn:
// gummi writes the spec from the inputs, the steps are given each input
// wrapped, an optional one left out fills as its default, and the run's
// end — its artifact and notes — is on the stream. The report is kept
// from the session, and the drive ends with the run.
func TestARunTriagesItsInputsWithNoSessionTurn(t *testing.T) {
	sf := newSessionFake("reply")
	h := newHarness(t, sf)
	out, f := h.run(Options{
		Envelope: 2000, WorkflowYAML: triageRecipe, NoSession: true,
		Inputs: map[string]string{"report": "The parser crashes on an empty file.\nSteps: run it on /dev/null."},
	}, "")
	if out.Status != StatusDone || out.Status.ExitCode() != 0 {
		t.Fatalf("outcome %+v, want done; stream %s", out, h.buf.String())
	}
	if !strings.Contains(f.Title, "parser crashes") {
		t.Fatalf("minted %q, want it named from the report", f.Title)
	}
	sf.mu.Lock()
	session, brief := sf.session, sf.briefs["reproduce"]
	sf.mu.Unlock()
	if len(session) != 0 {
		t.Fatalf("the session took turns: %q", session)
	}
	for _, want := range []string{
		"<<<gummi-input name=\"report\">>>\nThe parser crashes on an empty file.",
		"<<<gummi-input name=\"version\">>>\nlatest\n",
		"(input logs was left empty)",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("the step's brief lacks %q:\n%s", want, brief)
		}
	}
	started := h.last("workflow_started")
	if started == nil || started["recipe"] != "triage" || started["spec"] != float64(1) {
		t.Fatalf("workflow_started %v", started)
	}
	if in, _ := started["inputs"].(map[string]any); in["version"] != "latest" || in["logs"] != "" {
		t.Fatalf("workflow_started inputs %v, want the defaults resolved", started["inputs"])
	}
	ended := h.last("workflow_ended")
	if ended == nil || ended["state"] != string(domain.RunDone) || ended["report"] == "" {
		t.Fatalf("workflow_ended %v; stream %v", ended, h.eventKinds())
	}
	arts, _ := ended["artifacts"].(map[string]any)
	triage, _ := arts["triage"].(map[string]any)
	if triage == nil || !strings.Contains(triage["text"].(string), "severity high") {
		t.Fatalf("workflow_ended artifacts %v", ended["artifacts"])
	}
	if d, _ := ended["decisions"].([]any); len(d) != 1 || !strings.Contains(d[0].(string), "severity high") {
		t.Fatalf("workflow_ended decisions %v", ended["decisions"])
	}
	if o, _ := ended["open"].([]any); len(o) != 1 || !strings.Contains(o[0].(string), "1.3") {
		t.Fatalf("workflow_ended open %v", ended["open"])
	}
	spec, ok := h.eng.SessionSpec(f, 1)
	if !ok || !strings.Contains(spec, "### Input: report") || !strings.Contains(spec, "Nothing is checked") {
		t.Fatalf("the spec gummi wrote:\n%s", spec)
	}
	if done := h.last("done"); done == nil || done["status"] != "done" {
		t.Fatalf("done %v", done)
	}
}

// A drive given a spec runs the workflow against it, and the session
// reads the run's report before the drive is done.
func TestARunJudgedAgainstAGivenSpecSendsTheReport(t *testing.T) {
	sf := newSessionFake("reply")
	h := newHarness(t, sf)
	out, f := h.run(Options{Envelope: 2000, WorkflowYAML: wfRecipe, Spec: wfSpec}, "")
	if out.Status != StatusDone {
		t.Fatalf("outcome %+v, want done; stream %s", out, h.buf.String())
	}
	if !strings.Contains(f.Title, "Workflow test") {
		t.Fatalf("minted %q, want the spec's title", f.Title)
	}
	sf.mu.Lock()
	session := sf.session
	sf.mu.Unlock()
	if len(session) != 1 || !strings.Contains(session[0], "Workflow run 1 has reported") {
		t.Fatalf("the session's turns %q, want the run's report alone", session)
	}
	if ended := h.last("workflow_ended"); ended == nil || ended["state"] != string(domain.RunDone) {
		t.Fatalf("workflow_ended %v", ended)
	}
}

// A run whose done-when check fails ends partial, and so does the drive,
// exiting 5.
func TestARunWhoseCheckFailsIsPartial(t *testing.T) {
	h := newHarness(t, newSessionFake("reply"))
	out, _ := h.run(Options{
		Envelope: 2000, WorkflowYAML: triageRecipe, NoSession: true, Checks: []string{"false"},
		Inputs: map[string]string{"report": "it crashes"},
	}, "triage the crash")
	if out.Status != StatusPartial || out.Status.ExitCode() != 5 {
		t.Fatalf("outcome %+v, want partial; stream %s", out, h.buf.String())
	}
	if done := h.last("done"); done == nil || done["status"] != "partial" {
		t.Fatalf("done %v", done)
	}
}

// What the recipe would refuse, and options that contradict, fail before
// any card is minted.
func TestAWorkflowDriveIsRefusedBeforeAnyCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"an undeclared input", Options{WorkflowYAML: triageRecipe, Inputs: map[string]string{"report": "x", "log": "y"}}, "no input log"},
		{"a required input left out", Options{WorkflowYAML: triageRecipe}, "needs the input report"},
		{"checks beside a spec", Options{WorkflowYAML: wfRecipe, Spec: wfSpec, Checks: []string{"true"}}, "says its own done-when"},
		{"an unknown recipe", Options{Workflow: "no-such-recipe"}, "no-such-recipe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, newSessionFake("reply"))
			tc.opts.Envelope = 500
			out, err := h.driver(tc.opts).Run(context.Background(), "")
			if out.Status != StatusError || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("outcome %+v, %v; want refused with %q", out, err, tc.want)
			}
			if all, _ := h.store.ListFeatures(context.Background()); len(all) != 0 {
				t.Fatalf("a card was minted: %v", all)
			}
		})
	}
}

// TestRunWorkflowWithWorkflowsOff: a --workflow drive runs its recipe even
// when the workspace has workflows off.
func TestRunWorkflowWithWorkflowsOff(t *testing.T) {
	sf := newSessionFake("reply")
	h := newHarnessWorkflows(t, sf, false)
	out, f := h.run(Options{
		Envelope: 2000, WorkflowYAML: triageRecipe, NoSession: true,
		Inputs: map[string]string{"report": "The parser crashes on an empty file."},
	}, "")
	if out.Status != StatusDone {
		t.Fatalf("outcome %+v; stream %s", out, h.buf.String())
	}
	if on := h.eng.SessionWorkflows(context.Background(), f.ID); on == nil || !*on {
		t.Errorf("the session's switch = %v, want on", on)
	}
}
