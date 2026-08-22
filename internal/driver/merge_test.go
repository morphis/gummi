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

// driveVerified drives the quick route to a verified branch and returns the
// harness, a fresh driver (a fresh CLI process), and the feature id — the
// setup behind every headless merge/clean test.
func driveVerified(t *testing.T) (*harness, *Driver, domain.FeatureID) {
	t.Helper()
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// leave real committed work so the branch is genuinely ahead.
			_ = os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600)
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "add a json export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	return h, h.driver(Options{}), domain.FeatureID(out.ID)
}

// gitHead returns the main checkout's current HEAD sha.
func gitHead(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Merge lands a verified card: the stream carries a `merged` event whose
// commit is the actual sha now on main, the card moves to done, and the
// outcome is StatusDone (exit 0).
func TestMergeLandsVerifiedBranch(t *testing.T) {
	h, d, id := driveVerified(t)
	before := gitHead(t, h.root)

	out, err := d.Merge(context.Background(), id, "feat(export): land the json export headlessly")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if st := h.stageOf(id); st != domain.StageDone {
		t.Fatalf("feature at %s after merge, want done", st)
	}
	if f, _ := h.store.GetFeature(context.Background(), id); f.Stage != domain.StageDone {
		t.Fatalf("stored stage = %s, want done", f.Stage)
	}

	merged := lastEvent(h, "merged")
	if merged == nil {
		t.Fatalf("no merged event in stream; got %v", h.eventKinds())
	}
	commit, _ := merged["commit"].(string)
	if commit == "" || commit == before {
		t.Fatalf("merged.commit %q: expected a fresh landed sha != pre-merge HEAD %q", commit, before)
	}
	if want := gitHead(t, h.root); commit != want {
		t.Fatalf("merged.commit %q != main HEAD %q", commit, want)
	}
	f, _ := h.store.GetFeature(context.Background(), id)
	if merged["branch"] != f.BranchName() {
		t.Fatalf("merged.branch = %v, want %s", merged["branch"], f.BranchName())
	}
}

// Merge refuses a card that is not at a verified branch — non-zero outcome,
// and main's history is untouched.
func TestMergeRefusesUnverified(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(77, domain.StageImplement)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	id := f.ID
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).Merge(context.Background(), id, "feat(x): not verified")
	if err == nil {
		t.Fatal("Merge accepted an unverified card")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by refused merge: %s -> %s", before, got)
	}
	if lastEvent(h, "merged") != nil {
		t.Fatal("refused merge still emitted a merged event")
	}
}

// Merge refuses a malformed commit message before any git mutation: the
// card is verified but the message fails validation, so main is untouched.
func TestMergeRefusesInvalidMessage(t *testing.T) {
	h, d, id := driveVerified(t)
	before := gitHead(t, h.root)

	out, err := d.Merge(context.Background(), id, "not a conventional commits subject")
	if err == nil {
		t.Fatal("Merge accepted an invalid commit message")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by invalid-message merge: %s -> %s", before, got)
	}
	if lastEvent(h, "merged") != nil {
		t.Fatal("invalid-message merge still emitted a merged event")
	}
}

// Merge refuses a verified card that is linked to an outbound PR — the
// both-or-neither landing invariant — naming the linked repo#number, before
// any git mutation.
func TestMergeRefusesLinkedPR(t *testing.T) {
	h, d, id := driveVerified(t)
	before := gitHead(t, h.root)

	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := h.store.SetPullRequest(context.Background(), id, ref); err != nil {
		t.Fatal(err)
	}

	out, err := d.Merge(context.Background(), id, "feat(export): land the json export headlessly")
	if err == nil {
		t.Fatal("Merge accepted a linked card")
	}
	if !strings.Contains(err.Error(), "o/r#42") {
		t.Fatalf("error %q does not name the linked PR", err)
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by refused merge: %s -> %s", before, got)
	}
	if lastEvent(h, "merged") != nil {
		t.Fatal("refused merge still emitted a merged event")
	}
}

// Merge succeeds once a linked PR is unlinked — the guard is keyed off the
// live PullRequest field, not a one-time check.
func TestMergeSucceedsAfterUnlink(t *testing.T) {
	h, d, id := driveVerified(t)

	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := h.store.SetPullRequest(context.Background(), id, ref); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPullRequest(context.Background(), id, domain.PullRequestRef{}); err != nil {
		t.Fatal(err)
	}

	out, err := d.Merge(context.Background(), id, "feat(export): land the json export headlessly")
	if err != nil {
		t.Fatalf("Merge after unlink: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
}

// Clean removes a landed card's worktree and branch, emits a cleaned event,
// and keeps the card record.
func TestCleanRemovesLandedBranch(t *testing.T) {
	h, d, id := driveVerified(t)
	// land it first so there is something to clean.
	if _, err := h.driver(Options{}).Merge(context.Background(), id, "feat(export): land the json export"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	f, _ := h.store.GetFeature(context.Background(), id)
	branch := f.BranchName()

	if ex, _ := h.wt.Exists(context.Background(), &f); !ex {
		t.Fatal("setup: worktree missing before clean")
	}

	out, err := d.Clean(context.Background(), id)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if ex, _ := h.wt.Exists(context.Background(), &f); ex {
		t.Error("worktree still present after clean")
	}
	if ok, _ := h.wt.BranchExists(context.Background(), &f); ok {
		t.Error("branch still present after clean")
	}
	if got := lastEvent(h, "cleaned"); got == nil || got["branch"] != branch {
		t.Fatalf("cleaned event = %v, want branch %s", got, branch)
	}
	// the card record stays (a done entry).
	if _, err := h.store.GetFeature(context.Background(), id); err != nil {
		t.Fatalf("card record lost after clean: %v", err)
	}
}

// Clean refuses a card that has not actually landed — nothing is removed.
func TestCleanRefusesUnlanded(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(78, domain.StageImplement)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	id := f.ID

	out, err := h.driver(Options{}).Clean(context.Background(), id)
	if err == nil {
		t.Fatal("Clean accepted an unlanded card")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if lastEvent(h, "cleaned") != nil {
		t.Fatal("refused clean still emitted a cleaned event")
	}
}

// driveVerifiedNamed drives a verified card whose Repo is the configured
// named repo "b", via the multi-repo harness.
func driveVerifiedNamed(t *testing.T) (*harness, *Driver, domain.FeatureID) {
	t.Helper()
	h := newMultiRepoHarness(t, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			_ = os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600)
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	out, err := h.driver(Options{Repo: "b"}).Run(context.Background(), "add a json export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	return h, h.driver(Options{}), domain.FeatureID(out.ID)
}

// TestMergeLandsOnNamedRepo: a card in a configured named repo merges onto
// that repo's main and never the default's.
func TestMergeLandsOnNamedRepo(t *testing.T) {
	h, d, id := driveVerifiedNamed(t)
	f, _ := h.store.GetFeature(context.Background(), id)
	if f.Repo != "b" {
		t.Fatalf("card repo = %q, want b", f.Repo)
	}
	defBefore := gitHead(t, h.root)
	namedBefore := gitHead(t, h.byName["b"])

	out, err := d.Merge(context.Background(), id, "feat(export): land the json export")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if st := h.stageOf(id); st != domain.StageDone {
		t.Fatalf("feature at %s after merge, want done", st)
	}
	if got := gitHead(t, h.byName["b"]); got == namedBefore {
		t.Errorf("named repo main did not advance: %s", got)
	}
	if got := gitHead(t, h.root); got != defBefore {
		t.Errorf("default repo main advanced on a named-repo merge: %s -> %s", defBefore, got)
	}
	merged := lastEvent(h, "merged")
	if merged == nil {
		t.Fatalf("no merged event; got %v", h.eventKinds())
	}
	commit, _ := merged["commit"].(string)
	if want := gitHead(t, h.byName["b"]); commit != want {
		t.Fatalf("merged.commit %q != named-repo main HEAD %q", commit, want)
	}
}

// TestCleanNamedRepo: clean removes a landed named-repo card's worktree and
// branch through the card's own manager.
func TestCleanNamedRepo(t *testing.T) {
	h, _, id := driveVerifiedNamed(t)
	if _, err := h.driver(Options{}).Merge(context.Background(), id, "feat(export): land the json export"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	f, _ := h.store.GetFeature(context.Background(), id)
	branch := f.BranchName()

	if ex, _ := h.pool.Exists(context.Background(), &f); !ex {
		t.Fatal("setup: worktree missing before clean")
	}
	out, err := h.driver(Options{}).Clean(context.Background(), id)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if ex, _ := h.pool.Exists(context.Background(), &f); ex {
		t.Error("worktree still present after clean")
	}
	if ok, _ := h.pool.BranchExists(context.Background(), &f); ok {
		t.Error("branch still present after clean")
	}
	if got := lastEvent(h, "cleaned"); got == nil || got["branch"] != branch {
		t.Fatalf("cleaned event = %v, want branch %s", got, branch)
	}
}
