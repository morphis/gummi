package worktree

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// PushTarget is where a card's branch goes when it is pushed: a remote and
// the branch name on it. It is the one answer every reader of "where does
// this branch live remotely" shares — the log's pushed mark, the printed
// push line after a rewrite, and the publish acts (DESIGN §22.5) — so a
// push and the PR opened for it can never resolve two different places.
type PushTarget struct {
	Remote string
	Branch string
	// How names the rule that chose Remote, for the person reading the
	// confirm: "pushRemote", "pushDefault", "upstream", "origin", "sole
	// remote" or "chosen".
	How string
}

// TrackingRef is the remote-tracking ref the target's branch is mirrored
// at locally ("refs/remotes/origin/feat/x").
func (t PushTarget) TrackingRef() string {
	return "refs/remotes/" + t.Remote + "/" + t.Branch
}

var (
	// ErrNoRemote is a repository with no remote at all: nothing to push to.
	ErrNoRemote = errors.New("the repository has no remote")
	// ErrRemoteAmbiguous is several remotes and nothing in git's own
	// configuration naming one: gummi does not guess between them.
	ErrRemoteAmbiguous = errors.New("several remotes and none is configured as the push remote")
)

// PushTarget resolves the branch's push target the way git itself would
// choose the remote, without relying on @{push} (which `push.default=simple`
// refuses to resolve for a branch with no upstream, or one whose upstream has
// another name):
//
//  1. branch.<b>.pushRemote, then remote.pushDefault, then the upstream's
//     remote — the triangular-workflow settings git already honours;
//  2. otherwise "origin", otherwise the sole remote;
//  3. otherwise ErrRemoteAmbiguous (or ErrNoRemote).
//
// The remote branch is the card's own branch name, or its upstream's name
// when the upstream lives on that same remote under another name (an adopted
// branch). It is never the card's base branch: a branch that tracks
// origin/main still pushes as itself, never onto main.
func (m *Manager) PushTarget(ctx context.Context, f *domain.Feature) (PushTarget, error) {
	return pushTargetIn(ctx, m.repo, f.BranchName(), m.BaseBranch(ctx), m.Remotes(ctx))
}

func pushTargetIn(ctx context.Context, repo, branch, base string, remotes []string) (PushTarget, error) {
	cfg := func(key string) string {
		v, err := runGit(ctx, repo, "config", "--get", key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	upRemote := cfg("branch." + branch + ".remote")
	if upRemote == "." {
		upRemote = ""
	}
	upBranch := strings.TrimPrefix(cfg("branch."+branch+".merge"), "refs/heads/")
	target := PushTarget{Branch: branch}
	switch {
	case cfg("branch."+branch+".pushRemote") != "":
		target.Remote, target.How = cfg("branch."+branch+".pushRemote"), "pushRemote"
	case cfg("remote.pushDefault") != "":
		target.Remote, target.How = cfg("remote.pushDefault"), "pushDefault"
	case upRemote != "":
		target.Remote, target.How = upRemote, "upstream"
	case slices.Contains(remotes, "origin"):
		target.Remote, target.How = "origin", "origin"
	case len(remotes) == 1:
		target.Remote, target.How = remotes[0], "sole remote"
	case len(remotes) == 0:
		return PushTarget{}, ErrNoRemote
	default:
		return PushTarget{}, ErrRemoteAmbiguous
	}
	if upRemote == target.Remote && upBranch != "" && upBranch != base {
		target.Branch = upBranch
	}
	return target, nil
}

// SetPushRemote records a person's choice of remote for the branch as
// branch.<b>.pushRemote — git's own setting, so the choice is asked once and
// every other git tool honours it too.
func (m *Manager) SetPushRemote(ctx context.Context, f *domain.Feature, remote string) error {
	if !slices.Contains(m.Remotes(ctx), remote) {
		return errors.New(remote + " is not a configured remote")
	}
	_, err := runGit(ctx, m.repo, "config", "branch."+f.BranchName()+".pushRemote", remote)
	return err
}

// Upstream is the card's push target, as the remote and branch the printed
// push line and the pushed mark read; ok is false when there is none to
// name (no remote, or an ambiguous choice no setting resolves).
func (m *Manager) Upstream(ctx context.Context, f *domain.Feature) (remote, branch string, ok bool) {
	t, err := m.PushTarget(ctx, f)
	if err != nil {
		return "", "", false
	}
	return t.Remote, t.Branch, true
}
