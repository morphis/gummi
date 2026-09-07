package reentry

import (
	"reflect"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

const note = "the persistence step was never in the spec"

// This table is the rule set's single specification: one row per route,
// plus the boundaries that make the routes safe. Read it top to bottom
// as the spec. Nothing here constructs a store, a session or an agent —
// that is the property the package exists to have, and a test that
// needed one would mean Decide had stopped being pure.
func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Outcome
	}{
		// --- the floor ------------------------------------------------
		{
			name: "an empty line routes nowhere",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: PlanWrong, Note: "   "},
			want: Outcome{Reason: "no-note"},
		},
		{
			name: "a sentence nothing classified is a turn, never an invented move",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: "", Note: note},
			want: Outcome{Note: note, Reason: "unclassified"},
		},
		{
			name: "a word outside the vocabulary is a turn too",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: Intent("rewrite_everything"), Note: note},
			want: Outcome{Note: note, Reason: "unclassified"},
		},
		{
			name: "a question is answered, not routed",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: Question, Note: note},
			want: Outcome{Note: note, Reason: "question"},
		},

		// --- the design stage -----------------------------------------
		{
			name: "at plan with nobody live a complaint starts the design stage with the line — confirmed, it spends",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: PlanWrong, Note: note},
			want: Outcome{Action: RerunInPlace, Target: domain.StagePlan, Note: note, Confirm: true, Reason: "design-stage-rerun"},
		},
		{
			name: "at plan a missing requirement does the same — the artifact is what the stage is about to write",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: RequirementMissing, Note: note},
			want: Outcome{Action: RerunInPlace, Target: domain.StagePlan, Note: note, Confirm: true, Reason: "design-stage-rerun"},
		},
		{
			name: "at plan a question is still just answered",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: Question, Note: note},
			want: Outcome{Note: note, Reason: "question"},
		},
		{
			name: "at plan an unknown word is still a turn",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: Intent("banana"), Note: note},
			want: Outcome{Note: note, Reason: "unclassified"},
		},

		// --- rewinds from verify --------------------------------------
		{
			name: "verify + missing requirement walks back to plan through implement",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: RequirementMissing, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StageImplement, domain.StagePlan},
				Edit:    Edit{Section: "Problem", Text: note},
				Note:    note,
				Confirm: true, Reason: "requirement_missing",
			},
		},
		{
			name: "verify + wrong plan lands in the chosen-approach section",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: PlanWrong, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StageImplement, domain.StagePlan},
				Edit:    Edit{Section: "Chosen approach", Text: note},
				Note:    note,
				Confirm: true, Reason: "plan_wrong",
			},
		},
		{
			name: "verify + wrong implementation is one edge, and writes nothing — the artifact is right",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: ImplementationWrong, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StageImplement,
				Path:    []domain.Stage{domain.StageImplement},
				Note:    note,
				Confirm: true, Reason: "implementation_wrong",
			},
		},

		// --- rewinds from implement -----------------------------------
		{
			name: "implement + missing requirement is one edge back to plan",
			in:   Input{Stage: domain.StageImplement, Kind: domain.KindFeature, Intent: RequirementMissing, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StagePlan},
				Edit:    Edit{Section: "Problem", Text: note},
				Note:    note,
				Confirm: true, Reason: "requirement_missing",
			},
		},
		{
			name: "implement + wrong implementation re-runs in place — and confirms, since it starts a run",
			in:   Input{Stage: domain.StageImplement, Kind: domain.KindFeature, Intent: ImplementationWrong, Note: note},
			want: Outcome{
				Action: RerunInPlace, Target: domain.StageImplement,
				Note: note, Confirm: true, Reason: "implementation_wrong-in-place",
			},
		},

		// --- the verification plan ------------------------------------
		{
			name: "a missing check is an edit plus a re-run in place, never a rewind",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: CheckMissing, Note: note},
			want: Outcome{
				Action: RerunInPlace, Target: domain.StageVerify,
				Edit: Edit{Section: "Verification plan", Text: note},
				Note: note, Confirm: true, Reason: "check-missing",
			},
		},
		{
			name: "a bug's checks live under its own heading",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindBug, Intent: CheckMissing, Note: note},
			want: Outcome{
				Action: RerunInPlace, Target: domain.StageVerify,
				Edit: Edit{Section: "Verification", Text: note},
				Note: note, Confirm: true, Reason: "check-missing",
			},
		},
		{
			name: "research has no verification section, so the route degrades to a turn",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindResearch, Intent: CheckMissing, Note: note},
			want: Outcome{Note: note, Reason: "check-missing-no-section"},
		},

		// --- the other kinds' headings --------------------------------
		{
			name: "a bug's missing requirement lands under Summary",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindBug, Intent: RequirementMissing, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StageImplement, domain.StagePlan},
				Edit:    Edit{Section: "Summary", Text: note},
				Note:    note,
				Confirm: true, Reason: "requirement_missing",
			},
		},
		{
			name: "a bug's wrong plan lands under Root cause",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindBug, Intent: PlanWrong, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StageImplement, domain.StagePlan},
				Edit:    Edit{Section: "Root cause", Text: note},
				Note:    note,
				Confirm: true, Reason: "plan_wrong",
			},
		},
		{
			name: "a research card's wrong plan lands under Direction",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindResearch, Intent: PlanWrong, Note: note},
			want: Outcome{
				Action: Rewind, Target: domain.StagePlan,
				Path:    []domain.Stage{domain.StageImplement, domain.StagePlan},
				Edit:    Edit{Section: "Direction", Text: note},
				Note:    note,
				Confirm: true, Reason: "plan_wrong",
			},
		},

		// --- unreachable targets --------------------------------------
		{
			name: "todo has no rerun edge, so nothing rewinds out of it",
			in:   Input{Stage: domain.StageTodo, Kind: domain.KindFeature, Intent: PlanWrong, Note: note},
			want: Outcome{Note: note, Reason: "plan_wrong-unreachable"},
		},
		{
			name: "a missing check found at implement re-runs implement, not verify — nothing rewinds forward",
			in:   Input{Stage: domain.StageImplement, Kind: domain.KindFeature, Intent: CheckMissing, Note: note},
			want: Outcome{
				Action: RerunInPlace, Target: domain.StageImplement,
				Edit: Edit{Section: "Verification plan", Text: note},
				Note: note, Confirm: true, Reason: "check-missing",
			},
		},
		{
			name: "a done card has no edge to walk",
			in:   Input{Stage: domain.StageDone, Kind: domain.KindFeature, Intent: ImplementationWrong, Note: note},
			want: Outcome{Note: note, Reason: "implementation_wrong-unreachable"},
		},

		// --- go on ----------------------------------------------------
		{
			name: "proceed at a clear design gate is the approval",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: Proceed, Note: "looks right, go", Forward: domain.StageImplement},
			want: Outcome{Action: Advance, Target: domain.StageImplement, Note: "looks right, go", Confirm: true, Reason: "proceed"},
		},
		{
			name: "proceed at a blocked gate is the blocker, said back",
			in:   Input{Stage: domain.StagePlan, Kind: domain.KindFeature, Intent: Proceed, Note: "go", Forward: domain.StageImplement, Blocked: "open comments"},
			want: Outcome{Note: "go", Reason: "proceed-blocked"},
		},
		{
			name: "proceed at a passing verify gate lands — confirmed",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: Proceed, Note: "ship it", Forward: domain.StageDone},
			want: Outcome{Action: Advance, Target: domain.StageDone, Note: "ship it", Confirm: true, Reason: "proceed"},
		},
		{
			name: "proceed where the forward act is a run re-runs the stage — confirmed, it spends",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: Proceed, Note: "run it", Rerun: true},
			want: Outcome{Action: RerunInPlace, Target: domain.StageVerify, Note: "run it", Confirm: true, Reason: "proceed-rerun"},
		},
		{
			name: "proceed with nothing forward on offer is a turn",
			in:   Input{Stage: domain.StageDone, Kind: domain.KindFeature, Intent: Proceed, Note: "go"},
			want: Outcome{Note: "go", Reason: "proceed-nowhere"},
		},

		// --- the split ------------------------------------------------
		{
			name: "a separate card leaves this one where it is, and confirms",
			in:   Input{Stage: domain.StageVerify, Kind: domain.KindFeature, Intent: SeparateCard, Note: note},
			want: Outcome{Action: NewCard, Note: note, Confirm: true, Reason: "separate-card"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decide(%+v)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}

// Every rewind the table can produce must be walkable on the real graph,
// edge by edge — the property that makes it impossible for a
// classification to invent a transition. Asserted against
// workflow.CanTransition rather than against the path builder, so a
// future graph change that drops a rerun edge fails here rather than
// at runtime.
func TestRewindsTakeOnlyLegalEdges(t *testing.T) {
	stages := []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify, domain.StageDone}
	kinds := []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch}
	for _, stage := range stages {
		for _, kind := range kinds {
			for _, intent := range Vocabulary() {
				out := Decide(Input{Stage: stage, Kind: kind, Intent: intent, Note: note})
				if out.Action != Rewind {
					if len(out.Path) != 0 {
						t.Errorf("%s/%s/%s: %s carries a path %v", stage, kind, intent, out.Action, out.Path)
					}
					continue
				}
				from := stage
				for _, to := range out.Path {
					if err := workflow.CanTransition(from, to); err != nil {
						t.Errorf("%s/%s/%s: rewind path %v takes an illegal edge: %v", stage, kind, intent, out.Path, err)
					}
					from = to
				}
				if from != out.Target {
					t.Errorf("%s/%s/%s: path %v ends at %s, not at Target %s", stage, kind, intent, out.Path, from, out.Target)
				}
			}
		}
	}
}

// A rewind that moves the card without recording why is the bug this
// package exists to fix. The one exception is stated in the table above
// and re-stated here so it cannot be widened by accident: an
// implementation that does not match a correct artifact has nothing to
// write into that artifact.
func TestRewindsCarryTheirArtifactEdit(t *testing.T) {
	stages := []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify, domain.StageDone}
	kinds := []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch}
	for _, stage := range stages {
		for _, kind := range kinds {
			for _, intent := range Vocabulary() {
				out := Decide(Input{Stage: stage, Kind: kind, Intent: intent, Note: note})
				if out.Action != Rewind && out.Action != RerunInPlace {
					continue
				}
				if intent == ImplementationWrong || intent == Proceed || out.Reason == "design-stage-rerun" {
					// the artifact is right (wrong implementation), nothing
					// was complained about at all (go on), or the artifact
					// is what the stage is about to write (the design
					// stage): none has anything to write
					if !out.Edit.Empty() {
						t.Errorf("%s/%s/%s: writes %q into a correct artifact", stage, kind, intent, out.Edit.Section)
					}
					continue
				}
				if out.Edit.Empty() {
					t.Errorf("%s/%s/%s: %s with no artifact edit", stage, kind, intent, out.Action)
				}
				if out.Edit.Text != note {
					t.Errorf("%s/%s/%s: edit text %q, want the typed line", stage, kind, intent, out.Edit.Text)
				}
			}
		}
	}
}

// Confirm is not a per-row preference: it is "does this move the card or
// spend its credits". Everything but a turn does one or the other, so
// everything but a turn confirms. Stated as a property so a new route
// cannot quietly ship a silent move or a silent spend.
func TestEverythingButATurnConfirms(t *testing.T) {
	stages := []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify, domain.StageDone}
	for _, stage := range stages {
		for _, intent := range Vocabulary() {
			for _, in := range []Input{
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: note},
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: note, Forward: domain.StageImplement},
				{Stage: stage, Kind: domain.KindFeature, Intent: intent, Note: note, Rerun: true},
			} {
				out := Decide(in)
				want := out.Action != Turn
				if out.Confirm != want {
					t.Errorf("%s/%s/%+v: %s Confirm=%v, want %v", stage, intent, in, out.Action, out.Confirm, want)
				}
			}
		}
	}
}

// Proceed is confined by construction: it can only produce the act the
// caller said the stop offers. No Forward and no Rerun means nowhere to
// go, whatever the stage; a blocked gate is never crossed.
func TestProceedOnlyTakesWhatTheStopOffers(t *testing.T) {
	stages := []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify, domain.StageDone}
	for _, stage := range stages {
		bare := Decide(Input{Stage: stage, Kind: domain.KindFeature, Intent: Proceed, Note: "go"})
		if bare.Action != Turn {
			t.Errorf("%s: proceed with nothing on offer produced %s", stage, bare.Action)
		}
		blocked := Decide(Input{Stage: stage, Kind: domain.KindFeature, Intent: Proceed, Note: "go", Forward: domain.StageDone, Blocked: "open comments"})
		if blocked.Action != Turn || blocked.Reason != "proceed-blocked" {
			t.Errorf("%s: proceed at a blocked gate produced %+v", stage, blocked)
		}
		fwd := Decide(Input{Stage: stage, Kind: domain.KindFeature, Intent: Proceed, Note: "go", Forward: domain.StageVerify})
		if fwd.Action != Advance || fwd.Target != domain.StageVerify || !fwd.Confirm {
			t.Errorf("%s: proceed with an advance on offer produced %+v", stage, fwd)
		}
	}
}

func TestParseIntent(t *testing.T) {
	for _, i := range Vocabulary() {
		if got, ok := ParseIntent(string(i)); !ok || got != i {
			t.Errorf("ParseIntent(%q) = %q,%v", i, got, ok)
		}
		if Describe(i) == "" {
			t.Errorf("Describe(%q) is empty — the prompt would offer a word with no meaning", i)
		}
	}
	loose := map[string]Intent{
		"  PLAN_WRONG  ":         PlanWrong,
		"plan-wrong":             PlanWrong,
		"plan wrong":             PlanWrong,
		"`check_missing`":        CheckMissing,
		"INTENT_placeholder":     "",
		"":                       "",
		"implementation_wrongly": "",
		"separate":               "",
	}
	for in, want := range loose {
		got, ok := ParseIntent(in)
		if got != want || ok != (want != "") {
			t.Errorf("ParseIntent(%q) = %q,%v, want %q", in, got, ok, want)
		}
	}
}
