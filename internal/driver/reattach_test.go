package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A run persists its gate-approval mode on the card, so a later resume can
// inherit it (the mode is not re-derived from the flag every invocation).
func TestRunPersistsGateApproval(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
	})
	out, err := h.driver(Options{GateApproval: GateOff}).Run(context.Background(), "a feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, err := h.store.GetFeature(context.Background(), domain.FeatureID(out.ID))
	if err != nil {
		t.Fatal(err)
	}
	if f.GateApproval != domain.GateOff {
		t.Fatalf("persisted gate-approval = %q, want %q", f.GateApproval, domain.GateOff)
	}
}

// A resume that does NOT re-pass --gate-approval inherits the mode the run
// persisted, rather than silently reverting to auto: a feature created under
// caller still checkpoints its design gate on a bare resume.
func TestResumeInheritsPersistedGate(t *testing.T) {
	h := newHarness(t, true, happyResumeScript())
	f := feature(1, domain.StageSpec)
	f.GateApproval = domain.GateOff
	putDraft(t, h, &f, "# Spec\nExport as JSON.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	d := h.driver(Options{}) // no --gate-approval on this resume
	out, err := d.Resume(context.Background(), f.ID, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// inherited caller mode → the spec gate checkpoints instead of auto-crossing.
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question (inherited caller gate); stream=%v", out.Status, h.eventKinds())
	}
	if d.opts.GateApproval != GateOff || d.actor != "caller" {
		t.Fatalf("driver gate = %q/actor %q, want caller/caller", d.opts.GateApproval, d.actor)
	}
}

// A resume that DOES re-pass --gate-approval overrides and re-persists the
// card's mode: caller→auto lets the design gate auto-cross to a verified
// branch, and the stored mode is updated so subsequent resumes inherit auto.
func TestResumeOverridesPersistedGate(t *testing.T) {
	h := newHarness(t, true, happyResumeScript())
	f := feature(1, domain.StageSpec)
	f.GateApproval = domain.GateOff
	putDraft(t, h, &f, "# Spec\nExport as JSON.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{GateApproval: GateGates, GateApprovalSet: true}).
		Resume(context.Background(), f.ID, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done (override to auto crosses the gate); stream=%v", out.Status, h.eventKinds())
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Fatalf("persisted gate-approval = %q, want %q (override re-persisted)", got.GateApproval, domain.GateGates)
	}
}

// Crossing into a feature's first worktree stage must baseline its
// gummi-checks the same way the TUI does off EnteredWorktree (msgs.go
// discoverChecks/baselineChecks): a headless drive with no shell loop to
// chain those passes onto still needs the artifact to gain the block, or
// a later crash mid-verify has nothing to cheaply re-attach to.
func TestHeadlessAdvanceBaselinesChecksAtWorktreeEntry(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				return msgIdle(o.Model, "```gummi-checks\n- name: smoke\n  cmd: \"true\"\n```")
			}
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
	if _, err := h.driver(Options{}).Run(context.Background(), "add a json export"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	f := h.storeFeature(t)
	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "```gummi-checks") {
		t.Fatalf("expected a gummi-checks block baselined at worktree entry, found none; spec:\n%s", raw)
	}

	// A crash mid-verify must find the baselined block on reattach and
	// finalize with no fresh agent pass.
	h.buf.Reset()
	out, err := h.driver(Options{}).Verify(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done (verify reattach on the baselined block); stream=%v", out.Status, h.eventKinds())
	}
}

// The worktree-entry discovery pass is best-effort: a scribe session that
// hits its budget cap (a soft stop, no trailing idle) must not wedge the
// drive forever waiting for an event that will never arrive — it should
// leave the gummi-checks block absent and let the drive continue.
func TestHeadlessAdvanceSurvivesDiscoveryBudgetExhaustion(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				return []agent.Event{{Kind: agent.EventBudgetExhausted}}
			}
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

	done := make(chan struct{})
	var out Outcome
	var runErr error
	go func() {
		out, runErr = h.driver(Options{}).Run(context.Background(), "add a json export")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung — discovery's budget-exhausted event never let the drive continue")
	}
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done (quick route stops at verified); stream=%v", out.Status, h.eventKinds())
	}

	f := h.storeFeature(t)
	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "```gummi-checks") {
		t.Fatalf("expected no gummi-checks block (discovery's scribe session hit budget exhaustion, not a usable reply); spec:\n%s", raw)
	}
}

// The worktree-entry discovery pass must not hang the drive forever when its
// scribe session stalls — streams a message but never reaches idle, error,
// or budget exhaustion. discoverAndBaselineChecks bounds the call with a
// deadline (the driver's --stage-timeout, or a small fixed default when
// unset) so a non-terminating session returns promptly, leaves the
// gummi-checks block absent, and lets the drive continue.
func TestHeadlessAdvanceSurvivesDiscoveryStall(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				// stall: a message with no trailing idle/error/exhausted, so
				// DiscoverChecks's event loop would wait forever without a
				// deadline on the context.
				return []agent.Event{{Kind: agent.EventMessage, Text: "still surveying"}}
			}
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

	done := make(chan struct{})
	var out Outcome
	var runErr error
	go func() {
		out, runErr = h.driver(Options{StageTimeout: 200 * time.Millisecond}).Run(context.Background(), "add a json export")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung — a stalling discovery scribe session was never bounded by a deadline")
	}
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done (quick route stops at verified); stream=%v", out.Status, h.eventKinds())
	}

	f := h.storeFeature(t)
	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "```gummi-checks") {
		t.Fatalf("expected no gummi-checks block (discovery's scribe session stalled, not a usable reply); spec:\n%s", raw)
	}
}

// `gummi verify` re-runs the spec's acceptance checks on the existing branch
// and finalizes the verify gate to a `done` with no fresh agent pass — the
// cheap re-attach for a run whose verify was cut off in the finalize tail.
func TestVerifyReattachFinalizes(t *testing.T) {
	h := driveToVerified(t)
	f := h.storeFeature(t)
	writeSpecChecks(t, h, f, "- name: smoke\n  cmd: \"true\"\n") // a passing check

	// a fresh driver models a fresh CLI process (`gummi verify FD-001`).
	out, err := h.driver(Options{}).Verify(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	if !h.has("verify") || !h.has("done") {
		t.Fatalf("missing verify/done milestones; stream=%v", h.eventKinds())
	}
	got := h.storeFeature(t)
	if got.VerifiedAt.IsZero() {
		t.Fatal("verify re-attach did not leave verified_at stamped")
	}
	if got.Stage != domain.StageVerify {
		t.Fatalf("stage = %q, want verify (gummi never merges)", got.Stage)
	}
}

// An unresolved user thread holds the finalize gate even when every
// acceptance check passes: `gummi verify` reports blocked (exit 3), not
// done, so the caller is not told the branch is ready to land while
// status --json still reports verified:false and the stage never moved.
func TestVerifyReattachBlockedByOpenThread(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageVerify) // parked at verify, verified_at unset
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// a passing check plus an unresolved USER thread that holds the gate open.
	writePromotedSpec(t, h, f,
		"## Verification plan\n\n```gummi-checks\n- name: smoke\n  cmd: \"true\"\n```\n"+
			"\n%% @user: does the new export cover nested records?\n")

	out, err := h.driver(Options{}).Verify(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked (open thread held the gate); stream=%v", out.Status, h.eventKinds())
	}
	if out.Status.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3 (blocked)", out.Status.ExitCode())
	}
	if ev := lastEvent(h, "blocked"); ev == nil || ev["open_questions"].(float64) != 1 {
		t.Fatalf("blocked event = %v, want open_questions=1", ev)
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageVerify {
		t.Fatalf("stage = %q, want verify (gate held open, not merged)", got.Stage)
	}
	if !got.VerifiedAt.IsZero() {
		t.Fatal("verified_at was stamped while the gate was blocked — status would report verified:true")
	}
}

// A live (non-pre-existing) acceptance-check failure blocks the cheap
// re-attach: `gummi verify` escalates instead of finalizing.
func TestVerifyReattachFailingChecksEscalate(t *testing.T) {
	h := driveToVerified(t)
	f := h.storeFeature(t)
	writeSpecChecks(t, h, f, "- name: smoke\n  cmd: \"false\"\n") // a failing check

	out, err := h.driver(Options{}).Verify(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation on a failing check; stream=%v", out.Status, h.eventKinds())
	}
	if ev := lastEvent(h, "escalation"); ev == nil || !strings.Contains(ev["reason"].(string), "smoke") {
		t.Fatalf("escalation event = %v, want it to name the failing check", ev)
	}
}

// A cheap re-attach it can't trust — here a spec with no gummi-checks — fails
// with exit 1 and points back to `resume`, never silently finalizing.
func TestVerifyReattachNoChecksIsUnavailable(t *testing.T) {
	h := driveToVerified(t)
	f := h.storeFeature(t)
	// a spec with a Verification section but no gummi-checks block.
	writePromotedSpec(t, h, f, "## Verification plan\n\nManual only.\n")

	out, err := h.driver(Options{}).Verify(context.Background(), f.ID)
	if out.Status != StatusError || err == nil {
		t.Fatalf("status/err = %q/%v, want error (unavailable) when no checks exist", out.Status, err)
	}
	if ev := lastEvent(h, "error"); ev == nil || !strings.Contains(ev["error"].(string), "resume") {
		t.Fatalf("error event = %v, want it to point back to resume", ev)
	}
}

// backendHint classifies the failures operators most often misread as a
// gummi bug (auth loss, a dead/stalled backend stream) and stays silent
// otherwise.
func TestBackendHint(t *testing.T) {
	cases := map[string]string{
		"API Error: Response stalled mid-stream":      "stream",
		"claude exited mid-session: 401 Unauthorized": "unauth",
		"request failed: forbidden":                   "unauth",
		"engine event stream closed":                  "stream",
		"unknown work item ID":                        "", // not a backend condition
	}
	for msg, want := range cases {
		got := backendHint(msg)
		switch {
		case want == "" && got != "":
			t.Errorf("backendHint(%q) = %q, want empty", msg, got)
		case want == "unauth" && !strings.Contains(got, "auth"):
			t.Errorf("backendHint(%q) = %q, want an auth hint", msg, got)
		case want == "stream" && got == "":
			t.Errorf("backendHint(%q) = empty, want a stream/backend hint", msg)
		}
	}
}

// --- helpers ----------------------------------------------------------

// driveToVerified runs the quick route end to end so the feature is parked
// at a verified branch (stage verify, branch ahead) — the starting point for
// a re-attach.
func driveToVerified(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				return msgIdle(o.Model, "```gummi-checks\n- name: smoke\n  cmd: \"true\"\n```")
			}
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
	if _, err := h.driver(Options{}).Run(context.Background(), "add a json export"); err != nil {
		t.Fatalf("drive to verified: %v", err)
	}
	// a fresh buffer so re-attach assertions see only the verify run's stream.
	h.buf.Reset()
	return h
}

// storeFeature returns the single feature from the store.
func (h *harness) storeFeature(t *testing.T) domain.Feature {
	t.Helper()
	f, err := h.store.GetFeature(context.Background(), h.only())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// writePromotedSpec overwrites the feature's promoted spec (its workspace
// home, where the engine reads acceptance checks from).
func writePromotedSpec(t *testing.T, h *harness, f domain.Feature, body string) {
	t.Helper()
	path := filepath.Join(h.root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeSpecChecks writes a spec whose Verification plan carries a
// gummi-checks block with the given YAML entries.
func writeSpecChecks(t *testing.T, h *harness, f domain.Feature, entries string) {
	t.Helper()
	body := "## Verification plan\n\n```gummi-checks\n" + entries + "```\n"
	writePromotedSpec(t, h, f, body)
}
