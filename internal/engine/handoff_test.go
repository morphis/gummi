package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// verifiedCardWithWork walks a fresh feature to verify with a real commit
// on its branch — the exact state the three endings are choices between,
// and the one Advance refuses to leave without a landing.
func verifiedCardWithWork(t *testing.T, e *Engine, store *state.Store, wt *worktree.Manager, num int) domain.Feature {
	t.Helper()
	f := feature(num, "ship it", domain.StagePlan)
	putFeature(t, store, f)
	mustAdvance(t, e, f.ID)
	fillPromotedSection(t, wt, f, "Implementation notes", "Add a settings toggle; persist per-device.")
	for stage := domain.StagePlan; stage != domain.StageVerify; {
		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced {
			t.Fatalf("walk to verify: status=%d at %s, want advanced", res.Status, stage)
		}
		stage = res.To
	}
	wtDir := filepath.Join(wt.Root(), f.WorktreePath())
	if err := os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wtDir, "add", "work.txt")
	gitIn(t, wtDir, "commit", "-q", "-m", "work")
	fillPromotedSection(t, wt, f, "Verification plan", "Run the repo's discovered checks.")
	return f
}

// TestHandOffClosesTheCardAndKeepsTheBranch is the whole verb: the card
// reaches done, the branch is still there, and nothing merged. Before it
// existed the only way to end this card without landing it was `delete`,
// which destroys the branch — so the honest answer cost the work.
func TestHandOffClosesTheCardAndKeepsTheBranch(t *testing.T) {
	ctx := context.Background()
	e, _, store, wt := advanceEngine(t)
	f := verifiedCardWithWork(t, e, store, wt, 1)

	// the precondition the whole verb exists for: Advance on its own
	// refuses to close this card, because a landing is owed.
	if res := mustAdvance(t, e, f.ID); res.Status != StatusNeedsMerge {
		t.Fatalf("pre-handoff advance: status=%d, want needs-merge", res.Status)
	}

	res, err := e.HandOff(ctx, f.ID, "user")
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if res.Status != StatusAdvanced || res.To != domain.StageDone {
		t.Fatalf("handoff: status=%d to=%s, want advanced/done", res.Status, res.To)
	}

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageDone {
		t.Fatalf("stage after hand-off = %s, want done", got.Stage)
	}
	if !got.HandedOff() {
		t.Fatal("hand-off left no stamp — a done card with no ending reads as abandoned")
	}
	if got.LandedSHA != "" {
		t.Fatalf("hand-off recorded a landing: %q", got.LandedSHA)
	}
	mgr, err := e.WorktreesFor(ctx, &got)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := mgr.BranchExists(ctx, &got); err != nil || !exists {
		t.Fatalf("branch gone after hand-off (exists=%v err=%v) — keeping it is the point", exists, err)
	}
	if landed, err := mgr.Landed(ctx, &got); err != nil || landed {
		t.Fatalf("hand-off landed the branch (landed=%v err=%v)", landed, err)
	}
}

// TestHandOffCommitsLooseWork: the branch IS the deliverable, so nothing
// downstream is going to sweep the worktree's remainder onto main. Loose
// work left behind would be lost to the next clean or delete.
func TestHandOffCommitsLooseWork(t *testing.T) {
	ctx := context.Background()
	e, _, store, wt := advanceEngine(t)
	f := verifiedCardWithWork(t, e, store, wt, 2)

	wtDir := filepath.Join(wt.Root(), f.WorktreePath())
	if err := os.WriteFile(filepath.Join(wtDir, "late.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.HandOff(ctx, f.ID, "user"); err != nil {
		t.Fatalf("HandOff: %v", err)
	}

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := e.WorktreesFor(ctx, &got)
	if err != nil {
		t.Fatal(err)
	}
	if dirty, err := mgr.TrackedDirty(ctx, &got); err != nil {
		t.Fatal(err)
	} else if dirty {
		t.Fatal("hand-off left tracked-dirty work behind on the kept branch")
	}
	out := gitOut(t, wtDir, "log", "-1", "--name-only", "--pretty=format:")
	if !strings.Contains(out, "late.txt") {
		t.Fatalf("the final checkpoint did not carry the loose file: %q", out)
	}
}

// TestHandOffDoesNotWaiveTheFloor: hand-off answers "who lands this",
// not "is this finished". A gate blocker holds it exactly as it holds a
// landing, and the card stays where it was.
func TestHandOffDoesNotWaiveTheFloor(t *testing.T) {
	ctx := context.Background()
	e, _, store, wt := advanceEngine(t)
	f := verifiedCardWithWork(t, e, store, wt, 3)

	// an unresolved %% thread in the artifact blocks every human gate
	fillPromotedSection(t, wt, f, "Verification plan",
		"Run the repo's discovered checks.\n%% @user(2026-01-01): is the toggle covered?")

	res, err := e.HandOff(ctx, f.ID, "user")
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if res.Status != StatusBlockedQuestions {
		t.Fatalf("blocked hand-off: status=%d, want blocked-questions", res.Status)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageVerify {
		t.Fatalf("a refused hand-off moved the card to %s", got.Stage)
	}
}

// TestHandOffRefusesALandedBranch: the two endings are exclusive, and a
// card whose work is on main is not something anyone can take away.
func TestHandOffRefusesALandedBranch(t *testing.T) {
	ctx := context.Background()
	e, _, store, wt := advanceEngine(t)
	f := verifiedCardWithWork(t, e, store, wt, 4)

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := e.WorktreesFor(ctx, &got)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SquashMerge(ctx, &got, "feat(x): land it"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.HandOff(ctx, f.ID, "user"); err == nil {
		t.Fatal("hand-off accepted a branch already on main")
	} else if !strings.Contains(err.Error(), "already landed") {
		t.Fatalf("refusal does not name the landing: %v", err)
	}
}

// TestHandOffRefusesAResearchCard: an RS card carries no branch, so
// there is nothing to keep and the verb means nothing for it.
func TestHandOffRefusesAResearchCard(t *testing.T) {
	ctx := context.Background()
	e, _, store, _ := advanceEngine(t)
	f := feature(5, "what should we use", domain.StageVerify)
	f.ID = domain.FeatureID("RS-005")
	f.Kind = domain.KindResearch
	putFeature(t, store, f)

	if _, err := e.HandOff(ctx, f.ID, "user"); err == nil {
		t.Fatal("hand-off accepted a research card")
	} else if !strings.Contains(err.Error(), "no branch") {
		t.Fatalf("refusal does not say why: %v", err)
	}
}
