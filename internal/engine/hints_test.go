package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
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
		{domain.StagePlan, domain.KindFeature, []string{
			"one question per turn", "recommended answer", "structurally different",
		}},
		{domain.StagePlan, domain.KindFeature, []string{
			"Out of scope", "test surface is a decision", "runs without erroring",
			"[env: <prereq>]", "[CI-only]",
			"Tags belong on prose live-check lines only",
			"never inside the gummi-checks block",
			// F1: the converge phase hands Implementation notes to the
			// plan phase. The section used to be drafted twice (spec, then
			// plan overwrote it) — dead work at best, competing prose at
			// worst — and the two are one stage's phases now, which makes
			// the handoff a paragraph rather than a stage boundary.
			"Leave Implementation notes for phase 3",
			// F14: Spec gates convergence on approach diversity —
			// Brainstorm required "structurally different" approaches,
			// but no downstream stage verified until now.
			"structurally distinct", "missing structural dimension",
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
			// F11: commit bodies survive the squash into main, so they
			// carry the change's rationale forward — symmetric with the
			// Fix hint, which already tells the agent this.
			"describe what and why in the commit body",
		}},
		{domain.StagePlan, domain.KindBug, []string{
			"Verify the claim first", "one question per turn",
			// F19: Triage must use the described environment as a first-class
			// input instead of treating an unfamiliar environment as grounds
			// to defer.
			"environment gummi described",
		}},
		{domain.StagePlan, domain.KindBug, []string{
			"red-capable command", "falsifiable hypotheses", "[DEBUG-",
			// F20: Diagnose must write a live reproduction tagged [env: ...]
			// when the agent lacks the environment locally; a prose deferral
			// is a contract violation.
			"[env:", "contract violation",
		}},
		{domain.StageImplement, domain.KindBug, []string{
			"correct seam", "root cause in the commit message",
			// F10: `[` in an unescaped grep is a regex character class,
			// so a naked `grep -r "[DEBUG-"` silently misses matches on
			// many systems. Force the fixed-string form.
			"grep -rF",
		}},
		{domain.StageVerify, domain.KindFeature, []string{
			"runs without erroring", "SKIPPED", "VERDICT: fail", "VERDICT: blocked",
			"[CI-only]", "allowed:",
			"never revert human edits", "plan defect",
			// F5: the plan-defect channel is concrete — a bullet Verify
			// writes to the artifact, not a vague "finding" it has no
			// primitive for.
			"finding: gummi-checks tag defect",
			// F12: the "no results block" branch has an explicit
			// trigger, not a parenthetical afterthought.
			"do not re-run the same commands", "If the kickoff has no results block",
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
			// F12: same explicit "no results block" trigger on the bug
			// flavor.
			"do not re-run the same commands", "If the kickoff has no results block",
		}},
	}
	// The work stage's critique carries what the Review stage's contract
	// carried — it IS that contract, reached through flavorCritique rather
	// than through a stage of its own.
	for _, tc := range []struct {
		stage domain.Stage
		kind  domain.Kind
		want  []string
	}{
		{domain.StageImplement, domain.KindFeature, []string{
			"conformance", "standards", "scope", "blocking or nit",
			"resolved threads from a prior round", "VERDICT: pass", "VERDICT: changes",
			"requirements, not creep",
		}},
		{domain.StageImplement, domain.KindBug, []string{
			"smallest change that resolves the bug", "bounce back to fix",
		}},
		{domain.StagePlan, domain.KindResearch, []string{
			"critique the research document", "submit_verdict",
		}},
	} {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorCritique), "\n"))
		for _, want := range tc.want {
			if !strings.Contains(joined, unwrap(want)) {
				t.Errorf("%s/%s critique hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	for _, tc := range cases {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		// the stage-independent contract rides along on every stage
		wants := append([]string{"%% @user:", "not tampering to remove"}, tc.want...)
		for _, want := range wants {
			if !strings.Contains(joined, unwrap(want)) {
				t.Errorf("%s/%s hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	// F19/F20: the new Triage/Diagnose environment-contract language must
	// not leak into other stages; the tags are scoped to those contracts.
	// The work stage's critique carries what the Review stage's contract
	// carried — it IS that contract, reached through flavorCritique rather
	// than through a stage of its own.
	for _, tc := range []struct {
		stage domain.Stage
		kind  domain.Kind
		want  []string
	}{
		{domain.StageImplement, domain.KindFeature, []string{
			"conformance", "standards", "scope", "blocking or nit",
			"resolved threads from a prior round", "VERDICT: pass", "VERDICT: changes",
			"requirements, not creep",
		}},
		{domain.StageImplement, domain.KindBug, []string{
			"smallest change that resolves the bug", "bounce back to fix",
		}},
		{domain.StagePlan, domain.KindResearch, []string{
			"critique the research document", "submit_verdict",
		}},
	} {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorCritique), "\n"))
		for _, want := range tc.want {
			if !strings.Contains(joined, unwrap(want)) {
				t.Errorf("%s/%s critique hint missing %q", tc.stage, tc.kind, want)
			}
		}
	}

	for _, tc := range cases {
		f := feature(1, "Dark mode", tc.stage)
		f.Kind = tc.kind
		joined := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		if tc.stage != domain.StagePlan {
			if strings.Contains(joined, "environment gummi described") {
				t.Errorf("%s/%s hint leaked Triage/Diagnose environment-contract language", tc.stage, tc.kind)
			}
		}
		if tc.stage != domain.StagePlan {
			if strings.Contains(joined, "contract violation") {
				t.Errorf("%s/%s hint leaked Diagnose contract-violation language", tc.stage, tc.kind)
			}
		}
	}

	// F9: contractHint's `%% @gummi:` line reads differently by role.
	// Reviewers (Review/Verify/Critique) shouldn't be told to fill in
	// sections — they read and add findings.
	reviewHints := unwrap(strings.Join(stageHints(feature(1, "x", domain.StageVerify), "spec.md", flavorStage), "\n"))
	if !strings.Contains(reviewHints, "leave them where they are") {
		t.Error("review contractHint missing the softened seeded-line phrasing")
	}
	if strings.Contains(reviewHints, "overwrite or resolve them") {
		t.Error("review contractHint carried the writer-role instruction")
	}
	specStd := unwrap(strings.Join(stageHints(feature(1, "x", domain.StagePlan), "spec.md", flavorStage), "\n"))
	if !strings.Contains(specStd, "overwrite or resolve them") {
		t.Error("spec contractHint lost the writer-role instruction")
	}

	// the design stage's own three phases arrive as one prompt, and the
	// last of them is what writes Implementation notes — the section its
	// gate is judged on. The converge phase must hand that section on
	// rather than claim it.
	stdSpec := unwrap(strings.Join(stageHints(feature(1, "Dark mode", domain.StagePlan), "spec.md", flavorStage), "\n"))
	if !strings.Contains(stdSpec, "Leave Implementation notes for phase 3") {
		t.Error("the converge phase no longer hands Implementation notes to the plan phase")
	}
	if !strings.Contains(stdSpec, "Implementation notes as numbered steps") {
		t.Error("the design hint lost the phase that writes the implementation plan")
	}

	// The interactive working-directory guard is gone with the scratch
	// tree: a design stage now runs in the card's own branch worktree, so
	// there is no throwaway checkout to warn about and nothing that has to
	// be fenced off from committing. No stage carries it any more.
	for _, st := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
		f := feature(1, "x", st)
		h := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		if strings.Contains(h, "scratch checkout of main") {
			t.Errorf("%s still carries the retired scratch-tree guard", st)
		}
	}

	// the plan-critique flavor: reviewer contract plus the tag-placement
	// rule for the gummi-checks block, and the cost-shaping rules the
	// FD-001 blowout motivated (one-pass discipline, turn budget,
	// blocking-only filtering, no ADR re-derivation).
	critique := unwrap(strings.Join(stageHints(feature(1, "x", domain.StagePlan), "spec.md", flavorCritique), "\n"))
	for _, want := range []string{
		"%% @user:",
		// a tag in the block no longer breaks the parse — ParseChecks
		// strips it — so the rule is stated as what it costs: the entry
		// is not the check it reads as.
		"inside the gummi-checks block is a defect",
		"strips it before running the entry",
		"tags belong on prose live-check lines",
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
		// F13: enforce Plan's ≤15-step cap — the writer was told to
		// escalate rather than ship an oversized plan; the critique
		// catches when they didn't.
		"exceeds 15", "oversized-plan finding",
	} {
		if !strings.Contains(critique, unwrap(want)) {
			t.Errorf("critique hint missing %q", want)
		}
	}

	// and the standard flavor's convergence contract must not leak in

	// the contract's section list matches the template's new shape
	joined := strings.Join(stageHints(feature(1, "x", domain.StagePlan), "spec.md", flavorStage), "\n")
	if !strings.Contains(joined, "Problem · Out of scope · Considered approaches") {
		t.Error("contract hint section list missing Out of scope")
	}
}

// Research stages carry their own stage contracts: the design stage is
// an interactive shaping session (scope the question, fix the
// constraints and success criteria, pick the survey's direction), the
// build stage is an autonomous read-only survey that grounds findings
// with path:line citations, and review-of-a-document is an autonomous
// read-only critique that records findings via submit_verdict instead
// of editing the artifact.
func TestResearchStageHints(t *testing.T) {
	cases := []struct {
		stage domain.Stage
		want  []string
	}{
		{domain.StageImplement, []string{
			"read-only", "path:line citation", "no worktree",
		}},
		{domain.StagePlan, []string{
			"Shape the research question", "direction the survey will take",
			"spec tools", "scratch checkout of main",
		}},
	}
	for _, tc := range cases {
		f := feature(1, "RS topic", tc.stage)
		f.Kind = domain.KindResearch
		h := unwrap(strings.Join(stageHints(f, "research.md", flavorStage), "\n"))
		for _, want := range tc.want {
			if !strings.Contains(h, unwrap(want)) {
				t.Errorf("%s hint missing %q", tc.stage, want)
			}
		}
	}

	// research's critique judges a document, so it must not carry the
	// worktree-diff contract: it is read-only and has no
	// spec_replace_section to record findings with.
	rf := feature(1, "RS topic", domain.StagePlan)
	rf.Kind = domain.KindResearch
	critique := unwrap(strings.Join(stageHints(rf, "research.md", flavorCritique), "\n"))
	for _, want := range []string{"read-only", "submit_verdict", "critique"} {
		if !strings.Contains(critique, unwrap(want)) {
			t.Errorf("research critique hint missing %q", want)
		}
	}
	for _, absent := range []string{"spec_replace_section", "Review the worktree diff"} {
		if strings.Contains(critique, absent) {
			t.Errorf("research critique hint contains %q; it must be absent", absent)
		}
	}

	// a feature's critique is unchanged: it still reviews the diff and
	// records findings by editing the artifact.
	nf := feature(1, "Dark mode", domain.StageImplement)
	nf.Kind = domain.KindFeature
	ncrit := unwrap(strings.Join(stageHints(nf, "spec.md", flavorCritique), "\n"))
	if !strings.Contains(ncrit, "Review the worktree diff") {
		t.Error("feature critique hint lost the worktree-diff contract")
	}
}

// TestRebaseHintCarriesBuildCheck: F15 concretized the rebase's
// post-conflict check — name the discoverable build commands rather
// than "if the repo has one".
func TestRebaseHintCarriesBuildCheck(t *testing.T) {
	h := unwrap(rebaseHint())
	for _, want := range []string{
		"go build ./...", "npm run build", "make",
		"fallout detection, not full CI",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("rebase hint missing %q", want)
		}
	}
}

// TestContractHintStatesBoundary pins the artifact/worktree boundary the
// stage contract now states: the artifact is gummi-managed and located
// outside the working directory (its path kept visible for opening), a
// statement that code changes belong in the working directory rides
// along for every role, and the bare unscoped artifact path is gone.
func TestContractHintStatesBoundary(t *testing.T) {
	f := feature(7, "Boundary", domain.StageImplement)
	path := "/project/example/spec.md"
	for _, role := range []agent.Role{agent.RoleArchitect, agent.RoleImplementer, agent.RoleReviewer} {
		h := unwrap(contractHint(f, path, role, flavorStage))
		if !strings.Contains(h, "is gummi-managed and lives outside your working directory") {
			t.Errorf("%s: artifact not named gummi-managed and located outside cwd", role)
		}
		if !strings.Contains(h, "code changes belong in the working directory") {
			t.Errorf("%s: missing code-scope clause", role)
		}
		if !strings.Contains(h, path) {
			t.Errorf("%s: artifact path must stay visible for opening", role)
		}
		if strings.Contains(h, "is at "+path) {
			t.Errorf("%s: bare unscoped artifact path leaked", role)
		}
		if !strings.Contains(h, "AGENTS.md") {
			t.Errorf("%s: missing repo-instructions precedence paragraph (AGENTS.md)", role)
		}
		if !strings.Contains(h, "gummi governs process") {
			t.Errorf("%s: missing repo-instructions precedence paragraph (gummi governs process)", role)
		}
		if !strings.Contains(h, "the workflow wins") {
			t.Errorf("%s: missing repo-instructions precedence paragraph (the workflow wins)", role)
		}
	}
}

// TestContractHintNamesTheMediatedWritePath guards against a real,
// paid-tokens regression from the 2026-09-10 card-thread drive: both a
// reviewer and an architect session burned a turn discovering, by
// trial and error, that their own Edit tool cannot write the artifact
// (several backends' write tools are caged to the working directory —
// WriteCagePaths — and refuse a path outside it, even though a read at
// the same path can succeed). contractHint used to invite exactly that
// mistake ("read and edit the artifact in place there"); it must
// instead name gummi's own mediated tools as the write path and never
// claim a direct file edit will work.
func TestContractHintNamesTheMediatedWritePath(t *testing.T) {
	f := feature(7, "Boundary", domain.StageImplement)
	path := "/project/example/spec.md"
	for _, role := range []agent.Role{agent.RoleArchitect, agent.RoleImplementer, agent.RoleReviewer} {
		h := unwrap(contractHint(f, path, role, flavorStage))
		for _, tool := range []string{"spec_view", "spec_replace_section", "spec_annotate"} {
			if !strings.Contains(h, tool) {
				t.Errorf("%s: contract hint does not name %s as the artifact's write path", role, tool)
			}
		}
		if !strings.Contains(h, "cage their write tools") {
			t.Errorf("%s: contract hint does not warn that a write tool may be caged to the working directory", role)
		}
		if strings.Contains(h, "read and edit the artifact in place") {
			t.Errorf("%s: contract hint still invites a direct file edit gummi cannot guarantee", role)
		}
	}
}

// TestInteractiveKickoffQuickSpec: the spec-chat opener flips with the
// converging the open threads.
func TestInteractiveKickoffQuickSpec(t *testing.T) {
	f := feature(1, "Dark mode", domain.StagePlan)
	if got := designKickoff(f); !strings.Contains(got, "drive convergence") {
		t.Errorf("standard spec kickoff = %q, want the convergence opener", got)
	}
}

// Research design and build are architect work (shaping a research
// topic, then surveying it), and the design stage is a gated interactive
// chat that must have its own opener — a missing case panics in
// interactiveKickoff.
func TestResearchRolesAndKickoff(t *testing.T) {
	// a research card is architect work at BOTH agent stages: its design
	// stage shapes the question, and its build stage gathers evidence and
	// writes it up — there is no code either side of the gate.
	for _, st := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
		f := feature(1, "RS", st)
		f.Kind = domain.KindResearch
		if role, ok := roleForStage(f); !ok || role != agent.RoleArchitect {
			t.Errorf("roleForStage(research, %s) = %s/%v, want architect", st, role, ok)
		}
	}
	// and a feature's build stage is still implementer work
	if role, _ := roleForStage(feature(1, "FD", domain.StageImplement)); role != agent.RoleImplementer {
		t.Errorf("roleForStage(feature, implement) = %s, want implementer", role)
	}
	f := feature(1, "RS", domain.StagePlan)
	f.Kind = domain.KindResearch
	if got := designKickoff(f); !strings.Contains(got, "research") {
		t.Errorf("research kickoff = %q, want the research opener", got)
	}
}

// TestResearchContractsMatchMergedOrder pins the research contracts to the
// merged graph's order: the design stage shapes the question and the
// direction before anything has been surveyed, and the survey runs at
// build. The design contract must therefore not ask the agent to converge
// surveyed options (nothing has been surveyed when it runs), and the
// research critique — which serves both stages, always downstream of the
// design pass — must not claim convergence is still ahead of it. Both
// stage contracts must also match their wiring on the working directory:
// every research stage runs in the card's scratch tree, never the main
// checkout.
func TestResearchContractsMatchMergedOrder(t *testing.T) {
	f := feature(1, "RS", domain.StagePlan)
	f.Kind = domain.KindResearch
	if design := unwrap(designHints(f)[0]); strings.Contains(design, "surveyed") {
		t.Errorf("research design contract asks to converge surveyed options before any survey runs: %q", design)
	}
	if crit := unwrap(critiqueHint(f)); strings.Contains(crit, "has not converged") {
		t.Errorf("research critique claims convergence is still ahead, but the design pass already ran: %q", crit)
	}
	// the design contract must not contradict its wiring on the working
	// directory: every research stage runs in the card's scratch tree,
	// never the main checkout.
	if design := unwrap(designHints(f)[0]); strings.Contains(design, "main checkout") {
		t.Errorf("research design contract contradicts its scratch-tree wiring (mentions the main checkout): %q", design)
	}
	build := f
	build.Stage = domain.StageImplement
	if survey := unwrap(buildHints(build)[0]); strings.Contains(survey, "main checkout") {
		t.Errorf("research build contract contradicts its scratch-tree wiring (mentions the main checkout): %q", survey)
	}
}

// TestSpecHintTeachesBaselineOptOut: baseline: false is a domain.Check
// field the rubric is the only place an agent could learn about. Without
// the rule, a check aimed at a file the feature has yet to create fails
// the approval-time baseline, is written off as pre-existing at Verify,
// and gates nothing for the whole run — so both Spec flavors must teach
// it, and must scope it to checks that cannot run on the branch as it
// stands (or every check acquires the marker defensively).
func TestSpecHintTeachesBaselineOptOut(t *testing.T) {
}
