package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

func adoptedCard(stage domain.Stage) domain.Feature {
	return domain.Feature{
		ID: "FD-042", Num: 42, Kind: domain.KindFeature, Title: "Rework", Slug: "rework",
		Stage: stage, BranchScheme: domain.BranchSchemeAdopted, Branch: "feat/theirs",
	}
}

// TestAnAdoptedCardWalksTheWholeGraph is the invariant guard for DESIGN
// §10 D22. Adoption was the obvious place to carve a hole in decision 3 —
// the branch already has code on it, so why plan? — and the answer is
// that the plan stage is where the inherited diff gets read and a rework
// is designed against it. An adopted card enters at todo like every other
// card, and no edge exists to let it skip ahead.
func TestAnAdoptedCardWalksTheWholeGraph(t *testing.T) {
	if got := workflow.Initial(); got != domain.StageTodo {
		t.Fatalf("initial stage = %s, want todo", got)
	}
	// There is no edge into implement that does not come through plan, for
	// an adopted card or any other: the graph has no per-card variation to
	// carve one into.
	for _, from := range []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		for _, to := range workflow.Next(from) {
			if to == domain.StageImplement && from != domain.StagePlan && from != domain.StageVerify {
				t.Errorf("implement is reachable from %s, which would let an adopted card skip its design", from)
			}
		}
	}
	if err := workflow.CanTransition(domain.StageTodo, domain.StageImplement); err == nil {
		t.Error("todo → implement is legal; an adopted card could skip planning")
	}
}

// TestAdoptedStagesAreToldWhoseCodeThisIs covers the contract half: every
// stage of an adopted card runs against somebody else's work, and the
// hints above it were all written for a branch that started empty.
func TestAdoptedStagesAreToldWhoseCodeThisIs(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		hints := stageHints(adoptedCard(stage), "/spec.md", "/scratch", flavorStage)
		joined := strings.Join(hints, "\n")
		if !strings.Contains(joined, "feat/theirs") {
			t.Errorf("%s: hints never name the adopted branch", stage)
		}
		for _, want := range []string{
			"Never rebase",          // D22: never rewrites a branch it did not cut
			"not this card's fault", // D22: inherited failures are baselined, not owned
			"ON TOP of",             // D22: the existing commits are not to be discarded
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: hints are missing %q", stage, want)
			}
		}
	}

	// An ordinary card must not be told any of it: the advice is about a
	// branch somebody else wrote, and there is nobody else here.
	plain := adoptedCard(domain.StagePlan)
	plain.BranchScheme, plain.Branch = domain.BranchSchemeKind, ""
	if joined := strings.Join(stageHints(plain, "/spec.md", "/scratch", flavorStage), "\n"); strings.Contains(joined, "Never rebase") {
		t.Error("an ordinary card is told it inherited a branch")
	}
}
