package main

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

// readFixture is a committed throwaway repo with an initialized gummi
// workspace, store, and worktree manager — the setup the read-only commands
// open (minus the CLI's os.Getwd / lock).
type readFixture struct {
	ctx   context.Context
	root  string
	ws    state.Workspace
	store *state.Store
	wt    *worktree.Manager
}

func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return &readFixture{ctx: context.Background(), root: root, ws: ws, store: store, wt: wt}
}

// mkFeature persists a minimal quick-route feature (FD-001) at the Spec
// stage, with an optional external ref. Each fixture drives a single feature.
func (f *readFixture) mkFeature(t *testing.T, ref string) domain.Feature {
	t.Helper()
	const num = 1
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify("json export")
	now := time.Now()
	feat := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFeature, Title: "JSON export", Slug: slug,
		Stage: domain.StageSpec, Skip: domain.QuickRoute(), Budget: domain.Budget{Envelope: 500},
		ExternalRef: ref, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateFeature(f.ctx, &feat); err != nil {
		t.Fatal(err)
	}
	return feat
}

// putDraft writes a spec draft for feat at its drafts home.
func (f *readFixture) putDraft(t *testing.T, feat *domain.Feature, body string) {
	t.Helper()
	if err := os.MkdirAll(f.ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ws.DraftsDir(), spec.DraftFilename(feat)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// resolveFeatureID finds a feature by its canonical id and by its external
// ref, and errors on an arg that is neither.
func TestResolveFeatureIDByIDAndRef(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "JIRA-9")

	byID, err := resolveFeatureID(f.ctx, f.store, string(feat.ID))
	if err != nil || byID.ID != feat.ID {
		t.Fatalf("by id = %+v, err=%v", byID, err)
	}
	byRef, err := resolveFeatureID(f.ctx, f.store, "JIRA-9")
	if err != nil || byRef.ID != feat.ID {
		t.Fatalf("by ref = %+v, err=%v; want %s", byRef, err, feat.ID)
	}
	if _, err := resolveFeatureID(f.ctx, f.store, "nope"); err == nil {
		t.Fatal("resolved an arg that is neither an id nor a ref")
	}
}

// status reports the stage, route, spend, an open %% question as a blocker,
// and a not-yet-created branch state.
func TestBuildStatusBlockersAndRoute(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "JIRA-9")
	f.putDraft(t, &feat, "# Spec\nThe toggle persists.\n%% @user(2026-01-01): per-device or synced?\n")

	v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat)
	if v.Stage != string(domain.StageSpec) {
		t.Fatalf("stage = %q, want spec", v.Stage)
	}
	if v.Route != "quick" {
		t.Fatalf("route = %q, want quick", v.Route)
	}
	if v.Blockers.OpenQuestions != 1 || v.Blockers.OpenDiff != 0 {
		t.Fatalf("blockers = %+v, want 1 open question / 0 diff", v.Blockers)
	}
	if v.Spend.Envelope != 500 {
		t.Fatalf("envelope = %d, want 500", v.Spend.Envelope)
	}
	if v.BranchState != "none" {
		t.Fatalf("branch_state = %q, want none (no worktree yet)", v.BranchState)
	}
	if v.Ref != "JIRA-9" {
		t.Fatalf("ref = %q, want JIRA-9", v.Ref)
	}
}

// mkVerifyFeature persists FD-001 at the Verify stage with a real commit on
// its branch, so branch_state reads "ahead" — the exact state that would trip
// a naive "stage==verify && ahead" verified guess mid-run.
func (f *readFixture) mkVerifyFeature(t *testing.T) domain.Feature {
	t.Helper()
	const num = 1
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify("json export")
	now := time.Now()
	feat := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFeature, Title: "JSON export", Slug: slug,
		Stage: domain.StageVerify, Skip: domain.QuickRoute(), Budget: domain.Budget{Envelope: 500},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateFeature(f.ctx, &feat); err != nil {
		t.Fatal(err)
	}
	if _, err := f.wt.Create(f.ctx, &feat); err != nil {
		t.Fatal(err)
	}
	wtPath, err := f.wt.Path(&feat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.wt.CommitAll(f.ctx, &feat, "work"); err != nil {
		t.Fatal(err)
	}
	return feat
}

// status derives `verified` from the persisted verify marker, not from a
// naive "at verify with an ahead branch" guess: a feature mid-verify reads
// verified:false even with an ahead branch (the false-positive guard), and
// only a stamped VerifiedAt — what the engine sets at the stop-at-verified
// gate — flips it true, while `done` stays false until an actual merge.
func TestBuildStatusVerified(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkVerifyFeature(t)

	// mid-verify: the branch is already ahead, but the verify gate has not
	// been crossed → not verified, not done.
	v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat)
	if v.BranchState != "ahead" {
		t.Fatalf("branch_state = %q, want ahead (setup)", v.BranchState)
	}
	if v.Verified {
		t.Fatal("verified=true mid-verify: false positive on an ahead branch")
	}
	if v.Done {
		t.Fatal("done=true before any merge")
	}

	// the engine stamps this marker when it reaches the stop-at-verified gate.
	if err := f.store.SetVerifiedAt(f.ctx, feat.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	verified, err := f.store.GetFeature(f.ctx, feat.ID)
	if err != nil {
		t.Fatal(err)
	}
	v = buildStatus(f.ctx, f.store, f.wt, f.ws, &verified)
	if !v.Verified {
		t.Fatal("verified=false at the terminal verified-branch state")
	}
	if v.Done {
		t.Fatal("done=true without a merge — verified is not done")
	}
}

// artifactPath resolves a draft, and gummi spec has bytes to print.
func TestArtifactPathResolvesDraft(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "")
	if p := artifactPath(f.wt, f.ws, &feat); p != "" {
		t.Fatalf("artifact resolved to %q before a draft exists", p)
	}
	f.putDraft(t, &feat, "# FD-001: JSON export\n\nbody\n")
	p := artifactPath(f.wt, f.ws, &feat)
	if p == "" {
		t.Fatal("artifact not resolved after writing a draft")
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) == 0 {
		t.Fatalf("reading resolved artifact: %v", err)
	}
}

// diff on a feature with no worktree fails clearly rather than dumping
// something misleading.
func TestDiffNoWorktreeErrors(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "")
	if _, err := f.wt.Diff(f.ctx, &feat); err == nil {
		t.Fatal("diff on a feature with no worktree returned no error")
	}
}

// status.Running reads the pid file: absent/stale reads false, a pid
// pointing at a live process reads true. This is the probe an orchestrating
// agent uses to tell an orphan gummi (wrapper died, gummi kept running)
// apart from a truly-dead run — the ambiguity the FD-001 report flagged.
func TestBuildStatusRunning(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "")

	// no pid file → not running.
	v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat)
	if v.Running {
		t.Fatal("running=true with no pid file recorded")
	}

	// a pid pointing at nobody → not running.
	if err := state.WritePIDFile(f.ws.PIDFile(), 1<<30); err != nil {
		t.Fatal(err)
	}
	v = buildStatus(f.ctx, f.store, f.wt, f.ws, &feat)
	if v.Running {
		t.Fatal("running=true for a pid that cannot exist")
	}

	// the current process is always alive → true.
	if err := state.WritePIDFile(f.ws.PIDFile(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	v = buildStatus(f.ctx, f.store, f.wt, f.ws, &feat)
	if !v.Running {
		t.Fatal("running=false with the pid file pointing at this test process")
	}
}
