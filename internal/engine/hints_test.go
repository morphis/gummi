package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestStageHintsCarryMethodology pins the load-bearing protocol phrases
// of each stage hint: the interview discipline for interactive stages,
// the two review lenses and their verdict basis, the diagnose feedback
// loop, and the scope boundary the spec template introduces. Loose
// substrings, so hint prose can evolve without churn here.
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
		}},
		{domain.StagePlan, domain.KindFeature, []string{
			"numbered steps", "tracer bullets",
		}},
		{domain.StageImplement, domain.KindFeature, []string{
			"Out of scope section is\nbinding",
		}},
		{domain.StageTriage, domain.KindBug, []string{
			"Verify the claim first", "one question per turn",
		}},
		{domain.StageDiagnose, domain.KindBug, []string{
			"red-capable command", "falsifiable hypotheses", "[DEBUG-",
		}},
		{domain.StageFix, domain.KindBug, []string{
			"correct seam", "root cause in\nthe commit message",
		}},
		{domain.StageReview, domain.KindFeature, []string{
			"conformance", "standards", "scope", "blocking or nit",
			"resolved threads\nfrom a prior round", "VERDICT: pass", "VERDICT: changes",
		}},
		{domain.StageReview, domain.KindBug, []string{
			"smallest change that resolves the bug", "bounce back to fix",
		}},
		{domain.StageVerify, domain.KindFeature, []string{
			"runs without erroring", "SKIPPED", "VERDICT: fail", "VERDICT: blocked",
			"[CI-only]", "allowed:",
		}},
		{domain.StageVerify, domain.KindBug, []string{
			"no longer\nreproduces", "SKIPPED", "VERDICT: blocked", "[CI-only]",
		}},
	}
	for _, tc := range cases {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := strings.Join(stageHints(f, "spec.md", flavorStage), "\n")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s/%s hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	// the contract's section list matches the template's new shape
	joined := strings.Join(stageHints(feature(1, "x", domain.StageBrainstorm), "spec.md", flavorStage), "\n")
	if !strings.Contains(joined, "Problem · Out of scope · Considered approaches") {
		t.Error("contract hint section list missing Out of scope")
	}
}
