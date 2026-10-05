package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// mergeInProgress reports whether git has a merge in progress in dir: a merge
// landing that is undone must leave no MERGE_HEAD behind.
func mergeInProgress(t *testing.T, dir string) bool {
	t.Helper()
	p := mustGit(t, dir, "rev-parse", "--git-path", "MERGE_HEAD")
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	_, err := os.Stat(p)
	return err == nil
}

func TestLandMergeKeepsBranchCommits(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)
	writeFile(t, p, "second.txt", "second commit\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "second feature commit")
	branchTip := mustGit(t, p, "rev-parse", "HEAD")
	mainBefore := mustGit(t, root, "rev-parse", "HEAD")

	msg := "FD-009: land me\n\nKeeps both feature commits."
	sha, err := m.Land(ctx, f, msg, domain.LandMerge)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Errorf("returned sha %q != HEAD %q", sha, got)
	}
	if got := mustGit(t, root, "log", "-1", "--format=%B"); strings.TrimSpace(got) != msg {
		t.Errorf("merge commit message = %q, want %q", got, msg)
	}
	parents := strings.Fields(mustGit(t, root, "log", "-1", "--format=%P"))
	if len(parents) != 2 || parents[0] != mainBefore || parents[1] != branchTip {
		t.Errorf("parents = %v, want [%s %s]", parents, mainBefore, branchTip)
	}
	for _, rel := range []string{"sq.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s missing from main checkout: %v", rel, err)
		}
	}
	if _, err := runGit(ctx, root, "merge-base", "--is-ancestor", branchTip, "HEAD"); err != nil {
		t.Errorf("branch tip is not reachable from main: %v", err)
	}
	if mergeInProgress(t, root) {
		t.Error("MERGE_HEAD left behind after a completed merge landing")
	}
	if landed, err := m.Landed(ctx, f); !landed || err != nil {
		t.Errorf("Landed after merge landing = %v, %v; want true", landed, err)
	}
	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout not clean after merge:\n%s", out)
	}

	// the landed sha is recorded, so the branch deletes even though its
	// commits are not squashed: plain -d would also accept it now, but the
	// landing must not depend on that.
	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteLandedBranch(ctx, f); err != nil {
		t.Fatalf("branch landed by merge not deleted: %v", err)
	}
}

func TestLandMergeConflictIsUndone(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)
	writeFile(t, p, "README.md", "branch version\n")
	mustGit(t, p, "commit", "-qam", "branch readme")
	writeFile(t, root, "README.md", "main version\n")
	mustGit(t, root, "commit", "-qam", "main readme")
	mainHead := mustGit(t, root, "rev-parse", "HEAD")

	_, err := m.Land(ctx, f, "FD-009: conflicting", domain.LandMerge)
	var ce *MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *MergeConflictError", err)
	}
	if ce.Method != domain.LandMerge {
		t.Errorf("conflict method = %q, want %q", ce.Method, domain.LandMerge)
	}
	if len(ce.Files) != 1 || ce.Files[0] != "README.md" {
		t.Errorf("conflicted files = %v, want [README.md]", ce.Files)
	}
	if !strings.HasPrefix(ce.Error(), "merge conflicts in README.md") {
		t.Errorf("conflict text = %q, want it to name the merge method", ce.Error())
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main HEAD moved by failed merge: %s -> %s", mainHead, got)
	}
	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout dirty after undone merge:\n%s", out)
	}
	if mergeInProgress(t, root) {
		t.Error("MERGE_HEAD left behind after an undone merge")
	}
	if landed, _ := m.Landed(ctx, f); landed {
		t.Error("conflicted merge recorded as landed")
	}
}

// TestLandMergeContentAlreadyOnMainUndone covers a merge that git would
// take without a conflict but that stages nothing: the branch's change is
// already on main. The merge state it wrote must be undone, main must not
// move, and the refusal must say the branch already landed.
func TestLandMergeContentAlreadyOnMainUndone(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	writeFile(t, root, "sq.txt", "feature work\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "same change on main")
	mainHead := mustGit(t, root, "rev-parse", "HEAD")

	_, err := m.Land(ctx, f, "FD-009: again", domain.LandMerge)
	if err == nil || !strings.Contains(err.Error(), "already landed") {
		t.Fatalf("err = %v, want an already-landed refusal", err)
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main HEAD moved by refused merge: %s -> %s", mainHead, got)
	}
	if mergeInProgress(t, root) {
		t.Error("MERGE_HEAD left behind after a refused merge")
	}
	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout dirty after refused merge:\n%s", out)
	}
}

func TestLandMergeRefusedForGoalCard(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	f.GoalID = "GL-001"
	mainHead := mustGit(t, root, "rev-parse", "HEAD")

	if _, err := m.Land(ctx, f, "FD-009: land me", domain.LandMerge); err == nil {
		t.Fatal("goal card landed as a merge commit")
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main HEAD moved by refused landing: %s -> %s", mainHead, got)
	}
}

func TestLandUnknownMethodRefused(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	mainHead := mustGit(t, root, "rev-parse", "HEAD")

	if _, err := m.Land(ctx, f, "FD-009: land me", domain.LandMethod("rebase")); err == nil {
		t.Fatal("unknown landing method accepted")
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main HEAD moved by refused landing: %s -> %s", mainHead, got)
	}
}

func TestLandSquashIsSquashMerge(t *testing.T) {
	for _, method := range []domain.LandMethod{domain.LandSquash, ""} {
		root := newRepo(t)
		m, f, _ := committedFeature(t, root)
		sha, err := m.Land(ctx, f, "FD-009: land me", method)
		if err != nil {
			t.Fatalf("Land(%q): %v", method, err)
		}
		if parents := strings.Fields(mustGit(t, root, "log", "-1", "--format=%P")); len(parents) != 1 {
			t.Errorf("Land(%q) squash landing has %d parents, want 1", method, len(parents))
		}
		if got := mustGit(t, root, "rev-parse", "HEAD"); got != sha {
			t.Errorf("Land(%q) returned sha %q != HEAD %q", method, sha, got)
		}
	}
}
