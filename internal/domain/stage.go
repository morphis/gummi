package domain

// Stage is one node in gummi's fixed workflow. The set of stages and
// their legal transitions are compiled in (see internal/workflow) and
// never configurable.
type Stage string

const (
	// StageTodo is the backlog: the card exists but no work has started.
	StageTodo Stage = "todo"
	// StagePlan is the design stage: explore the problem, converge on an
	// approach, and write the plan. It replaces the five stages the three
	// workflows used to spell this in — brainstorm/spec/plan for a
	// feature, triage/diagnose for a bug, investigate/shape for research
	// — which were one slot wearing three sets of names. Its artifact is
	// the card's design document, and it ends at a human gate.
	StagePlan Stage = "plan"
	// StageImplement is the autonomous implementation in the card's
	// worktree. It replaces implement and fix.
	StageImplement Stage = "implement"
	// StageVerify runs the repo checks plus the artifact's verification
	// plan. Never skippable.
	StageVerify Stage = "verify"
	// StageDone is terminal: a verified branch handed to the user.
	StageDone Stage = "done"
)

// Stages lists every stage, in workflow order. One list, because there is
// one workflow: the three graphs were the same shape wearing three sets
// of names, and the kind now selects the stage's CONTRACT (which hints it
// gets, which artifact it writes) rather than its own graph.
var Stages = []Stage{StageTodo, StagePlan, StageImplement, StageVerify, StageDone}

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
// SuperStates are the board's columns. There is no research column: a
// research card occupies the same positions as any other now, and the
// board already names its kind on the card itself (the RS- prefix). A
// column per kind would put back the per-kind concept the merge removes.
var SuperStates = []SuperState{SuperTodo, SuperInProgress, SuperReviewVerify, SuperDone}

// SuperState returns the kanban group s belongs to.
func (s Stage) SuperState() SuperState {
	switch s {
	case StageTodo:
		return SuperTodo
	case StagePlan, StageImplement:
		return SuperInProgress
	case StageVerify:
		return SuperReviewVerify
	case StageDone:
		return SuperDone
	}
	return SuperTodo
}

// AtOrPastCoding reports whether st is the coding stage or beyond — the
// point at which a card's dependencies are considered settled and it may
// no longer take on new ones. Kind is orthogonal: one stage list covers
// both features and bugs. The dependency gate and the TUI dependency
// picker share this single definition.
func AtOrPastCoding(st Stage) bool {
	switch st {
	case StageImplement, StageVerify, StageDone:
		return true
	}
	return false
}
