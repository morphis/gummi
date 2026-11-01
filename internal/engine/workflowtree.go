package engine

// A workflow run and the session's tree (DESIGN §25.6). Steps work in the
// session's own checkout, so gummi records the tree as a snapshot at the
// run's start and at each step's start and end; the diffs come from those.
// This file is that half of a run: the run's directory in the workspace,
// the snapshots and the diff files written into it, the done-when checks
// at a run's end, and a person's Revert run.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflowrecipe"
	"github.com/morphis/gummi/internal/worktree"
)

// RunDir is a run's directory, .gummi/runs/<ID>/<n>/: the recipe copy, the
// artifacts, the diff files and each step's output. It is in the
// workspace, not the tree, so it is never in the session's diff.
func (e *Engine) RunDir(session domain.FeatureID, n int) string {
	return filepath.Join(e.pool.Root(), ".gummi", "runs", string(session), strconv.Itoa(n))
}

// writeRunFile writes rel under the run's directory, making what it needs.
func (e *Engine) writeRunFile(session domain.FeatureID, n int, rel, content string) (string, error) {
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%q is not a path inside the run's directory", rel)
	}
	path := filepath.Join(e.RunDir(session, n), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- rel is checked local above, joined under the run's own directory
		return "", err
	}
	return path, atomicfile.Write(path, []byte(content), 0o600)
}

// stepFile is where a step's file of a kind (outputs, diffs) goes in the
// run's directory: build/2/implement → outputs/build/2/implement.md.
func stepFile(kind, path, ext string) string {
	return filepath.Join(kind, filepath.FromSlash(path)+ext)
}

// sessionTree is the session's checkout: the directory its steps work in.
func (e *Engine) sessionTree(ctx context.Context, session domain.FeatureID) (string, error) {
	sess, err := e.cfg.Store.GetFeature(ctx, session)
	if err != nil {
		return "", err
	}
	dir, _, err := e.locateFor(ctx, sess, true)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", fmt.Errorf("%s has no tree to run in", session)
	}
	return dir, nil
}

// runTree is one tree a run spans: the session's own (Repo empty) or an
// attached repository's.
type runTree struct {
	Repo string
	Dir  string
}

// runTrees lists the trees a run spans: the session's checkout first, then
// each attached repository whose worktree is on disk.
func (e *Engine) runTrees(ctx context.Context, session domain.FeatureID) ([]runTree, error) {
	dir, err := e.sessionTree(ctx, session)
	if err != nil {
		return nil, err
	}
	out := []runTree{{Dir: dir}}
	f, err := e.cfg.Store.GetFeature(ctx, session)
	if err != nil {
		return out, nil //nolint:nilerr // the primary tree alone still works
	}
	for _, t := range e.attachedTrees(ctx, f) {
		if _, err := os.Stat(t.Path); err == nil {
			out = append(out, runTree{Repo: t.Att.Repo, Dir: t.Path})
		}
	}
	return out, nil
}

// snapshotTreesWith snapshots every tree the session spans, keeping out
// what keepOf names for each, and joins them (domain.EncodeTrees).
func (e *Engine) snapshotTreesWith(ctx context.Context, session domain.FeatureID, keepOf func(runTree) ([]string, error)) (string, error) {
	trees, err := e.runTrees(ctx, session)
	if err != nil {
		return "", err
	}
	var primary string
	attached := map[string]string{}
	for _, t := range trees {
		keep, err := keepOf(t)
		if err != nil {
			return "", err
		}
		h, err := worktree.Snapshot(ctx, t.Dir, keep...)
		if err != nil {
			return "", err
		}
		if t.Repo == "" {
			primary = h
		} else {
			attached[t.Repo] = h
		}
	}
	return domain.EncodeTrees(primary, attached), nil
}

// snapshotRun records the session's trees as they are now, as run n reads
// them: without the ignored paths that were on disk when the run started.
func (e *Engine) snapshotRun(ctx context.Context, session domain.FeatureID, n int) (string, error) {
	return e.snapshotTreesWith(ctx, session, func(t runTree) ([]string, error) {
		return e.runKeepOutFor(session, n, t.Repo), nil
	})
}

// treeDiff is the difference in one tree between two snapshots.
type treeDiff struct {
	runTree
	Patch string
	Paths []string
}

// diffEach diffs every tree the two snapshots both hold. A tree one of
// them lacks (a repository attached after the run started) is left out.
func (e *Engine) diffEach(ctx context.Context, session domain.FeatureID, a, b string) ([]treeDiff, error) {
	return e.diffEachWith(ctx, session, a, b, false)
}

// diffEachWith is diffEach; pathsOnly leaves every Patch empty and never
// builds one.
func (e *Engine) diffEachWith(ctx context.Context, session domain.FeatureID, a, b string, pathsOnly bool) ([]treeDiff, error) {
	trees, err := e.runTrees(ctx, session)
	if err != nil {
		return nil, err
	}
	pa, aa := domain.DecodeTrees(a)
	pb, ab := domain.DecodeTrees(b)
	var out []treeDiff
	for _, t := range trees {
		from, to := pa, pb
		if t.Repo != "" {
			var okA, okB bool
			from, okA = aa[t.Repo]
			to, okB = ab[t.Repo]
			if !okA || !okB {
				continue
			}
		}
		var patch string
		var paths []string
		var err error
		if pathsOnly {
			paths, err = worktree.DiffPaths(ctx, t.Dir, from, to)
		} else {
			patch, paths, err = worktree.DiffTrees(ctx, t.Dir, from, to)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, treeDiff{runTree: t, Patch: patch, Paths: paths})
	}
	return out, nil
}

// diffTrees is the diff between two snapshots across every tree: the
// primary's patch as it always was, then each attached repository's under a
// "# repo: <name>" line, and every path that differs, an attached one as
// "<repo>:<path>".
func (e *Engine) diffTrees(ctx context.Context, session domain.FeatureID, a, b string) (string, []string, error) {
	each, err := e.diffEach(ctx, session, a, b)
	if err != nil {
		return "", nil, err
	}
	var patch strings.Builder
	var paths []string
	for _, d := range each {
		if d.Repo == "" {
			patch.WriteString(d.Patch)
			paths = append(paths, d.Paths...)
			continue
		}
		if strings.TrimSpace(d.Patch) != "" {
			fmt.Fprintf(&patch, "# repo: %s\n%s", d.Repo, d.Patch)
		}
		for _, p := range d.Paths {
			paths = append(paths, d.Repo+":"+p)
		}
	}
	return patch.String(), paths, nil
}

// diffPaths is diffTrees' paths alone: no patch is built, for a reader
// that only names the files two snapshots differ in.
func (e *Engine) diffPaths(ctx context.Context, session domain.FeatureID, a, b string) ([]string, error) {
	each, err := e.diffEachWith(ctx, session, a, b, true)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, d := range each {
		for _, p := range d.Paths {
			if d.Repo != "" {
				p = d.Repo + ":" + p
			}
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// runIgnoredFile is the file in a run's directory that lists the ignored
// paths on disk when the run started (worktree.Ignored), NUL-separated.
const runIgnoredFile = "ignored"

// recordRunIgnored lists the ignored paths in each tree the session spans
// now as run n's: they stay out of every snapshot the run takes, so an edit
// of .gitignore under the run neither shows their contents in its diff nor
// lets a revert delete them. The primary's go in the file "ignored", an
// attached repository's in "ignored@<repo>".
func (e *Engine) recordRunIgnored(ctx context.Context, session domain.FeatureID, n int) error {
	trees, err := e.runTrees(ctx, session)
	if err != nil {
		return err
	}
	for _, t := range trees {
		paths, err := worktree.Ignored(ctx, t.Dir)
		if err != nil {
			return err
		}
		if _, err := e.writeRunFile(session, n, ignoredFileOf(t.Repo), strings.Join(paths, "\x00")); err != nil {
			return err
		}
	}
	return nil
}

func ignoredFileOf(repo string) string {
	if repo == "" {
		return runIgnoredFile
	}
	return runIgnoredFile + "@" + repo
}

// runKeepOutFor is runKeepOut for the tree of an attached repository
// (the primary's when repo is empty).
func (e *Engine) runKeepOutFor(session domain.FeatureID, n int, repo string) []string {
	raw, err := os.ReadFile(filepath.Join(e.RunDir(session, n), ignoredFileOf(repo))) // #nosec G304 -- the run's own directory
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(string(raw), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// writeRunDiff writes the diff from the run's starting tree to now (the
// tree snapshot) as the file a step's {{run.diff}} names, and returns
// its path.
func (e *Engine) writeRunDiff(ctx context.Context, run *domain.Run, stepPath, now string) (string, error) {
	patch, _, err := e.diffTrees(ctx, run.Session, run.StartTree, now)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(patch) == "" {
		patch = "(the run has changed nothing in the tree yet)\n"
	}
	return e.writeRunFile(run.Session, run.N, stepFile("diffs", stepPath, ".diff"), patch)
}

// StepDiff is a step's own diff, from its start snapshot to its end one
// (to now while it runs), and the paths it touched.
func (e *Engine) StepDiff(ctx context.Context, st domain.RunStep) (string, []string, error) {
	if st.StartTree == "" {
		return "", nil, nil
	}
	end := st.EndTree
	if end == "" {
		var err error
		if end, err = e.snapshotRun(ctx, st.Session, st.Run); err != nil {
			return "", nil, err
		}
	}
	return e.diffTreesCached(ctx, st.Session, st.StartTree, end, st.EndTree != "")
}

// diffTreesCached is diffTrees, kept for two snapshots that are settled
// (keep): a tree hash names its content, so their diff never changes, and
// a face that reads it again on every refresh need not run git each time.
// A snapshot spanning attached repositories names each tree's hash, so it
// keys the cache just as well.
func (e *Engine) diffTreesCached(ctx context.Context, session domain.FeatureID, a, b string, keep bool) (string, []string, error) {
	if !keep {
		return e.diffTrees(ctx, session, a, b)
	}
	key := a + ".." + b
	rt := e.wfRT()
	rt.mu.Lock()
	hit, ok := rt.diffs[key]
	failed := time.Now().Before(rt.retryAfter["diff/"+key])
	rt.mu.Unlock()
	if ok {
		return hit.patch, hit.paths, nil
	}
	if failed {
		return "", nil, errors.New("the diff could not be read a moment ago: not tried again yet")
	}
	patch, paths, err := e.diffTrees(ctx, session, a, b)
	if err != nil {
		// a diff git cannot read (an object gone) is not asked again on
		// every refresh of a face
		rt.mu.Lock()
		rt.retryAfter["diff/"+key] = time.Now().Add(10 * time.Second)
		rt.mu.Unlock()
		return "", nil, err
	}
	rt.mu.Lock()
	if len(patch) <= maxCachedDiffBytes {
		if len(rt.diffs) >= maxCachedDiffs || rt.diffBytes+len(patch) > maxCachedDiffBytes {
			clear(rt.diffs)
			rt.diffBytes = 0
		}
		rt.diffs[key] = cachedDiff{patch: patch, paths: paths}
		rt.diffBytes += len(patch)
	}
	rt.mu.Unlock()
	return patch, paths, nil
}

// diffPathsCached is diffPaths for two settled snapshots: a patch cached
// under the same pair already holds the paths, and a paths-only read is
// kept apart so it never pays for a patch (and holds none in memory).
func (e *Engine) diffPathsCached(ctx context.Context, session domain.FeatureID, a, b string) ([]string, error) {
	key := a + ".." + b
	rt := e.wfRT()
	rt.mu.Lock()
	if hit, ok := rt.diffs[key]; ok {
		rt.mu.Unlock()
		return hit.paths, nil
	}
	if hit, ok := rt.diffPathsOnly[key]; ok {
		rt.mu.Unlock()
		return hit, nil
	}
	rt.mu.Unlock()
	paths, err := e.diffPaths(ctx, session, a, b)
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	if len(rt.diffPathsOnly) >= maxCachedDiffs*16 {
		clear(rt.diffPathsOnly)
	}
	rt.diffPathsOnly[key] = paths
	rt.mu.Unlock()
	return paths, nil
}

// maxCachedDiffs bounds diffTreesCached's memory: past it, the cache starts
// again.
const maxCachedDiffs = 64

// maxCachedDiffBytes bounds the patches diffTreesCached holds, together:
// past it the cache starts again, and a patch larger than it is not kept.
const maxCachedDiffBytes = 32 << 20

type cachedDiff struct {
	patch string
	paths []string
}

// RunDiff is a run's diff, from its starting tree to a fresh snapshot of
// the session's tree, and the paths that differ.
func (e *Engine) RunDiff(ctx context.Context, session domain.FeatureID, n int) (string, []string, error) {
	run, err := e.cfg.Store.Run(ctx, session, n)
	if err != nil {
		return "", nil, err
	}
	if run == nil || run.StartTree == "" {
		return "", nil, fmt.Errorf("run %d has not started", n)
	}
	now, err := e.snapshotRun(ctx, session, n)
	if err != nil {
		return "", nil, err
	}
	return e.diffTrees(ctx, session, run.StartTree, now)
}

// --- the done-when checks -------------------------------------------------

// frozenSpec is the spec revision a run froze, less any earlier run's
// Report section, and its done-when items.
func (e *Engine) frozenSpec(ctx context.Context, run *domain.Run) (string, []domain.DoneWhen) {
	rev, ok, err := e.cfg.Store.SpecRevision(ctx, run.Session, run.Spec, run.SpecRev)
	if err != nil || !ok {
		return "", nil
	}
	items, _, _ := spec.ParseDoneWhen(rev.Content)
	// an earlier run's report stays in the stored spec for the person, but
	// never reaches a step: its decisions, notes and verdicts are that
	// run's, and the spec is all a step is told of the work
	return spec.DropSection(rev.Content, specSectionReport), items
}

// doneWhenCheckTimeout bounds each done-when check: one that hangs (a test
// in watch mode, a server that never exits) would otherwise hold the run's
// baseline, its loop or its end for good, and no stop could free it.
const doneWhenCheckTimeout = 15 * time.Minute

// doneWhenChecks runs each item's check command in dir, keyed by item, each
// bounded by doneWhenCheckTimeout.
func doneWhenChecks(ctx context.Context, dir string, items []domain.DoneWhen) map[string]verify.Result {
	var checks []domain.Check
	for _, it := range items {
		if strings.TrimSpace(it.Check) != "" {
			checks = append(checks, domain.Check{Name: it.ID, Cmd: it.Check})
		}
	}
	out := map[string]verify.Result{}
	if len(checks) == 0 || dir == "" {
		return out
	}
	for _, r := range verify.RunBounded(ctx, dir, checks, doneWhenCheckTimeout) {
		out[r.Name] = r
	}
	return out
}

// itemResults judges each done-when item: by its check where it has one,
// else by the last step's MET:/UNMET: line, else unjudged. A check that
// passed but ran no tests (verify.Result.RanNoTests) leaves its item
// unjudged: it proves nothing, so it is no ✓.
func itemResults(items []domain.DoneWhen, checks map[string]verify.Result, lastOutput string) []domain.ItemResult {
	lines := workflowrecipe.ParseItemLines(lastOutput)
	var out []domain.ItemResult
	for _, it := range items {
		r := domain.ItemResult{Item: it.ID, Verdict: domain.ItemUnjudged}
		if c, has := checks[it.ID]; has {
			r.Checked = true
			r.Verdict = domain.ItemUnmet
			switch {
			case c.Status == verify.StatusTimeout:
				r.Verdict, r.Detail = domain.ItemUnjudged, fmt.Sprintf("the check did not finish (it is stopped at %s, or when the run is)", doneWhenCheckTimeout)
			case c.Status == verify.StatusNotRun:
				r.Verdict, r.Detail = domain.ItemUnjudged, "the check never ran"
			case c.OK && c.RanNoTests:
				r.Verdict, r.Detail = domain.ItemUnjudged, ranNoTests
			case c.OK:
				r.Verdict = domain.ItemMet
			default:
				r.Detail = strings.TrimSpace(tail(c.Output, 600))
			}
		} else if l, has := lines[it.ID]; has {
			r.Verdict, r.Detail = domain.ItemUnmet, l.Why
			if l.Met {
				r.Verdict = domain.ItemMet
			}
		}
		out = append(out, r)
	}
	return out
}

// ranNoTests is a vacuous check's detail.
const ranNoTests = "the check ran no tests"

// checkedPass reports whether every checked result passed. A checked item
// left unjudged — its check ran no tests — did not pass.
func checkedPass(rs []domain.ItemResult) bool {
	for _, r := range rs {
		if r.Checked && r.Verdict != domain.ItemMet {
			return false
		}
	}
	return true
}

func verdictMark(v domain.ItemVerdict) string {
	switch v {
	case domain.ItemMet:
		return "✓"
	case domain.ItemUnmet:
		return "✗"
	}
	return "–"
}

// --- Revert run ------------------------------------------------------------

// RevertPreview is what Revert run would put back: every path that differs
// between the run's starting tree and the session's tree now.
type RevertPreview struct {
	Run   int
	Paths []string
	// Tree is the snapshot of the session's tree the preview was read
	// from: the revert it is confirmed for names it, and is refused when
	// the tree has changed since (WorkflowRevert).
	Tree string
	// Note says what reverting also undoes.
	Note string
	// Step is the step it reverts to before, "" for the run's start.
	Step string
}

// WorkflowRevertPreview lists what reverting run n would put back.
func (e *Engine) WorkflowRevertPreview(ctx context.Context, session domain.FeatureID, n int) (RevertPreview, error) {
	return e.WorkflowRevertPreviewTo(ctx, session, n, "")
}

// revertBase is the tree a revert of run n puts back: where the run
// started, or with step the path of one of its steps, where that step
// started — the snapshot gummi took at its boundary.
func (e *Engine) revertBase(ctx context.Context, run *domain.Run, step string) (string, string, error) {
	if step == "" {
		return run.StartTree, "when the run started", nil
	}
	st, err := e.cfg.Store.RunStep(ctx, run.Session, run.N, step)
	if err != nil {
		return "", "", err
	}
	if st == nil || st.StartTree == "" {
		return "", "", fmt.Errorf("step %s of run %d has not started", step, run.N)
	}
	return st.StartTree, "before step " + step + " started", nil
}

// WorkflowRevertPreviewTo lists what reverting run n would put back: to
// where it started, or with step to where that step started.
func (e *Engine) WorkflowRevertPreviewTo(ctx context.Context, session domain.FeatureID, n int, step string) (RevertPreview, error) {
	run, err := e.cfg.Store.Run(ctx, session, n)
	if err != nil {
		return RevertPreview{}, err
	}
	if run == nil || run.StartTree == "" {
		return RevertPreview{}, fmt.Errorf("run %d has not started", n)
	}
	base, when, err := e.revertBase(ctx, run, step)
	if err != nil {
		return RevertPreview{}, err
	}
	now, err := e.snapshotRun(ctx, session, n)
	if err != nil {
		return RevertPreview{}, err
	}
	paths, err := e.diffPaths(ctx, session, base, now)
	if err != nil {
		return RevertPreview{}, err
	}
	dir, err := e.sessionTree(ctx, session)
	if err != nil {
		return RevertPreview{}, err
	}
	note := "Each path goes back to how it was when the run started, staged as it was then, and a path the run created is deleted. "
	if step != "" {
		note = "Each path goes back to how it was " + when + ", staged as it was when the run started, and a path made since is deleted. "
	}
	note += "This also undoes anything the session or a person changed in these paths since then."
	if since := e.sinceRun(ctx, dir, run); since != "" {
		note += " " + since
	}
	return RevertPreview{Run: n, Step: step, Paths: paths, Tree: now, Note: note}, nil
}

// sinceRun says what has happened in the tree since run started that a
// revert of it would undo too: the runs after it and the commits made
// since, "" when nothing has.
func (e *Engine) sinceRun(ctx context.Context, dir string, run *domain.Run) string {
	var parts []string
	if runs, err := e.cfg.Store.SessionRuns(ctx, run.Session); err == nil {
		var later []string
		for _, r := range runs {
			if r.N > run.N && r.StartTree != "" {
				later = append(later, strconv.Itoa(r.N))
			}
		}
		switch len(later) {
		case 0:
		case 1:
			parts = append(parts, "run "+later[0]+", which ran after it")
		default:
			parts = append(parts, "runs "+strings.Join(later, ", ")+", which ran after it")
		}
	}
	if run.StartHead != "" {
		if out, err := objectiveGit(ctx, dir, "rev-list", "--count", run.StartHead+"..HEAD"); err == nil {
			if c, _ := strconv.Atoi(strings.TrimSpace(out)); c > 0 {
				word := "commits"
				if c == 1 {
					word = "commit"
				}
				parts = append(parts, fmt.Sprintf("%d %s made since it started (the branch keeps them; the revert undoes their changes in the tree)", c, word))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "It undoes " + strings.Join(parts, ", and ") + "."
}

// WorkflowTreeBusy says why a workflow run is still writing the session's
// tree, "" when none is: its open run has a step under way (running,
// asking or paused mid-step), or a step's agent of any of its runs is
// mid-turn — a follow-up a person sent an ended step. A person's own
// writes to the tree (the Tree tab, Revert run) wait for it.
func (e *Engine) WorkflowTreeBusy(ctx context.Context, session domain.FeatureID) string {
	st := e.cfg.Store
	if st == nil {
		return ""
	}
	runs, err := st.SessionRuns(ctx, session)
	if err != nil {
		return ""
	}
	for _, r := range runs {
		steps, err := st.RunSteps(ctx, session, r.N)
		if err != nil {
			continue
		}
		for _, s := range steps {
			switch {
			case r.State == domain.RunPaused && s.State.Live():
				return fmt.Sprintf("step %s of workflow run %d is paused mid-step in this tree", s.Path, r.N)
			case r.State.Open() && s.State.Live():
				return fmt.Sprintf("step %s of workflow run %d is running in this tree", s.Path, r.N)
			case s.Card != "" && e.freeformBusy(s.Card):
				return fmt.Sprintf("step %s of workflow run %d is running in this tree, answering a message", s.Path, r.N)
			}
		}
	}
	return ""
}

// WorkflowRevert puts the session's tree back to run n's starting tree:
// each path that differs is restored, and one the run created is deleted;
// the index entries of those paths go back to how they were when the run
// started (a run from before that recorded no index leaves it alone). The
// branch is left alone. seen is the preview's Tree: a
// revert is confirmed against what its preview listed, so it is refused
// when the tree has changed since. Refused while a step, or the session's
// own agent, is working in the tree.
func (e *Engine) WorkflowRevert(ctx context.Context, session domain.FeatureID, n int, seen string) ([]string, error) {
	return e.WorkflowRevertTo(ctx, session, n, "", seen)
}

// WorkflowRevertTo is WorkflowRevert to where step started, or with step
// "" to where the run did: a person undoing the run from one of its steps
// on, keeping what the steps before it did.
func (e *Engine) WorkflowRevertTo(ctx context.Context, session domain.FeatureID, n int, step, seen string) ([]string, error) {
	st := e.cfg.Store
	if st == nil {
		return nil, errors.New("workflows need the store")
	}
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := st.Run(ctx, session, n)
	if err != nil {
		return nil, err
	}
	if run == nil || run.StartTree == "" {
		return nil, fmt.Errorf("run %d has not started", n)
	}
	if why := e.WorkflowTreeBusy(ctx, session); why != "" {
		return nil, fmt.Errorf("%s: revert once it has stopped", why)
	}
	if run.State == domain.RunRunning {
		return nil, fmt.Errorf("run %d is running: pause or stop it before reverting it", n)
	}
	if phase := e.RunPhase(session, n); phase != "" {
		return nil, fmt.Errorf("gummi is running run %d's checks in this tree: revert once they are done", n)
	}
	if e.freeformBusy(session) {
		return nil, errors.New("the session's agent is working in this tree: revert once its turn ends")
	}
	if seen == "" {
		return nil, errors.New("a revert is confirmed against its preview: read what it would put back first")
	}
	dir, err := e.sessionTree(ctx, session)
	if err != nil {
		return nil, err
	}
	base, when, err := e.revertBase(ctx, run, step)
	if err != nil {
		return nil, err
	}
	now, err := e.snapshotRun(ctx, session, n)
	if err != nil {
		return nil, err
	}
	if now != seen {
		return nil, errors.New("the tree has changed since the revert was previewed: read what it would put back again")
	}
	// Each tree goes back to its own snapshot at base. A tree the run did
	// not start with (a repository attached under it) is left alone: the run
	// cannot prove what it made there.
	each, err := e.diffEach(ctx, session, base, now)
	if err != nil {
		return nil, err
	}
	basePrimary, baseAttached := domain.DecodeTrees(base)
	var paths, primaryPaths []string
	for _, d := range each {
		start := basePrimary
		if d.Repo != "" {
			start = baseAttached[d.Repo]
		}
		if err := worktree.RevertTo(ctx, d.Dir, start, d.Paths, e.runKeepOutFor(session, n, d.Repo)...); err != nil {
			return nil, err
		}
		for _, p := range d.Paths {
			if d.Repo != "" {
				p = d.Repo + ":" + p
			} else {
				primaryPaths = append(primaryPaths, p)
			}
			paths = append(paths, p)
		}
	}
	// what the run (or anyone since) staged of these paths goes back too,
	// or the next commit would bring the reverted work back
	// (the index kept at the start is the primary tree's)
	if err := worktree.RestoreIndex(ctx, dir, run.StartIndex, primaryPaths); err != nil {
		return nil, fmt.Errorf("the files are put back, but not the index: %w", err)
	}
	run.RevertedTo = step
	if step == "" {
		run.RevertedTo = domain.RevertedToStart
	}
	if err := st.UpdateRun(ctx, run); err != nil {
		slog.Warn("recording a run's revert", "session", session, "run", n, "err", err)
	}
	e.runNote(session, fmt.Sprintf("run %d reverted to %s: %d path%s put back", n, when, len(paths), plural(len(paths))))
	e.workflowEvent(session)
	return paths, nil
}

// freeformBusy reports whether a card's session is mid-turn.
func (e *Engine) freeformBusy(id domain.FeatureID) bool {
	ff := e.Freeform(id)
	return ff != nil && ff.Busy()
}
