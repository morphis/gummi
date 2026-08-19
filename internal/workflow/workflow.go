package workflow

import (
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// gummi compiles in three workflows — one each for features, bugs, and
// research — and never exposes them as configuration (DESIGN §10.3). They
// share the never-skippable Review → Verify quality floor; feature and bug
// carry skip edges on their design-side stages, while research does not.
// The kind on a work item selects its graph.

// transition is one legal edge in a workflow graph.
type transition struct {
	from, to domain.Stage
	// needsSkip gates skip-edges: the edge exists only when the named
	// flag was set at item creation.
	needsSkip func(domain.SkipFlags) bool
}

// graph is one workflow: its start stage and its legal edges.
type graph struct {
	initial domain.Stage
	table   []transition
}

// featureGraph is the design-driven workflow. Brainstorm and Plan are the
// only skippable stages; Spec is the gated convergence point.
var featureGraph = graph{
	initial: domain.StageTodo,
	table: []transition{
		// forward path
		{from: domain.StageTodo, to: domain.StageBrainstorm},
		{from: domain.StageBrainstorm, to: domain.StageSpec},
		{from: domain.StageSpec, to: domain.StagePlan},
		{from: domain.StagePlan, to: domain.StageImplement},
		{from: domain.StageImplement, to: domain.StageReview},
		{from: domain.StageReview, to: domain.StageVerify},
		{from: domain.StageVerify, to: domain.StageDone},

		// skip edges, only for flags set at creation
		{from: domain.StageTodo, to: domain.StageSpec, needsSkip: skipBrainstorm},
		{from: domain.StageSpec, to: domain.StageImplement, needsSkip: skipPlan},

		// rerun edges: findings or failed checks bounce work back
		{from: domain.StageReview, to: domain.StageImplement},
		{from: domain.StageVerify, to: domain.StageImplement},
	},
}

// bugGraph is the diagnosis-driven workflow. Triage (reproduce) and
// Diagnose (root cause) are skippable for obvious bugs; both may be
// skipped, so a combined todo→fix edge exists since they are adjacent.
// Fix is the bug's Implement, and it shares the same Review → Verify
// floor — where Verify additionally proves the repro is gone.
var bugGraph = graph{
	initial: domain.StageTodo,
	table: []transition{
		// forward path
		{from: domain.StageTodo, to: domain.StageTriage},
		{from: domain.StageTriage, to: domain.StageDiagnose},
		{from: domain.StageDiagnose, to: domain.StageFix},
		{from: domain.StageFix, to: domain.StageReview},
		{from: domain.StageReview, to: domain.StageVerify},
		{from: domain.StageVerify, to: domain.StageDone},

		// skip edges, only for flags set at creation
		{from: domain.StageTodo, to: domain.StageDiagnose, needsSkip: skipTriage},
		{from: domain.StageTriage, to: domain.StageFix, needsSkip: skipDiagnose},
		{from: domain.StageTodo, to: domain.StageFix, needsSkip: skipTriageAndDiagnose},

		// rerun edges: findings or failed checks bounce work back to Fix
		{from: domain.StageReview, to: domain.StageFix},
		{from: domain.StageVerify, to: domain.StageFix},
	},
}

// researchGraph is the investigation-driven workflow: todo → investigate →
// shape → review → verify → done. It has no skip edges — investigate and
// shape are both always mandatory — and shares the never-skippable Review →
// Verify floor. Its rerun edges bounce findings back to investigate (the
// research WorkStage), never to shape.
var researchGraph = graph{
	initial: domain.StageTodo,
	table: []transition{
		// forward path
		{from: domain.StageTodo, to: domain.StageInvestigate},
		{from: domain.StageInvestigate, to: domain.StageShape},
		{from: domain.StageShape, to: domain.StageReview},
		{from: domain.StageReview, to: domain.StageVerify},
		{from: domain.StageVerify, to: domain.StageDone},

		// rerun edges: findings or failed checks bounce work back to investigate
		{from: domain.StageReview, to: domain.StageInvestigate},
		{from: domain.StageVerify, to: domain.StageInvestigate},
	},
}

func skipBrainstorm(s domain.SkipFlags) bool        { return s.Brainstorm }
func skipPlan(s domain.SkipFlags) bool              { return s.Plan }
func skipTriage(s domain.SkipFlags) bool            { return s.Triage }
func skipDiagnose(s domain.SkipFlags) bool          { return s.Diagnose }
func skipTriageAndDiagnose(s domain.SkipFlags) bool { return s.Triage && s.Diagnose }

// For returns the compiled workflow for a work kind. The empty kind reads
// as a feature, so items predating bugs resolve to the feature workflow.
func For(kind domain.Kind) graph {
	switch kind {
	case domain.KindBug:
		return bugGraph
	case domain.KindResearch:
		return researchGraph
	default:
		return featureGraph
	}
}

// Initial returns the stage every new item of the given kind starts in.
func Initial(kind domain.Kind) domain.Stage { return For(kind).initial }

// Interactive reports whether a stage is a gummi-native chat stage — the
// user talks to the agent, and the work happens against the draft before
// any worktree exists. These are the design stages (brainstorm/spec for
// features, triage/diagnose for bugs, shape for research); every other
// working stage is autonomous and runs in the worktree (or, for research,
// the main checkout). Single source of truth for both the engine's session
// mode and the UI's worktree-creation gate.
func Interactive(s domain.Stage) bool {
	switch s {
	case domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose, domain.StageShape:
		return true
	}
	return false
}

// WorkStage returns a kind's implementation stage — Fix for bugs, Implement
// for features, Investigate for research. It is the target of the
// Review/Verify "request changes" rerun edges and the autonomous stage the
// fix→re-review loop bounces to.
func WorkStage(kind domain.Kind) domain.Stage {
	switch kind {
	case domain.KindBug:
		return domain.StageFix
	case domain.KindResearch:
		return domain.StageInvestigate
	default:
		return domain.StageImplement
	}
}

// NeedsWorktree reports whether a stage of the given kind runs in a
// throwaway worktree. Feature and bug stages that are neither the backlog
// (todo) nor interactive run worktree-bound — the exact predicate that has
// governed worktree creation for them all along. Every research stage runs
// worktree-less: a research branch never receives a commit, so a worktree
// would break the merge/clean/rebase/commit-message/done assumptions.
// This is the single source of truth for the engine's worktree-creation
// gate and the UI's worktree-creation gate.
func NeedsWorktree(kind domain.Kind, stage domain.Stage) bool {
	if kind == domain.KindResearch {
		return false
	}
	return stage != domain.StageTodo && !Interactive(stage)
}

// CanTransition reports whether moving from→to is legal for an item of
// the given kind created with the given skip flags. The error explains
// why not.
func CanTransition(kind domain.Kind, from, to domain.Stage, skip domain.SkipFlags) error {
	if !from.Valid() {
		return fmt.Errorf("unknown stage %q", from)
	}
	if !to.Valid() {
		return fmt.Errorf("unknown stage %q", to)
	}
	for _, t := range For(kind).table {
		if t.from != from || t.to != to {
			continue
		}
		if t.needsSkip != nil && !t.needsSkip(skip) {
			return fmt.Errorf("transition %s → %s requires a skip flag not set on this item", from, to)
		}
		return nil
	}
	return fmt.Errorf("illegal transition %s → %s", from, to)
}

// Next lists the stages legally reachable from `from` for the given kind
// and skip flags, in table order.
func Next(kind domain.Kind, from domain.Stage, skip domain.SkipFlags) []domain.Stage {
	var out []domain.Stage
	for _, t := range For(kind).table {
		if t.from != from {
			continue
		}
		if t.needsSkip != nil && !t.needsSkip(skip) {
			continue
		}
		out = append(out, t.to)
	}
	return out
}

// Terminal reports whether s has no outgoing transitions in the given
// kind's workflow.
func Terminal(kind domain.Kind, s domain.Stage) bool {
	for _, t := range For(kind).table {
		if t.from == s {
			return false
		}
	}
	return true
}
