package engine

// A run's baseline (DESIGN §25.5): the spec's done-when checks, run in the
// session's tree before the run's first step. A check that already passes
// before the run, or a go test whose -run matches no test, proves nothing
// about what the run did, and nobody could tell from its end result alone.
// gummi runs the checks when the run is proposed, and again before its
// first step when the tree (or the spec) has moved since, records each
// checked item's result, tells the session what proves nothing, and shows
// the person on the proposal. Evidence, never a gate (decision 28): no
// proposal is refused and no run waits on a person over what a baseline
// finds; a run's first step waits only for the checks to have run.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

func baselineKey(session domain.FeatureID, n int) string {
	return fmt.Sprintf("baseline/%s/%d", session, n)
}

// baselineSpec is the spec revision a baseline of run reads and its
// done-when items: the revision the run froze, or, before it starts, the
// spec as it is now.
func (e *Engine) baselineSpec(ctx context.Context, run *domain.Run) (int, []domain.DoneWhen, error) {
	if run.SpecRev > 0 {
		_, items := e.frozenSpec(ctx, run)
		return run.SpecRev, items, nil
	}
	sess, err := e.cfg.Store.GetFeature(ctx, run.Session)
	if err != nil {
		return 0, nil, err
	}
	text, ok := e.SessionSpec(sess, run.Spec)
	if !ok {
		return 0, nil, fmt.Errorf("spec %d is gone", run.Spec)
	}
	rev := 0
	if revs, _ := e.cfg.Store.SpecRevisions(ctx, run.Session, run.Spec); len(revs) > 0 {
		rev = revs[0].Rev
	}
	items, _, _ := spec.ParseDoneWhen(text)
	return rev, items, nil
}

func hasChecks(items []domain.DoneWhen) bool {
	for _, it := range items {
		if strings.TrimSpace(it.Check) != "" {
			return true
		}
	}
	return false
}

// baselineCurrent reports whether a started run's baseline is the one its
// first step may start on: done, of the revision the run froze, and taken
// in the tree the run started in — or taken once the run had started, so a
// tree that moves under it never has the checks run round again.
func baselineCurrent(run *domain.Run) bool {
	b := run.Baseline
	return b != nil && !b.Running && b.SpecRev == run.SpecRev && (b.Tree == run.StartTree || b.Started)
}

// askBaselineLocked asks for run's baseline. A spec with no checks has
// one at once, and askBaselineLocked reports true; otherwise the checks
// run in the background, unless they already are, and its EventWorkflow
// ticks the run on. The caller holds the run lock.
func (e *Engine) askBaselineLocked(ctx context.Context, run *domain.Run) (bool, error) {
	rev, items, err := e.baselineSpec(ctx, run)
	if err != nil || !hasChecks(items) {
		b := &domain.RunBaseline{SpecRev: rev, Tree: run.StartTree, Started: run.StartTree != "", At: e.now()}
		if err != nil {
			b.SpecRev, b.Err = run.SpecRev, err.Error()
		}
		if run.Baseline != nil {
			b.Told = run.Baseline.Told
		}
		run.Baseline = b
		return true, e.cfg.Store.UpdateRun(ctx, run)
	}
	if e.wfRT().isBusy(baselineKey(run.Session, run.N)) {
		return false, nil
	}
	if run.Baseline == nil {
		run.Baseline = &domain.RunBaseline{}
	}
	if !run.Baseline.Running {
		run.Baseline.Running = true
		if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
			return false, err
		}
	}
	e.baselineAsync(run.Session, run.N)
	return false, nil
}

// rebaseline runs again the baseline of a proposal that was under way when
// gummi stopped: it would otherwise read as running for good.
func (e *Engine) rebaseline(ctx context.Context, r domain.Run) {
	if r.State != domain.RunProposed || r.Baseline == nil || !r.Baseline.Running ||
		!r.UpdatedAt.Before(e.wfRT().boot) || e.wfRT().isBusy(baselineKey(r.Session, r.N)) {
		return
	}
	unlock := e.lockRuns(r.Session)
	defer unlock()
	if cur, err := e.cfg.Store.Run(ctx, r.Session, r.N); err == nil && cur != nil && cur.State == domain.RunProposed {
		_, _ = e.askBaselineLocked(ctx, cur)
	}
}

func (e *Engine) baselineAsync(session domain.FeatureID, n int) {
	rt := e.wfRT()
	key := baselineKey(session, n)
	if !rt.claim(key) {
		return
	}
	if !e.goWorkflow(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		// a stop or a withdrawal cancels the checks: they run in the tree
		ctx, cancel := context.WithCancel(e.lifetime())
		defer cancel()
		defer rt.setCancel(key, cancel)()
		e.runBaseline(ctx, session, n)
	}) {
		rt.release(key)
	}
}

// runBaseline runs run n's done-when checks in the session's tree as it is
// now, records each checked item's result and the tree they ran at, and
// tells the session about every item that proves nothing it was not told
// of already.
func (e *Engine) runBaseline(ctx context.Context, session domain.FeatureID, n int) {
	st := e.cfg.Store
	run, err := st.Run(ctx, session, n)
	if err != nil || run == nil {
		return
	}
	b := domain.RunBaseline{Started: run.StartTree != "", At: e.now()}
	rev, items, err := e.baselineSpec(ctx, run)
	b.SpecRev = rev
	var dir string
	if err == nil {
		dir, err = e.sessionTree(ctx, session)
	}
	if err == nil {
		// the tree as the run reads it: once it has started, without the
		// ignored paths it listed then; before, without those on disk now
		b.Tree, err = e.snapshotTreesWith(ctx, session, func(t runTree) ([]string, error) {
			if run.StartTree == "" {
				return worktree.Ignored(ctx, t.Dir)
			}
			return e.runKeepOutFor(session, n, t.Repo), nil
		})
	}
	if err == nil {
		for _, r := range itemResults(items, doneWhenChecks(ctx, dir, items), "") {
			if r.Checked {
				b.Results = append(b.Results, r)
			}
		}
	} else {
		// the checks could not be run: the run goes on without its baseline
		b.Err, b.Tree, b.Started = err.Error(), run.StartTree, run.StartTree != ""
	}
	if ctx.Err() != nil {
		// stopped or withdrawn meanwhile: what the checks found is no baseline
		return
	}

	unlock := e.lockRuns(session)
	cur, err := st.Run(ctx, session, n)
	if err != nil || cur == nil {
		unlock()
		return
	}
	if old := cur.Baseline; old != nil && old.SpecRev == b.SpecRev {
		b.Told = old.Told
	}
	var fresh []domain.ItemResult
	for _, p := range b.Problems() {
		if !slices.Contains(b.Told, p.Item) {
			fresh = append(fresh, p)
			b.Told = append(b.Told, p.Item)
		}
	}
	cur.Baseline = &b
	err = st.UpdateRun(ctx, cur)
	unlock()
	if err != nil {
		return
	}
	switch {
	case b.Err != "":
		e.runNote(session, fmt.Sprintf("run %d: its baseline could not be run: %s", n, b.Err))
	case len(b.Problems()) == 0:
		e.runNote(session, fmt.Sprintf("run %d: its baseline found every checked item failing before the run", n))
	default:
		var lines []string
		for _, p := range b.Problems() {
			lines = append(lines, BaselineLine(p))
		}
		e.runNote(session, fmt.Sprintf("run %d: its baseline: %s", n, strings.Join(lines, "; ")))
	}
	if len(fresh) == 0 || cur.State == domain.RunDenied || cur.State.Ended() {
		return
	}
	_ = e.sendSessionTurn(WithActor(ctx, state.ActorWorkflow), session, baselineMessage(cur, items, fresh))
}

// baselineMessage is what the session is told of the items in its run's
// baseline that prove nothing, and what it may do about them.
func baselineMessage(run *domain.Run, items []domain.DoneWhen, problems []domain.ItemResult) string {
	cmd := map[string]string{}
	for _, it := range items {
		cmd[it.ID] = strings.TrimSpace(it.Check)
	}
	which := "These items' checks prove"
	if len(problems) == 1 {
		which = "This item's check proves"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow run %d's baseline: gummi ran spec S%d's done-when checks in the tree before the run's first step. "+
		"%s nothing about the run:\n", run.N, run.Spec, which)
	for _, p := range problems {
		fmt.Fprintf(&b, "- %s (`%s`): ", p.Item, cmd[p.Item])
		if p.CannotRun {
			b.WriteString(p.Detail + " — it fails whatever the run does. Install what it needs, or rewrite the check.\n")
			continue
		}
		if p.Verdict == domain.ItemUnjudged {
			b.WriteString("ran no tests — its go test -run matches no test, so it passes whatever the code does.\n")
			continue
		}
		b.WriteString("already passes before the run, so its passing at the end will not show the run did anything.\n")
	}
	b.WriteString("\n")
	if run.State == domain.RunProposed {
		b.WriteString("The run still waits on the person and is not held for this. To make these items count, rewrite their checks in the spec " +
			"(spec_replace_section) so each fails now, then withdraw this proposal with workflow_stop and propose again; or let it run as it is.")
	} else {
		b.WriteString("The run goes on and is not held for this; its report shows each checked item before and after it. " +
			"If these items should prove more, rewrite their checks in the spec for a later run.")
	}
	return b.String()
}

// BaselineLine is one checked item's baseline in the words the proposal
// and the run's block say it: "DW-2 already passes before the run",
// "DW-3 ran no tests", "DW-4 cannot run in this tree", or "DW-1 fails
// before the run".
func BaselineLine(r domain.ItemResult) string {
	if r.CannotRun {
		return r.Item + " cannot run in this tree"
	}
	switch r.Verdict {
	case domain.ItemMet:
		return r.Item + " already passes before the run"
	case domain.ItemUnjudged:
		return r.Item + " ran no tests"
	}
	return r.Item + " fails before the run"
}

// BaselineProblem reports whether a baseline result proves nothing about
// the run: its check already passes, or it ran no tests.
func BaselineProblem(r domain.ItemResult) bool {
	return r.Verdict == domain.ItemMet || r.Verdict == domain.ItemUnjudged
}

// ResultWord is a checked item's verdict, before the run, as a result
// says it.
func ResultWord(v domain.ItemVerdict) string {
	switch v {
	case domain.ItemMet:
		return "passed"
	case domain.ItemUnmet:
		return "failed"
	}
	return "ran no tests"
}

// resultNow is a checked item's verdict after the run, in the present.
func resultNow(v domain.ItemVerdict) string {
	switch v {
	case domain.ItemMet:
		return "passes"
	case domain.ItemUnmet:
		return "fails"
	}
	return "runs no tests"
}

// ResultChange is a checked item's result before and after the run, in
// words a person reads without a legend: "failed before the run, now
// passes", "passed before the run, still passes"; "" for an item with no
// baseline.
func ResultChange(r domain.ItemResult) string {
	if !r.Checked || r.Before == "" {
		return ""
	}
	how := "now"
	if r.Before == r.Verdict {
		how = "still"
	}
	return ResultWord(r.Before) + " before the run, " + how + " " + resultNow(r.Verdict)
}

// withBaseline sets each checked result's Before from the run's baseline.
func withBaseline(results []domain.ItemResult, b *domain.RunBaseline) []domain.ItemResult {
	if b == nil || b.Running {
		return results
	}
	before := map[string]domain.ItemVerdict{}
	for _, r := range b.Results {
		before[r.Item] = r.Verdict
	}
	for i := range results {
		if v, ok := before[results[i].Item]; ok && results[i].Checked {
			results[i].Before = v
		}
	}
	return results
}
