package workflow

import (
	"fmt"

	"github.com/morphia/gummi/internal/domain"
)

// transition is one legal edge in the fixed workflow graph.
type transition struct {
	from, to domain.Stage
	// needsSkip gates skip-edges: the edge exists only when the named
	// flag was set at feature creation.
	needsSkip func(domain.SkipFlags) bool
}

// table is the entire workflow. Review and Verify appear as the only
// path between Implement and Done — that is the never-skippable
// quality floor. Rerun edges (Review→Implement, Verify→Implement)
// implement the fix→re-review loop.
var table = []transition{
	// forward path
	{from: domain.StageTodo, to: domain.StageBrainstorm},
	{from: domain.StageBrainstorm, to: domain.StageSpec},
	{from: domain.StageSpec, to: domain.StagePlan},
	{from: domain.StagePlan, to: domain.StageImplement},
	{from: domain.StageImplement, to: domain.StageReview},
	{from: domain.StageReview, to: domain.StageVerify},
	{from: domain.StageVerify, to: domain.StageDone},

	// skip edges, only for flags set at creation
	{from: domain.StageTodo, to: domain.StageSpec, needsSkip: func(s domain.SkipFlags) bool { return s.Brainstorm }},
	{from: domain.StageSpec, to: domain.StageImplement, needsSkip: func(s domain.SkipFlags) bool { return s.Plan }},

	// rerun edges: findings or failed checks bounce work back
	{from: domain.StageReview, to: domain.StageImplement},
	{from: domain.StageVerify, to: domain.StageImplement},
}

// Initial returns the stage every new feature starts in.
func Initial() domain.Stage { return domain.StageTodo }

// CanTransition reports whether moving from→to is legal for a feature
// created with the given skip flags. The error explains why not.
func CanTransition(from, to domain.Stage, skip domain.SkipFlags) error {
	if !from.Valid() {
		return fmt.Errorf("unknown stage %q", from)
	}
	if !to.Valid() {
		return fmt.Errorf("unknown stage %q", to)
	}
	for _, t := range table {
		if t.from != from || t.to != to {
			continue
		}
		if t.needsSkip != nil && !t.needsSkip(skip) {
			return fmt.Errorf("transition %s → %s requires a skip flag not set on this feature", from, to)
		}
		return nil
	}
	return fmt.Errorf("illegal transition %s → %s", from, to)
}

// Next lists the stages legally reachable from `from` for the given
// skip flags, in table order.
func Next(from domain.Stage, skip domain.SkipFlags) []domain.Stage {
	var out []domain.Stage
	for _, t := range table {
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

// Terminal reports whether s has no outgoing transitions.
func Terminal(s domain.Stage) bool {
	for _, t := range table {
		if t.from == s {
			return false
		}
	}
	return true
}
