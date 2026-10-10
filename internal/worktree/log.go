package worktree

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// LogEntry is one commit on a card's branch, oldest-first in Log's answer.
type LogEntry struct {
	SHA     string
	Short   string
	Subject string
	// Body is the message after the subject line, trimmed.
	Body   string
	Author string
	At     time.Time
	// Files, Add and Del are the commit's own numstat. A binary file
	// counts as a file and adds no lines.
	Files int
	Add   int
	Del   int
	// Pushed marks a commit the branch's upstream already has: rewriting
	// it means the remote needs a force push, which gummi never runs.
	Pushed bool
}

// Message is the commit's whole message as git stores it.
func (e LogEntry) Message() string {
	if e.Body == "" {
		return e.Subject
	}
	return e.Subject + "\n\n" + e.Body
}

// Log is the card's own commits — base (exclusive) to the branch tip — in
// the order they were made, oldest first. base is the revision a rewrite
// or squash would reset onto (ResolveCollapseBase), so the list is
// exactly the range those verbs act on and never the cards beneath a
// stacked one.
func (m *Manager) Log(ctx context.Context, f *domain.Feature, base string) ([]LogEntry, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return nil, err
	}
	return m.logRange(ctx, m.repo, f, base, "refs/heads/"+branch)
}

func (m *Manager) logRange(ctx context.Context, dir string, f *domain.Feature, base, tip string) ([]LogEntry, error) {
	// %x1e opens a commit and %x1f separates its fields; neither byte can
	// appear in git object text. --numstat follows the message.
	out, err := runGitRaw(ctx, dir, "log", "--reverse", "--numstat",
		"--format=%x1e%H%x1f%an%x1f%at%x1f%B%x1f", base+".."+tip)
	if err != nil {
		return nil, err
	}
	pushed := m.pushedSet(ctx, f, base)
	var entries []LogEntry
	for rec := range strings.SplitSeq(out, "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1f", 5)
		if len(parts) < 5 {
			continue
		}
		e := LogEntry{SHA: parts[0], Author: parts[1]}
		if len(e.SHA) >= 7 {
			e.Short = e.SHA[:7]
		}
		if secs, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
			e.At = time.Unix(secs, 0).UTC()
		}
		e.Subject, e.Body, _ = strings.Cut(strings.TrimSpace(parts[3]), "\n")
		e.Body = strings.TrimSpace(e.Body)
		e.Files, e.Add, e.Del = sumNumstat(parts[4])
		e.Pushed = pushed[e.SHA]
		entries = append(entries, e)
	}
	return entries, nil
}

// pushedSet is the commits of base..upstream: what the branch's tracked
// remote branch already carries. Empty for a branch tracking nothing or
// whose remote-tracking ref is gone.
func (m *Manager) pushedSet(ctx context.Context, f *domain.Feature, base string) map[string]bool {
	remote, rb, ok := m.Upstream(ctx, f)
	if !ok {
		return nil
	}
	out, err := runGit(ctx, m.repo, "rev-list", base+"..refs/remotes/"+remote+"/"+rb)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for sha := range strings.FieldsSeq(out) {
		set[sha] = true
	}
	return set
}

// Refusals Rewrite and PlanRewrite return before any git mutation, each a
// distinct precondition so a caller can say what to do about it. The
// worktree preconditions Collapse has (ErrDirtyWorktree, ErrRebaseInProgress,
// ErrBaseNotAncestor, ErrNoCommitsBeyondBase) apply unchanged.
var (
	// ErrPlanMismatch marks a plan that does not cover the branch's
	// commits exactly, in order, once each — or names a commit the branch
	// no longer has. The page or terminal built it from a log that has
	// since moved.
	ErrPlanMismatch = errors.New("the plan does not match the branch's commits")
	// ErrAttribution marks a message carrying agent-authorship metadata,
	// which a rewrite must not put into history any more than the landing
	// message may.
	ErrAttribution = errors.New("a commit message carries agent attribution")
	// ErrPushedNotAcknowledged marks a plan that rewrites commits the
	// upstream already has, sent without the caller's yes to the force push
	// it will need.
	ErrPushedNotAcknowledged = errors.New("the plan rewrites commits already pushed")
)

// RewriteGroup is one commit of the rewritten branch: the run of
// existing commits it replaces, oldest first, and the message it carries.
// A one-commit group with an empty Message keeps that commit as it is.
type RewriteGroup struct {
	Commits []string
	Message string
}

// RewritePlan is the branch as it should read afterwards, oldest first:
// contiguous runs of the current commits, each becoming one commit. There
// is no reordering and no dropping — every commit belongs to exactly one
// group — so the branch's final tree cannot change, and neither can the
// verdict of a verify that already ran on it.
type RewritePlan struct {
	// Head is the tip the plan was built against; a branch that has moved
	// since is refused rather than rewritten from a stale view.
	Head   string
	Groups []RewriteGroup
}

// RewritePreview is what a plan would do, computed without touching git.
type RewritePreview struct {
	Entries []LogEntry
	// Changed is how many commits of the result differ from what the
	// branch has now (a reworded commit, a squashed run, and every commit
	// above the first of those, whose parent moves).
	Changed int
	// Pushed reports that a commit the upstream has would be replaced.
	Pushed bool
	// Noop reports a plan that leaves the branch exactly as it is.
	Noop bool
}

type rewriteResolved struct {
	wt      string
	branch  string
	current []LogEntry
	groups  []resolvedGroup
	// firstChange is the index of the first group that is not kept as is.
	firstChange int
}

type resolvedGroup struct {
	RewriteGroup
	first, last LogEntry
	message     string
	keep        bool // survives as the very same commit
	// files, add and del are what the group changes as one commit: a
	// file touched by several of its commits counts once
	files, add, del int
}

// resolve validates plan against the branch and every worktree
// precondition, without mutating anything.
func (m *Manager) resolveRewrite(ctx context.Context, f *domain.Feature, base string, plan RewritePlan) (*rewriteResolved, error) {
	wt, branch, err := m.featurePaths(f)
	if err != nil {
		return nil, err
	}
	if m.rebaseInProgress(ctx, wt) {
		return nil, fmt.Errorf("%s: %w", f.ID, ErrRebaseInProgress)
	}
	for _, head := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD"} {
		if inProgress, err := gitOK(ctx, wt, "rev-parse", "--verify", "--quiet", head); err != nil {
			return nil, err
		} else if inProgress {
			return nil, fmt.Errorf("%s: %w", f.ID, ErrRebaseInProgress)
		}
	}
	if dirty, err := worktreeDirty(ctx, wt); err != nil {
		return nil, err
	} else if dirty {
		return nil, fmt.Errorf("%s: %w", f.ID, ErrDirtyWorktree)
	}
	if ancestor, err := gitOK(ctx, wt, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return nil, err
	} else if !ancestor {
		return nil, fmt.Errorf("%s: %w", f.ID, ErrBaseNotAncestor)
	}
	current, err := m.logRange(ctx, wt, f, base, "HEAD")
	if err != nil {
		return nil, err
	}
	if len(current) == 0 {
		return nil, fmt.Errorf("%s: %w", f.ID, ErrNoCommitsBeyondBase)
	}
	if plan.Head != "" && plan.Head != current[len(current)-1].SHA {
		return nil, fmt.Errorf("%s: %w: the branch has moved since the plan was made", f.ID, ErrPlanMismatch)
	}

	byPrefix := func(ref string) (int, bool) {
		if len(ref) < 7 {
			return 0, false
		}
		idx := -1
		for i, e := range current {
			if strings.HasPrefix(e.SHA, ref) {
				if idx >= 0 {
					return 0, false // ambiguous
				}
				idx = i
			}
		}
		return idx, idx >= 0
	}
	r := &rewriteResolved{wt: wt, branch: branch, current: current, firstChange: -1}
	next := 0
	for gi, g := range plan.Groups {
		if len(g.Commits) == 0 {
			return nil, fmt.Errorf("%s: %w: group %d has no commits", f.ID, ErrPlanMismatch, gi+1)
		}
		rg := resolvedGroup{RewriteGroup: g}
		for _, ref := range g.Commits {
			i, ok := byPrefix(ref)
			if !ok || i != next {
				return nil, fmt.Errorf("%s: %w: commit %q is not next in the branch's order", f.ID, ErrPlanMismatch, ref)
			}
			next++
		}
		rg.first = current[next-len(g.Commits)]
		rg.last = current[next-1]
		rg.message = strings.TrimSpace(g.Message)
		if rg.message == "" && len(g.Commits) > 1 {
			return nil, fmt.Errorf("%s: %w: squashed group %d needs a message", f.ID, ErrPlanMismatch, gi+1)
		}
		if rg.message == "" {
			rg.message = rg.first.Message()
		}
		if hit := MatchesAttribution(rg.message); hit != "" {
			return nil, fmt.Errorf("%s: %w: %q", f.ID, ErrAttribution, hit)
		}
		rg.keep = len(g.Commits) == 1 && strings.TrimSpace(rg.message) == strings.TrimSpace(rg.first.Message()) &&
			(r.firstChange < 0)
		if !rg.keep && r.firstChange < 0 {
			r.firstChange = gi
		}
		rg.files, rg.add, rg.del = rg.first.Files, rg.first.Add, rg.first.Del
		if len(g.Commits) > 1 {
			if rg.files, rg.add, rg.del, err = numstat(ctx, wt, rg.first.SHA+"^", rg.last.SHA); err != nil {
				return nil, err
			}
		}
		r.groups = append(r.groups, rg)
	}
	if next != len(current) {
		return nil, fmt.Errorf("%s: %w: %d of %d commits are covered", f.ID, ErrPlanMismatch, next, len(current))
	}
	return r, nil
}

// PlanRewrite says what plan would do to the card's branch and refuses it
// for the reasons Rewrite would, touching nothing.
func (m *Manager) PlanRewrite(ctx context.Context, f *domain.Feature, base string, plan RewritePlan) (RewritePreview, error) {
	r, err := m.resolveRewrite(ctx, f, base, plan)
	if err != nil {
		return RewritePreview{}, err
	}
	return r.preview(), nil
}

func (r *rewriteResolved) preview() RewritePreview {
	p := RewritePreview{Noop: r.firstChange < 0}
	for gi, g := range r.groups {
		e := LogEntry{
			SHA: g.last.SHA, Short: g.last.Short, Author: g.first.Author, At: g.first.At,
		}
		e.Subject, e.Body, _ = strings.Cut(g.message, "\n")
		e.Body = strings.TrimSpace(e.Body)
		e.Files, e.Add, e.Del = g.files, g.add, g.del
		for _, c := range g.Commits {
			for _, cur := range r.current {
				if strings.HasPrefix(cur.SHA, c) {
					e.Pushed = e.Pushed || cur.Pushed
				}
			}
		}
		if r.firstChange >= 0 && gi >= r.firstChange {
			p.Changed++
			// a rewritten commit replaces whatever it was made from; if any
			// of those was pushed, the remote has a commit this branch no
			// longer does
			if e.Pushed {
				p.Pushed = true
			}
		}
		p.Entries = append(p.Entries, e)
	}
	return p
}

// Rewrite replaces the card's own commits with plan's groups, in place.
//
// Every result commit is built with commit-tree from the tree of the last
// commit of the run it replaces, on top of the result commit before it. So
// the branch's final tree is the old one by construction, nothing can
// conflict, the worktree is never checked out from, and no hook runs.
// Author identity and date are kept from the first commit of each run;
// the committer is whoever is running gummi, now.
//
// It returns the new tip, or "" when the plan changes nothing. A plan that
// replaces a pushed commit is refused unless acknowledgePushed is set, and
// the caller owes the reader the force push (engine.PushCommandFor): gummi never pushes.
func (m *Manager) Rewrite(ctx context.Context, f *domain.Feature, base string, plan RewritePlan, acknowledgePushed bool) (string, error) {
	r, err := m.resolveRewrite(ctx, f, base, plan)
	if err != nil {
		return "", err
	}
	if r.firstChange < 0 {
		return "", nil
	}
	if r.preview().Pushed && !acknowledgePushed {
		return "", fmt.Errorf("%s: %w — the remote will need a force push, which gummi does not run", f.ID, ErrPushedNotAcknowledged)
	}

	preSHA := r.current[len(r.current)-1].SHA
	preTree, err := runGit(ctx, r.wt, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	parent, err := runGit(ctx, r.wt, "rev-parse", base+"^{commit}")
	if err != nil {
		return "", err
	}
	for gi, g := range r.groups {
		if gi < r.firstChange {
			parent = g.last.SHA // untouched: the same commit, the same parent
			continue
		}
		tree, err := runGit(ctx, r.wt, "rev-parse", g.last.SHA+"^{tree}")
		if err != nil {
			return "", err
		}
		sha, err := m.commitTree(ctx, r.wt, tree, parent, g)
		if err != nil {
			return "", err
		}
		parent = sha
	}
	newTree, err := runGit(ctx, r.wt, "rev-parse", parent+"^{tree}")
	if err != nil {
		return "", err
	}
	if newTree != preTree {
		// unreachable by construction; asserted anyway, like Collapse
		return "", &CollapseInvariantError{BeforeSHA: preSHA, BeforeTree: preTree, AfterTree: newTree}
	}
	// The index and the files already match: only the branch moves. The
	// old tip is the expected value, so a commit made in the meantime is
	// not overwritten.
	if _, err := runGit(ctx, r.wt, "update-ref", "-m", "gummi: rewrite history", "refs/heads/"+r.branch, parent, preSHA); err != nil {
		return "", err
	}
	return parent, nil
}

// commitTree writes one result commit, keeping the author of the first
// commit of the run it replaces.
func (m *Manager) commitTree(ctx context.Context, wt, tree, parent string, g resolvedGroup) (string, error) {
	meta, err := runGit(ctx, wt, "log", "-1", "--format=%an%x1f%ae%x1f%aI", g.first.SHA)
	if err != nil {
		return "", err
	}
	fields := strings.Split(meta, "\x1f")
	if len(fields) != 3 {
		return "", fmt.Errorf("reading the author of %s", g.first.Short)
	}
	// message via a file-less path: commit-tree reads -m as given, and
	// --cleanup does not apply, so trailing newline is ours to add
	args := []string{"commit-tree", tree, "-p", parent, "-m", g.message}
	// commit-tree is the one commit git does not sign on commit.gpgsign
	// alone, so a rewrite would otherwise strip the signatures it replaces
	if on, _ := runGit(ctx, wt, "config", "--type=bool", "--get", "commit.gpgsign"); on == "true" {
		args = append(args, "-S")
	}
	return runGitEnv(ctx, wt, []string{
		"GIT_AUTHOR_NAME=" + fields[0],
		"GIT_AUTHOR_EMAIL=" + fields[1],
		"GIT_AUTHOR_DATE=" + fields[2],
	}, args...)
}

// CommitDiff is one of the card's own commits as a patch: what that
// commit changed, against its parent. sha must be one of Log's commits
// (a prefix of at least seven characters is enough), so the card's log is
// as far as this reads.
func (m *Manager) CommitDiff(ctx context.Context, f *domain.Feature, base, sha string) (string, error) {
	entries, err := m.Log(ctx, f, base)
	if err != nil {
		return "", err
	}
	if len(sha) < 7 {
		return "", fmt.Errorf("%s: %w: %q", f.ID, ErrPlanMismatch, sha)
	}
	full := ""
	for _, e := range entries {
		if strings.HasPrefix(e.SHA, sha) {
			full = e.SHA
			break
		}
	}
	if full == "" {
		return "", fmt.Errorf("%s: %w: %q is not on the branch", f.ID, ErrPlanMismatch, sha)
	}
	return runGitRaw(ctx, m.repo, "show", "--format=", "--no-color", "--no-ext-diff", "--patch", full)
}

// numstat is what from..to changes: files, lines added, lines removed.
func numstat(ctx context.Context, dir, from, to string) (files, add, del int, err error) {
	out, err := runGit(ctx, dir, "diff", "--numstat", from, to)
	if err != nil {
		return 0, 0, 0, err
	}
	files, add, del = sumNumstat(out)
	return files, add, del, nil
}

// sumNumstat totals `--numstat` output. A binary file ("-" for both
// counts) counts as a file and adds no lines.
func sumNumstat(out string) (files, add, del int) {
	for line := range strings.Lines(out) {
		cols := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(cols) < 3 {
			continue
		}
		files++
		if n, err := strconv.Atoi(cols[0]); err == nil {
			add += n
		}
		if n, err := strconv.Atoi(cols[1]); err == nil {
			del += n
		}
	}
	return files, add, del
}
