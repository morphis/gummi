package engine

import (
	"context"
	"encoding/json"
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

// fillPromotedSection overwrites a section's body in a feature's promoted
// (post gate) artifact — the workspace-home copy at f.ArtifactPath() — so a
// forward-walk test can supply the content a later gate's requiredSections
// row expects, the same way a real coding-stage agent would have written it.
func fillPromotedSection(t *testing.T, wt *worktree.Manager, f domain.Feature, name, body string) {
	t.Helper()
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := spec.ReplaceSection(string(raw), name, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gatedVerifyBug returns a bug parked at the verify stage with a worktree,
// a bug report containing verificationBody, and a branch that is ahead of
// main by one commit. The caller must have configured env probes so that at
// least one probes clean-present for the omission gate to arm.
func gatedVerifyBug(t *testing.T, store *state.Store, wt *worktree.Manager, verificationBody string) domain.Feature {
	t.Helper()
	f := bugFeature("gated verify bug")
	f.Stage = domain.StageVerify
	putFeature(t, store, f)
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, verificationBody)

	wtDir := filepath.Join(wt.Root(), f.WorktreePath())
	if err := os.WriteFile(filepath.Join(wtDir, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wtDir, "add", "fix.txt")
	gitIn(t, wtDir, "commit", "-q", "-m", "fix")
	return f
}

func mustAdvance(t *testing.T, e *Engine, id domain.FeatureID) AdvanceResult {
	t.Helper()
	res, err := e.Advance(context.Background(), id, "user")
	if err != nil {
		t.Fatalf("Advance %s: %v", id, err)
	}
	return res
}

// gateEvents filters a card's event log down to its EventGate rows.
func gateEvents(t *testing.T, store *state.Store, id domain.FeatureID) []state.CardEvent {
	t.Helper()
	evs, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.CardEvent
	for _, ev := range evs {
		if ev.Kind == state.EventGate {
			out = append(out, ev)
		}
	}
	return out
}

// TestAdvanceRecordsGateEvent: a successful crossing appends a
// state.EventGate carrying exactly the actor Advance was called with —
// the decision receipt (internal/ui) reads this back to count only the
// gates autopilot ("auto") crossed on its own, never a human's.
func TestAdvanceRecordsGateEvent(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dark mode", domain.StageTodo)
	putFeature(t, store, f)

	if _, err := e.Advance(ctx, f.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	gates := gateEvents(t, store, f.ID)
	if len(gates) != 1 {
		t.Fatalf("gate events = %+v, want exactly 1", gates)
	}
	var p state.GatePayload
	if err := json.Unmarshal([]byte(gates[0].Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.From != string(domain.StageTodo) || p.To != string(domain.StagePlan) || p.Actor != "auto" {
		t.Errorf("gate payload = %+v, want from=%s to=%s actor=auto", p, domain.StageTodo, domain.StagePlan)
	}
}

// TestAdvanceGateEventsOneRowPerCrossing walks a card through several
// crossings under different actors and checks each gets its own row, in
// order, rather than colliding on a shared dedupe key.
func TestAdvanceGateEventsOneRowPerCrossing(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dark mode", domain.StageTodo)
	putFeature(t, store, f)

	actors := []string{"user", "auto", "caller"}
	for _, actor := range actors {
		if _, err := e.Advance(ctx, f.ID, actor); err != nil {
			t.Fatalf("Advance(%s): %v", actor, err)
		}
	}
	gates := gateEvents(t, store, f.ID)
	if len(gates) != len(actors) {
		t.Fatalf("gate events = %+v, want %d (one per crossing)", gates, len(actors))
	}
	for i, want := range actors {
		var p state.GatePayload
		if err := json.Unmarshal([]byte(gates[i].Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.Actor != want {
			t.Errorf("gate[%d].Actor = %q, want %q", i, p.Actor, want)
		}
	}
}

// TestAdvanceNoopWritesNoGateEvent: a terminal card has nothing to
// advance — Advance never even reaches the transition, so no gate event
// should appear.
func TestAdvanceNoopWritesNoGateEvent(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dark mode", domain.StageDone)
	putFeature(t, store, f)

	res, err := e.Advance(ctx, f.ID, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("status = %v, want StatusNoop", res.Status)
	}
	if gates := gateEvents(t, store, f.ID); len(gates) != 0 {
		t.Fatalf("gate events = %+v, want none for a noop advance", gates)
	}
}

// A feature walks the full forward floor; leaving Spec creates the
// worktree and promotes the artifact to its workspace home.
func TestAdvanceForwardWalkFeature(t *testing.T) {
	e, ws, store, wt := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "dark mode", domain.StageTodo)
	putFeature(t, store, f)

	// todo → plan: the kickoff hop, no worktree yet
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StagePlan {
		t.Fatalf("todo kickoff: status=%d to=%s", res.Status, res.To)
	}
	if res.EnteredWorktree {
		t.Fatal("worktree created at the todo kickoff")
	}
	if ok, _ := wt.Exists(ctx, &f); ok {
		t.Fatal("worktree exists before the design gate")
	}

	// the design gate owes both of the merged stage's sections
	fillPromotedSection2 := func(section, body string) {
		draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
		raw, err := os.ReadFile(draft)
		if err != nil {
			t.Fatal(err)
		}
		updated, _, err := spec.ReplaceSection(string(raw), section, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(draft, []byte(updated), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := spec.EnsureDraft(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)), &f); err != nil {
		t.Fatal(err)
	}
	fillPromotedSection2("Chosen approach", "\nA settings toggle.\n\n")
	fillPromotedSection2("Implementation notes", "\nAdd a settings toggle; persist per-device.\n\n")

	// plan → implement: the approval gate cuts the worktree and promotes
	// the artifact to its workspace home
	res = mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageImplement {
		t.Fatalf("design approval: status=%d to=%s", res.Status, res.To)
	}
	if !res.EnteredWorktree {
		t.Fatal("design approval did not report EnteredWorktree")
	}
	if ok, _ := wt.Exists(ctx, &f); !ok {
		t.Fatal("worktree missing after the design gate")
	}
	if _, err := os.Stat(filepath.Join(wt.Root(), f.ArtifactPath())); err != nil {
		t.Fatalf("artifact not promoted to its workspace home: %v", err)
	}

	// implement → verify: no further worktree creation
	res = mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageVerify {
		t.Fatalf("advance to verify: status=%d to=%s", res.Status, res.To)
	}
	if res.EnteredWorktree {
		t.Fatal("worktree re-created leaving into verify")
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

	for _, want := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
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
func TestAdvanceBlockedByQuestions(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := feature(1, "gated", domain.StagePlan)
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
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StagePlan {
		t.Fatalf("blocked gate still transitioned to %s", got.Stage)
	}
}

// Unresolved diff annotations block the gate on the diff backend.
func TestAdvanceBlockedByDiff(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	f := feature(1, "diff gated", domain.StageImplement)
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
		f := feature(1, "ship it", domain.StagePlan)
		putFeature(t, store, f)
		mustAdvance(t, e, f.ID) // spec → plan, creates the worktree
		// the plan→implement gate expects Implementation notes drafted.
		fillPromotedSection(t, wt, f, "Implementation notes", "Add a settings toggle; persist per-device.")
		// walk to verify
		for stage := domain.StagePlan; stage != domain.StageVerify; {
			res := mustAdvance(t, e, f.ID)
			if res.Status != StatusAdvanced {
				t.Fatalf("walk to verify: status=%d at %s, want advanced", res.Status, stage)
			}
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

		// the verify→done gate expects Verification plan drafted.
		fillPromotedSection(t, wt, f, "Verification plan", "Run the repo's discovered checks.")

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
		e, _, store, wt := advanceEngine(t)
		f := feature(2, "nothing to land", domain.StagePlan)
		putFeature(t, store, f)
		for stage := domain.StagePlan; stage != domain.StageVerify; {
			res := mustAdvance(t, e, f.ID)
			if res.Status != StatusAdvanced {
				t.Fatalf("walk to verify: status=%d at %s, want advanced", res.Status, stage)
			}
			stage = res.To
			if stage == domain.StagePlan {
				// the plan→implement gate (the next crossing) expects
				// Implementation notes drafted — the blank template
				// promoted at spec approval leaves it empty.
				fillPromotedSection(t, wt, f, "Implementation notes", "Add a settings toggle; persist per-device.")
			}
		}
		// the verify→done gate expects Verification plan drafted.
		fillPromotedSection(t, wt, f, "Verification plan", "Run the repo's discovered checks.")

		res := mustAdvance(t, e, f.ID)
		if res.Status != StatusAdvanced || res.To != domain.StageDone {
			t.Fatalf("empty branch: status=%d to=%s, want advanced/done", res.Status, res.To)
		}
	})
}

func TestAdvance_OmissionGate_Blocks(t *testing.T) {
	ctx := context.Background()
	e, ws, store, wt := advanceEngine(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := gatedVerifyBug(t, store, wt, "Run local unit tests only.")
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedOmission {
		t.Fatalf("status=%d, want blocked-omission", res.Status)
	}
	if res.Reason == "" {
		t.Fatal("blocked-omission result has no reason")
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.Stage != domain.StageVerify {
		t.Fatalf("blocked gate transitioned to %s", got.Stage)
	}
	if got, _ := store.GetFeature(ctx, f.ID); !got.VerifiedAt.IsZero() {
		t.Fatalf("verified_at stamped on blocked omission gate: %v", got.VerifiedAt)
	}
}

func TestAdvance_OmissionGate_WaiverPasses(t *testing.T) {
	ctx := context.Background()
	e, ws, store, wt := advanceEngine(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The waiver line disarms the omission gate; a resolution marker below
	// it closes the open thread so Advance's open-question gate does not
	// also fire.
	body := "%% @user: no-live-check docker unavailable in this sandbox\n%% @user(2026-08-21): resolved\n\nRun local unit tests only."
	f := gatedVerifyBug(t, store, wt, body)
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusNeedsMerge {
		t.Fatalf("status=%d, want needs-merge", res.Status)
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.VerifiedAt.IsZero() {
		t.Fatal("needs-merge gate did not persist verified_at with waiver")
	}
}

func TestAdvance_OmissionGate_EnvTagPasses(t *testing.T) {
	ctx := context.Background()
	e, ws, store, wt := advanceEngine(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := gatedVerifyBug(t, store, wt, "Run the docker check [env: docker].")
	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusNeedsMerge {
		t.Fatalf("status=%d, want needs-merge", res.Status)
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.VerifiedAt.IsZero() {
		t.Fatal("needs-merge gate did not persist verified_at with env tag")
	}
}

func TestAdvance_OmissionGate_FeatureKindPasses(t *testing.T) {
	ctx := context.Background()
	e, ws, store, wt := advanceEngine(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := feature(1, "gated feature", domain.StageVerify)
	putFeature(t, store, f)
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: x\n  cmd: echo ok\n")

	wtDir := filepath.Join(wt.Root(), f.WorktreePath())
	if err := os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wtDir, "add", "work.txt")
	gitIn(t, wtDir, "commit", "-q", "-m", "work")

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusNeedsMerge {
		t.Fatalf("status=%d, want needs-merge", res.Status)
	}
	if got, _ := store.GetFeature(ctx, f.ID); got.VerifiedAt.IsZero() {
		t.Fatal("needs-merge gate did not persist verified_at for feature")
	}
}

func TestAdvance_OmissionGate_FallsThroughOnUnreadableArtifact(t *testing.T) {
	e, ws, store, wt := advanceEngine(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := gatedVerifyBug(t, store, wt, "Run local unit tests only.")
	// Replace the artifact file with a directory so os.ReadFile fails.
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o750); err != nil {
		t.Fatal(err)
	}

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusNeedsMerge {
		t.Fatalf("status=%d, want needs-merge (unreadable artifact falls through)", res.Status)
	}
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
	f := feature(2, "new", domain.StagePlan)
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
	f := feature(2, "new", domain.StagePlan)
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
	f := feature(2, "new", domain.StagePlan)
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

	for _, st := range []domain.Stage{domain.StageVerify, domain.StageDone} {
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
		f := feature(1, "designing", domain.StagePlan)
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
		// the design gate IS the coding-stage entry now: one design stage
		// means plan → implement is both, so a dependency blocks here.
		if len(deps) != 1 {
			t.Fatalf("design-stage DependencyBlockers = %+v, want the unmet dep", deps)
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
	if res.Status != StatusAdvanced || res.To != domain.StagePlan {
		t.Fatalf("status=%d to=%s, want advanced/brainstorm (design edge not gated)", res.Status, res.To)
	}
}

// --- research workflow (worktree-less routing) ---

// A research card walks its whole graph without ever materializing a
// worktree: research routes investigate/shape/review/verify/done all
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
		domain.StagePlan, domain.StageImplement, domain.StageVerify, domain.StageDone,
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
	f := feature(1, "research no land", domain.StagePlan)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	putFeature(t, store, f)

	for stage := domain.StagePlan; stage != domain.StageVerify; {
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

// TestAdvanceVerifyDocument mirrors verifychecks_test.go's shape for the
// new deterministic document gate: a research card at verify whose
// artifact fails the citation floor (no open user threads, so the new
// verifydoc gate is exercised rather than StatusBlockedQuestions) stays at
// verify with the broken citation named in the report; fixing the
// citation advances it straight to done.
func TestAdvanceVerifyDocument(t *testing.T) {
	e, ws, store, wt := advanceEngine(t)
	f := feature(1, "rs verify", domain.StageVerify)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	putFeature(t, store, f)

	if err := os.MkdirAll(ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	// Both documents draft `## Findings`: the done edge's undrafted gate
	// runs ahead of the document floor, so a document still holding that
	// section's template prompt would be held there and never reach the
	// citation check this test is about.
	failing := "# RS-001: rs verify\n\n## Findings\n\n" +
		"Broken cite `internal/missing.go:1` here.\n"
	if err := os.WriteFile(draft, []byte(failing), 0o600); err != nil {
		t.Fatal(err)
	}

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedDocument {
		t.Fatalf("status=%d, want StatusBlockedDocument", res.Status)
	}
	if len(res.DocumentReport.Citations) != 1 {
		t.Fatalf("DocumentReport.Citations = %+v, want exactly 1 issue", res.DocumentReport.Citations)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StageVerify {
		t.Fatalf("blocked document gate still transitioned to %s", got.Stage)
	}

	// fix the citation: an existing, in-range file under the repo root
	if err := os.MkdirAll(filepath.Join(wt.RepoRoot(), "internal"), 0o750); err != nil {
		t.Fatal(err)
	}
	cited := filepath.Join(wt.RepoRoot(), "internal", "foo.go")
	if err := os.WriteFile(cited, []byte("package foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	passing := "# RS-001: rs verify\n\n## Findings\n\n" +
		"Cite `internal/foo.go:1` here.\n"
	if err := os.WriteFile(draft, []byte(passing), 0o600); err != nil {
		t.Fatal(err)
	}

	res = mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageDone {
		t.Fatalf("passing document: status=%d to=%s, want advanced/done", res.Status, res.To)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StageDone {
		t.Fatalf("research card did not reach done: %s", got.Stage)
	}
}

// TestRequiredSections is a table test over the free function's whole
// contract: what each gate owes, and nothing for every edge that owes
// nothing. Research is in the table now — its edges used to return nil on
// the theory that verifydoc covered the done edge, which it only does for
// a document that has citations to break in the first place.
func TestRequiredSections(t *testing.T) {
	cases := []struct {
		name string
		kind domain.Kind
		from domain.Stage
		to   domain.Stage
		want []string
	}{
		{
			"feature's design gate owes both sections", domain.KindFeature,
			domain.StagePlan, domain.StageImplement,
			[]string{"Chosen approach", "Implementation notes"},
		},
		{
			"bug's design gate owes the root cause", domain.KindBug,
			domain.StagePlan, domain.StageImplement,
			[]string{"Root cause"},
		},
		{
			"feature to done", domain.KindFeature, domain.StageVerify, domain.StageDone,
			[]string{"Verification plan"},
		},
		{
			"bug to done", domain.KindBug, domain.StageVerify, domain.StageDone,
			[]string{"Verification"},
		},
		{
			// what the shape contract's own stop condition names
			"research's design gate owes question, constraints, direction", domain.KindResearch,
			domain.StagePlan, domain.StageImplement,
			[]string{"Questions", "Constraints", "Direction"},
		},
		{
			"research's build gate owes the survey", domain.KindResearch,
			domain.StageImplement, domain.StageVerify,
			[]string{"Findings"},
		},
		{
			// Findings only: Slices is deliberately not owed, because a
			// zero-slice RS is a legitimate terminal (see requiredSections)
			"research to done owes findings", domain.KindResearch, domain.StageVerify,
			domain.StageDone,
			[]string{"Findings"},
		},
		{
			"the todo kickoff owes nothing", domain.KindFeature, domain.StageTodo,
			domain.StagePlan, nil,
		},
		{
			"implement to verify owes nothing", domain.KindFeature,
			domain.StageImplement, domain.StageVerify, nil,
		},
		{
			"research's todo kickoff owes nothing either", domain.KindResearch,
			domain.StageTodo, domain.StagePlan, nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requiredSections(c.kind, c.from, c.to)
			if len(got) != len(c.want) {
				t.Fatalf("requiredSections(%s, %s, %s) = %v, want %v", c.kind, c.from, c.to, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("requiredSections(%s, %s, %s) = %v, want %v", c.kind, c.from, c.to, got, c.want)
				}
			}
		})
	}
}

// writeDraftBody drops body at the draft location for f — the pre-promotion
// home every design-stage artifact lives at before its stage's approval
// gate promotes it into the workspace or worktree.
func writeDraftBody(t *testing.T, ws state.Workspace, f domain.Feature, body string) {
	t.Helper()
	if err := os.MkdirAll(ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAdvanceBlockedByUndraftedChosenApproach is the whole point of this
// change: the measured failure (4/9 spec sessions drafted nothing, the run
// still recorded an auto-approved gate and finished verified) was an
// auto-approved crossing, so the fix must hold even under GateAutopilot, which
// no other floor check in Advance treats specially — the gate is a
// property of the artifact, not of who is watching it. The assertion goes
// through the real e.Advance blocker chain, not requiredSections or
// undraftedBlockingGate directly.
func TestAdvanceBlockedByUndraftedChosenApproach(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := feature(1, "dark mode", domain.StagePlan)
	f.GateApproval = domain.GateAutopilot
	putFeature(t, store, f)

	writeDraftBody(t, ws, f, "# Spec\n\n## Problem\n\nToggle needed.\n\n"+
		"## Chosen approach\n\n%% @gummi: converge on one during the plan stage\n")

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedUndrafted {
		t.Fatalf("status=%d, want StatusBlockedUndrafted", res.Status)
	}
	// both design sections: the merged stage owns what spec and plan each
	// owned before, so its gate owes both
	if len(res.Undrafted) != 2 || res.Undrafted[0] != "Chosen approach" {
		t.Fatalf("Undrafted = %v, want [Chosen approach Implementation notes]", res.Undrafted)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StagePlan {
		t.Fatalf("blocked undrafted gate still transitioned to %s", got.Stage)
	}
}

// A drafted Chosen approach section is not a false positive: the gate
// passes and the crossing proceeds normally.
func TestAdvanceNotBlockedWhenChosenApproachDrafted(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := feature(1, "dark mode", domain.StagePlan)
	putFeature(t, store, f)

	writeDraftBody(t, ws, f, "# Spec\n\n## Problem\n\nToggle needed.\n\n"+
		"## Chosen approach\n\nStore the preference per-device.\n\n"+
		"## Implementation notes\n\nAdd the toggle; persist per-device.\n")

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageImplement {
		t.Fatalf("status=%d to=%s, want advanced/implement", res.Status, res.To)
	}
}

// A bug leaving diagnose with an undrafted Root cause blocks the same way,
// on the bug graph's own required section.
func TestAdvanceBlockedByUndraftedRootCause(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := bugFeature("crash on empty input")
	f.Stage = domain.StagePlan
	putFeature(t, store, f)

	writeDraftBody(t, ws, f, "# Report\n\n## Summary\n\nCrashes on empty input.\n\n"+
		"## Root cause\n\n%% @gummi: the plan stage records the root cause here — the why\n")

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedUndrafted {
		t.Fatalf("status=%d, want StatusBlockedUndrafted", res.Status)
	}
	if len(res.Undrafted) != 1 || res.Undrafted[0] != "Root cause" {
		t.Fatalf("Undrafted = %v, want [Root cause]", res.Undrafted)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StagePlan {
		t.Fatalf("blocked undrafted gate still transitioned to %s", got.Stage)
	}
}

// A card whose artifact does not exist on disk at all — never drafted, or
// moved out from under it — must not block: the zero-on-error contract
// proves a missing artifact can never wedge a gate shut permanently.
func TestAdvanceUndraftedGateFallsThroughOnMissingArtifact(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	f := feature(1, "no artifact yet", domain.StagePlan)
	putFeature(t, store, f)

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusAdvanced || res.To != domain.StageImplement {
		t.Fatalf("status=%d to=%s, want advanced/implement (missing artifact must fall through)", res.Status, res.To)
	}
}

// TestResearchGatesOweSectionsOnEveryEdge walks the blank research
// template past all three research edges. It is the regression for a
// research card that went todo→done in four keypresses, spending nothing,
// with all nine sections still holding their `%% @gummi:` prompts:
// requiredSections had no research row at all, so every research gate
// returned nil and the verifydoc floor on the done edge passed vacuously
// (no citations to break, no questions to leave unanswered).
//
// The done edge owes Findings alone — see requiredSections for why Slices
// is deliberately not required, and TestResearchDoneGateAllowsAZeroSliceCard
// for the case that would break if it were.
func TestResearchGatesOweSectionsOnEveryEdge(t *testing.T) {
	f := researchFeature(domain.StagePlan)
	blank := spec.ResearchTemplate(&f)

	for _, tc := range []struct {
		name     string
		from, to domain.Stage
		want     []string
	}{
		{"design", domain.StagePlan, domain.StageImplement, []string{"Questions", "Constraints", "Direction"}},
		{"build", domain.StageImplement, domain.StageVerify, []string{"Findings"}},
		{"done", domain.StageVerify, domain.StageDone, []string{"Findings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := UndraftedGateSections(domain.KindResearch, tc.from, tc.to, blank)
			if len(got) != len(tc.want) {
				t.Fatalf("undrafted = %v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Fatalf("undrafted = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A research document whose sections were actually written crosses every
// one of those edges: the gate must not be a standing "no" for research
// the way returning nil made it a standing "yes".
func TestResearchGatesPassOnADraftedDocument(t *testing.T) {
	doc := "# RS-003: quota accounting\n\n" +
		"## Questions\n\nDoes the meter double-count a retried call?\n\n" +
		"## Constraints\n\nRead-only; one week.\n\n" +
		"## Findings\n\nThe retry path re-enters the meter at `internal/meter.go:42`.\n\n" +
		"## Direction\n\nMeter at the transport seam, not the call site.\n\n" +
		"## Slices\n\n```yaml\n- title: move the meter\n  one-liner: meter at the transport seam\n" +
		"  depends-on: []\n  requirements: []\n  id: \"\"\n```\n"

	for _, edge := range [][2]domain.Stage{
		{domain.StagePlan, domain.StageImplement},
		{domain.StageImplement, domain.StageVerify},
		{domain.StageVerify, domain.StageDone},
	} {
		if got := UndraftedGateSections(domain.KindResearch, edge[0], edge[1], doc); got != nil {
			t.Errorf("%s→%s: undrafted = %v, want none", edge[0], edge[1], got)
		}
	}
}

// TestResearchDoneGateAllowsAZeroSliceCard pins the one thing the done
// edge must NOT do. `## Slices` is the decomposition's input, so requiring
// it here looks obviously right and is wrong: "this needs no follow-on
// work" is a conclusion research is allowed to reach, and the rest of the
// system already treats it as terminal rather than broken —
// DecomposeForCard no-ops on a doc with no unsettled rows, and
// TestZeroSliceRSExitsDoneCleanly drives such a card to done without ever
// spawning an architect. An earlier pass at this gate did demand Slices
// and broke all four of those paths at once.
func TestResearchDoneGateAllowsAZeroSliceCard(t *testing.T) {
	doc := "# RS-003: quota accounting\n\n" +
		"## Findings\n\nThe retry path re-enters the meter at `internal/meter.go:42`.\n\n" +
		"## Slices\n\n%% @gummi: the proposed follow-on work, one row per slice\n"

	if got := UndraftedGateSections(domain.KindResearch, domain.StageVerify, domain.StageDone, doc); len(got) != 0 {
		t.Fatalf("undrafted = %v, want none: a surveyed card with nothing to mint still reaches done", got)
	}
	// The evidence is still owed, so the gate has not simply been switched off.
	empty := "# RS-003: quota accounting\n\n## Findings\n\n%% @gummi: what the investigation learned\n"
	if got := UndraftedGateSections(domain.KindResearch, domain.StageVerify, domain.StageDone, empty); len(got) != 1 || got[0] != "Findings" {
		t.Fatalf("undrafted = %v, want [Findings]", got)
	}
}

// TestAdvanceBlockedByUndraftedResearchDocument drives the blank template
// through the real e.Advance blocker chain, under GateAutopilot — the way
// the four-keypress walk happened. The `%% @gummi:` prompts are gummi's
// own markers, so the open-questions blocker never sees them: the
// undrafted gate is the only thing standing between a template and a
// finished research card.
func TestAdvanceBlockedByUndraftedResearchDocument(t *testing.T) {
	e, ws, store, _ := advanceEngine(t)
	f := researchFeature(domain.StagePlan)
	f.GateApproval = domain.GateAutopilot
	putFeature(t, store, f)
	writeDraftBody(t, ws, f, spec.ResearchTemplate(&f))

	res := mustAdvance(t, e, f.ID)
	if res.Status != StatusBlockedUndrafted {
		t.Fatalf("status=%d, want StatusBlockedUndrafted", res.Status)
	}
	if len(res.Undrafted) != 3 || res.Undrafted[0] != "Questions" {
		t.Fatalf("Undrafted = %v, want [Questions Constraints Direction]", res.Undrafted)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); got.Stage != domain.StagePlan {
		t.Fatalf("blocked undrafted gate still transitioned to %s", got.Stage)
	}
}
