package engine

// A programme is a sequence of goals, not a nested one ("goals do not
// nest" stays true). The plan gate is the only place a person agrees what
// done means, and for work that begins with discovery that cannot be known
// once, up front — so the honest structure puts a gate wherever knowledge
// changes hands: one goal makes the rig trustworthy, the next finds out
// what is true, the next builds on it. Each is its own budget, its own
// hand-over, and a failure that costs a phase rather than the whole.
//
// What that needs from gummi is small: a goal may name the goal it
// continues (`after:`), it may not start until that one has ended, and what
// that one came to know comes with it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// goalAfter names the goal that goal continues: the one its doc's
// gummi-goal block names, else the one it was created to continue.
func (e *Engine) goalAfter(ctx context.Context, goal domain.Feature, doc string) domain.FeatureID {
	if after, _, err := spec.ParseGoalProgramme(doc); err == nil && after != "" {
		return domain.FeatureID(strings.ToUpper(after))
	}
	for _, en := range mustGoalLog(ctx, e, goal.ID) {
		if en.Action == state.GoalContinued {
			return domain.FeatureID(en.Ref)
		}
	}
	return ""
}

// goalProgrammeProblem is the plan gate's question about a goal's place in
// a programme: does the goal it continues exist, and has it ended. A goal
// may be PLANNED while the one before it runs — that is most of the point —
// but it does not start on a trunk that does not have that one's work yet,
// or on findings that goal has not finished making.
func (e *Engine) goalProgrammeProblem(ctx context.Context, goal domain.Feature, doc string) string {
	_, order, err := spec.ParseGoalProgramme(doc)
	if err != nil {
		return err.Error()
	}
	for _, repo := range order {
		if problem := e.goalRepoProblem(repo); problem != "" {
			return "land_order " + problem
		}
	}
	after := e.goalAfter(ctx, goal, doc)
	if after == "" {
		return ""
	}
	if after == goal.ID {
		return fmt.Sprintf("%s cannot continue itself", goal.ID)
	}
	prev, err := e.cfg.Store.GetFeature(ctx, after)
	if err != nil || !prev.IsGoal() {
		return fmt.Sprintf("this goal continues %s, which is not a goal on this board", after)
	}
	switch {
	case prev.Stage != domain.StageDone:
		return fmt.Sprintf("this goal continues %s, which has not ended (it is at %s) — land it first. The plan can wait here, agreed, until it has", after, prev.Stage)
	case prev.HandedOff():
		return fmt.Sprintf("this goal continues %s, which was handed off rather than landed: its work is not on the trunk this goal's cards would fork from. "+
			"Land it, or take `after:` out if this goal does not build on its code", after)
	}
	return ""
}

// ContinueGoal makes goal start from what prev came to know: prev's
// reference, its registry and the findings of it that still hold are carried
// into goal's notebook, and prev's hand-over becomes a reference document —
// the plan conversation reads what was handed over, not a summary of it.
// Safe to repeat: what goal already has wins, and the link is recorded once.
func (e *Engine) ContinueGoal(ctx context.Context, goalID, prevID domain.FeatureID) error {
	prev, err := e.cfg.Store.GetFeature(ctx, prevID)
	if err != nil || !prev.IsGoal() {
		return fmt.Errorf("%s is not a goal on this board", prevID)
	}
	if prevID == goalID {
		return fmt.Errorf("%s cannot continue itself", goalID)
	}
	for _, en := range mustGoalLog(ctx, e, goalID) {
		if en.Action == state.GoalContinued && en.Ref == string(prevID) {
			return nil
		}
	}
	nb := e.goalNotebook(goalID)
	if err := nb.Import(e.goalNotebook(prevID), string(prevID)); err != nil {
		return err
	}
	if rep, rerr := e.GoalReport(ctx, prevID); rerr == nil {
		body := fmt.Sprintf("# %s — %s: the hand-over\n\n%s", prev.ID, prev.Title, RenderGoalReport(rep))
		if err := os.MkdirAll(nb.ReferenceDir(), 0o750); err != nil {
			return err
		}
		if err := atomicfile.Write(filepath.Join(nb.ReferenceDir(), string(prevID)+"-handover.md"), []byte(body), 0o600); err != nil {
			return err
		}
	}
	e.goalLog(ctx, goalID, state.GoalPayload{Action: state.GoalContinued, Ref: string(prevID), By: "user",
		Detail: fmt.Sprintf("continues %s (%s); what it knew came with it", prevID, prev.Title)})
	return nil
}

// orderGoalTrees puts a goal's trees in the order they land. The plan's
// land_order first, in its order; then anything it did not name, home
// first — which with no land_order at all is the order there always was.
func orderGoalTrees(trees []worktree.GoalTree, order []string) []worktree.GoalTree {
	rank := map[string]int{}
	for i, name := range order {
		rank[name] = i
	}
	out := append([]worktree.GoalTree(nil), trees...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i].Repo]
		rj, jok := rank[out[j].Repo]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return out[i].Home && !out[j].Home
	})
	return out
}

// goalLandOrder reads the landing order a goal's plan agreed.
func (e *Engine) goalLandOrder(goal domain.Feature) []string {
	path := e.artifactFile(&goal)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_, order, _ := spec.ParseGoalProgramme(string(raw))
	return order
}
