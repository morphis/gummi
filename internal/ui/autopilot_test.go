package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verdict"
)

// --- pure logic: cursor, forward edges, confirm label, body text ---

func TestAutopilotCursorForReadsEmptyAsGates(t *testing.T) {
	cases := map[string]int{
		domain.GateOff:   0,
		domain.GateGates: 1,
		domain.GateFull:  2,
		"":               1, // empty reads as gates, same as domain.Feature.GateApproval everywhere else
	}
	for mode, want := range cases {
		if got := autopilotCursorFor(mode); got != want {
			t.Errorf("autopilotCursorFor(%q) = %d, want %d", mode, got, want)
		}
	}
}

func TestAutopilotConfirmLabelPerBucket(t *testing.T) {
	cases := []struct {
		bucket string
		want   string
	}{
		{"todo", "Start on autopilot"},
		{"gate", "Cross the gate and continue"},
		{"running", "Set"},
		{"", "Set"}, // unknown bucket falls back to the safe, inert label
	}
	for _, c := range cases {
		p := autopilotPlan{bucket: c.bucket}
		if got := p.confirmLabel(); got != c.want {
			t.Errorf("confirmLabel(bucket=%q) = %q, want %q", c.bucket, got, c.want)
		}
	}
}

// TestAutopilotForwardEdges: only the stages a finished session can
// safely be walked past on its own — the corrective loop's own targets —
// resolve; a parked Review or Verify gate never does, because crossing
// either is a human judgment call (an escalation, or the landing
// decision) rather than a mechanical next step.
func TestAutopilotForwardEdges(t *testing.T) {
	safe := map[domain.Stage]domain.Stage{
		domain.StagePlan:        domain.StageImplement,
		domain.StageDiagnose:    domain.StageFix,
		domain.StageImplement:   domain.StageReview,
		domain.StageFix:         domain.StageReview,
		domain.StageInvestigate: domain.StageShape,
	}
	for from, to := range safe {
		got, ok := autopilotForward(domain.Feature{Stage: from})
		if !ok || got != to {
			t.Errorf("autopilotForward(%s) = (%s, %v), want (%s, true)", from, got, ok, to)
		}
	}
	excluded := []domain.Stage{
		domain.StageReview, domain.StageVerify, domain.StageTodo,
		domain.StageBrainstorm, domain.StageSpec, domain.StageShape,
		domain.StageTriage, domain.StageDone,
	}
	for _, from := range excluded {
		if _, ok := autopilotForward(domain.Feature{Stage: from}); ok {
			t.Errorf("autopilotForward(%s) should refuse to cross on its own", from)
		}
	}
}

func TestAutopilotBodyNamesConcreteConsequence(t *testing.T) {
	f := domain.Feature{ID: "FD-051", Stage: domain.StageTodo, Budget: domain.Budget{Envelope: 2400}}
	plan := autopilotPlan{
		bucket:    "todo",
		to:        domain.StageBrainstorm,
		remaining: []domain.Stage{domain.StageBrainstorm, domain.StageSpec, domain.StagePlan, domain.StageImplement, domain.StageReview, domain.StageVerify},
	}
	body := strings.Join(autopilotBody(f, plan, domain.GateFull), " ")

	wantCorrective := verdict.MaxRounds(domain.RoundKindCorrective)
	if wantCorrective != 5 {
		t.Fatalf("test assumes the corrective cap is 5 (sourced from verdict.MaxRounds); it is now %d — update the test's expectation, not the source", wantCorrective)
	}
	for _, want := range []string{
		"brainstorm, spec, plan, implement, review and verify",
		"5 corrections",
		"2400 credit envelope",
		"parks to the inbox if it can't finish",
		"never lands on main",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q does not mention %q", body, want)
		}
	}
}

// TestAutopilotBodyNoEnvelopeWhenUncapped: an envelope clause only
// belongs in the body when the card actually carries one (f.Budget.
// Envelope > 0) — DESIGN's own "0 = no cap" reading.
func TestAutopilotBodyNoEnvelopeWhenUncapped(t *testing.T) {
	f := domain.Feature{ID: "FD-051", Stage: domain.StageTodo}
	plan := autopilotPlan{bucket: "todo", to: domain.StageBrainstorm, remaining: []domain.Stage{domain.StageBrainstorm}}
	body := strings.Join(autopilotBody(f, plan, domain.GateFull), " ")
	if strings.Contains(body, "credit envelope") {
		t.Errorf("body mentions a credit envelope for an uncapped card: %q", body)
	}
}

// TestAutopilotBodyOffNeverStarts: off's body must not claim any stage
// runs, must carry no corrective budget, and must say plainly that
// nothing starts — the text-level counterpart of requirement 6.
func TestAutopilotBodyOffNeverStarts(t *testing.T) {
	f := domain.Feature{ID: "FD-051", Stage: domain.StageTodo}
	plan := autopilotPlan{bucket: "todo", to: domain.StageBrainstorm, remaining: []domain.Stage{domain.StageBrainstorm}}
	body := strings.Join(autopilotBody(f, plan, domain.GateOff), " ")
	if strings.Contains(body, "corrections") || strings.Contains(body, "runs brainstorm") {
		t.Errorf("off's body should not describe a run: %q", body)
	}
	if !strings.Contains(body, "waits for you") {
		t.Errorf("off's body should say it waits for you: %q", body)
	}
}

// TestAutopilotAnswersRuleTable is DESIGN §10.17's rule table, exhaustive
// over every (mode × decisionKind) pair the TUI ever asks about,
// including the empty mode string (domain.Feature.GateApproval's own
// "empty reads as GateGates" rule).
func TestAutopilotAnswersRuleTable(t *testing.T) {
	modes := []string{domain.GateOff, domain.GateGates, domain.GateFull, ""}
	kinds := []decisionKind{decisionAsk, decisionGate, decisionVerify, decisionBudget, decisionIdle}

	// want[mode][kind]
	want := map[string]map[decisionKind]bool{
		domain.GateOff: {
			decisionAsk: false, decisionGate: false, decisionVerify: false,
			decisionBudget: false, decisionIdle: false,
		},
		domain.GateGates: {
			decisionAsk: false, decisionGate: true, decisionVerify: false,
			decisionBudget: false, decisionIdle: true,
		},
		domain.GateFull: {
			decisionAsk: true, decisionGate: true, decisionVerify: true,
			decisionBudget: false, decisionIdle: true,
		},
		"": { // empty reads as GateGates
			decisionAsk: false, decisionGate: true, decisionVerify: false,
			decisionBudget: false, decisionIdle: true,
		},
	}

	for _, mode := range modes {
		for _, kind := range kinds {
			got := autopilotAnswers(mode, kind)
			if got != want[mode][kind] {
				t.Errorf("autopilotAnswers(%q, %q) = %v, want %v", mode, kind, got, want[mode][kind])
			}
		}
	}
}

// TestAutopilotAnswersNeverBudget restates the one universal refusal on
// its own, so a future rule-table edit that accidentally starts granting
// budget under some mode fails loudly and specifically, not just as one
// row in the table above.
func TestAutopilotAnswersNeverBudget(t *testing.T) {
	for _, mode := range []string{domain.GateOff, domain.GateGates, domain.GateFull, ""} {
		if autopilotAnswers(mode, decisionBudget) {
			t.Errorf("autopilotAnswers(%q, budget) = true, want false — budget always parks", mode)
		}
	}
}

// --- plan resolution against live Shell state ---

func TestAutopilotPlanTodoCard(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "todo card", Slug: "todo-card", Stage: domain.StageTodo}
	plan := m.planAutopilot(f)
	if plan.bucket != "todo" {
		t.Fatalf("bucket = %q, want todo", plan.bucket)
	}
	if plan.to != domain.StageBrainstorm {
		t.Fatalf("to = %s, want brainstorm", plan.to)
	}
	want := []domain.Stage{domain.StageBrainstorm, domain.StageSpec, domain.StagePlan, domain.StageImplement, domain.StageReview, domain.StageVerify}
	if !stagesEqual(plan.remaining, want) {
		t.Fatalf("remaining = %v, want %v", plan.remaining, want)
	}
}

// TestAutopilotPlanParkedGateBucket: a card parked at a clean, crossable
// gate (a critiqued plan, in this case) resolves to the "gate" bucket
// with the forward edge that crossing it would take.
func TestAutopilotPlanParkedGateBucket(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	f := domain.Feature{ID: "FD-001", Stage: domain.StagePlan}
	m.inbox.add(f.ID, attnGate, "plan critiqued: clean — review & approve")
	plan := m.planAutopilot(f)
	if plan.bucket != "gate" || plan.to != domain.StageImplement {
		t.Fatalf("plan = %+v, want bucket=gate to=implement", plan)
	}
}

// TestAutopilotPlanVerifyGateStaysRunningBucket: even though a passed
// verify parks with the same attnGate kind as any other gate, autopilot
// must never treat it as something it can cross itself — that is the
// landing decision, and the switch's guarantee is that it never lands on
// main by itself.
func TestAutopilotPlanVerifyGateStaysRunningBucket(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	f := domain.Feature{ID: "FD-001", Stage: domain.StageVerify}
	m.inbox.add(f.ID, attnGate, "verify passed — review & land on main")
	plan := m.planAutopilot(f)
	if plan.bucket != "running" || plan.to != "" {
		t.Fatalf("plan = %+v, want the inert running bucket", plan)
	}
}

// TestAutopilotPlanEscalatedReviewStaysRunningBucket: a review gate that
// only reached the inbox by escalating (round cap or unclear verdict) is
// a judgment call, not a mechanical next step — the switch leaves it
// alone rather than re-driving a loop that already gave up.
func TestAutopilotPlanEscalatedReviewStaysRunningBucket(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	f := domain.Feature{ID: "FD-001", Stage: domain.StageReview}
	m.raiseEscalation(f.ID, "review still requesting changes after 3 rounds — needs you")
	plan := m.planAutopilot(f)
	if plan.bucket != "running" {
		t.Fatalf("bucket = %q, want running", plan.bucket)
	}
}

// --- startAutopilot: the actual write + (maybe) start ---

func TestAutopilotStartTodoCardEntersInitialStage(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "todo card", Slug: "todo-card", Stage: domain.StageTodo}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	plan := m.planAutopilot(f)

	cmd := m.startAutopilot(f, domain.GateFull, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
	}
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateFull {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateFull)
	}
	// brainstorm is interactive (workflow.Interactive): entering it needs
	// no engine, so the todo card visibly leaves todo even though nothing
	// is running yet — the same "clears the way; a plain enter opens it"
	// contract autoStepStage documents.
	if got.Stage != domain.StageBrainstorm {
		t.Errorf("stage = %s, want brainstorm", got.Stage)
	}
}

// TestAutopilotOffNeverStarts: requirement 6 — off only ever writes the
// mode, on a todo card exactly as everywhere else.
func TestAutopilotOffNeverStarts(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "todo card", Slug: "todo-card", Stage: domain.StageTodo}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	plan := m.planAutopilot(f)

	cmd := m.startAutopilot(f, domain.GateOff, plan)
	cmd()
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateOff {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateOff)
	}
	if got.Stage != domain.StageTodo {
		t.Errorf("stage = %s, want todo — off must not start anything", got.Stage)
	}
}

// TestAutopilotRunningBucketOnlyWritesMode: nothing to cross, nothing to
// start — setting a mode on such a card is the write and nothing else.
func TestAutopilotRunningBucketOnlyWritesMode(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "mid card", Slug: "mid-card", Stage: domain.StageImplement}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	plan := m.planAutopilot(f) // no session, no gate item -> "running" bucket
	if plan.bucket != "running" {
		t.Fatalf("bucket = %q, want running", plan.bucket)
	}

	cmd := m.startAutopilot(f, domain.GateGates, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
	}
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateGates)
	}
	if got.Stage != domain.StageImplement {
		t.Errorf("stage = %s, want implement — nothing should move", got.Stage)
	}
}

// TestAutopilotCrossesParkedGateToAutonomousStage: a card parked at a
// clean, crossable gate (Plan, critiqued) — setting gates or full must
// both write the mode and actually schedule the next stage's session,
// exactly as requirement 5 promises for a parked card ("crosses that
// gate and carries on").
func TestAutopilotCrossesParkedGateToAutonomousStage(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("ok"))
	m = advanceTo(t, m, domain.StagePlan)

	f := m.rows[0].F
	m.inbox.add(f.ID, attnGate, "plan critiqued: clean — review & approve")
	plan := m.planAutopilot(f)
	if plan.bucket != "gate" || plan.to != domain.StageImplement {
		t.Fatalf("plan = %+v, want bucket=gate to=implement", plan)
	}

	cmd := m.startAutopilot(f, domain.GateFull, plan)
	msg := cmd()
	if nm, ok := msg.(noticeMsg); ok && nm.isErr {
		t.Fatalf("startAutopilot failed: %s", nm.text)
	}
	// the crossing itself happens inside the command, through the engine's
	// advance floor; starting the stage behind the gate is what the Update
	// loop does with the message it hands back (msgs.go's
	// autopilotContinueMsg), so the message has to be routed to see it.
	model, next := m.update(msg)
	m = model.(*Shell)
	m = pump(t, m, next)

	got, err := m.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement", got.Stage)
	}
	s := eng.Get(f.ID)
	if s == nil {
		t.Fatal("crossing the gate did not schedule a session for the new stage")
	}
	// running, queued, or already finished: pump drains the started run to
	// completion and the fake answers instantly, so what is being asserted
	// is that a session for the new stage exists at all — the crossing
	// started the work rather than only writing a mode.
	if st := s.State(); st != engine.StateRunning && st != engine.StateQueued && st != engine.StateDone {
		t.Errorf("session state = %v, want the new stage started", st)
	}
}

// --- key routing ---

func TestAutopilotKeyOpensOverlay(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 0 // FD-051 · rate limits, todo
	m.boardVerb("A")
	d, ok := m.Overlay.Top().(*autopilotDialog)
	if !ok {
		t.Fatalf("top overlay is %T, want *autopilotDialog", m.Overlay.Top())
	}
	if d.feature.ID != "FD-051" {
		t.Errorf("dialog opened for %s, want FD-051", d.feature.ID)
	}
}

// TestAutopilotKeyRefusesOnDrivenAbroadCard: another process owns this
// card's writes; the `A` key must refuse exactly like every other
// card-writing verb (shell.go's boardVerb top-level guard), not silently
// open a dialog whose confirm would then race the other process.
func TestAutopilotKeyRefusesOnDrivenAbroadCard(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 0
	m.rows[0].DrivenAbroad = true
	m.rows[0].Foreign.PID = 4242
	m.boardVerb("A")
	if m.Overlay.HasDialogs() {
		t.Fatal("A should refuse on a card driven abroad, not open the overlay")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "4242") {
		t.Fatalf("notice = %+v, want an error naming the driving pid", m.notice)
	}
}

// --- dialog navigation ---

func TestAutopilotDialogNavigation(t *testing.T) {
	var got string
	f := domain.Feature{ID: "FD-001", GateApproval: domain.GateGates}
	plan := autopilotPlan{bucket: "running"}
	d := newAutopilotDialog(f, plan, func(mode string) tea.Cmd {
		got = mode
		return nil
	})
	if d.cursor != 1 {
		t.Fatalf("initial cursor = %d, want 1 (gates)", d.cursor)
	}
	if done, _ := d.HandleKey(tea.KeyPressMsg{Text: "down"}); done {
		t.Fatal("down should not close the dialog")
	}
	if d.cursor != 2 {
		t.Fatalf("cursor after down = %d, want 2 (full)", d.cursor)
	}
	// clamps, does not wrap
	d.HandleKey(tea.KeyPressMsg{Text: "down"})
	if d.cursor != 2 {
		t.Fatalf("cursor overshot the last stop: %d", d.cursor)
	}
	d.HandleKey(tea.KeyPressMsg{Text: "up"})
	d.HandleKey(tea.KeyPressMsg{Text: "up"})
	d.HandleKey(tea.KeyPressMsg{Text: "up"})
	if d.cursor != 0 {
		t.Fatalf("cursor undershot the first stop: %d", d.cursor)
	}
	d.HandleKey(tea.KeyPressMsg{Text: "down"})
	d.HandleKey(tea.KeyPressMsg{Text: "down"}) // cursor -> full (2)
	done, _ := d.HandleKey(tea.KeyPressMsg{Text: "enter"})
	if !done {
		t.Fatal("enter should close the dialog")
	}
	if got != domain.GateFull {
		t.Fatalf("onSubmit mode = %q, want full", got)
	}
}

func TestAutopilotDialogEscCancelsWithoutSubmitting(t *testing.T) {
	called := false
	f := domain.Feature{ID: "FD-001"}
	d := newAutopilotDialog(f, autopilotPlan{bucket: "running"}, func(string) tea.Cmd {
		called = true
		return nil
	})
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "esc"})
	if !done || cmd != nil {
		t.Fatalf("esc: done=%v cmd=%v, want done, no cmd", done, cmd)
	}
	if called {
		t.Fatal("esc must not submit")
	}
}

// --- golden ---

// TestAutopilotOverlayGolden renders the overlay over FD-051 · rate
// limits — populatedShell's own todo card — matching the state the
// design mock walks through.
func TestAutopilotOverlayGolden(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 0
	f := m.rows[0].F
	m.Overlay.Push(newAutopilotDialog(f, m.planAutopilot(f), func(string) tea.Cmd { return nil }))
	golden.RequireEqual(t, []byte(m.View().Content))
}

// --- helpers ---

func stagesEqual(a, b []domain.Stage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gates and full promise different things, and the dialog is what
// someone reads before leaving the room: full runs the card unattended
// and spends the corrective budget doing it, gates crosses design gates
// but still stops the moment the agent needs an answer. Sharing a
// sentence between them would make one of the two a lie.
func TestAutopilotBodyDistinguishesGatesFromFull(t *testing.T) {
	f := domain.Feature{
		ID: "FD-051", Num: 51, Title: "rate limits", Slug: "rate-limits",
		Stage: domain.StageTodo,
	}
	plan := autopilotPlan{bucket: "todo", to: domain.StageBrainstorm, remaining: []domain.Stage{domain.StageBrainstorm, domain.StageSpec}}

	full := strings.Join(autopilotBody(f, plan, domain.GateFull), " ")
	gates := strings.Join(autopilotBody(f, plan, domain.GateGates), " ")

	if !strings.Contains(full, "without you") {
		t.Errorf("full body does not say it runs without you: %q", full)
	}
	if strings.Contains(gates, "without you") {
		t.Errorf("gates body claims full's promise: %q", gates)
	}
	if !strings.Contains(gates, "still stops") {
		t.Errorf("gates body does not say it still stops for a question: %q", gates)
	}
	// the corrective budget is full's; naming it under gates would imply
	// a bounce loop that mode does not run.
	if !strings.Contains(full, "corrections") {
		t.Errorf("full body omits the corrective budget: %q", full)
	}
	if strings.Contains(gates, "corrections") {
		t.Errorf("gates body names a budget that does not apply to it: %q", gates)
	}
	// both guarantees appear whichever stop is selected
	for name, body := range map[string]string{"full": full, "gates": gates} {
		if !strings.Contains(body, "never lands on main") || !strings.Contains(body, "parks to the inbox") {
			t.Errorf("%s body drops a guarantee: %q", name, body)
		}
	}
}
