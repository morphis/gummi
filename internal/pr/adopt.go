package pr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// headJSONFields is what Head asks gh for beyond what Resolve already
// took: the branch behind the PR, and whether it lives in somebody else's
// copy of the repository.
const headJSONFields = "headRefName,isCrossRepository,headRepositoryOwner"

type ghHead struct {
	HeadRefName         string `json:"headRefName"`
	IsCrossRepository   bool   `json:"isCrossRepository"`
	HeadRepositoryOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
}

// PullHead is the branch behind a pull request, as a card would adopt it.
//
// Ref is what GitHub calls the branch; Local is what it will be called
// here. For a PR raised inside the repository itself those are the same
// name, which is what makes the branch gummi works on the same branch the
// PR is built from. For a PR raised from a FORK they cannot be: the
// branch lives in a repository gummi has no write access to, so the local
// copy is given a name that says where it came from and never pretends to
// be the contributor's own ref.
type PullHead struct {
	Ref      string // the branch name on the PR's head repository
	Local    string // the local branch name to adopt
	FromFork bool
	Owner    string // the login owning the head repository
}

// Refspec is the fetch that brings this PR's head down as Local.
//
// `pull/<N>/head` is used rather than the head repository's own branch
// because it exists for every PR — fork or not — on the repository the PR
// was opened against, which is the one remote gummi can be sure of. The
// alternative, adding a remote per contributor, would change the
// repository's configuration to read one branch.
func (h PullHead) Refspec(number int) string {
	return "pull/" + strconv.Itoa(number) + "/head:" + h.Local
}

// Hint is the sentence a caller shows about where this branch can end up.
// A fork's PR is the case worth being explicit about: gummi can adopt it
// and rework it, but the result is a local branch the user owns, and
// saying otherwise would set up a push that cannot happen.
func (h PullHead) Hint() string {
	if !h.FromFork {
		return "adopting " + h.Local + " — the branch this PR is built from"
	}
	return "adopting " + h.Local + " — a local copy of @" + h.Owner + "'s " + h.Ref +
		"; gummi cannot write to their fork, so the reworked branch is yours to push somewhere you can"
}

// Head resolves the branch behind ref. repoDir is only used to let gh
// auto-detect a repository when ref carries none, matching Resolve.
func Head(ctx context.Context, ghBinary string, ref domain.PullRequestRef, repoDir string) (PullHead, error) {
	args := []string{"pr", "view", strconv.Itoa(ref.Number), "--json", headJSONFields}
	if ref.Repo != "" {
		args = append(args, "--repo", ref.Repo)
	}
	out, err := run(ctx, ghBinary, repoDir, args...)
	if err != nil {
		return PullHead{}, err
	}
	var h ghHead
	if err := json.Unmarshal(out, &h); err != nil {
		return PullHead{}, fmt.Errorf("parsing gh pr view output: %w", err)
	}
	if strings.TrimSpace(h.HeadRefName) == "" {
		return PullHead{}, fmt.Errorf("%s#%d has no head branch to adopt", ref.Repo, ref.Number)
	}
	head := PullHead{Ref: h.HeadRefName, Local: h.HeadRefName, FromFork: h.IsCrossRepository, Owner: h.HeadRepositoryOwner.Login}
	if head.FromFork {
		// Named for the PR rather than the contributor's branch, because
		// two forks may well have a `fix-the-thing` each and because the
		// number is the thing a reader can look up.
		head.Local = "pr-" + strconv.Itoa(ref.Number) + "-" + sanitizeRefPart(h.HeadRefName)
	}
	if err := domain.ValidateAdoptedBranch(head.Local); err != nil {
		return PullHead{}, fmt.Errorf("%s#%d: %w", ref.Repo, ref.Number, err)
	}
	return head, nil
}

// sanitizeRefPart flattens a branch name into one path segment safe to
// embed in another ref: slashes and the characters git refuses become
// dashes. It is only ever applied to the fork case, where the name is
// already being changed and nothing depends on it matching upstream.
func sanitizeRefPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ' ' || r == '~' || r == '^' || r == ':' || r == '?' || r == '*' || r == '[' || r == '@':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-.")
}
