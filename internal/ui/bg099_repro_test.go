package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// TestBG099PlanNeverPromisesAStageItHandsBack is BG-099's regression
// test. The dialog's plan named every stage left on the card as one the
// switch would run. Autopilot never runs a stage that needs a person: it
// crosses into one and hands the card straight back (autoStepStage, and
// closeHandedOver's reading of the same rule). Every kind's first stage
// is one of those, so a card started from todo was promised its whole
// workflow and delivered a single gate crossing — the dialog someone
// reads before walking away describing something that would not happen.
//
// Driven over every domain.Kind rather than the bug card the drive found
// it on: the defect is not about bugs, it is about the first stage of
// any workflow, and a fourth kind added later must not reintroduce it.
func TestBG099PlanNeverPromisesAStageItHandsBack(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		t.Run(string(kind), func(t *testing.T) {
			f := domain.Feature{
				ID: "XX-001", Num: 1, Title: "a card", Slug: "a-card",
				Kind: kind, Stage: domain.StageTodo, Budget: domain.Budget{Envelope: 2400},
			}
			seq := stageSequence(f)
			if len(seq) < 2 {
				t.Fatalf("%s has no stage after todo to plan against: %v", kind, seq)
			}
			to := seq[1]
			plan := autopilotPlan{bucket: "todo", to: to, remaining: remainingStages(f, to)}

			// the premise the whole test rests on: the stage a card in
			// todo starts at is one autopilot may not run. If a workflow
			// ever begins on an autonomous stage this test still holds —
			// there is simply nothing for the first assertion to catch.
			var interactive []domain.Stage
			for _, st := range plan.remaining {
				if workflow.Interactive(st) {
					interactive = append(interactive, st)
				}
			}

			for _, mode := range []string{domain.GateGates, domain.GateFull} {
				body := strings.Join(autopilotBody(f, plan, mode), " ")
				runs := "runs " + englishList(runnableTail(plan.remaining))
				if len(runnableTail(plan.remaining)) > 0 && !strings.Contains(body, runs) {
					t.Errorf("%s: body does not name what it runs (%q)\n%s", mode, runs, body)
				}
				for _, st := range interactive {
					// the exact shape of the lie: the stage appears inside
					// the list of stages the run is promised to cover.
					if strings.Contains(body, runs) && strings.Contains(runs, string(st)) {
						t.Errorf("%s: body promises to run %s, which it hands back instead\n%s", mode, st, body)
					}
				}
				if len(interactive) > 0 && !strings.Contains(body, "it never runs "+englishList(interactive)+" on its own") {
					t.Errorf("%s: body never says %v need a person\n%s", mode, interactive, body)
				}
			}
		})
	}
}

// runnableTail is the stages of a plan autopilot may actually run, which
// is what the dialog's list must be.
func runnableTail(all []domain.Stage) []domain.Stage {
	var out []domain.Stage
	for _, st := range all {
		if !workflow.Interactive(st) {
			out = append(out, st)
		}
	}
	return out
}
