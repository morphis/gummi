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
			// F1: Spec must hand off Implementation notes to the Plan stage.
			// The section was drafted twice (Spec, then Plan overwrote it) —
			// dead work at best, competing prose at worst.
			"Do not draft Implementation notes here",
		}},
		{domain.StagePlan, domain.KindFeature, []string{
			"numbered steps", "tracer bullets",
			// scope-cap: keeps the critique surface bounded (see the
			// FD-001 regression — an unbounded plan drove multi-round,
			// envelope-exhausting critiques).
			"≤15 numbered steps",
			// closure subsections: each is CONDITIONAL on spec content —
			// projects without ADRs, gated tests, downstream consumers,
			// or reachable Out-of-scope items ship no closure tables.
			// Their presence in the hint is what lets a plan writer
			// shift audit work to plan-time when the spec triggers them.
			"`Reference mapping`", "`Skip-gate ledger`",
			"`Downstream handoffs`", "`Out-of-scope confirmations`",
			// plan self-audit: shifts row-vs-step verification from
			// critique-time to plan-time.
			"walk each table you shipped",
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
			// F5: the plan-defect channel is concrete — a bullet Verify
			// writes to the artifact, not a vague "finding" it has no
			// primitive for.
			"finding: gummi-checks tag defect",
		}},
		{domain.StageVerify, domain.KindBug, []string{
			"no longer reproduces", "SKIPPED", "VERDICT: blocked", "[CI-only]",
			"plan defect",
			// F4: the regression-test check is inspection — the fix is
			// already applied here; execution against a reverted state
			// would need git surgery the agent is not authorized to do.
			"do not attempt to run the test against a reverted state",
			// F5: same plan-defect channel on the bug flavor.
			"finding: gummi-checks tag defect",
		}},
	}
	for _, tc := range cases {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		// the stage-independent contract rides along on every stage
		wants := append([]string{"%% @user:", "never treat them as tampering"}, tc.want...)
		for _, want := range wants {
			if !strings.Contains(joined, unwrap(want)) {
				t.Errorf("%s/%s hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	// F1: standard Spec must not carry the quick-spec's plan-drafting
	// instruction — Implementation notes are Plan's job when a Plan stage
	// follows, and drafting them twice was dead work.
	stdSpec := unwrap(strings.Join(stageHints(feature(1, "Dark mode", domain.StageSpec), "spec.md", flavorStage), "\n"))
	if strings.Contains(stdSpec, "Implementation notes as the implementation plan") {
		t.Error("standard Spec leaked the quick-spec plan-drafting instruction")
	}

	// F2: interactive stages run in the main checkout — the guard fences
	// them off from editing repo files or committing on main. Every
	// interactive stage carries it; no autonomous stage does.
	for _, st := range []domain.Stage{
		domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose,
	} {
		kind := domain.KindFeature
		if st == domain.StageTriage || st == domain.StageDiagnose {
			kind = domain.KindBug
		}
		f := feature(1, "x", st)
		f.Kind = kind
		h := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		for _, want := range []string{"not an isolated worktree", "do not run git commit"} {
			if !strings.Contains(h, want) {
				t.Errorf("%s hint missing interactive guard %q", st, want)
			}
		}
	}
	autonomous := unwrap(strings.Join(stageHints(feature(1, "x", domain.StageImplement), "spec.md", flavorStage), "\n"))
	if strings.Contains(autonomous, "not an isolated worktree") {
		t.Error("autonomous Implement stage carries the interactive-only guard")
	}

	// the plan-critique flavor: reviewer contract plus the tag-placement
	// rule for the gummi-checks block, and the cost-shaping rules the
	// FD-001 blowout motivated (one-pass discipline, turn budget,
	// blocking-only filtering, no ADR re-derivation).
	critique := unwrap(strings.Join(stageHints(feature(1, "x", domain.StagePlan), "spec.md", flavorCritique), "\n"))
	for _, want := range []string{
		"%% @user:",
		"inside the gummi-checks block corrupts it",
		"tags belong on prose live-check lines only",
		"never a tag inside the gummi-checks block",
		"VERDICT: pass", "VERDICT: changes",
		// one-pass discipline + turn budget: bounds intra-session cost
		// (the outer round cap in reviewloop.go doesn't).
		"one pass", "≤4 turns",
		// blocking-only filtering on critique (Review keeps nits): the
		// critique is a pre-implementation cheap pass, not a full review.
		"blocking findings only",
		// audit the plan's Reference mapping instead of walking cited
		// ADRs/RFCs — the FD-001 completeness-lens re-derivation was
		// the single largest cost driver.
		"Prefer the `Reference mapping`",
	} {
		if !strings.Contains(critique, unwrap(want)) {
			t.Errorf("critique hint missing %q", want)
		}
	}

	// the quick spec flavor: one-pass drafting with the plan folded into
	// Implementation notes, and the same verification-plan rubric as the
	// standard flavor — the route trades gates, never artifact rigor
	qf := feature(1, "Dark mode", domain.StageSpec)
	qf.Skip = domain.QuickRoute()
	quick := unwrap(strings.Join(stageHints(qf, "spec.md", flavorStage), "\n"))
	for _, want := range []string{
		"quick route", "one pass", "two or three clarifying questions",
		"Implementation notes as the implementation plan",
		"runs without erroring", "[env: <prereq>]", "[CI-only]",
		"never inside the gummi-checks block",
		"do not start implementing",
		// the Plan claims shape must stay synchronized with the standard
		// planHint's — the two branches forked once and cost a critique
		// contract of truth. Keep them pinned to the same required shapes.
		"helper <name>: keyed by <field>",
		"golden <name> =",
		"invariant, ordering rule, or error-path",
	} {
		if !strings.Contains(quick, unwrap(want)) {
			t.Errorf("quick spec hint missing %q", want)
		}
	}
	// and the standard flavor's convergence contract must not leak in
	if strings.Contains(quick, "converge with the user on exactly one approach") {
		t.Error("quick spec hint carries the standard convergence contract")
	}

	// the contract's section list matches the template's new shape
	joined := strings.Join(stageHints(feature(1, "x", domain.StageBrainstorm), "spec.md", flavorStage), "\n")
	if !strings.Contains(joined, "Problem · Out of scope · Considered approaches") {
		t.Error("contract hint section list missing Out of scope")
	}
}

// TestInteractiveKickoffQuickSpec: the spec-chat opener flips with the
// route — a quick card's agent leads by drafting, a standard card's by
// converging the open threads.
func TestInteractiveKickoffQuickSpec(t *testing.T) {
	f := feature(1, "Dark mode", domain.StageSpec)
	if got := interactiveKickoff(f); !strings.Contains(got, "drive convergence") {
		t.Errorf("standard spec kickoff = %q, want the convergence opener", got)
	}
	f.Skip = domain.QuickRoute()
	if got := interactiveKickoff(f); !strings.Contains(got, "draft the complete spec") {
		t.Errorf("quick spec kickoff = %q, want the one-pass opener", got)
	}
}
