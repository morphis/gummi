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
// durable spec is the context carrier, so every stage points the agent
// at it and states the stage's job and completion gate (DESIGN §3, §5).
func stageHints(f domain.Feature, specPath string) []string {
	hints := []string{
		fmt.Sprintf("You are working on feature %s: %s.", f.ID, f.Title),
	}
	if f.OneLiner != "" {
		hints = append(hints, "One-liner: "+f.OneLiner)
	}
	hints = append(hints, "The feature's design doc (the spec) is at "+specPath+
		". It is the single source of truth; read it first and keep it current.")

	switch f.Stage {
	case domain.StageBrainstorm:
		hints = append(hints, strings.TrimSpace(`
Stage: Brainstorm (interactive). Explore the problem and candidate
approaches with the user. Append a problem statement and two or more
candidate approaches (with tradeoffs) to the spec. Flag every
unresolved question with a `+"`%% @architect: ...`"+` marker line so gummi
can surface it as a checklist. Do not converge yet.`))
	case domain.StageSpec:
		hints = append(hints, strings.TrimSpace(`
Stage: Spec (interactive). Converge on exactly one approach with the
user. Fill in the chosen approach, implementation notes, and a
verification plan. Resolve open `+"`%%`"+` questions by replying with
`+"`%% @architect: resolved — ...`"+`. The user approves the spec to
advance; do not start implementing.`))
	case domain.StagePlan:
		hints = append(hints, strings.TrimSpace(`
Stage: Plan (autonomous). Derive a line-level implementation plan from
the approved spec and write it into the spec's implementation notes.
Be concrete: files to touch, functions, test surface. Stop when the
plan is written; the user approves it.`))
	case domain.StageImplement:
		hints = append(hints, strings.TrimSpace(`
Stage: Implement (autonomous). Implement the feature in this worktree
using the spec and plan as context. Make focused edits, run the
relevant checks as you go, and keep changes reviewable. If you need a
decision or hit a blocker, stop and say so clearly rather than
guessing.`))
	case domain.StageReview:
		hints = append(hints, strings.TrimSpace(`
Stage: Review (autonomous, fresh context). Review the worktree diff
against the spec. Write findings into the spec's review section.
Serious findings should bounce the feature back to Implement; be
specific and actionable.`))
	case domain.StageVerify:
		hints = append(hints, strings.TrimSpace(`
Stage: Verify (autonomous). Run the repo's check commands and the
spec's verification plan. Record results in the spec. Report pass or
fail plainly with the evidence.`))
	}
	return hints
}
