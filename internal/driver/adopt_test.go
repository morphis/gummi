package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// git runs one git command in the harness repo, failing the test on error.
func (h *harness) git(args ...string) string {
	h.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = h.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// somebodyElsesBranch puts a branch in the harness repo with a commit on
// it that gummi had no part in — the premise of every adoption.
func (h *harness) somebodyElsesBranch(name string) {
	h.t.Helper()
	h.git("checkout", "-q", "-b", name)
	if err := os.WriteFile(filepath.Join(h.root, "theirs.txt"), []byte("their work\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	// Only their file: `git add .` here would sweep the workspace's own
	// .gummi state onto this branch, and checking main back out would then
	// delete it from under the harness.
	h.git("add", "theirs.txt")
	h.git("commit", "-q", "-m", "their unfinished work")
	h.git("checkout", "-q", "main")
}

// driveAdopted drives a card minted onto an existing branch all the way
// to verified, and returns it.
func driveAdopted(t *testing.T, branch string) (*harness, *Driver, domain.FeatureID) {
	t.Helper()
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Read the inherited diff; rework planned.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			_ = os.WriteFile(filepath.Join(o.WorkDir, "ours.txt"), []byte("our rework\n"), 0o600)
			return msgIdle(o.Model, "Reworked, on top of theirs.")
		},
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	h.somebodyElsesBranch(branch)
	out, err := h.driver(Options{Adopt: branch}).Run(context.Background(), "finish what they started")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %q, want verified", out.Status)
	}
	return h, h.driver(Options{}), domain.FeatureID(out.ID)
}

// TestDriveAdoptedCardKeepsTheirWork is the end-to-end premise: a card
// minted onto somebody else's branch runs the ordinary workflow in a
// worktree that already holds their commits, and finishes with both their
// work and its own on the branch.
func TestDriveAdoptedCardKeepsTheirWork(t *testing.T) {
	h, _, id := driveAdopted(t, "feat/theirs")
	ctx := context.Background()

	f, err := h.store.GetFeature(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Adopted() {
		t.Fatalf("card is not adopted: scheme = %q", f.BranchScheme)
	}
	if got := f.BranchName(); got != "feat/theirs" {
		t.Errorf("branch = %s, want the adopted one (gummi cut nothing)", got)
	}
	// Their commit is still on the branch, and ours is on top of it.
	log := h.git("log", "--oneline", "feat/theirs")
	if !strings.Contains(log, "their unfinished work") {
		t.Errorf("the inherited commit is gone from the branch:\n%s", log)
	}
	p, err := h.wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"theirs.txt", "ours.txt"} {
		if _, err := os.Stat(filepath.Join(p, name)); err != nil {
			t.Errorf("%s missing from the finished worktree: %v", name, err)
		}
	}
}

// TestCleanKeepsAnAdoptedBranch is the custody guard at the driver's
// surface (DESIGN §10 D22). Cleaning up is the one routine act that
// destroys a branch, and an adopted branch is the part gummi never owned:
// the worktree goes, the ref stays, and the clean still SUCCEEDS, because
// a surviving branch is the intended outcome and not a partial failure.
func TestCleanKeepsAnAdoptedBranch(t *testing.T) {
	h, d, id := driveAdopted(t, "feat/theirs")
	ctx := context.Background()

	if _, err := h.driver(Options{}).Merge(ctx, id, "feat(export): finish their work"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	f, err := h.store.GetFeature(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	out, err := d.Clean(ctx, id)
	if err != nil {
		t.Fatalf("Clean refused an adopted card instead of keeping its branch: %v", err)
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %q, want a clean success", out.Status)
	}
	if ex, _ := h.wt.Exists(ctx, &f); ex {
		t.Error("worktree still present after clean — that half was gummi's to remove")
	}
	if ok, _ := h.wt.BranchExists(ctx, &f); !ok {
		t.Error("the adopted branch was deleted; gummi did not cut it and must not destroy it")
	}
	got := lastEvent(h, "cleaned")
	if got == nil || got["branch"] != "feat/theirs" {
		t.Fatalf("cleaned event = %v, want branch feat/theirs", got)
	}
	if kept, _ := got["branch_kept"].(bool); !kept {
		t.Errorf("cleaned event = %v, want branch_kept true so a script can tell this from an ordinary clean", got)
	}
}
