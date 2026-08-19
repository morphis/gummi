package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
	"github.com/morphis/gummi/internal/worktree"
)

// advanceEngine builds an agent-less engine over a fresh repo — Advance
// runs the shared floor with no coding agent, exactly as a static board
// (or the headless driver before its agent starts) drives it.
func advanceEngine(t *testing.T) (*Engine, state.Workspace, *state.Store, *worktree.Manager) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Store: store, Worktrees: wt, Workspace: ws})
	t.Cleanup(func() { e.Close() })
	return e, ws, store, wt
}

// putFeature persists f so Advance can load it.
func putFeature(t *testing.T, store *state.Store, f domain.Feature) {
	t.Helper()
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs a git command inside dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mustAdvance(t *testing.T, e *Engine, id domain.FeatureID) AdvanceResult {
	t.Helper()
	res, err := e.Advance(context.Background(), id, "user")
	if err != nil {
		t.Fatalf("Advance %s: %v", id, err)
	}
	return res
}

// A feature walks the full forward floor; leaving Spec creates the
// worktree and promotes the artifact to its workspace home.
func TestAdvanceForwardWalkFeature(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dark mode", domain.StageTodo)
	putFeature(t, store, f)

	// todo → brainstorm → spec: no worktree yet
	for _, want := range []domain.Stage{domain.StageBrainstorm, domain.StageSpec} {
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != want {
			t.Fatalf("advance to %s: status=%d to=%s", want, res.Status, res.To)
		}
		if res.EnteredWorktree {
			t.Fatalf("worktree created before spec approval (at %s)", want)
		}
	}
	if ok, _ := wt.Exists(ctx, &f); ok {
		t.Fatal("worktree exists before spec approval")
	}

	// spec → plan: the approval gate creates the worktree + promotes the spec
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StagePlan {
		t.Fatalf("spec approval: status=%d to=%s", res.Status, res.To)
	}
	if !res.EnteredWorktree {
		t.Fatal("spec approval did not report EnteredWorktree")
	}
	if ok, _ := wt.Exists(ctx, &f); !ok {
		t.Fatal("worktree missing after spec approval")
	}
	if _, err := os.Stat(filepath.Join(wt.Root(), f.ArtifactPath())); err != nil {
		t.Fatalf("spec not promoted to its workspace home: %v", err)
	}

	// plan → implement → review → verify: no further worktree creation
	for _, want := range []domain.Stage{domain.StageImplement, domain.StageReview, domain.StageVerify} {
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != want {
			t.Fatalf("advance to %s: status=%d to=%s", want, res.Status, res.To)
		}
		if res.EnteredWorktree {
			t.Fatalf("worktree re-created leaving into %s", want)
		}
	}
}

// A bug walks its own graph; leaving Diagnose creates the worktree and
// promotes the report under .gummi/bugs.
func TestAdvanceBugWorktreeGate(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	f := feature(1, "login loops", domain.StageTodo)
	f.ID = domain.FeatureID("BG-001")
	f.Kind = domain.KindBug
	putFeature(t, store, f)

	for _, want := range []domain.Stage{domain.StageTriage, domain.StageDiagnose, domain.StageFix} {
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != want {
			t.Fatalf("advance to %s: status=%d to=%s", want, res.Status, res.To)
		}
	}
	if _, err := os.Stat(filepath.Join(wt.Root(), f.ArtifactPath())); err != nil {
		t.Fatalf("bug report not at its workspace home: %v", err)
	}
}

// Skip flags route todo → spec → implement directly, still creating the
// worktree when the item first enters a work stage.
func TestAdvanceSkipEdges(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	f := feature(1, "tiny fix", domain.StageTodo)
	f.Skip = domain.SkipFlags{Brainstorm: true, Plan: true}
	putFeature(t, store, f)

	if res := mustAdvance(t, e, f.ID); res.To != domain.StageSpec {
		t.Fatalf("todo skip-edge to %s, want spec", res.To)
	}
	res := mustAdvance(t, e, f.ID)
	if res.To != domain.StageImplement || !res.EnteredWorktree {
		t.Fatalf("spec skip-edge: to=%s entered=%v, want implement+worktree", res.To, res.EnteredWorktree)
	}
	if ok, _ := wt.Exists(context.Background(), &f); !ok {
		t.Fatal("worktree missing after skip-plan spec approval")
	}
}

// Unresolved user %% threads in the artifact block the gate — no
// transition, a typed blocked status with the open count.
func TestAdvanceBlockedByQuestions(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := feature(1, "gated", domain.StageSpec)
	putFeature(t, store, f)

	// seed a draft carrying one open @user question
	if err := os.MkdirAll(ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	body := "# Spec\nThe toggle persists.\n%% @user(2026-01-01): per-device or synced?\n"
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedQuestions || res.Blockers != 1 {
		t.Fatalf("status=%d blockers=%d, want blocked-questions/1", res.Status, res.Blockers)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StageSpec {
		t.Fatalf("blocked gate still transitioned to %s", got.Stage)
	}
}

// Unresolved diff annotations block the gate on the diff backend.
func TestAdvanceBlockedByDiff(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "diff gated", domain.StageReview)
	putFeature(t, store, f)
	if _, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "a.go", Anchor: "h", Excerpt: "x", Comment: "fix this",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedDiff || res.Blockers != 1 {
		t.Fatalf("status=%d blockers=%d, want blocked-diff/1", res.Status, res.Blockers)
	}
}

// The verify→done gate reports NeedsMerge when the branch carries its own
// commits, and transitions straight to Done when there is nothing to land.
func TestAdvanceVerifyDoneGate(t *testing.T) {
	ctx := context.Background()

	// branch ahead → NeedsMerge, no transition
	t.Run("ahead needs merge", func(t *testing.T) {
		e, _, store, wt := advanceEngine(t)
		f := feature(1, "ship it", domain.StageSpec)
		putFeature(t, store, f)
		mustAdvance(t, e, f.ID) // spec → plan, creates the worktree
		// walk to verify
		for stage := domain.StagePlan; stage != domain.StageVerify; {
			res := mustAdvance(t, e, f.ID)
			stage = res.To
		}
		// commit real work on the branch
		wtDir := filepath.Join(wt.Root(), f.WorktreePath())
		if err := os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("w\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wtDir, "add", "work.txt")
		gitIn(t, wtDir, "commit", "-q", "-m", "work")

		// mid-verify, before the gate is reached, the feature is not yet
		// marked verified — the guard status's `verified` relies on.
		if got, _ := store.GetFeature(ctx, f.ID); !got.VerifiedAt.IsZero() {
			t.Fatalf("verified_at stamped before the verify gate: %v", got.VerifiedAt)
		}

		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusNeedsMerge {
			t.Fatalf("status=%d, want needs-merge", res.Status)
		}
		if got, _ := store.GetFeature(ctx, f.ID); got.Stage != domain.StageVerify {
			t.Fatalf("needs-merge gate transitioned to %s", got.Stage)
		}
		// reaching the gate stamps the verified marker (persisted + on the
		// returned record), while the stage stays at verify.
		if res.Feature.VerifiedAt.IsZero() {
			t.Fatal("needs-merge result did not carry a verified_at stamp")
		}
		if got, _ := store.GetFeature(ctx, f.ID); got.VerifiedAt.IsZero() {
			t.Fatal("needs-merge gate did not persist verified_at")
		}
	})

	// no branch commits → straight to Done
	t.Run("empty branch to done", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(2, "nothing to land", domain.StageSpec)
		putFeature(t, store, f)
		for stage := domain.StageSpec; stage != domain.StageVerify; {
			res := mustAdvance(t, e, f.ID)
			stage = res.To
		}
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != domain.StageDone {
			t.Fatalf("empty branch: status=%d to=%s, want advanced/done", res.Status, res.To)
		}
	})
}

// A terminal item has no forward edge.
func TestAdvanceNoopAtTerminal(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	f := feature(1, "done already", domain.StageDone)
	putFeature(t, store, f)
	if res := mustAdvance(t, e, f.ID); res.Status != StatusNoop {
		t.Fatalf("status=%d, want noop", res.Status)
	}
}

// The actor is recorded in the transition history.
func TestAdvanceActorRecorded(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "who did it", domain.StageTodo)
	putFeature(t, store, f)
	if _, err := e.Advance(ctx, f.ID, "caller"); err != nil {
		t.Fatal(err)
	}
	hist, err := store.History(ctx, f.ID)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history=%v err=%v", hist, err)
	}
	if hist[0].Actor != "caller" {
		t.Fatalf("actor = %q, want caller", hist[0].Actor)
	}
}

// --- plan-time historical envelope estimation (moved from the UI) ---

func TestEstimateEnvelopeFromHistory(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	done := feature(1, "prior", domain.StageDone)
	putFeature(t, store, done)
	if err := store.AddSpend(ctx, done.ID, 100, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	f := feature(2, "new", domain.StageSpec)
	putFeature(t, store, f)

	credits, samples := e.estimateEnvelope(ctx, &f)
	// 100 median × 1.25 = 125 → floored at MinEnvelope 150
	if f.Budget.Envelope != 150 || credits != 150 || samples != 1 {
		t.Fatalf("estimate: envelope=%d credits=%d samples=%d, want 150/150/1", f.Budget.Envelope, credits, samples)
	}
	notice := (AdvanceResult{EstimatedCredits: credits, EstimateSamples: samples}).EstimateNotice()
	if want := " · envelope estimated at 150 credits from 1 metered feature(s)"; notice != want {
		t.Fatalf("notice = %q, want %q", notice, want)
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.Budget.Envelope != 150 {
		t.Fatalf("envelope not persisted: %d", got.Budget.Envelope)
	}
}

func TestEstimateEnvelopeRespectsExplicit(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	done := feature(1, "prior", domain.StageDone)
	putFeature(t, store, done)
	_ = store.AddSpend(ctx, done.ID, 120, 0, 0, 0)
	f := feature(2, "new", domain.StageSpec)
	f.Budget.Envelope = 200
	putFeature(t, store, f)

	credits, samples := e.estimateEnvelope(ctx, &f)
	if credits != 0 || samples != 0 || f.Budget.Envelope != 200 {
		t.Fatalf("explicit envelope overridden: credits=%d samples=%d env=%d", credits, samples, f.Budget.Envelope)
	}
	if (AdvanceResult{}).EstimateNotice() != "" {
		t.Fatal("empty estimate produced a non-empty notice")
	}
}

func TestEstimateEnvelopeNoHistory(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	wip := feature(1, "wip", domain.StageImplement)
	putFeature(t, store, wip)
	_ = store.AddSpend(ctx, wip.ID, 50, 0, 0, 0)
	f := feature(2, "new", domain.StageSpec)
	putFeature(t, store, f)

	credits, samples := e.estimateEnvelope(ctx, &f)
	if credits != 0 || samples != 0 || f.Budget.Envelope != 0 {
		t.Fatalf("no-history estimate applied: credits=%d samples=%d env=%d", credits, samples, f.Budget.Envelope)
	}
}

// --- dependency gate (FD-059) ---

// unmetDeps returns one BlockingDep per direct dependency short of Done,
// skipping those already landed.
func TestUnmetDeps(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dependent", domain.StagePlan)
	putFeature(t, store, f)
	implemented := feature(2, "dep in flight", domain.StageImplement)
	putFeature(t, store, implemented)
	done := feature(3, "dep landed", domain.StageDone)
	putFeature(t, store, done)
	if err := store.AddDependency(ctx, f.ID, implemented.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDependency(ctx, f.ID, done.ID); err != nil {
		t.Fatal(err)
	}

	deps, err := e.unmetDeps(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].ID != implemented.ID || deps[0].Stage != domain.StageImplement {
		t.Fatalf("unmetDeps = %+v, want only the in-flight dep", deps)
	}
}

// A card at Plan with an unmet dependency is blocked from entering its
// coding stage: StatusBlockedDependency, BlockingDeps naming the dep, and
// the stored stage left at Plan.
func TestAdvanceBlockedByDependency(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dependent", domain.StagePlan)
	putFeature(t, store, f)
	dep := feature(2, "dep", domain.StageImplement)
	putFeature(t, store, dep)
	if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedDependency {
		t.Fatalf("status=%d, want blocked-dependency", res.Status)
	}
	if len(res.BlockingDeps) != 1 || res.BlockingDeps[0].ID != dep.ID || res.BlockingDeps[0].Stage != domain.StageImplement {
		t.Fatalf("BlockingDeps = %+v, want %s@implement", res.BlockingDeps, dep.ID)
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.Stage != domain.StagePlan {
		t.Fatalf("blocked gate transitioned to %s, want Plan unchanged", got.Stage)
	}
}

// Once the dependency reaches Done, the same Advance lands in Implement.
func TestAdvanceDependencyMet(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dependent", domain.StagePlan)
	putFeature(t, store, f)
	dep := feature(2, "dep", domain.StageImplement)
	putFeature(t, store, dep)
	if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}
	if res := mustAdvance(t, e, f.ID); res.Status != StatusBlockedDependency {
		t.Fatalf("pre-landing status=%d, want blocked-dependency", res.Status)
	}

	for _, st := range []domain.Stage{domain.StageReview, domain.StageVerify, domain.StageDone} {
		if _, err := store.Transition(ctx, dep.ID, st, "test"); err != nil {
			t.Fatalf("walking dep to %s: %v", st, err)
		}
	}
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageImplement {
		t.Fatalf("post-landing status=%d to=%s, want advanced/implement", res.Status, res.To)
	}
}

// Skip edges into the coding stage are gated too: Spec (plan skipped) →
// Implement, and a bug's Diagnose → Fix.
func TestAdvanceDependencyGateSkipEdges(t *testing.T) {
	ctx := context.Background()

	t.Run("feature spec skip-plan", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(1, "dependent", domain.StageSpec)
		f.Skip = domain.SkipFlags{Plan: true}
		putFeature(t, store, f)
		dep := feature(2, "dep", domain.StageImplement)
		putFeature(t, store, dep)
		if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
			t.Fatal(err)
		}
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusBlockedDependency {
			t.Fatalf("status=%d, want blocked-dependency on skip edge", res.Status)
		}
	})

	t.Run("bug diagnose", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(1, "bug", domain.StageDiagnose)
		f.ID = domain.FeatureID("BG-001")
		f.Kind = domain.KindBug
		putFeature(t, store, f)
		dep := feature(2, "dep", domain.StageImplement)
		putFeature(t, store, dep)
		if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
			t.Fatal(err)
		}
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusBlockedDependency {
			t.Fatalf("status=%d, want blocked-dependency on bug diagnose", res.Status)
		}
	})
}

// DependencyBlockers is the badge half of the Advance gate: at the coding
// stage it withholds an unmet dep (matching StatusBlockedDependency); in
// brainstorm/spec (next step not the coding stage) it returns nil even with
// the same unmet dep; and once the dep reaches Done it returns nil. The
// badge and the gate thus share one definition and can never disagree.
func TestDependencyBlockers(t *testing.T) {
	ctx := context.Background()

	t.Run("blocked at coding stage", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(1, "dependent", domain.StagePlan)
		putFeature(t, store, f)
		dep := feature(2, "dep", domain.StageImplement)
		putFeature(t, store, dep)
		if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
			t.Fatal(err)
		}
		deps, err := e.DependencyBlockers(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 1 || deps[0].ID != dep.ID || deps[0].Stage != domain.StageImplement {
			t.Fatalf("DependencyBlockers = %+v, want %s@implement", deps, dep.ID)
		}
	})

	t.Run("stage-aware: design stage not blocked", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(1, "designing", domain.StageBrainstorm)
		putFeature(t, store, f)
		dep := feature(2, "dep", domain.StageImplement)
		putFeature(t, store, dep)
		if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
			t.Fatal(err)
		}
		deps, err := e.DependencyBlockers(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 0 {
			t.Fatalf("design-stage DependencyBlockers = %+v, want nil", deps)
		}
	})

	t.Run("all deps done", func(t *testing.T) {
		e, _, store, _ := advanceEngine(t)
		f := feature(1, "dependent", domain.StagePlan)
		putFeature(t, store, f)
		dep := feature(2, "dep", domain.StageDone)
		putFeature(t, store, dep)
		if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
			t.Fatal(err)
		}
		deps, err := e.DependencyBlockers(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 0 {
			t.Fatalf("all-done DependencyBlockers = %+v, want nil", deps)
		}
	})
}

// A forward edge that does not target the coding stage (Todo → Brainstorm)
// is never dependency-gated.
func TestAdvanceDependencyGateNotCoding(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dependent", domain.StageTodo)
	putFeature(t, store, f)
	dep := feature(2, "dep", domain.StageImplement)
	putFeature(t, store, dep)
	if err := store.AddDependency(ctx, f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageBrainstorm {
		t.Fatalf("status=%d to=%s, want advanced/brainstorm (design edge not gated)", res.Status, res.To)
	}
}

// The invariant: every forward path into the coding stage resolves through
// nextStage to the coding stage, so the Advance gate can never be bypassed.
// Review/Verify rerun edges are excluded — nextStage takes the forward edge
// (nexts[0]) there, never the coding rerun.
func TestNextStageCoversEveryCodingEntry(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		work := workflow.WorkStage(kind)
		for _, from := range domain.Stages {
			if from == domain.StageReview || from == domain.StageVerify {
				continue // rerun edges are bounces, not forward moves
			}
			for _, skip := range skipCombos() {
				f := feature(1, "invariant", from)
				f.Kind = kind
				f.Skip = skip
				enters := false
				for _, n := range workflow.Next(kind, from, skip) {
					if n == work {
						enters = true
					}
				}
				if enters && (&Engine{}).nextStage(f) != work {
					t.Fatalf("kind=%s from=%s skip=%+v: a forward path enters %s but nextStage resolves to %s — the Advance gate is bypassed",
						kind, from, skip, work, (&Engine{}).nextStage(f))
				}
			}
		}
	}
}

func skipCombos() []domain.SkipFlags {
	var out []domain.SkipFlags
	for _, b := range []bool{false, true} {
		for _, p := range []bool{false, true} {
			for _, tr := range []bool{false, true} {
				for _, dg := range []bool{false, true} {
					out = append(out, domain.SkipFlags{Brainstorm: b, Plan: p, Triage: tr, Diagnose: dg})
				}
			}
		}
	}
	return out
}

// --- research workflow (worktree-less routing) ---

// A research card walks its whole graph without ever materializing a
// worktree: NeedsWorktree routes investigate/shape/review/verify/done all
// to the main checkout, so Advance never reports EnteredWorktree and no
// worktree exists.
func TestAdvanceResearchNoWorktree(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "rs topic", domain.StageTodo)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	putFeature(t, store, f)

	for _, want := range []domain.Stage{
		domain.StageInvestigate, domain.StageShape, domain.StageReview,
		domain.StageVerify, domain.StageDone,
	} {
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != want {
			t.Fatalf("advance to %s: status=%d to=%s", want, res.Status, res.To)
		}
		if res.EnteredWorktree {
			t.Fatalf("research card created a worktree at %s", want)
		}
	}
	if ok, _ := wt.Exists(ctx, &f); ok {
		t.Fatal("research card materialized a worktree")
	}
}

// Verify→done for a research card never reports NeedsMerge: there is no
// branch to land, so the merge gate falls through straight to the
// transition, and the squash-merge/commit-message scribe path (only ever
// reached from StatusNeedsMerge) is never entered.
func TestAdvanceResearchVerifyDoneNoMerge(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	f := feature(1, "research no land", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	putFeature(t, store, f)

	for stage := domain.StageInvestigate; stage != domain.StageVerify; {
		res := mustAdvance(t, e, f.ID)
		stage = res.To
	}
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageDone {
		t.Fatalf("research verify→done: status=%d to=%s, want advanced/done", res.Status, res.To)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StageDone {
		t.Fatalf("research card did not reach done: %s", got.Stage)
	}
}
