package engine

import (
	"context"
	"errors"

	"github.com/morphis/gummi/internal/domain"
)

// leadAvailable reports whether a lead turn can run for goal.
func (e *Engine) leadAvailable(goal domain.Feature) bool { return false }

// runLeadTurn runs one lead turn over reasons.
func (e *Engine) runLeadTurn(ctx context.Context, view GoalView, reasons []string) ([]GoalStart, error) {
	return nil, errors.New("no lead is configured")
}

// resolveGoalCatchUp resolves an open catch-up merge on a goal branch.
func (e *Engine) resolveGoalCatchUp(ctx context.Context, goal domain.Feature, files []string) error {
	return errors.New("no resolver is configured")
}
