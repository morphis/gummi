package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// planText is the design stage's full system instruction for a kind, as one
// string — which is how the session receives it.
func planText(t *testing.T, kind domain.Kind) string {
	t.Helper()
	f := domain.Feature{ID: "FD-001", Kind: kind, Stage: domain.StagePlan}
	return strings.Join(stageHints(f, "/tmp/spec.md", flavorStage), "\n\n")
}

// TestPlanPhasesAreOneSession: the phases are sections of a single static
// prompt, and nothing used to say so. A session read the phase boundary as
// a stop — asking the user for permission to continue when attended, and
// simply ending with Implementation notes empty when not.
func TestPlanPhasesAreOneSession(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		text := planText(t, kind)
		if !strings.Contains(text, "ONE session's order of work") {
			t.Errorf("%s plan hints never say the phases share a session:\n%s", kind, text)
		}
		if !strings.Contains(text, "LAST phase's output") {
			t.Errorf("%s plan hints never say which phase ends the stage", kind)
		}
		// and that finishing means ending the turn, not asking for
		// permission to reach a gate gummi raises by itself — observed
		// costing a whole round trip on an otherwise finished plan
		if !strings.Contains(text, "end your turn") {
			t.Errorf("%s plan hints do not say what finishing looks like", kind)
		}
		if !strings.Contains(text, "gummi raises the stage's gate itself") {
			t.Errorf("%s plan hints do not say the gate arrives without being asked for", kind)
		}
	}
}

// The middle phase must not end on a sentence that reads as a stopping
// point. "The user approves the spec to advance" named an approval that
// does not exist between phases, and a session with nobody to ask stopped
// there.
func TestPlanMiddlePhaseHandsOnRatherThanStopping(t *testing.T) {
	text := planText(t, domain.KindFeature)
	if strings.Contains(text, "The user approves the spec to advance") {
		t.Error("phase 2 still claims an approval stands between it and phase 3")
	}
	if !strings.Contains(text, "go straight on to phase 3") {
		t.Error("phase 2 does not hand on to phase 3")
	}
}

// The stage's real gate is still stated, at its real place: the end.
func TestPlanStageStillNamesItsOwnGate(t *testing.T) {
	text := planText(t, domain.KindFeature)
	if !strings.Contains(text, "Stop when the plan is written; the user approves it.") {
		t.Error("the stage-ending approval is no longer stated")
	}
}

// A research plan is a single phase, so the preamble would be describing
// phases it does not have.
func TestResearchPlanCarriesNoPhasePreamble(t *testing.T) {
	if strings.Contains(planText(t, domain.KindResearch), "ONE session's order of work") {
		t.Error("the single-phase research plan carries the multi-phase preamble")
	}
}

// TestPlanAsksOnlyDecidingQuestions: each question in the headless loop
// costs a process exit, a new session and a fresh read of the repository,
// so a confirmation — "is this list complete?", "may I proceed?" — is
// among the most expensive ways to learn nothing.
func TestPlanAsksOnlyDecidingQuestions(t *testing.T) {
	text := planText(t, domain.KindFeature)
	if !strings.Contains(text, "Confirmations are not decisions") {
		t.Errorf("the interview contract does not rule out confirmation questions:\n%s", text)
	}
	if !strings.Contains(text, "change the shape of the work") {
		t.Error("the interview contract does not say which questions are worth a round trip")
	}
	// and it must still insist the real decisions go to the user
	if !strings.Contains(text, "the decisions are the user's") {
		t.Error("the interview contract lost the rule that decisions belong to the user")
	}
}

// TestChecksBlockRejectsNegativeAssertions: gummi's check runner reads
// exit codes and nothing else. An architect told to write
// symptom-asserting checks naturally writes one for the error path —
// "`list --format bogus` exits non-zero; stderr names both formats" — and
// putting it in the gummi-checks block records a FAIL, floors the stage's
// verdict to blocked, and escalates a branch that was correct. Observed
// doing exactly that on an otherwise clean drive.
func TestChecksBlockRejectsNegativeAssertions(t *testing.T) {
	text := planText(t, domain.KindFeature)
	if !strings.Contains(text, "passes when its command exits ZERO") {
		t.Errorf("the checks-block contract never states its pass condition:\n%s", text)
	}
	if !strings.Contains(text, "success IS a non-zero exit") {
		t.Error("the checks-block contract does not warn about error-path checks")
	}
	// and it must say what to do instead, or the rule just removes coverage
	if !strings.Contains(text, "Invert it into a command that exits") {
		t.Error("the contract forbids the shape without offering the working one")
	}
}
