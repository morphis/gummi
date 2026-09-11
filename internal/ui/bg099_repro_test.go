package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG099PlanNeverPromisesAStageItHandsBack is BG-099's regression
// test. The dialog's plan named every stage left on the card as one the
// switch would run, while autopilot would in fact cross into the first
// one and hand the card straight back — the dialog someone reads before
// walking away describing something that would not happen.
//
// The merge settled it from the other side: there is no stage that needs
// a person by nature any more, so every remaining stage is one autopilot
// runs and the list is honest by construction. What is asserted here is
// that construction — the body names the whole remainder and promises to
// hand nothing back — so a future stage that does need a person cannot
// be quietly folded into the same sentence.
//
// Driven over every domain.Kind rather than the bug card the drive found
// it on: the defect was never about bugs, it was about what the first
// stage of a workflow is, and a fourth kind added later must not
// reintroduce it.
func TestBG099PlanNeverPromisesAStageItHandsBack(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		t.Run(string(kind), func(t *testing.T) {
			f := domain.Feature{
				ID: "XX-001", Num: 1, Title: "a card", Slug: "a-card",
				Kind: kind, Stage: domain.StageTodo, Budget: domain.Budget{Envelope: 2400},
			}
			seq := stageSequence()
			if len(seq) < 2 {
				t.Fatalf("%s has no stage after todo to plan against: %v", kind, seq)
			}
			to := seq[1]
			plan := autopilotPlan{bucket: "todo", to: to, remaining: remainingStages(to)}

			// autopilot only: attended's body says one thing — every gate
			// waits for you — because that IS the mode. Naming a stage
			// list under it would describe a run it never performs.
			body := strings.Join(autopilotBody(f, plan, domain.GateAutopilot, "main"), " ")
			runs := "runs " + englishList(plan.remaining)
			if !strings.Contains(body, runs) {
				t.Errorf("body does not name what it runs (%q)\n%s", runs, body)
			}
			// and it claims to hand nothing back, because there is nothing
			// left on the card it would.
			if strings.Contains(body, "it never runs ") {
				t.Errorf("body says it hands a stage back, but every stage ahead is one it runs\n%s", body)
			}
		})
	}
}
