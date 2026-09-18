package engine

// The repositories a goal touches. A goal is not in a repository: you
// describe an outcome, and the cards that meet it name their own. What the
// goal has instead is a *home* repo — where its own card, branch, doc
// promotion, lead and review live — plus a goal branch in every other
// repository one of its cards is in (worktree/goal.go).
//
// The home is derived, never asked for: a goal is minted into a
// provisional one (the workspace default, or the first configured repo
// where there is no default), and its plan gate settles it on the repo its
// cards are actually in. Nothing of the goal's lands on the goal branch
// before that gate — the doc lives in .gummi/goals/, never in the tree —
// so re-homing is dropping an empty branch and cutting it again elsewhere.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// ProvisionalRepo is the repository a goal is minted into before its plan
// says where its work is. See worktree.Pool.ProvisionalRepo.
func (e *Engine) ProvisionalRepo() string {
	if e.pool == nil {
		return ""
	}
	return e.pool.ProvisionalRepo()
}

// goalCardRepos lists the distinct repositories of a goal's cards, sorted.
func (e *Engine) goalCardRepos(ctx context.Context, goalID domain.FeatureID) ([]string, error) {
	cards, err := e.cfg.Store.ListGoalCards(ctx, goalID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cards {
		if c.Kind == domain.KindResearch {
			// research never cuts a branch, so it puts nothing on any goal
			// branch and needs no tree of its own
			continue
		}
		if !seen[c.Repo] {
			seen[c.Repo] = true
			out = append(out, c.Repo)
		}
	}
	sort.Strings(out)
	return out, nil
}

// goalTrees resolves the goal's tree in every repository it touches: its
// home first, then the rest by name. It is what every goal verb that used
// to act on "the goal branch" loops over.
func (e *Engine) goalTrees(ctx context.Context, goal domain.Feature) ([]worktree.GoalTree, error) {
	repos, err := e.goalCardRepos(ctx, goal.ID)
	if err != nil {
		return nil, err
	}
	return e.pool.GoalTrees(ctx, &goal, repos)
}

// goalTreeIn ensures the goal has a tree in repo, cutting one if this is
// the first card the goal has put there. Every path that gives a goal a
// card — the plan gate, an attachment, the lead's card_create — goes
// through it, because a card whose repository has no goal branch has
// nothing to fork from.
func (e *Engine) goalTreeIn(ctx context.Context, goal domain.Feature, repo string) (worktree.GoalTree, error) {
	return e.pool.EnsureGoalTree(ctx, &goal, repo)
}

// goalRepoProblem reports why repo cannot hold a goal's card, "" when it
// can. It is the plan gate's repo rule, and it replaced a stricter one: a
// row used to have to be in the same repository as the goal, which is the
// thing a goal that spans repositories cannot honor.
func (e *Engine) goalRepoProblem(repo string) string {
	if e.pool == nil || e.pool.Known(repo) {
		return ""
	}
	if repo == "" {
		return "names no repository, and this workspace has no default one — give it a `repo:` from " + e.repoChoices()
	}
	return fmt.Sprintf("names the repository %q, which is not configured — use one of %s", repo, e.repoChoices())
}

// repoChoices renders the configured repository names for an error.
func (e *Engine) repoChoices() string {
	if e.pool == nil {
		return "the configured `repos:`"
	}
	names := e.pool.Names()
	if len(names) == 0 {
		return "the configured `repos:` in .gummi/config.yaml"
	}
	return strings.Join(names, ", ")
}

// goalHomeRepo picks the repository a goal's own card belongs in: the one
// most of its cards are in, ties going to the one that appears first in
// the plan. The goal's own branch is the one its cards' landings pile up
// on, so putting it where most of the work is keeps the most cards on a
// tree of their own repository — and in a single-repo workspace it is the
// only answer there is.
func goalHomeRepo(rows []domain.GoalCardRow, repoOf map[domain.FeatureID]string) string {
	count := map[string]int{}
	var order []string
	for _, r := range rows {
		repo := r.Repo
		if r.ID != "" {
			// an attached row brings its card's repository; the row's own
			// `repo:` is not the goal's to impose on a card that exists
			if have, ok := repoOf[r.ID]; ok {
				repo = have
			}
		}
		if _, seen := count[repo]; !seen {
			order = append(order, repo)
		}
		count[repo]++
	}
	best := ""
	for i, repo := range order {
		if i == 0 || count[repo] > count[best] {
			best = repo
		}
	}
	return best
}

// settleGoalHome moves the goal card to the repository its plan says the
// work is in, if that is not where it was provisionally minted. It runs at
// the plan gate, before a single card is minted or attached, so the goal
// branch it drops is always empty: the goal doc lives in .gummi/goals/ and
// never in the tree, and no card has landed anything yet.
//
// A tree it cannot drop (a branch with commits on it, somehow) fails the
// crossing rather than leaving the goal with two branches and no way to
// say which one its cards fork from.
func (e *Engine) settleGoalHome(ctx context.Context, goal *domain.Feature, rows []domain.GoalCardRow) error {
	repoOf := map[domain.FeatureID]string{}
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		c, err := e.cfg.Store.GetFeature(ctx, r.ID)
		if err != nil {
			return err
		}
		repoOf[r.ID] = c.Repo
	}
	want := goalHomeRepo(rows, repoOf)
	if want == goal.Repo {
		return nil
	}
	if e.pool != nil {
		if mgr, err := e.pool.ManagerForName(ctx, goal.Repo); err == nil {
			if err := mgr.RemoveGoalTree(ctx, goal, string(goal.ID)); err != nil {
				return fmt.Errorf("moving %s to %s: %w", goal.ID, repoName(want), err)
			}
		}
	}
	from := goal.Repo
	goal.Repo = want
	goal.UpdatedAt = e.now()
	if err := e.cfg.Store.UpdateFeature(ctx, goal); err != nil {
		return err
	}
	e.cardNote(ctx, goal.ID, goal.Stage, "home repository is "+repoName(want)+", where its cards are"+fromClause(from))
	return nil
}

// repoMain names a repository's main checkout in a sentence: a plain
// "main" when there is only the default repository to mean, "web's main"
// when the name carries information.
func repoMain(repo string) string {
	if repo == "" {
		return "main"
	}
	return repo + "'s main"
}

// repoName names a repository in a sentence: its configured name, or what
// the workspace default is called when it has no name of its own.
func repoName(repo string) string {
	if repo == "" {
		return "the default repository"
	}
	return repo
}

func fromClause(from string) string {
	if from == "" {
		return ""
	}
	return " (was " + from + ")"
}

// goalReposCard tells a goal's own sessions about repositories: which
// ones this workspace manages (so a plan can put a card in one) and,
// once the goal has trees, where each of its branches is checked out (so
// a review or verify of "the combined change" can read all of it).
//
// It says nothing at all in a single-repo workspace, which is most of
// them: a hint about choosing between one repository is noise.
func (e *Engine) goalReposCard(ctx context.Context, goal domain.Feature) string {
	if e.pool == nil {
		return ""
	}
	names := e.pool.Names()
	if len(names) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Repositories. This workspace manages several: " + strings.Join(names, ", ") + ".\n")
	b.WriteString("A goal is not in one of them — each card row in the goal doc names its own `repo:`, ")
	b.WriteString("and the goal has a branch of its own in every repository its cards are in. ")
	b.WriteString("A done-when item whose `check:` must run somewhere other than " + repoName(goal.Repo) + " names that repository too.\n")
	trees, err := e.goalTrees(ctx, goal)
	if err != nil || len(trees) < 2 {
		return strings.TrimSpace(b.String())
	}
	b.WriteString("This goal's branches are checked out at:\n")
	for _, t := range trees {
		if !t.Exists() {
			continue
		}
		line := "- " + repoName(t.Repo) + ": " + t.Dir
		if t.Home {
			line += " (the goal's own, and your working directory)"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("They are siblings on disk, so a command that needs two of them at once reaches the others by relative path.")
	return strings.TrimSpace(b.String())
}
