package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// unwrap collapses all whitespace runs to single spaces so hint
// fragments match regardless of where the prose happens to line-wrap.
func unwrap(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestStageHintsCarryMethodology pins the load-bearing protocol phrases
// of each stage hint: the interview discipline for interactive stages,
// the two review lenses and their verdict basis, the diagnose feedback
// loop, the scope boundary the spec template introduces, and the user
// amendment authority rule. Loose wrap-insensitive substrings, so hint
// prose can evolve without churn here.
func TestStageHintsCarryMethodology(t *testing.T) {
	cases := []struct {
		stage domain.Stage
		kind  domain.Kind
		want  []string
	}{
		{domain.StageBrainstorm, domain.KindFeature, []string{
			"one question per turn", "recommended answer", "structurally different",
		}},
		{domain.StageSpec, domain.KindFeature, []string{
			"Out of scope", "test surface is a decision", "runs without erroring",
			"[env: <prereq>]", "[CI-only]",
			"Tags belong on prose live-check lines only",
			"never inside the gummi-checks block",
		}},
		{domain.StagePlan, domain.KindFeature, []string{
			"numbered steps", "tracer bullets",
		}},
		{domain.StageImplement, domain.KindFeature, []string{
			"Out of scope section is binding",
		}},
		{domain.StageTriage, domain.KindBug, []string{
			"Verify the claim first", "one question per turn",
		}},
		{domain.StageDiagnose, domain.KindBug, []string{
			"red-capable command", "falsifiable hypotheses", "[DEBUG-",
		}},
		{domain.StageFix, domain.KindBug, []string{
			"correct seam", "root cause in the commit message",
		}},
		{domain.StageReview, domain.KindFeature, []string{
			"conformance", "standards", "scope", "blocking or nit",
			"resolved threads from a prior round", "VERDICT: pass", "VERDICT: changes",
			"requirements, not creep",
		}},
		{domain.StageReview, domain.KindBug, []string{
			"smallest change that resolves the bug", "bounce back to fix",
		}},
		{domain.StageVerify, domain.KindFeature, []string{
			"runs without erroring", "SKIPPED", "VERDICT: fail", "VERDICT: blocked",
			"[CI-only]", "allowed:",
			"never revert human edits", "plan defect",
		}},
		{domain.StageVerify, domain.KindBug, []string{
			"no longer reproduces", "SKIPPED", "VERDICT: blocked", "[CI-only]",
			"plan defect",
		}},
	}
	for _, tc := range cases {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		// the stage-independent contract rides along on every stage
		wants := append([]string{"%% @user:", "Gummi-Author: user", "never treat them as tampering"}, tc.want...)
		for _, want := range wants {
			if !strings.Contains(joined, unwrap(want)) {
				t.Errorf("%s/%s hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	// the plan-critique flavor: reviewer contract plus the tag-placement
	// rule for the gummi-checks block
	critique := unwrap(strings.Join(stageHints(feature(1, "x", domain.StagePlan), "spec.md", flavorCritique), "\n"))
	for _, want := range []string{
		"%% @user:", "Gummi-Author: user",
		"inside the gummi-checks block corrupts it",
		"tags belong on prose live-check lines only",
		"never a tag inside the gummi-checks block",
		"VERDICT: pass", "VERDICT: changes",
	} {
		if !strings.Contains(critique, unwrap(want)) {
			t.Errorf("critique hint missing %q", want)
		}
	}

	// the contract's section list matches the template's new shape
	joined := strings.Join(stageHints(feature(1, "x", domain.StageBrainstorm), "spec.md", flavorStage), "\n")
	if !strings.Contains(joined, "Problem · Out of scope · Considered approaches") {
		t.Error("contract hint section list missing Out of scope")
	}
}
