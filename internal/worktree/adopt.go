package worktree

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// adoptLogLimit caps the inherited commit list seeded into an adopted
// card's artifact. A long-lived branch can carry hundreds of commits, and
// the list is there to orient a reader, not to be a complete history —
// git is still there for anyone who wants all of it.
const adoptLogLimit = 30

// InspectBranch answers what a card would be inheriting if it adopted
// branch, measured against base: the commits already on it, the shape of
// what they changed, and how far behind it has fallen.
//
// It is the git half of cardmint's InspectAdopted callback, and it is
// also the mint-time existence check — a branch that is not there fails
// here, before a sequence number is spent, with the message that says how
// to go and get it. Everything it returns is descriptive: nothing is
// written, nothing is checked out, and the branch is not touched.
func (m *Manager) InspectBranch(ctx context.Context, branch, base string) (domain.AdoptedWork, error) {
	if err := domain.ValidateAdoptedBranch(branch); err != nil {
		return domain.AdoptedWork{}, err
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return domain.AdoptedWork{}, err
	} else if !ok {
		// The overwhelmingly common cause is a branch that exists only on
		// the remote, so lead with the command that fixes that rather than
		// with the bare fact.
		return domain.AdoptedWork{}, fmt.Errorf("no local branch %s in %s — fetch it first (git fetch origin %s:%s), or check the spelling",
			branch, m.repo, branch, branch)
	}
	if strings.TrimSpace(base) == "" {
		base = m.BaseBranch(ctx)
	}
	if base == branch {
		return domain.AdoptedWork{}, fmt.Errorf("%s is the branch this card would land on; adopting it would leave nothing to review", branch)
	}
	work := domain.AdoptedWork{Branch: branch, Base: base}
	// The fork point, and the refusal that matters most: without a merge
	// base there is no diff, no drift check and no merge, so a card
	// adopting such a branch is broken from its first stage. Better to say
	// so while it is still a sentence about two branch names.
	forkPoint, err := runGit(ctx, m.repo, "merge-base", base, branch)
	if err != nil {
		return domain.AdoptedWork{}, fmt.Errorf("%s and %s share no history, so there is nothing to diff %s against", branch, base, branch)
	}
	rng := forkPoint + ".." + branch
	if out, err := runGit(ctx, m.repo, "log", "--oneline", "--no-decorate", "-n", strconv.Itoa(adoptLogLimit), rng); err == nil {
		for line := range strings.Lines(out) {
			if line = strings.TrimSpace(line); line != "" {
				work.Commits = append(work.Commits, line)
			}
		}
	}
	// Raw, because runGit trims: `git diff --stat` indents every line by
	// one space, and losing it from the first line alone leaves the block
	// ragged against the rest of itself once it is rendered.
	if out, err := runGitRaw(ctx, m.repo, "diff", "--stat", forkPoint, branch); err == nil {
		work.Stat = strings.TrimRight(out, " \t\n")
	}
	if out, err := runGit(ctx, m.repo, "rev-list", "--count", branch+".."+base); err == nil {
		if n, cerr := strconv.Atoi(strings.TrimSpace(out)); cerr == nil {
			work.Behind = n
		}
	}
	// A branch with no commits of its own is adoptable but almost
	// certainly a mistake — a typo that landed on a stale ref, or a branch
	// cut and never used. Refusing it costs nothing and catches both,
	// where accepting it produces a card whose "inherited work" section is
	// empty and whose whole premise is false.
	if len(work.Commits) == 0 {
		return domain.AdoptedWork{}, fmt.Errorf("%s has no commits that %s does not already have — there is nothing to adopt", branch, base)
	}
	return work, nil
}

// InspectBranch resolves the named repo and inspects branch in it
// (Manager.InspectBranch).
func (p *Pool) InspectBranch(ctx context.Context, repo, branch, base string) (domain.AdoptedWork, error) {
	wt, err := p.ManagerForName(ctx, repo)
	if err != nil {
		return domain.AdoptedWork{}, err
	}
	return wt.InspectBranch(ctx, branch, base)
}

// FetchRef runs one fetch into the managed repository: `git fetch <remote>
// <refspec>`. It is the only place gummi reaches for a remote ref, and it
// exists for one caller — bringing a pull request's head down so a card
// can adopt it (DESIGN §7).
//
// It is deliberately not a force fetch. A refspec landing on a local
// branch that already exists and has diverged is refused by git, which is
// the behavior D22 asks for: gummi does not rewrite a branch it did not
// cut, and that includes one it fetched earlier.
func (m *Manager) FetchRef(ctx context.Context, remote, refspec string) error {
	if strings.TrimSpace(remote) == "" || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("invalid remote %q", remote)
	}
	if strings.TrimSpace(refspec) == "" || strings.HasPrefix(refspec, "-") {
		return fmt.Errorf("invalid refspec %q", refspec)
	}
	if _, err := runGit(ctx, m.repo, "fetch", "--", remote, refspec); err != nil {
		return fmt.Errorf("fetching %s from %s: %w", refspec, remote, err)
	}
	return nil
}

// BranchExistsNamed reports whether a local branch of this name is
// present, for a caller holding a branch name rather than a card.
func (m *Manager) BranchExistsNamed(ctx context.Context, branch string) (bool, error) {
	if err := domain.ValidateAdoptedBranch(branch); err != nil {
		return false, err
	}
	return gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
}

// FetchRef resolves the named repo and fetches into it (Manager.FetchRef).
func (p *Pool) FetchRef(ctx context.Context, repo, remote, refspec string) error {
	wt, err := p.ManagerForName(ctx, repo)
	if err != nil {
		return err
	}
	return wt.FetchRef(ctx, remote, refspec)
}

// BranchExistsNamed resolves the named repo and asks it
// (Manager.BranchExistsNamed).
func (p *Pool) BranchExistsNamed(ctx context.Context, repo, branch string) (bool, error) {
	wt, err := p.ManagerForName(ctx, repo)
	if err != nil {
		return false, err
	}
	return wt.BranchExistsNamed(ctx, branch)
}
