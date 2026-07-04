package engine

import (
	"fmt"
	"strings"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
)

// roleForStage maps a workflow stage to the agent role that performs it
// (DESIGN §3). Stages with no agent action return ok=false.
func roleForStage(s domain.Stage) (agent.Role, bool) {
	switch s {
	case domain.StageBrainstorm, domain.StageSpec, domain.StagePlan:
		return agent.RoleArchitect, true
	case domain.StageImplement:
		return agent.RoleImplementer, true
	case domain.StageReview:
		return agent.RoleReviewer, true
	case domain.StageVerify:
		return agent.RoleScribe, true
	default:
		return "", false
	}
}

// interactiveStage reports whether a stage is a gummi-native chat
// (you talk to the agent) rather than autonomous.
func interactiveStage(s domain.Stage) bool {
	return s == domain.StageBrainstorm || s == domain.StageSpec
}

// stageHints builds the system instructions for a feature's stage: the
// durable spec is the context carrier, so every stage gets the same
// compiled-in contract — the authoritative facts, the spec's shape, and
// the %% marker grammar — plus its own job and completion gate
// (DESIGN §3, §5). The workflow is compiled into gummi; so are its
// conventions. Stating them here keeps sessions from burning turns
// rediscovering them from the repo's docs.
func stageHints(f domain.Feature, specPath string) []string {
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
(repo checks always run; add the feature-specific live checks that
prove this works). Work the open marker threads one decision at a
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
relevant checks as you go, and keep changes reviewable. Keep the
spec's Progress section current: what's done, what's left, where to
resume. If you are addressing review findings, resolve each thread in
the Review section with how you fixed it. If you need a decision or
hit a blocker, stop and say so clearly rather than guessing.`))
	case domain.StageReview:
		hints = append(hints, strings.TrimSpace(`
Stage: Review (autonomous, fresh context). Review the worktree diff
against the spec. Write each finding into the spec's Review section as
one line describing it, followed by its own `+"`%% @reviewer:`"+` marker
detailing what must change — one thread per finding, so gummi tracks
the fix burn-down. Be specific and actionable. End your final message
with a verdict on its own line, exactly one of:
  VERDICT: pass       — no changes needed; ready to verify
  VERDICT: changes    — serious findings; bounce back to implement
gummi parses this exact line to drive the automatic
review→fix→review loop; without it the loop stalls.`))
	case domain.StageVerify:
		hints = append(hints, strings.TrimSpace(`
Stage: Verify (autonomous). gummi runs the repo's fixed check commands
(from .gummi/config.yaml) for you and gives you their results in the
kickoff — do not re-run them. Your job is the spec's Verification plan:
the feature-specific live checks. Record all results in the spec (the
Verification plan section, with a summary line in Progress) and report
pass or fail plainly with the evidence. (If the kickoff carries no
check results — e.g. guarded mode — run the repo commands yourself.)`))
	}
	return hints
}

// contractHint is the stage-independent contract: the authoritative
// facts and the spec/marker conventions the agent must not re-derive.
func contractHint(f domain.Feature, specPath string, role agent.Role) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %s for feature %s: %s.\n", role, f.ID, f.Title)
	if f.OneLiner != "" {
		b.WriteString("One-liner: " + f.OneLiner + "\n")
	}
	b.WriteString(`These facts are authoritative — do not re-derive them from gummi's
state database, source code, or design docs.

The feature's design doc (the spec) is at ` + specPath + `.
It exists — gummi materializes it from its template — and it is the
single source of truth: read it, work from it, keep it current. Its
sections, in order: Problem · Considered approaches · Chosen approach ·
Implementation notes · Progress · Review · Verification plan. Seeded
` + "`%% @gummi:`" + ` lines are placeholder notes — overwrite or resolve them
as you fill in their section.

Open questions and annotations are ` + "`%%`" + ` marker lines in the spec,
one line each:
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
