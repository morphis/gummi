package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// adoption is what `--adopt` / `--pr` resolve to: the local branch a card
// will be minted onto, and the pull request it came from when one was
// named. A zero adoption means an ordinary card.
type adoption struct {
	Branch string
	PR     domain.PullRequestRef
	Head   pr.PullHead
}

// HasPR reports whether this adoption came from a pull request, and so
// whether the card should be linked to it and its review threads pulled
// in once the card exists.
func (a adoption) HasPR() bool { return !a.PR.Empty() }

// resolveAdoption turns the two adoption flags into one branch name,
// fetching a pull request's head when that is what was asked for.
//
// The two flags are alternatives rather than a pair: `--adopt` names a
// branch that is already here, `--pr` finds the branch behind a pull
// request and brings it here. Passing both would be asking gummi to guess
// which one wins.
//
// Everything this does happens BEFORE a card is minted, deliberately: a
// PR that cannot be resolved, a branch that is not there, a fetch that
// collides with a local ref — all of them should fail as usage errors,
// with nothing created and no sequence number spent.
func resolveAdoption(branch, prSpec, repo string) (adoption, error) {
	branch, prSpec = strings.TrimSpace(branch), strings.TrimSpace(prSpec)
	switch {
	case branch == "" && prSpec == "":
		return adoption{}, nil
	case branch != "" && prSpec != "":
		return adoption{}, fmt.Errorf("--adopt and --pr both name a branch to work on; pass one")
	case branch != "":
		if err := domain.ValidateAdoptedBranch(branch); err != nil {
			return adoption{}, err
		}
		return adoption{Branch: branch}, nil
	}

	ghBinary := pr.GHBinary()
	if err := pr.Available(ghBinary); err != nil {
		return adoption{}, err
	}
	env, err := openPREnv()
	if err != nil {
		return adoption{}, err
	}
	defer env.cleanup()

	ctx := context.Background()
	mgr, err := env.wt.ManagerForName(ctx, repo)
	if err != nil {
		return adoption{}, err
	}
	dir := mgr.RepoRoot()
	// The branch argument is empty because --auto (resolve by matching a
	// card's branch) makes no sense here: there is no card yet, which is
	// the whole point.
	ref, err := pr.Resolve(ctx, ghBinary, prSpec, dir, "")
	if err != nil {
		return adoption{}, fmt.Errorf("resolving %s: %w", prSpec, err)
	}
	head, err := pr.Head(ctx, ghBinary, ref, dir)
	if err != nil {
		return adoption{}, fmt.Errorf("reading the head branch of %s#%d: %w", ref.Repo, ref.Number, err)
	}
	if err := fetchPullHead(ctx, env.wt, repo, ref, head); err != nil {
		return adoption{}, err
	}
	fmt.Fprintln(os.Stderr, head.Hint())
	return adoption{Branch: head.Local, PR: ref, Head: head}, nil
}

// fetchPullHead brings the PR's head down as a local branch, unless a
// branch of that name is already here.
//
// An existing local branch is left exactly as it is rather than fast
// forwarded or reset: it may be the user's own copy with work on it, and
// overwriting it to save one command would be the precise thing D22 says
// gummi does not do to a branch it did not cut. The mint that follows
// adopts whatever is actually there, and the message says so.
func fetchPullHead(ctx context.Context, pool *worktree.Pool, repo string, ref domain.PullRequestRef, head pr.PullHead) error {
	exists, err := pool.BranchExistsNamed(ctx, repo, head.Local)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintf(os.Stderr, "%s is already here; adopting it as it stands (gummi will not move it to match the PR)\n", head.Local)
		return nil
	}
	return pool.FetchRef(ctx, repo, "origin", head.Refspec(ref.Number))
}

// linkAdoptedPR records the pull request on a freshly minted card and
// pulls its unresolved review threads in as diff annotations, so the
// human's comments are on the card's diff BEFORE its first stage reads
// anything.
//
// That ordering is the point of doing this here rather than leaving the
// user to run `gummi pr link` and `gummi pr comments --ingest` by hand
// afterwards: an adopted card's plan stage is being asked to design a
// rework, and review comments are the best available statement of what
// the rework is for. Arriving one stage later, they would be findings
// against a plan that never knew about them.
//
// Failures here are reported and not fatal. The card exists, it is
// adopted onto the right branch, and it can be driven; a `gh` that is not
// logged in is a reason to run one more command later, not a reason to
// throw the card away.
func linkAdoptedPR(ctx context.Context, store *state.Store, wt *worktree.Manager, f *domain.Feature, ref domain.PullRequestRef) {
	if err := store.SetPullRequest(ctx, f.ID, ref); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s could not be linked to %s: %v\n", f.ID, ref.URL, err)
		return
	}
	f.PullRequest = ref
	fmt.Fprintf(os.Stderr, "%s linked to %s#%d\n", f.ID, ref.Repo, ref.Number)

	ghBinary := pr.GHBinary()
	if err := pr.Available(ghBinary); err != nil {
		fmt.Fprintf(os.Stderr, "warning: review comments not ingested (%v); run `gummi pr comments %s --ingest` later\n", err, f.ID)
		return
	}
	threads, _, _, err := pr.FetchReviewThreads(ctx, ghBinary, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: fetching review threads for %s: %v\n", f.ID, err)
		return
	}
	if len(threads) == 0 {
		return
	}
	// The annotations anchor against the card's own worktree diff, so the
	// worktree has to exist first. Adoption is the one case where it is
	// worth materializing before the first stage runs: the diff is already
	// there to anchor to, which is not true of any other new card.
	if _, err := wt.Ensure(ctx, f); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not attach %s to %s: %v\n", f.ID, f.BranchName(), err)
		return
	}
	var lines []string
	if diff, derr := wt.Diff(ctx, f); derr == nil {
		lines = strings.Split(strings.TrimRight(diff, "\n"), "\n")
	}
	res, err := pr.Ingest(ctx, store, f.ID, lines, threads)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: ingesting review threads for %s: %v\n", f.ID, err)
		return
	}
	fmt.Fprintf(os.Stderr, "%d review comment%s from %s#%d are on %s's diff (%d could not be anchored)\n",
		res.Written, cardPlural(res.Written), ref.Repo, ref.Number, f.ID, res.Orphaned)
}
