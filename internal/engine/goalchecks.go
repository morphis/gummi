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
	"slices"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/substrate"
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
	results := make([]verify.Result, len(checks))
	skip, release := e.holdCheckSubstrates(ctx, goal, doc, checks, results)
	defer release()
	order := []string{goal.Repo}
	groups := map[string][]domain.Check{}
	at := map[string][]int{}
	for i, c := range checks {
		if skip[i] {
			continue // its substrate is not this goal's to use right now
		}
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
		e.tidyGoalTree(ctx, goal, tree.Dir)
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

// holdCheckSubstrates takes, for the length of a goal's checks, the lease
// of every substrate one of its done-when items names.
//
// A done-when `check:` is a command in a checkout, and a command that
// drives a cluster is as scarce, slow, stateful and shared as any
// experiment run on the same machines — "two jobs on it at once are not
// slow but wrong, in a way that reads as the code's fault" (§17.7). Until
// an item could say which substrate its check needs, nothing but
// internal/experiment ever took a lease, and a goal's own verify ran
// cluster-driving commands beside a run that believed it had the machines
// to itself.
//
// A substrate somebody else holds, or one that is not ready, does not make
// the check fail: it makes it NOT RUN, which is decision 21's rule that a
// check which cannot run is no opinion rather than a block. Those results
// are written straight into out, and their indices come back in skip so
// the caller does not run them.
func (e *Engine) holdCheckSubstrates(ctx context.Context, goal domain.Feature, doc string, checks []domain.Check, out []verify.Result) (skip map[int]bool, release func()) {
	skip = map[int]bool{}
	release = func() {}
	items, _, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return skip, release
	}
	wants := map[string]string{} // check name → substrate
	for _, it := range items {
		if name := strings.TrimSpace(it.Substrate); name != "" && strings.TrimSpace(it.Check) != "" {
			wants[it.CheckName()] = name
		}
	}
	if len(wants) == 0 {
		return skip, release
	}
	mgr, err := e.Substrates()
	if err != nil {
		return skip, release
	}
	names := make([]string, 0, len(wants))
	for _, n := range wants {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	sort.Strings(names) // one order for every holder, so two goals cannot deadlock

	held := map[string]*substrate.Lease{}
	problem := map[string]string{}
	for _, name := range names {
		lease, aerr := mgr.Acquire(name, substrate.Holder{Who: string(goal.ID), Purpose: "the goal's checks"})
		if aerr != nil {
			problem[name] = aerr.Error()
			continue
		}
		if st := lease.Status(ctx); st.State != substrate.Ready {
			problem[name] = fmt.Sprintf("%s is %s", name, st.State)
			lease.Release()
			continue
		}
		held[name] = lease
	}
	for i, c := range checks {
		name, wanted := wants[c.Name]
		if !wanted {
			continue
		}
		why, bad := problem[name]
		if !bad {
			continue
		}
		skip[i] = true
		out[i] = verify.Result{
			Name: c.Name, Cmd: c.Cmd, Status: verify.StatusNotRun,
			Output: "this check needs the " + name + " substrate and could not have it: " + why,
		}
	}
	return skip, func() {
		for _, lease := range held {
			lease.Release()
		}
	}
}
