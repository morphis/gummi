package domain

// Stage is one node in gummi's fixed workflow. The set of stages and
// their legal transitions are compiled in (see internal/workflow) and
// never configurable.
type Stage string

const (
	// StageTodo is the backlog: the feature exists but no work has started.
	StageTodo Stage = "todo"
	// StageBrainstorm explores the problem and candidate approaches
	// (interactive, role: architect). Skippable at creation.
	StageBrainstorm Stage = "brainstorm"
	// StageSpec converges on one approach; gated on human approval of
	// the spec (interactive, role: architect).
	StageSpec Stage = "spec"
	// StagePlan derives a line-level implementation plan from the spec;
	// gated on human approval. Skippable at creation.
	StagePlan Stage = "plan"
	// StageImplement is the autonomous implementation in the worktree.
	StageImplement Stage = "implement"
	// StageReview is a fresh-context autonomous review. Never skippable.
	StageReview Stage = "review"
	// StageVerify runs the repo checks plus the spec's verification
	// plan. Never skippable.
	StageVerify Stage = "verify"
	// StageDone is terminal: a verified branch handed to the user.
	StageDone Stage = "done"
)

// Stages lists every stage in workflow order.
var Stages = []Stage{
	StageTodo, StageBrainstorm, StageSpec, StagePlan,
	StageImplement, StageReview, StageVerify, StageDone,
}

// Valid reports whether s is one of the compiled-in stages.
func (s Stage) Valid() bool {
	for _, st := range Stages {
		if s == st {
			return true
		}
	}
	return false
}

// SuperState is the kanban grouping of stages.
type SuperState string

const (
	SuperTodo         SuperState = "todo"
	SuperInProgress   SuperState = "in progress"
	SuperReviewVerify SuperState = "review / verify"
	SuperDone         SuperState = "done"
)

// SuperStates lists the kanban groups in display order.
var SuperStates = []SuperState{SuperTodo, SuperInProgress, SuperReviewVerify, SuperDone}

// SuperState returns the kanban group s belongs to.
func (s Stage) SuperState() SuperState {
	switch s {
	case StageTodo:
		return SuperTodo
	case StageBrainstorm, StageSpec, StagePlan, StageImplement:
		return SuperInProgress
	case StageReview, StageVerify:
		return SuperReviewVerify
	case StageDone:
		return SuperDone
	}
	return SuperTodo
}

// SkipFlags are the only per-feature workflow flexibility, set at
// creation. Review and Verify have no flags: they can never be skipped.
type SkipFlags struct {
	Brainstorm bool
	Plan       bool
}
