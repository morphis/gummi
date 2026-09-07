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

// transition is one legal edge in the workflow graph. rerun marks the
// graph's backward edges — a stage returning to the stage that produced
// it — which the manual rewind surfaces take (the TUI's bounce action,
// the headless --bounce).
type transition struct {
	from, to domain.Stage
	rerun    bool
}

// graph is the workflow: its start stage and its legal edges.
type graph struct {
	initial domain.Stage
	table   []transition
}

// theGraph is gummi's workflow. One graph, for every kind.
//
//	todo → plan → implement → verify → done
//
// It used to be three graphs across thirteen stages, and they were the
// same shape wearing three sets of names: explore/converge/build for a
// feature was brainstorm/spec/plan/implement, for a bug was
// triage/diagnose/fix, for research was investigate/shape. The code
// fought that duplication rather than removing it — one rubric constant
// existed "to keep the three from drifting", WorkStage existed only
// because one slot was spelled three ways, and SkipFlags carried five
// booleans for two slots that each graph half-ignored.
//
// The kind still matters, but it selects the stage's CONTRACT — which
// hints it gets, which artifact template it writes — not its own graph.
//
// Backward edges: implement → plan (the plan was wrong) and verify →
// implement (the existing rerun edge). Neither review nor the critique
// loops need an edge: a critique iterates its own stage in place, which
// is what makes it a pass rather than a stage.
var theGraph = graph{
	initial: domain.StageTodo,
	table: []transition{
		{from: domain.StageTodo, to: domain.StagePlan},
		{from: domain.StagePlan, to: domain.StageImplement},
		{from: domain.StageImplement, to: domain.StageVerify},
		{from: domain.StageVerify, to: domain.StageDone},

		// rerun edges
		{from: domain.StageImplement, to: domain.StagePlan, rerun: true},
		{from: domain.StageVerify, to: domain.StageImplement, rerun: true},
	},
}

// Initial returns the stage every new card starts in.
func Initial() domain.Stage { return theGraph.initial }

// CanTransition reports whether moving from→to is legal. The error
// explains why not.
//
// The kind is gone from the signature along with the three graphs, and so
// are the skip flags: there is nothing left to skip. Brainstorm and Plan
// were skippable because they were separate stages; they are one stage
// now, and the way to spend less on a trivial card is to run it on
// autopilot (it still runs the stage, it just does not wait), not to
// remove a stage from its graph.
func CanTransition(from, to domain.Stage) error {
	if !from.Valid() {
		return fmt.Errorf("unknown stage %q", from)
	}
	if !to.Valid() {
		return fmt.Errorf("unknown stage %q", to)
	}
	for _, t := range theGraph.table {
		if t.from == from && t.to == to {
			return nil
		}
	}
	return fmt.Errorf("illegal transition %s → %s", from, to)
}

// Next lists the stages legally reachable from `from`, in table order.
func Next(from domain.Stage) []domain.Stage {
	var out []domain.Stage
	for _, t := range theGraph.table {
		if t.from == from {
			out = append(out, t.to)
		}
	}
	return out
}

// RerunTarget reports where a manual rewind from s lands: the target of
// the rerun edge the graph declares from s, and whether one exists. The
// TUI's bounce action and the headless --bounce take exactly these edges,
// so each rewind target is defined once here, beside the edge itself,
// rather than re-enumerated per surface.
func RerunTarget(s domain.Stage) (domain.Stage, bool) {
	for _, t := range theGraph.table {
		if t.from == s && t.rerun {
			return t.to, true
		}
	}
	return "", false
}

// Terminal reports whether s has no outgoing transitions.
func Terminal(s domain.Stage) bool {
	return len(Next(s)) == 0
}
