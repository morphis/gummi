package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The repository as a whole, rather than one card's corner of it: every
// local branch with where it stands against the base and its upstream,
// the main checkout's own state, and the three writes a person makes to
// a repository between cards — fetching, fast-forwarding the base to its
// upstream, and deleting a branch no card holds. None of them pushes:
// publishing is a card's own act (DESIGN §22), not this view's.

// ErrNoUpstream refuses a fast-forward of a base that tracks nothing.
var ErrNoUpstream = errors.New("the branch tracks no upstream")

// ErrDiverged refuses a fast-forward of a base holding commits its
// upstream lacks: moving it would take a merge or a rebase, and which is
// the person's call.
var ErrDiverged = errors.New("the branch and its upstream have diverged")

// ErrMainDirty refuses a fast-forward over uncommitted tracked changes
// in the main checkout.
var ErrMainDirty = errors.New("the main checkout has uncommitted changes")

// ErrBranchInUse refuses deleting a branch that is checked out.
var ErrBranchInUse = errors.New("the branch is checked out")

// ErrBranchUnmerged refuses deleting, unforced, a branch holding commits
// the base lacks.
var ErrBranchUnmerged = errors.New("the branch has commits the base lacks")

// BranchInfo is one local branch as the repository view reads it.
type BranchInfo struct {
	Name      string
	SHA       string
	Subject   string
	Committed time.Time
	// Upstream is the remote-tracking branch ("origin/main"), empty when
	// the branch tracks nothing. UpstreamGone marks one whose remote
	// branch a prune removed; Ahead and Behind count against a live one.
	Upstream     string
	UpstreamGone bool
	// UpstreamRemote is the remote Upstream is on ("origin").
	UpstreamRemote string
	Ahead, Behind  int
	// AheadBase and BehindBase count against the base branch; both are
	// zero on the base itself.
	AheadBase, BehindBase int
	// Worktree is where the branch is checked out, empty when nowhere.
	Worktree string
}

// RepoStatus is the main checkout's own state.
type RepoStatus struct {
	Root string
	// Base is the branch the main checkout has out — what cards fork from
	// and land on unless they name another. Empty when it is detached.
	Base    string
	Origin  string
	Remotes []string
	// Dirty reports uncommitted tracked changes in the main checkout.
	Dirty bool
	// Fetched is when the repository last fetched; zero when it never has.
	Fetched time.Time
}

// currentBranch is the main checkout's branch, empty when it is detached.
// BaseBranch answers the same question for prose and so never comes back
// empty; a write must not act on the name it substitutes.
func (m *Manager) currentBranch(ctx context.Context) string {
	out, err := runGit(ctx, m.repo, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// Status reads the main checkout's state. It never touches the network.
func (m *Manager) Status(ctx context.Context) RepoStatus {
	st := RepoStatus{
		Root: m.repo, Base: m.currentBranch(ctx),
		Origin: RemoteOrigin(ctx, m.repo), Remotes: m.Remotes(ctx),
	}
	st.Dirty, _ = m.MainTrackedDirty(ctx)
	if p, err := runGit(ctx, m.repo, "rev-parse", "--git-path", "FETCH_HEAD"); err == nil && p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(m.repo, p)
		}
		if fi, serr := os.Stat(p); serr == nil {
			st.Fetched = fi.ModTime()
		}
	}
	return st
}

// Branches lists the repository's local branches, by name.
func (m *Manager) Branches(ctx context.Context) ([]BranchInfo, error) {
	const format = "%(refname:short)%00%(objectname)%00%(upstream:short)%00%(upstream:track)%00%(committerdate:unix)%00%(worktreepath)%00%(upstream:remotename)%00%(subject)"
	out, err := runGitRaw(ctx, m.repo, "for-each-ref", "--format="+format, "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("listing branches in %s: %w", m.repo, err)
	}
	base := m.currentBranch(ctx)
	var branches []BranchInfo
	for line := range strings.Lines(out) {
		f := strings.Split(strings.TrimRight(line, "\n"), "\x00")
		if len(f) != 8 || f[0] == "" {
			continue
		}
		b := BranchInfo{Name: f[0], SHA: f[1], Upstream: f[2], Worktree: f[5], UpstreamRemote: f[6], Subject: f[7]}
		b.UpstreamGone, b.Ahead, b.Behind = parseTrack(f[3])
		if secs, perr := strconv.ParseInt(f[4], 10, 64); perr == nil {
			b.Committed = time.Unix(secs, 0)
		}
		if base != "" && b.Name != base {
			b.BehindBase, b.AheadBase = m.leftRight(ctx, "refs/heads/"+base, "refs/heads/"+b.Name)
		}
		branches = append(branches, b)
	}
	return branches, nil
}

// parseTrack reads for-each-ref's %(upstream:track): "[gone]",
// "[ahead 2]", "[behind 1]", "[ahead 2, behind 1]", or nothing when the
// two agree.
func parseTrack(s string) (gone bool, ahead, behind int) {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	if s == "gone" {
		return true, 0, 0
	}
	for part := range strings.SplitSeq(s, ", ") {
		if n, ok := strings.CutPrefix(part, "ahead "); ok {
			ahead, _ = strconv.Atoi(n)
		}
		if n, ok := strings.CutPrefix(part, "behind "); ok {
			behind, _ = strconv.Atoi(n)
		}
	}
	return false, ahead, behind
}

// leftRight counts the commits only a has and only b has.
func (m *Manager) leftRight(ctx context.Context, a, b string) (left, right int) {
	out, err := runGit(ctx, m.repo, "rev-list", "--left-right", "--count", a+"..."+b)
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0
	}
	left, _ = strconv.Atoi(f[0])
	right, _ = strconv.Atoi(f[1])
	return left, right
}

// Fetch updates every remote's tracking refs and prunes the ones whose
// remote branch is gone. A read of the remotes and a write to this
// repository's own refs/remotes: no local branch moves. A remote that
// wants a password fails rather than asking a terminal nobody is at.
func (m *Manager) Fetch(ctx context.Context) error {
	if len(m.Remotes(ctx)) == 0 {
		return ErrNoRemote
	}
	_, err := runGitEnv(ctx, m.repo, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "--all", "--prune")
	return err
}

// FastForwardBase moves the main checkout's branch up to its upstream as
// already fetched, and reports the tips before and after (equal when
// there was nothing to take). It only ever fast-forwards: a dirty main
// checkout, a base with commits of its own, and a base tracking nothing
// are each refused, so the checkout a person works in is never merged
// into or rewritten behind them.
func (m *Manager) FastForwardBase(ctx context.Context) (from, to string, err error) {
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	base := m.currentBranch(ctx)
	if base == "" {
		return "", "", errors.New("the main checkout is not on a branch")
	}
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "--quiet", "@{upstream}^{commit}"); err != nil {
		return "", "", fmt.Errorf("%s: %w", base, ErrNoUpstream)
	}
	if from, err = runGit(ctx, m.repo, "rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	if current, cerr := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", "@{upstream}", "HEAD"); cerr == nil && current {
		return from, from, nil
	}
	if ff, ferr := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", "HEAD", "@{upstream}"); ferr != nil || !ff {
		return "", "", fmt.Errorf("%s: %w", base, ErrDiverged)
	}
	if dirty, derr := m.MainTrackedDirty(ctx); derr != nil {
		return "", "", derr
	} else if dirty {
		return "", "", ErrMainDirty
	}
	if _, err := runGit(ctx, m.repo, "merge", "--ff-only", "@{upstream}"); err != nil {
		return "", "", err
	}
	if to, err = runGit(ctx, m.repo, "rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	return from, to, nil
}

// DeleteBranchNamed deletes a local branch by name: one no card holds,
// which is the caller's to establish. It refuses a branch checked out
// anywhere, and — unless force — one with commits the base lacks.
func (m *Manager) DeleteBranchNamed(ctx context.Context, name string, force bool) error {
	branches, err := m.Branches(ctx)
	if err != nil {
		return err
	}
	for _, b := range branches {
		if b.Name != name {
			continue
		}
		if b.Worktree != "" {
			return fmt.Errorf("%s at %s: %w", name, b.Worktree, ErrBranchInUse)
		}
		if b.AheadBase > 0 && !force {
			return fmt.Errorf("%s: %w", name, ErrBranchUnmerged)
		}
		// -D: the check above is the merge test, against the base and
		// not whatever git's -d would compare with
		_, err := runGit(ctx, m.repo, "branch", "-D", "--", name)
		return err
	}
	return fmt.Errorf("no branch %s in %s", name, m.repo)
}
