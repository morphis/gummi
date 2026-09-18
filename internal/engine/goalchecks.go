package engine

// A goal's checks do not all run in the same directory. A done-when item's
// `check:` is a command, a command needs a working directory, and a goal
// that spans repositories has one goal tree per repository — so the item
// says which (domain.DoneWhen.Repo) and its command runs there. Everything
// else in a goal's checks block runs in the goal's home tree, which is
// what a single-repo goal has and all it ever had.
//
// The trees are siblings under .gummi/worktrees, so a check that genuinely
// needs two repositories at once reaches the other by relative path
// (../GL-007@web) — the one thing a single command in a single directory
// cannot otherwise do.

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/verify"
)

// goalItemRepo is the repository a done-when item's check runs in: the one
// it names, or the goal's home when it names none.
func goalItemRepo(it domain.DoneWhen, home string) string {
	if it.Repo == "" {
		return home
	}
	return it.Repo
}

// goalCheckRepos maps a goal's check names to the repository their
// commands run in, from the done-when items in its doc. A check nothing
// claims is the home repo's.
func goalCheckRepos(doc string, home string) map[string]string {
	out := map[string]string{}
	items, _, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return out
	}
	for _, it := range items {
		if it.Check == "" || it.Repo == "" || it.Repo == home {
			continue
		}
		out[it.CheckName()] = it.Repo
		out[it.ID] = it.Repo
	}
	return out
}

// runGoalChecks runs a goal's checks, each in the goal tree of the
// repository it belongs to, and returns the results in the order the
// checks were given — a result that moved would read as another check's.
//
// A check naming a repository the goal has no tree in fails rather than
// falling back to the home tree: running a command somewhere other than
// where it was meant to prove something is worse than not running it, and
// a goal with a missing tree is a goal to repair.
func (e *Engine) runGoalChecks(ctx context.Context, goal domain.Feature, doc string, checks []domain.Check) ([]verify.Result, error) {
	byRepo := goalCheckRepos(doc, goal.Repo)
	order := []string{goal.Repo}
	groups := map[string][]domain.Check{}
	at := map[string][]int{}
	for i, c := range checks {
		repo := goal.Repo
		if r, ok := byRepo[c.Name]; ok {
			repo = r
		}
		if _, seen := groups[repo]; !seen && repo != goal.Repo {
			order = append(order, repo)
		}
		groups[repo] = append(groups[repo], c)
		at[repo] = append(at[repo], i)
	}
	results := make([]verify.Result, len(checks))
	for _, repo := range order {
		group := groups[repo]
		if len(group) == 0 {
			continue
		}
		tree, err := e.pool.GoalTree(&goal, repo)
		if err != nil {
			return nil, err
		}
		if !tree.Exists() {
			for n, i := range at[repo] {
				results[i] = verify.Result{
					Name: group[n].Name, Cmd: group[n].Cmd, Status: verify.StatusNotRun,
					Output: fmt.Sprintf("%s has no goal tree in %s, so this check had nowhere to run", goal.ID, repoName(repo)),
				}
			}
			continue
		}
		out, err := verify.RunWithBudget(ctx, tree.Dir, group, verifyStageTimeout)
		if err != nil {
			return nil, err
		}
		for n, i := range at[repo] {
			if n < len(out) {
				results[i] = out[n]
			}
		}
	}
	return results, nil
}
