package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// The quick route runs end to end to a verified branch and STOPS there —
// the feature never advances to Done (gummi never merges).
func TestQuickRouteToVerified(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// leave real work so the engine's checkpoint commits it and the
			// branch is genuinely ahead — proving stop-at-verified is a
			// NeedsMerge, not an empty-branch shortcut.
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
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	id := domain.FeatureID(out.ID)
	if st := h.stageOf(id); st != domain.StageVerify {
		t.Fatalf("feature at %s, want Verify (stop-at-verified never merges to Done)", st)
	}
	// the stop-at-verified gate stamps the verify marker `status --json`'s
	// `verified` reads, even though the stage stays at Verify (never merged).
	if f, err := h.store.GetFeature(context.Background(), id); err != nil {
		t.Fatal(err)
	} else if f.VerifiedAt.IsZero() {
		t.Fatal("reached a verified branch but verified_at was not stamped")
	}
	if !h.has("created") || !h.has("gate") || !h.has("done") {
		t.Fatalf("missing created/gate/done; stream=%v", h.eventKinds())
	}
	if h.eventKinds()[0] != "created" {
		t.Fatalf("first line = %q, want created (FD correlation)", h.eventKinds()[0])
	}
}

// An empty branch (no committed work) lands nothing: verify-pass advances
// straight to Done via the same crossGate, still without a merge.
func TestQuickRouteEmptyBranchToDone(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "tweak")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if st := h.stageOf(domain.FeatureID(out.ID)); st != domain.StageDone {
		t.Fatalf("empty branch feature at %s, want Done", st)
	}
}

// A design question (convention path) checkpoints as `question` with a
// non-zero exit; resume --answer continues to a verified branch.
func TestSpecQuestionThenResume(t *testing.T) {
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return convAsk(o.Model, "Include a schema header?", "no (recommended)", "yes")
			}
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}
	q := lastEvent(h, "question")
	if q == nil {
		t.Fatalf("no question event; stream=%v", h.eventKinds())
	}
	if q["recommended"] != "no (recommended)" {
		t.Fatalf("recommended = %v, want the marked option", q["recommended"])
	}

	ans := "no"
	out2, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Answer: &ans})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusDone {
		t.Fatalf("resume status = %q, want done; stream=%v", out2.Status, h.eventKinds())
	}
}

// --autonomous auto-takes the recommended option instead of checkpointing.
func TestAutonomousAutoAnswers(t *testing.T) {
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return convAsk(o.Model, "Schema header?", "no (recommended)", "yes")
			}
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
	})
	out, err := h.driver(Options{Autonomous: true}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done (auto-answered); stream=%v", out.Status, h.eventKinds())
	}
	if h.has("question") {
		t.Fatalf("--autonomous still emitted a question; stream=%v", h.eventKinds())
	}
}

// Review requesting changes bounces to implement (under the cap) and
// re-reviews; a subsequent pass reaches a verified branch.
func TestReviewChangesThenPass(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return toolVerdict(o.Model, "changes")
			}
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	// two review stages were entered (the bounce re-reviewed).
	if h.calls[domain.StageReview] != 2 {
		t.Fatalf("review entered %d times, want 2", h.calls[domain.StageReview])
	}
	if d := lastEvent(h, "done"); d == nil || d["review_rounds"].(float64) != 2 {
		t.Fatalf("done review_rounds = %v, want 2", d)
	}
}

// Review still requesting changes past the cap escalates (non-zero exit).
func TestReviewCapEscalates(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "changes") // never satisfied
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}
}

// Verify failing / blocked escalates rather than merging. The `next` field
// on a verify-fail (the human's remediation is to rewind to implement)
// names `--bounce`, so a caller driving the stream doesn't have to recall
// which verb un-parks the stop; a blocked verify — where re-implementing
// won't help — keeps the bare `resume`, pointing the human at the
// environment/plan instead.
func TestVerifyFailEscalates(t *testing.T) {
	for _, tc := range []struct {
		verdict  string
		wantNext string
	}{
		{"fail", ` --bounce --note "<why>"`},
		{"blocked", ""},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			h := newHarness(t, true, map[domain.Stage]stageFn{
				domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
					return msgIdle(o.Model, "Spec.")
				},
				domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
					return toolVerdict(o.Model, "pass")
				},
				domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
					return toolVerdict(o.Model, tc.verdict)
				},
			})
			out, err := h.driver(Options{}).Run(context.Background(), "feature")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if out.Status != StatusEscalation {
				t.Fatalf("verify %s: status = %q, want escalation", tc.verdict, out.Status)
			}
			if st := h.stageOf(domain.FeatureID(out.ID)); st != domain.StageVerify {
				t.Fatalf("verify %s: feature at %s, want Verify (not merged)", tc.verdict, st)
			}
			want := "gummi resume " + out.ID + tc.wantNext
			if e := lastEvent(h, "escalation"); e == nil || e["next"] != want {
				t.Fatalf("verify %s escalation.next = %v, want %q", tc.verdict, e["next"], want)
			}
		})
	}
}

// A verify-fail escalation is un-parked by `resume --bounce`: the driver
// rewinds the feature to its work stage (Implement for a feature, Fix for a
// bug), so the review → verify tail runs again. The --note becomes an
// addendum to the reborn implement kickoff — the same channel the diff
// surface's request-changes takes via Engine.RunWith.
func TestResumeBounceRewindsAndCompletes(t *testing.T) {
	var implementCalls []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, msg string) []agent.Event {
			implementCalls = append(implementCalls, msg)
			_ = os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600)
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return toolVerdict(o.Model, "fail") // first pass fails → escalation
			}
			return toolVerdict(o.Model, "pass") // after the bounce, the reworked branch passes
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("initial status = %q, want escalation (verify fail); stream=%v", out.Status, h.eventKinds())
	}
	if st := h.stageOf(domain.FeatureID(out.ID)); st != domain.StageVerify {
		t.Fatalf("stage = %s, want Verify (parked at the failed gate)", st)
	}

	h.buf.Reset()
	note := "verify hit a timing race in the exporter; guard the flush"
	out2, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Bounce: &note})
	if err != nil {
		t.Fatalf("Resume(bounce): %v", err)
	}
	if out2.Status != StatusDone {
		t.Fatalf("resume status = %q, want done (bounce → implement → review → verify pass); stream=%v",
			out2.Status, h.eventKinds())
	}
	// two implement runs total: the original one before the failed verify, and
	// the reborn one after --bounce.
	if h.calls[domain.StageImplement] != 2 {
		t.Fatalf("implement entered %d times, want 2 (bounce should re-run it)", h.calls[domain.StageImplement])
	}
	if len(implementCalls) != 2 {
		t.Fatalf("captured %d implement kickoff messages, want 2", len(implementCalls))
	}
	// the second implement kickoff must carry the --note addendum; the first
	// (fresh run) must not (nothing to reference yet).
	if strings.Contains(implementCalls[0], note) {
		t.Fatalf("fresh implement kickoff already carried the bounce note:\n%s", implementCalls[0])
	}
	if !strings.Contains(implementCalls[1], note) {
		t.Fatalf("reborn implement kickoff missing the --note addendum:\n%s", implementCalls[1])
	}
}

// A --bounce landing on a stage that is neither review nor verify is a
// usage error: the driver refuses to rewind (there is no forward-facing
// bounce edge from anywhere else) rather than silently transitioning.
func TestResumeBounceRefusesOffStage(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageSpec)
	putDraft(t, h, &f, "# Spec\nExport as JSON.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	empty := ""
	out, err := h.driver(Options{}).Resume(context.Background(), f.ID, ResumeInput{Bounce: &empty})
	if err == nil {
		t.Fatal("Resume(bounce) at Spec: want an error, got nil")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	// the feature must not have moved off Spec.
	if st := h.stageOf(f.ID); st != domain.StageSpec {
		t.Fatalf("Spec advanced to %s despite a refused bounce", st)
	}
}

// A budget-exhausted stage fails loud with the exhausted exit and carries
// the check_running precondition an orchestrating caller uses to catch an
// orphan gummi before following `next` (which would hit ErrLocked).
func TestBudgetExhausted(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
		domain.StageImplement: func(_ *harness, _ int, _ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	ev := lastEvent(h, "exhausted")
	if ev == nil {
		t.Fatalf("no exhausted event; stream=%v", h.eventKinds())
	}
	pre, ok := ev["preconditions"].(map[string]any)
	if !ok || pre == nil {
		t.Fatalf("exhausted event missing preconditions; ev=%v", ev)
	}
	cr, _ := pre["check_running"].(string)
	if !strings.Contains(cr, "gummi.pid") || !strings.Contains(cr, "kill -0") {
		t.Fatalf("exhausted.preconditions.check_running = %q, want a kill -0 probe over gummi.pid", cr)
	}
}

// happyResumeScript drives a feature parked at Spec through to a verified
// branch — the tail a `resume` re-runs after an envelope top-up.
func happyResumeScript() map[domain.Stage]stageFn {
	return map[domain.Stage]stageFn{
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
	}
}

// `resume --envelope N` raises a parked feature's credit budget before the
// stage re-runs — the headless path out of an exhausted exit. The raise is a
// floor: it lifts the envelope and emits an `envelope` event.
func TestResumeEnvelopeRaisesBudget(t *testing.T) {
	h := newHarness(t, true, happyResumeScript())
	f := feature(1, domain.StageSpec) // Budget.Envelope == 500
	putDraft(t, h, &f, "# Spec\nExport as JSON.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{Envelope: 900}).Resume(context.Background(), f.ID, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 900 {
		t.Fatalf("envelope = %d, want 900 (raised)", got.Budget.Envelope)
	}
	ev := lastEvent(h, "envelope")
	if ev == nil {
		t.Fatalf("no envelope event; stream=%v", h.eventKinds())
	}
	if ev["from"].(float64) != 500 || ev["to"].(float64) != 900 {
		t.Fatalf("envelope event = %v, want from=500 to=900", ev)
	}
}

// An --envelope at or below the current budget is a floor no-op: it never
// shrinks an in-flight envelope, and emits no envelope event.
func TestResumeEnvelopeFloorNoOp(t *testing.T) {
	h := newHarness(t, true, happyResumeScript())
	f := feature(1, domain.StageSpec) // Budget.Envelope == 500
	putDraft(t, h, &f, "# Spec\nExport as JSON.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if _, err := h.driver(Options{Envelope: 300}).Resume(context.Background(), f.ID, ResumeInput{}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 500 {
		t.Fatalf("envelope = %d, want 500 (unchanged; --envelope must never shrink)", got.Budget.Envelope)
	}
	if h.has("envelope") {
		t.Fatalf("emitted an envelope event for a no-op raise; stream=%v", h.eventKinds())
	}
}

// An open user %% thread in the artifact blocks the design gate.
func TestBlockedGate(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
	})
	// create the feature at Spec with a draft carrying one open @user thread.
	f := feature(1, domain.StageSpec)
	putDraft(t, h, &f, "# Spec\nThe toggle persists.\n%% @user(2026-01-01): per-device or synced?\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{}).drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked; stream=%v", out.Status, h.eventKinds())
	}
	b := lastEvent(h, "blocked")
	if b == nil || b["open_questions"].(float64) != 1 {
		t.Fatalf("blocked event = %v, want open_questions=1", b)
	}
}

// A research card whose document fails the deterministic citation floor
// (internal/verifydoc) drives to blocked at verify — no worktree involved,
// since research cards never materialize one — and the blocked event
// carries the document report's counts.
func TestBlockedByDocument(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	// research stages run read-only in the main checkout; only a backend
	// that can structurally enforce that is allowed to drive them.
	h.fake.Caps.ReadOnlyEnforce = true
	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	slug, _ := domain.Slugify("research card")
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: domain.StageVerify, CreatedAt: now, UpdatedAt: now,
	}
	putDraft(t, h, &f, "# RS-001: research card\n\n## Findings\n\n"+
		"Broken cite `internal/missing.go:1` here.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{}).drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked; stream=%v", out.Status, h.eventKinds())
	}
	b := lastEvent(h, "blocked")
	if b == nil {
		t.Fatalf("no blocked event; stream=%v", h.eventKinds())
	}
	doc, ok := b["document"].(map[string]interface{})
	if !ok {
		t.Fatalf("blocked event missing document summary: %v", b)
	}
	if doc["citations"].(float64) != 1 {
		t.Fatalf("document summary = %v, want citations=1", doc)
	}
	if h.stageOf(f.ID) != domain.StageVerify {
		t.Fatalf("feature advanced past verify on a failing document, want it parked")
	}
}

// A stage that never idles trips the per-stage inactivity timeout.
func TestStageTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, _ agent.SessionOpts, _ string) []agent.Event {
			<-block // never returns events → no activity
			return nil
		},
	})
	out, err := h.driver(Options{StageTimeout: 150 * time.Millisecond}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusTimeout {
		t.Fatalf("status = %q, want timeout; stream=%v", out.Status, h.eventKinds())
	}
}

// --gate-approval=caller checkpoints the design gate as a question;
// resume --approve crosses it and drives to a verified branch.
func TestCallerGateApproveResume(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	out, err := h.driver(Options{GateApproval: GateCaller}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question (caller gate); stream=%v", out.Status, h.eventKinds())
	}
	if g := lastEvent(h, "gate"); g == nil || g["to"] != string(domain.StageImplement) {
		t.Fatalf("gate event = %v, want to=implement", g)
	}
	out2, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusDone {
		t.Fatalf("resume status = %q, want done; stream=%v", out2.Status, h.eventKinds())
	}
}

// A bare `resume` (no --approve/--request-changes/--answer) landing on an
// interactive stage already driven to its gate must re-present the gate, not
// re-enter the stage. The prior run left a live spec session carrying the
// finished interview; re-attaching would send no turn (the interview is
// done) and the driver would block until --stage-timeout, then misreport a
// backend stall. crossGate under --gate-approval=caller re-emits the same
// checkpoint the first run produced — instantly, with no turn and no timeout.
func TestResumeCompletedCallerGateReCheckpoints(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
	})
	out, err := h.driver(Options{GateApproval: GateCaller}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("run status = %q, want question (caller gate); stream=%v", out.Status, h.eventKinds())
	}

	// bare resume: no decision. A short timeout makes a regression (turn-less
	// await) fail fast instead of hanging the suite.
	h.buf.Reset()
	out2, err := h.driver(Options{GateApproval: GateCaller, StageTimeout: 2 * time.Second}).
		Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusQuestion {
		t.Fatalf("resume status = %q, want question (gate re-presented); stream=%v", out2.Status, h.eventKinds())
	}
	if h.has("timeout") {
		t.Fatalf("bare resume timed out on a completed interactive stage; stream=%v", h.eventKinds())
	}
	if g := lastEvent(h, "gate"); g == nil || g["to"] != string(domain.StageImplement) {
		t.Fatalf("gate event = %v, want to=implement", g)
	}
	if h.stageOf(domain.FeatureID(out.ID)) != domain.StageSpec {
		t.Fatalf("feature advanced past spec on a bare resume; want it parked at the gate")
	}
}

// The same completed-stage resume under --gate-approval=auto crosses the
// gate instead of checkpointing: a feature parked at spec by `--until spec`
// carries a finished interview, and a bare resume (no --until) must advance
// through it to a verified branch, not park a turn-less session.
func TestResumeCompletedAutoGateAdvances(t *testing.T) {
	h := newHarness(t, true, happyResumeScript())
	out, err := h.driver(Options{Until: domain.StageSpec}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusStopped {
		t.Fatalf("run status = %q, want stopped (--until spec); stream=%v", out.Status, h.eventKinds())
	}

	h.buf.Reset()
	out2, err := h.driver(Options{StageTimeout: 2 * time.Second}).
		Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusDone {
		t.Fatalf("resume status = %q, want done (auto-advanced past the gate); stream=%v", out2.Status, h.eventKinds())
	}
	if h.has("timeout") {
		t.Fatalf("bare resume timed out instead of crossing the gate; stream=%v", h.eventKinds())
	}
}

// The timeout hint is diagnosed from whether a turn was actually dispatched
// this stage: a backend that went silent after its turn points at the
// backend (or a too-short stage-timeout on this profile); a stage that
// timed out with no turn sent points the operator at the gate decision —
// not a phantom backend outage.
func TestTimeoutHintTracksSentTurn(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageSpec)

	d := h.driver(Options{})
	d.sentTurn = true
	d.timeout(f)
	if ev := lastEvent(h, "timeout"); ev == nil || !strings.Contains(ev["hint"].(string), "went silent") {
		t.Fatalf("sent-turn timeout hint = %v, want a stall/too-short diagnosis", ev)
	}

	h.buf.Reset()
	d.sentTurn = false
	d.timeout(f)
	if ev := lastEvent(h, "timeout"); ev == nil || !strings.Contains(ev["hint"].(string), "no turn was sent") {
		t.Fatalf("parked timeout hint = %v, want a gate-decision diagnosis", ev)
	}
}

// A timeout event carries the --stage-timeout that fired plus a check_running
// precondition, so an orchestrating caller sees exactly what limit tripped
// and can probe for an orphan gummi before retrying.
func TestTimeoutCarriesStageTimeoutUsedAndCheckRunning(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageSpec)

	d := h.driver(Options{StageTimeout: 7 * time.Minute})
	d.sentTurn = true
	d.timeout(f)
	ev := lastEvent(h, "timeout")
	if ev == nil {
		t.Fatalf("no timeout event; stream=%v", h.eventKinds())
	}
	if got := ev["stage_timeout_used"]; got != "7m0s" {
		t.Fatalf("stage_timeout_used = %v, want 7m0s", got)
	}
	pre, ok := ev["preconditions"].(map[string]any)
	if !ok || pre == nil {
		t.Fatalf("preconditions missing/wrong type; ev=%v", ev)
	}
	cr, _ := pre["check_running"].(string)
	if !strings.Contains(cr, "gummi.pid") || !strings.Contains(cr, "kill -0") {
		t.Fatalf("check_running = %q, want a kill -0 probe over gummi.pid", cr)
	}
}

// A timeout with --stage-timeout=0 (disabled) omits stage_timeout_used
// rather than lying with a "0s" number a caller might try to tune.
func TestTimeoutOmitsUsedWhenDisabled(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageSpec)

	// build the driver directly so the harness's 5s fallback for a zero
	// StageTimeout doesn't mask the disabled case.
	d := New(h.eng, h.store, h.ws, h.buf, Options{Envelope: 500, StageTimeout: 0})
	d.sentTurn = true
	d.timeout(f)
	ev := lastEvent(h, "timeout")
	if ev == nil {
		t.Fatalf("no timeout event; stream=%v", h.eventKinds())
	}
	if _, present := ev["stage_timeout_used"]; present {
		t.Fatalf("stage_timeout_used present with --stage-timeout=0; ev=%v", ev)
	}
}

// Terminal events carry a `next` field naming the exact resume command that
// advances the stop, so a caller driving the stream never has to recall which
// verb a given stop takes (the mismatch behind the deadlock report). A
// terminal success (`done`) carries none — there is nothing to resume.
func TestNextCommandSelfDocumentsResume(t *testing.T) {
	t.Run("question names --answer", func(t *testing.T) {
		h := newHarness(t, false, map[domain.Stage]stageFn{
			domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
				return convAsk(o.Model, "Include a schema header?", "no (recommended)", "yes")
			},
		})
		out, err := h.driver(Options{}).Run(context.Background(), "add export")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		q := lastEvent(h, "question")
		if want := "gummi resume " + out.ID + ` --answer "<answer>"`; q == nil || q["next"] != want {
			t.Fatalf("question next = %v, want %q", q["next"], want)
		}
	})

	t.Run("caller gate names --approve", func(t *testing.T) {
		h := newHarness(t, true, map[domain.Stage]stageFn{
			domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
				return msgIdle(o.Model, "Spec drafted.")
			},
		})
		out, err := h.driver(Options{GateApproval: GateCaller}).Run(context.Background(), "feature")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		g := lastEvent(h, "gate")
		if want := "gummi resume " + out.ID + " --approve"; g == nil || g["next"] != want {
			t.Fatalf("gate next = %v, want %q", g["next"], want)
		}
	})

	t.Run("--until stop names --approve", func(t *testing.T) {
		h := newHarness(t, true, happyResumeScript())
		out, err := h.driver(Options{Until: domain.StageSpec}).Run(context.Background(), "feature")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		s := lastEvent(h, "stopped")
		if want := "gummi resume " + out.ID + " --approve"; s == nil || s["next"] != want {
			t.Fatalf("stopped next = %v, want %q", s["next"], want)
		}
	})

	t.Run("exhausted names --envelope doubled", func(t *testing.T) {
		h := newHarness(t, true, map[domain.Stage]stageFn{
			domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
				return msgIdle(o.Model, "Spec.")
			},
			domain.StageImplement: func(_ *harness, _ int, _ agent.SessionOpts, _ string) []agent.Event {
				return []agent.Event{{Kind: agent.EventBudgetExhausted}}
			},
		})
		out, err := h.driver(Options{Envelope: 500}).Run(context.Background(), "feature")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		e := lastEvent(h, "exhausted")
		if want := "gummi resume " + out.ID + " --envelope 1000"; e == nil || e["next"] != want {
			t.Fatalf("exhausted next = %v, want %q (double the dry envelope)", e["next"], want)
		}
	})

	t.Run("done carries no next", func(t *testing.T) {
		h := newHarness(t, true, happyResumeScript())
		out, err := h.driver(Options{}).Run(context.Background(), "feature")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if out.Status != StatusDone {
			t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
		}
		if d := lastEvent(h, "done"); d == nil {
			t.Fatal("no done event")
		} else if _, ok := d["next"]; ok {
			t.Fatalf("done event carried a next command: %v", d)
		}
	})
}

// The error event distinguishes a resumable mid-run failure (a durable,
// non-terminal card exists) from a pre-id setup failure where nothing
// landed — even though both keep exit code 1.
func TestErrorEventResumable(t *testing.T) {
	h := newHarness(t, true, nil)

	// a card parked at a non-terminal stage (implement) → resumable, with
	// the parked stage named.
	f := feature(1, domain.StageImplement)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.driver(Options{}).fail(context.Background(), string(f.ID), errors.New("implement turn failed")); err == nil {
		t.Fatal("fail returned a nil error")
	}
	e := lastEvent(h, "error")
	if e == nil {
		t.Fatalf("no error event; stream=%v", h.eventKinds())
	}
	if e["resumable"] != true {
		t.Errorf("error resumable = %v, want true (non-terminal card exists)", e["resumable"])
	}
	if e["stage"] != string(domain.StageImplement) {
		t.Errorf("error stage = %v, want implement", e["stage"])
	}
	if want := "gummi resume " + string(f.ID); e["next"] != want {
		t.Errorf("error next = %v, want %q (resumable retry)", e["next"], want)
	}

	// a pre-creation failure (no id) → not resumable, no stage.
	h.buf.Reset()
	if _, err := h.driver(Options{}).fail(context.Background(), "", errors.New("bad --until")); err == nil {
		t.Fatal("fail returned a nil error")
	}
	e = lastEvent(h, "error")
	if e == nil {
		t.Fatalf("no error event; stream=%v", h.eventKinds())
	}
	if e["resumable"] != false {
		t.Errorf("pre-id error resumable = %v, want false", e["resumable"])
	}
	if _, ok := e["stage"]; ok {
		t.Errorf("pre-id error carried a stage %v, want none", e["stage"])
	}
	if _, ok := e["next"]; ok {
		t.Errorf("pre-id error carried a next %v, want none (nothing landed)", e["next"])
	}
}

// A main-checkout tripwire hit must end the run promptly as a typed
// escalation naming the dirtied paths — not block until --stage-timeout and
// misreport a backend stall. The engine aborts the session and emits
// EventTripwire with nothing further on the stream; the driver must treat
// it as a decision boundary.
func TestTripwireNotTimeout(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(h *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec.")
		},
		domain.StageImplement: func(h *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// the agent dirties the MAIN checkout (h.root), not the worktree —
			// a clean->dirty transition that trips the engine's tripwire.
			if err := os.WriteFile(filepath.Join(h.root, "tripwire.txt"), []byte("dirty\n"), 0o600); err != nil {
				t.Fatalf("writing main checkout: %v", err)
			}
			return msgIdle(o.Model, "Implemented.")
		},
	})
	start := time.Now()
	out, err := h.driver(Options{StageTimeout: 3 * time.Second}).Run(context.Background(), "feature")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("tripwire run took %v — it blocked for the whole stage-timeout instead of failing promptly", elapsed)
	}
	if h.has("timeout") {
		t.Fatalf("tripwire reported a timeout; stream=%v", h.eventKinds())
	}
	e := lastEvent(h, "escalation")
	if e == nil {
		t.Fatalf("no escalation event; stream=%v", h.eventKinds())
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "tripwire.txt") {
		t.Fatalf("escalation reason = %q, want the dirtied path named", reason)
	}
}

// A silent backend death — the agent's event stream ends mid-turn with no
// Idle and no Error — must surface as a prompt, non-zero failure, not hang
// until --stage-timeout and misreport a backend stall. The engine turns the
// dead session into an error; the driver's existing endError path escalates
// it. (Turn 1 is the spec chat; turn 2 is the implement session, which the
// fake then kills.)
func TestSilentDeathNotTimeout(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
	})
	h.fake.DieAfter = 2
	start := time.Now()
	out, err := h.driver(Options{StageTimeout: 3 * time.Second}).Run(context.Background(), "die after spec")
	if err == nil {
		t.Fatalf("Run returned no error (status %q); stream=%v", out.Status, h.eventKinds())
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("silent death took %v — it blocked for the whole stage-timeout instead of failing promptly", elapsed)
	}
	if h.has("timeout") {
		t.Fatalf("silent death reported a timeout; stream=%v", h.eventKinds())
	}
}

// --- small test helpers ----------------------------------------------

// lastEvent returns the last NDJSON event of the given kind, or nil.
func lastEvent(h *harness, kind string) map[string]any {
	var found map[string]any
	for _, e := range h.events() {
		if e["event"] == kind {
			found = e
		}
	}
	return found
}

// feature builds a minimal feature at a stage (quick route).
func feature(num int, stage domain.Stage) domain.Feature {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify("gated feature")
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindFeature, Title: "gated feature", Slug: slug,
		Stage: stage, Skip: domain.QuickRoute(), Budget: domain.Budget{Envelope: 500},
		CreatedAt: now, UpdatedAt: now,
	}
}

// putDraft writes a spec draft for f at its drafts home.
func putDraft(t *testing.T, h *harness, f *domain.Feature, body string) {
	t.Helper()
	if err := os.MkdirAll(h.ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(f))
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A card at its coding gate with an unmet dependency drives to blocked; the
// `blocked` event's blocking_deps names the outstanding dependency.
func TestBlockedByDependency(t *testing.T) {
	h := newHarness(t, true, planApproveScript())
	f := feature(1, domain.StagePlan)
	f.Skip = domain.SkipFlags{}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	dep := feature(2, domain.StageImplement)
	if err := h.store.CreateFeature(context.Background(), &dep); err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddDependency(context.Background(), f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{}).drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked; stream=%v", out.Status, h.eventKinds())
	}
	b := lastEvent(h, "blocked")
	if b == nil {
		t.Fatalf("no blocked event; stream=%v", h.eventKinds())
	}
	deps, ok := b["blocking_deps"].([]interface{})
	if !ok || len(deps) != 1 {
		t.Fatalf("blocked blocking_deps = %v, want one dep", b["blocking_deps"])
	}
	dep0 := deps[0].(map[string]interface{})
	if dep0["id"] != string(dep.ID) || dep0["stage"] != string(domain.StageImplement) {
		t.Fatalf("blocking dep = %v, want %s@implement", dep0, dep.ID)
	}
	if h.stageOf(f.ID) != domain.StagePlan {
		t.Fatalf("feature advanced past Plan on a blocked dep, want it parked")
	}
}

// --gate-approval=caller pre-checks dependencies at the coding gate: it
// reports blocked (naming the dep) instead of offering --approve.
func TestCallerGatePreCheckDependency(t *testing.T) {
	h := newHarness(t, true, planApproveScript())
	f := feature(1, domain.StagePlan)
	f.Skip = domain.SkipFlags{}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	dep := feature(2, domain.StageSpec)
	if err := h.store.CreateFeature(context.Background(), &dep); err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddDependency(context.Background(), f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{GateApproval: GateCaller}).drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want blocked (pre-check), not question; stream=%v", out.Status, h.eventKinds())
	}
	if h.has("gate") {
		t.Fatalf("caller gate offered --approve despite an unmet dependency; stream=%v", h.eventKinds())
	}
	b := lastEvent(h, "blocked")
	if b == nil {
		t.Fatalf("no blocked event; stream=%v", h.eventKinds())
	}
	deps, ok := b["blocking_deps"].([]interface{})
	if !ok || len(deps) != 1 || deps[0].(map[string]interface{})["id"] != string(dep.ID) {
		t.Fatalf("blocking_deps = %v, want %s named", b["blocking_deps"], dep.ID)
	}
}

// planApproveScript scripts the plan writer (no verdict) and its critique
// (RoleReviewer, a passing verdict), so a feature parked at Plan can drive
// through the plan approval gate to its coding gate.
func planApproveScript() map[domain.Stage]stageFn {
	return map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				return msgIdle(o.Model, "Critiqued.\nVERDICT: pass")
			}
			return msgIdle(o.Model, "Plan written.")
		},
	}
}

// A resume that lands on a finished plan critique whose structured
// verdict was lost (a prior process that never persisted it) must not
// re-judge the dead snapshot as Unclear forever — it runs a fresh
// critique so the card recovers instead of escalating identically.
func TestResumeRejudgesLostCritiqueVerdict(t *testing.T) {
	h := newHarness(t, true, planApproveScript())
	f := feature(1, domain.StagePlan)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// a previous process finished the plan critique but its structured
	// verdict never crossed the persistence boundary: the restored session
	// is Done and the last assistant line is prose ("Verdict recorded:
	// **changes**") with no VERDICT: token, so SessionVerdict is Unclear.
	if err := h.store.SaveSession(context.Background(), state.SessionSnapshot{
		Feature: f.ID,
		Stage:   domain.StagePlan,
		Role:    string(agent.RoleReviewer),
		Flavor:  "critique",
		State:   "done",
		Transcript: []state.SessionMessage{
			{Author: "system", Content: "critique the plan"},
			{Author: "assistant", Content: "Verdict recorded: **changes**"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{}).Resume(context.Background(), f.ID, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// before the fix this resumed to "plan critique finished with no clear
	// verdict" and never left Plan; now a fresh critique ran and its verdict
	// carried the card past the plan gate.
	if h.stageOf(f.ID) == domain.StagePlan {
		t.Fatalf("feature stuck re-judging the lost critique verdict; status=%s stream=%v", out.Status, h.eventKinds())
	}
	if h.calls[domain.StagePlan] == 0 {
		t.Fatal("no fresh critique session was spawned on resume")
	}
}
