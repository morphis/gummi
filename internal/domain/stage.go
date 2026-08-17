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
	// StageTriage confirms and reproduces a bug and records severity +
	// repro steps (interactive, role: architect). Skippable at creation.
	// The bug workflow's analog of Brainstorm.
	StageTriage Stage = "triage"
	// StageDiagnose converges on the root cause; gated on human approval of
	// the diagnosis (interactive, role: architect). Skippable at creation.
	// The bug workflow's analog of Spec.
	StageDiagnose Stage = "diagnose"
	// StageFix is the autonomous fix in the worktree (role: implementer).
	// The bug workflow's analog of Implement.
	StageFix Stage = "fix"
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

// Stages lists every stage across both workflows, in workflow order:
// the shared entry (todo), the feature-specific stages, the bug-specific
// stages, then the shared tail (implement/fix converge into review →
// verify → done).
var Stages = []Stage{
	StageTodo,
	StageBrainstorm, StageSpec, StagePlan,
	StageTriage, StageDiagnose,
	StageFix, StageImplement,
	StageReview, StageVerify, StageDone,
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
	case StageBrainstorm, StageSpec, StagePlan, StageImplement,
		StageTriage, StageDiagnose, StageFix:
		return SuperInProgress
	case StageReview, StageVerify:
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
	case StageImplement, StageFix, StageReview, StageVerify, StageDone:
		return true
	}
	return false
}

// SkipFlags are the only per-item workflow flexibility, set at creation.
// Brainstorm/Plan gate the feature workflow; Triage/Diagnose gate the bug
// workflow; each workflow ignores the other's flags. Review and Verify
// have no flags in either workflow: they can never be skipped.
//
// Quick is not a skip of its own but a route marker: a quick feature is
// created with Brainstorm and Plan both skipped (QuickRoute), and the
// marker tells the Spec stage to draft the whole design in one pass
// instead of converging on a prior brainstorm. Skip flags may loosen
// after creation in one direction only: clearing a flag (restoring a
// stage) is always safe, setting one mid-flight is not.
type SkipFlags struct {
	Brainstorm bool // feature
	Plan       bool // feature
	Triage     bool // bug
	Diagnose   bool // bug
	Quick      bool // feature: one-pass spec route
}

// QuickRoute is the quick feature route: brainstorm and plan skipped,
// with the marker that selects the one-pass spec flavor. Creators use
// this instead of assembling the trio by hand, so a Quick flag never
// exists without the skips it implies.
func QuickRoute() SkipFlags {
	return SkipFlags{Brainstorm: true, Plan: true, Quick: true}
}
