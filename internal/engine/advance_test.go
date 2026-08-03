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
