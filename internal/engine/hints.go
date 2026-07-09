package engine

import (
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// The artifact section lists the contract states so an agent never has
// to rediscover the template's shape; they mirror the two templates in
// internal/spec.
const (
	featureSections = "Problem · Considered approaches · Chosen approach · " +
		"Implementation notes · Progress · Review · Verification plan"
	bugSections = "Summary · Reproduction · Expected vs actual · Environment · " +
		"Root cause · Fix · Review · Verification"
)

// roleForStage maps a workflow stage to the agent role that performs it
// (DESIGN §3). The bug workflow's design-side stages (triage, diagnose)
// are architect work like brainstorm/spec; fix is implementer work like
// implement. Stages with no agent action return ok=false.
func roleForStage(s domain.Stage) (agent.Role, bool) {
	switch s {
	case domain.StageBrainstorm, domain.StageSpec, domain.StagePlan,
		domain.StageTriage, domain.StageDiagnose:
		return agent.RoleArchitect, true
	case domain.StageImplement, domain.StageFix:
		return agent.RoleImplementer, true
	case domain.StageReview:
		return agent.RoleReviewer, true
	case domain.StageVerify:
		return agent.RoleScribe, true
	default:
		return "", false
	}
}

// interactiveStage reports whether a stage is a gummi-native chat (you
// talk to the agent) rather than autonomous. Delegates to the workflow
// package so the engine and the UI's worktree gate share one definition.
func interactiveStage(s domain.Stage) bool { return workflow.Interactive(s) }

// stageHints builds the system instructions for a feature's stage: the
// durable spec is the context carrier, so every stage gets the same
// compiled-in contract — the authoritative facts, the spec's shape, and
// the %% marker grammar — plus its own job and completion gate
// (DESIGN §3, §5). The workflow is compiled into gummi; so are its
// conventions. Stating them here keeps sessions from burning turns
// rediscovering them from the repo's docs. critique selects the
// plan-critique pass: same stage, reviewer's contract and job.
func stageHints(f domain.Feature, specPath string, critique bool) []string {
	if critique {
		return []string{contractHint(f, specPath, agent.RoleReviewer), planCritiqueHint()}
	}
	role, _ := roleForStage(f.Stage)
	hints := []string{contractHint(f, specPath, role)}

	switch f.Stage {
	case domain.StageBrainstorm:
		hints = append(hints, strings.TrimSpace(`
Stage: Brainstorm (interactive; the user is in gummi's chat pane).
Your job: interview the user, and write what you learn into the spec —
a sharp Problem section, and two or more candidate approaches with
tradeoffs under Considered approaches. Lead the conversation: open
with the two or three highest-leverage questions, keep turns short (no
monologues), and update the spec incrementally as answers arrive. Flag
every unresolved decision as its own marker thread. Do not converge on
one approach — convergence is the Spec stage's job.`))
	case domain.StageSpec:
		hints = append(hints, strings.TrimSpace(`
Stage: Spec (interactive; the user is in gummi's chat pane). Your job:
converge with the user on exactly one approach, then complete the
spec: Chosen approach, Implementation notes, and the Verification plan
(gummi discovers the repo's build/test/lint commands into a
gummi-checks block there at approval; add the feature-specific live
checks that prove this works). Work the open marker threads one decision at a
time, resolving each once the user decides. The user approves the spec
to advance — do not start implementing.`))
	case domain.StagePlan:
		hints = append(hints, strings.TrimSpace(`
Stage: Plan (autonomous). Derive a line-level implementation plan from
the approved spec and write it into the spec's Implementation notes:
files to touch, functions, test surface. Be concrete. Stop when the
plan is written; the user approves it.`))
	case domain.StageImplement:
		hints = append(hints, strings.TrimSpace(`
Stage: Implement (autonomous). Implement the feature in this worktree
using the spec and plan as context. Make focused edits, run the
relevant checks as you go, and keep changes reviewable. Commit your
work to this branch with focused git commits as you complete each
coherent piece — the branch lands on main as a single squash commit
when the user accepts the feature, and gummi checkpoint-commits
anything you leave uncommitted when the stage ends. Keep the
spec's Progress section current: what's done, what's left, where to
resume. If your changes alter how the repo is built, tested, or linted,
update the gummi-checks block in the Verification plan — the Verify
stage runs exactly those commands. If you are addressing review findings, resolve each thread in
the Review section with how you fixed it. If you need a decision or
hit a blocker, stop and say so clearly rather than guessing.`))
	case domain.StageTriage:
		hints = append(hints, strings.TrimSpace(`
Stage: Triage (interactive; the user is in gummi's chat pane). Your job:
confirm the bug is real and reproduce it. Pin down exact reproduction
steps, the expected vs actual behavior, the environment, and a severity,
and write them into the bug report (Reproduction, Expected vs actual,
Environment). Lead the conversation: open with the highest-leverage
questions, keep turns short. Flag anything still uncertain as its own
marker thread. Do NOT diagnose the root cause yet — that is the Diagnose
stage's job.`))
	case domain.StageDiagnose:
		hints = append(hints, strings.TrimSpace(`
Stage: Diagnose (interactive; the user is in gummi's chat pane). Your
job: find and confirm the root cause, working from the reproduction, and
record it in the bug report's Root cause section — where in the code,
why it happens, and the shape of the fix (not the fix itself). Put open
questions to the user one decision at a time and resolve each thread as
they decide. The user approves the diagnosis to advance — do not start
fixing.`))
	case domain.StageFix:
		hints = append(hints, strings.TrimSpace(`
Stage: Fix (autonomous). Implement the fix in this worktree, guided by
the bug report's Root cause. Make the smallest change that resolves the
bug, and ADD A REGRESSION TEST that fails before your change and passes
after — the Verify stage requires it. Commit your work to this branch
with git as you go — the branch lands on main as a single squash commit
when the user accepts the fix, and gummi checkpoint-commits anything
you leave uncommitted when the stage ends. Keep the report's Fix section
current: what you changed and why. If you are addressing review
findings, resolve each thread in the Review section with how you fixed
it. If you hit a blocker or need a decision, stop and say so rather than
guessing.`))
	case domain.StageReview:
		hints = append(hints, reviewHint(f.Kind))
	case domain.StageVerify:
		hints = append(hints, verifyHint(f.Kind))
	}
	return hints
}

// planCritiqueHint is the plan-critique pass contract: Review's shape
// transposed to design altitude. It runs fresh-context on the Plan
// stage after the plan is written, tries to refute the plan before the
// human approves it, and drives the automatic critique→replan loop
// with the same verdict grammar as Review.
func planCritiqueHint() string {
	return strings.TrimSpace(`
Stage: Plan critique (autonomous, fresh context). The implementation
plan was just written into the spec's Implementation notes. Your job is
to refute it before the user approves it — do not fix it yourself, and
do not review code (none exists yet). Read the whole spec and judge the
plan through three lenses:
  security     — attack surface the approach opens: input handling,
                 authz, secrets, injection, unsafe defaults
  correctness  — edge cases, error paths, concurrency, invariants the
                 plan breaks or forgets
  completeness — does the plan actually cover the spec's Chosen
                 approach, and does the Verification plan prove it?
Write each finding as its own ` + "`%% @reviewer:`" + ` marker anchored to the
plan line it indicts — one thread per finding, so gummi tracks the
burn-down. If the Verification plan is missing a check that would catch
one of your concerns, append that check to the Verification plan
section (machine-run commands go in its gummi-checks block; live-proof
steps read as prose). End your final message with a verdict on its own line, exactly
one of:
  VERDICT: pass       — plan is sound; ready for the user's approval
  VERDICT: changes    — serious findings; the plan must be revised
gummi parses this exact line to drive the automatic critique→replan
loop; without it the loop stalls.`)
}

// reviewHint is the Review stage contract. Review is shared by both
// workflows; only the artifact it reviews against and the stage a
// "changes" verdict bounces to differ by kind.
func reviewHint(kind domain.Kind) string {
	artifact, bounce := "spec", "implement"
	if kind == domain.KindBug {
		artifact, bounce = "bug report", "fix"
	}
	return strings.TrimSpace(fmt.Sprintf(`
Stage: Review (autonomous, fresh context). Review the worktree diff
against the %s. Write each finding into the %s's Review section as
one line describing it, followed by its own `+"`%%%% @reviewer:`"+` marker
detailing what must change — one thread per finding, so gummi tracks
the fix burn-down. Be specific and actionable. End your final message
with a verdict on its own line, exactly one of:
  VERDICT: pass       — no changes needed; ready to verify
  VERDICT: changes    — serious findings; bounce back to %s
gummi parses this exact line to drive the automatic
review→%s→review loop; without it the loop stalls.`, artifact, artifact, bounce, bounce))
}

// verifyHint is the Verify stage contract. The deterministic repo-check
// floor is identical; the adaptive part differs — a feature runs its
// spec's verification plan, a bug proves the reproduction is gone and a
// regression test locks the fix in.
func verifyHint(kind domain.Kind) string {
	if kind == domain.KindBug {
		return strings.TrimSpace(`
Stage: Verify (autonomous). gummi runs the check commands from the
report's gummi-checks block for you and gives you their results in the
kickoff — do not re-run them. Then prove the bug is fixed: run the
Reproduction steps from the bug report and confirm it no longer
reproduces, and confirm the regression test covers it (it should fail
without the fix). Record all results in the report's Verification
section and report pass or fail plainly with the evidence. (If the
kickoff carries no check results — e.g. guarded mode, or no block —
discover the repo's build/test/lint commands and run them yourself.)`)
	}
	return strings.TrimSpace(`
Stage: Verify (autonomous). gummi runs the check commands from the
spec's gummi-checks block for you and gives you their results in the
kickoff — do not re-run them. Your job is the spec's Verification plan:
the feature-specific live checks. Record all results in the spec (the
Verification plan section, with a summary line in Progress) and report
pass or fail plainly with the evidence. (If the kickoff carries no
check results — e.g. guarded mode, or no block — discover the repo's
build/test/lint commands and run them yourself.)`)
}

// contractHint is the stage-independent contract: the authoritative
// facts and the artifact/marker conventions the agent must not re-derive.
// The artifact is a spec for features, a bug report for bugs; the two
// carry different sections but the same %% marker grammar.
func contractHint(f domain.Feature, specPath string, role agent.Role) string {
	noun, artifact, sections := "feature", "spec (the design doc)", featureSections
	if f.Kind == domain.KindBug {
		noun, artifact, sections = "bug", "bug report", bugSections
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %s for %s %s: %s.\n", role, noun, f.ID, f.Title)
	if f.OneLiner != "" {
		b.WriteString("One-liner: " + f.OneLiner + "\n")
	}
	fmt.Fprintf(&b, `These facts are authoritative — do not re-derive them from gummi's
state database, source code, or design docs.

The %s's %s is at %s.
It exists — gummi materializes it from its template — and it is the
single source of truth: read it, work from it, keep it current. Its
sections, in order: %s. Seeded `+"`%%%% @gummi:`"+` lines are placeholder
notes — overwrite or resolve them as you fill in their section.

Open questions and annotations are `+"`%%%%`"+` marker lines in the %s,
one line each:`, noun, artifact, specPath, sections, artifact)
	b.WriteString(`
  %% @` + string(role) + `: <question or note>
  %% @` + string(role) + `: resolved — <answer>     (resolves its thread)
A marker attaches to the nearest preceding non-marker line (its
anchor). Consecutive marker lines share that anchor and form ONE
thread, so give each independent question its own anchor line —
markers stacked together collapse into a single checklist item. gummi
parses unresolved threads into the user's open-question checklist and
gates stage advancement on them.`)
	return b.String()
}
