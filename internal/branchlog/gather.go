package branchlog

import (
	"context"
	"errors"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// RefusedError is Refusal's sentence as an error, so a caller can tell
// "this card's history is not to be rewritten" from a git failure and
// answer it as the conflict it is.
type RefusedError struct{ Why string }

func (e *RefusedError) Error() string { return e.Why }

// Env is what reading and rewriting a card's log needs: the store, for
// the base a squash would reset onto, and the pool, for the card's
// repository.
type Env struct {
	Store *state.Store
	Pool  *worktree.Pool
}

// Read gathers f's log. busy is whether an agent session holds the card,
// which only the caller can know. A card with no branch to read is not an
// error: it comes back with no rows and a Why.
func (e Env) Read(ctx context.Context, f domain.Feature, busy bool) (Log, error) {
	if why := Refusal(f, State{Busy: busy}); why != "" && !hasBranch(f) {
		return Log{Why: why}, nil
	}
	mgr, base, err := e.resolve(ctx, f)
	if err != nil {
		if errors.Is(err, worktree.ErrNoWorktree) {
			return Log{Why: string(f.ID) + " has no worktree yet"}, nil
		}
		return Log{}, err
	}
	entries, err := mgr.Log(ctx, &f, base)
	if err != nil {
		return Log{}, err
	}
	landed, _ := e.Pool.Landed(ctx, &f)
	out := Log{Rows: Rows(entries), Why: Refusal(f, State{Busy: busy, Landed: landed})}
	if len(out.Rows) == 0 && out.Why == "" {
		out.Why = string(f.ID) + " has no commits of its own yet"
	}
	out.PushCommand = engine.PushCommandFor(ctx, mgr, &f)
	return out, nil
}

// hasBranch is whether a card, whatever refuses it rewriting, has a branch
// whose log can still be read: an adopted or landed card's is.
func hasBranch(f domain.Feature) bool {
	return f.Kind != domain.KindResearch && f.Stage != domain.StageTodo && !f.MainCheckout
}

// Plan is what plan would do, refusing it as Apply would. Nothing moves.
func (e Env) Plan(ctx context.Context, f domain.Feature, busy bool, plan worktree.RewritePlan) (worktree.RewritePreview, error) {
	mgr, base, err := e.guard(ctx, f, busy)
	if err != nil {
		return worktree.RewritePreview{}, err
	}
	return mgr.PlanRewrite(ctx, &f, base, plan)
}

// Apply rewrites the card's branch to plan and returns its new tip, or ""
// for a plan that changes nothing, plus the push command a rewrite of
// pushed commits leaves for the person to run. The caller holds the
// card's lock.
func (e Env) Apply(ctx context.Context, f domain.Feature, busy bool, plan worktree.RewritePlan, acknowledgePushed bool) (tip, pushCmd string, err error) {
	mgr, base, err := e.guard(ctx, f, busy)
	if err != nil {
		return "", "", err
	}
	prev, err := mgr.PlanRewrite(ctx, &f, base, plan)
	if err != nil {
		return "", "", err
	}
	tip, err = mgr.Rewrite(ctx, &f, base, plan, acknowledgePushed)
	if err != nil {
		return "", "", err
	}
	if prev.Pushed {
		pushCmd = engine.PushCommandFor(ctx, mgr, &f)
	}
	return tip, pushCmd, nil
}

func (e Env) resolve(ctx context.Context, f domain.Feature) (*worktree.Manager, string, error) {
	mgr, err := e.Pool.ManagerFor(ctx, &f)
	if err != nil {
		return nil, "", err
	}
	if ok, err := mgr.Exists(ctx, &f); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", worktree.ErrNoWorktree
	}
	base, err := worktree.ResolveCollapseBase(ctx, e.Store, mgr, &f)
	if err != nil {
		return nil, "", err
	}
	return mgr, base, nil
}

func (e Env) guard(ctx context.Context, f domain.Feature, busy bool) (*worktree.Manager, string, error) {
	landed, _ := e.Pool.Landed(ctx, &f)
	if why := Refusal(f, State{Busy: busy, Landed: landed}); why != "" {
		return nil, "", &RefusedError{Why: why}
	}
	return e.resolve(ctx, f)
}

// CommitDiff is one of the card's own commits as a patch.
func (e Env) CommitDiff(ctx context.Context, f domain.Feature, sha string) (string, error) {
	mgr, base, err := e.resolve(ctx, f)
	if err != nil {
		return "", err
	}
	return mgr.CommitDiff(ctx, &f, base, sha)
}
