package ui

import (
	"context"
	"errors"

	"github.com/morphis/gummi/internal/branchlog"
	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// ErrConflict is a request the card's state refuses: its history is not
// rewritable now, the plan was made against commits that have moved, or a
// force push has not been acknowledged. The wrapped message is the
// sentence to show.
var ErrConflict = errors.New("conflict")

type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }
func (e *conflictError) Is(t error) bool {
	return t == ErrConflict
}

// logError classes what the rewrite path returned: a refusal or a plan
// gone stale is the caller's to see as such, a message the scrub caught is
// a malformed request, and the rest is a git failure.
func logError(id domain.FeatureID, err error) error {
	var refused *branchlog.RefusedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refused),
		errors.Is(err, worktree.ErrPlanMismatch),
		errors.Is(err, worktree.ErrPushedNotAcknowledged),
		errors.Is(err, worktree.ErrDirtyWorktree),
		errors.Is(err, worktree.ErrRebaseInProgress),
		errors.Is(err, worktree.ErrNoCommitsBeyondBase),
		errors.Is(err, state.ErrLocked):
		return &conflictError{msg: cardLockedNotice(id, err)}
	case errors.Is(err, worktree.ErrAttribution):
		return invalid("%s", err.Error())
	}
	return err
}

func (d *WebDocs) logEnv() branchlog.Env { return branchlog.Env{Store: d.store, Pool: d.pool} }

// Log is the card's own commits and whether they may be rewritten now.
func (d *WebDocs) Log(ctx context.Context) (webapi.Log, error) {
	f := d.fresh(ctx)
	l, err := d.logEnv().Read(ctx, f, d.busy)
	if err != nil {
		return webapi.Log{}, err
	}
	return WebLog(d.base, l), nil
}

// WebLog projects a card's log onto the wire.
func WebLog(base string, l branchlog.Log) webapi.Log {
	out := webapi.Log{
		Base: base, Commits: make([]webapi.LogCommit, 0, len(l.Rows)),
		Why: l.Why, Rewritable: l.Why == "" && len(l.Rows) > 0, PushCommand: l.PushCommand,
	}
	for _, r := range l.Rows {
		out.Commits = append(out.Commits, webLogCommit(r))
	}
	if n := len(out.Commits); n > 0 {
		out.Head = out.Commits[n-1].SHA
	}
	return out
}

func webLogCommit(r branchlog.Row) webapi.LogCommit {
	return webapi.LogCommit{
		SHA: r.SHA, Short: r.Short, Subject: r.Subject, Body: r.Body, Author: r.Author, At: r.At,
		Files: r.Files, Add: r.Add, Del: r.Del, Checkpoint: r.Checkpoint, Pushed: r.Pushed, Warning: r.Warning,
	}
}

// CommitDiff is one of the card's own commits as a patch.
func (d *WebDocs) CommitDiff(ctx context.Context, sha string) (webapi.CommitDiff, error) {
	raw, err := d.logEnv().CommitDiff(ctx, d.fresh(ctx), sha)
	if err != nil {
		if errors.Is(err, worktree.ErrPlanMismatch) {
			return webapi.CommitDiff{}, ErrMoved
		}
		return webapi.CommitDiff{}, err
	}
	out := webapi.CommitDiff{SHA: sha, Files: []webapi.DiffFile{}}
	for _, f := range diffannot.Parse(diffannot.Lines(raw)) {
		out.Files = append(out.Files, webDiffFile(f, nil, false))
	}
	return out, nil
}

func workPlan(req webapi.RewriteRequest) (worktree.RewritePlan, error) {
	if len(req.Groups) == 0 {
		return worktree.RewritePlan{}, invalid("a plan needs at least one commit")
	}
	plan := worktree.RewritePlan{Head: req.Head}
	for _, g := range req.Groups {
		plan.Groups = append(plan.Groups, worktree.RewriteGroup{Commits: g.Commits, Message: g.Message})
	}
	return plan, nil
}

// PlanRewrite is a dry run: what the branch would read like under req,
// refused for the reasons a rewrite would be. Nothing moves.
func (d *WebDocs) PlanRewrite(ctx context.Context, req webapi.RewriteRequest) (webapi.RewritePreview, error) {
	plan, err := workPlan(req)
	if err != nil {
		return webapi.RewritePreview{}, err
	}
	f := d.fresh(ctx)
	prev, err := d.logEnv().Plan(ctx, f, d.busy, plan)
	if err != nil {
		return webapi.RewritePreview{}, logError(d.f.ID, err)
	}
	out := webapi.RewritePreview{
		Changed: prev.Changed, Pushed: prev.Pushed, Noop: prev.Noop,
		Commits: make([]webapi.LogCommit, 0, len(prev.Entries)),
	}
	for _, r := range branchlog.Rows(prev.Entries) {
		out.Commits = append(out.Commits, webLogCommit(r))
	}
	if prev.Pushed {
		if mgr, err := d.pool.ManagerFor(ctx, &f); err == nil {
			out.PushCommand = engine.PushCommandFor(ctx, mgr, &f)
		}
	}
	return out, nil
}

// Rewrite applies req under the card's lock and answers with the log as
// it now reads. A no-op plan answers with the log unchanged.
func (d *WebDocs) Rewrite(ctx context.Context, req webapi.RewriteRequest) (webapi.RewriteResult, error) {
	plan, err := workPlan(req)
	if err != nil {
		return webapi.RewriteResult{}, err
	}
	release, err := d.locks.Acquire(d.f.ID)
	if err != nil {
		return webapi.RewriteResult{}, logError(d.f.ID, err)
	}
	defer release()
	f := d.fresh(ctx)
	tip, pushCmd, err := d.logEnv().Apply(ctx, f, d.busy, plan, req.AcknowledgePushed)
	if err != nil {
		return webapi.RewriteResult{}, logError(d.f.ID, err)
	}
	log, err := d.Log(ctx)
	if err != nil {
		return webapi.RewriteResult{}, err
	}
	if tip == "" {
		tip = log.Head
	}
	return webapi.RewriteResult{Head: tip, PushCommand: pushCmd, Log: log}, nil
}
