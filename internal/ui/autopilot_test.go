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

// TestAutopilotModeForReadsEmptyAsAttended: every value resolves to one
// of the two modes, and empty resolves to attended — the same rule
// domain.Feature.GateApproval states, kept in one place so the dialog and
// the notice can never disagree about what a blank field means.
func TestAutopilotModeForReadsEmptyAsAttended(t *testing.T) {
	cases := map[string]string{
		domain.GateAttended:  domain.GateAttended,
		domain.GateAutopilot: domain.GateAutopilot,
		"":                   domain.GateAttended,
		"nonsense":           domain.GateAttended,
	}
	for mode, want := range cases {
		if got := autopilotModeFor(mode); got != want {
			t.Errorf("autopilotModeFor(%q) = %q, want %q", mode, got, want)
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
		domain.StagePlan:      domain.StageImplement,
		domain.StageImplement: domain.StageVerify,
	}
	for from, to := range safe {
		got, ok := autopilotForward(domain.Feature{Stage: from})
		if !ok || got != to {
			t.Errorf("autopilotForward(%s) = (%s, %v), want (%s, true)", from, got, ok, to)
		}
	}
	// verify and done are never autopilot's to cross: landing on main
	// stays a person's act. todo is the kickoff hop, which autoAdvance
	// takes without a plan.
	excluded := []domain.Stage{domain.StageVerify, domain.StageTodo, domain.StageDone}
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
		to:        domain.StagePlan,
		remaining: []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify},
	}
	// base is deliberately not "main": this pins the wiring, not just a
	// fallback value that happens to read right (REVIEW-ux-drive-2026-09-
	// 10-round2.md §3.4 — the literal "main" used to be hardcoded here
	// regardless of what openAutopilot actually resolved).
	body := strings.Join(autopilotBody(f, plan, domain.GateAutopilot, "master"), " ")

	wantCorrective := verdict.MaxRounds(domain.RoundKindCorrective)
	if wantCorrective != 5 {
		t.Fatalf("test assumes the corrective cap is 5 (sourced from verdict.MaxRounds); it is now %d — update the test's expectation, not the source", wantCorrective)
	}
	for _, want := range []string{
		// every stage ahead is one autopilot may run — one graph, no
		// stage that needs a person by nature — so the list is the whole
		// remainder and nothing is named as a stage it will only open.
		"plan, implement and verify",
		"5 corrective rounds",
		// "envelope" and "parks to the inbox" were §5's own two renames
		// for this dialog: "budget" (matching the new-card dialog) and
		// "stops and leaves the card in the inbox" (plain enough that a
		// reader does not have to already know what "the inbox" is), and
		// the round count now says what running out of it means instead
		// of leaving that to guesswork.
		"2400 credit budget",
		"stops and leaves the card in the inbox",
		"never lands on master",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q does not mention %q", body, want)
		}
	}
}

// TestAutopilotBodyNoEnvelopeWhenUncapped: a budget clause only belongs
// in the body when the card actually carries one (f.Budget.Envelope >
// 0) — DESIGN's own "0 = no cap" reading. Named for the field
// (Budget.Envelope) that still carries the old word internally; the
// prose it checks for says "budget" (§5).
func TestAutopilotBodyNoEnvelopeWhenUncapped(t *testing.T) {
	f := domain.Feature{ID: "FD-051", Stage: domain.StageTodo}
	plan := autopilotPlan{bucket: "todo", to: domain.StagePlan, remaining: []domain.Stage{domain.StagePlan}}
	body := strings.Join(autopilotBody(f, plan, domain.GateAutopilot, "main"), " ")
	if strings.Contains(body, "credit budget") {
		t.Errorf("body mentions a credit budget for an uncapped card: %q", body)
	}
}

// TestAutopilotBodyOffNeverStarts: off's body must not claim any stage
// runs, must carry no corrective budget, and must say plainly that
// nothing starts — the text-level counterpart of requirement 6.
func TestAutopilotBodyOffNeverStarts(t *testing.T) {
	f := domain.Feature{ID: "FD-051", Stage: domain.StageTodo}
	plan := autopilotPlan{bucket: "todo", to: domain.StagePlan, remaining: []domain.Stage{domain.StagePlan}}
	body := strings.Join(autopilotBody(f, plan, domain.GateAttended, "main"), " ")
	if strings.Contains(body, "corrective rounds") || strings.Contains(body, "runs brainstorm") {
		t.Errorf("off's body should not describe a run: %q", body)
	}
	if !strings.Contains(body, "waits for you") {
		t.Errorf("off's body should say it waits for you: %q", body)
	}
}

// TestAutopilotAnswersRuleTable is DESIGN §10.17's rule table, exhaustive
// over every (mode × decisionKind) pair the TUI ever asks about,
// including the empty mode string (domain.Feature.GateApproval's own
// "empty reads as GateAttended" rule).
//
// With three modes collapsed to two the table lost its middle row, and
// that is the whole of the interaction change: the retired "gates" mode
// answered decisionGate and decisionIdle on the card's behalf and was
// the DEFAULT. Attended answers nothing, and empty reads as attended, so
// a card nobody has handed over now stops where it used to walk.
func TestAutopilotAnswersRuleTable(t *testing.T) {
	modes := []string{domain.GateAttended, domain.GateAutopilot, ""}
	kinds := []decisionKind{decisionAsk, decisionGate, decisionVerify, decisionBudget, decisionIdle}

	// want[mode][kind]
	want := map[string]map[decisionKind]bool{
		domain.GateAttended: {
			decisionAsk: false, decisionGate: false, decisionVerify: false,
			decisionBudget: false, decisionIdle: false,
		},
		domain.GateAutopilot: {
			decisionAsk: true, decisionGate: true, decisionVerify: true,
			decisionBudget: false, decisionIdle: true,
		},
		"": { // empty reads as GateAttended: answers nothing
			decisionAsk: false, decisionGate: false, decisionVerify: false,
			decisionBudget: false, decisionIdle: false,
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
	for _, mode := range []string{domain.GateAttended, domain.GateAutopilot, ""} {
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
	if plan.to != domain.StagePlan {
		t.Fatalf("to = %s, want plan", plan.to)
	}
	want := []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify}
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
	f := domain.Feature{ID: "FD-001", Stage: domain.StageVerify}
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
	// every stage runs an agent now, the design stage included, so the
	// crossing out of todo needs an engine behind it — there is no stage
	// left that a card can be walked into with nothing configured.
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("shaping it")), Store: store,
		Pool: wt, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "todo card", Slug: "todo-card", Stage: domain.StageTodo}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	plan := m.planAutopilot(f)

	cmd := m.startAutopilot(f, domain.GateAutopilot, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
	}
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAutopilot {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateAutopilot)
	}
	// the todo card visibly leaves todo — the same "clears the way, then
	// starts what is behind it" contract autoStepStage documents.
	if got.Stage != domain.StagePlan {
		t.Errorf("stage = %s, want plan", got.Stage)
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

	cmd := m.startAutopilot(f, domain.GateAttended, plan)
	cmd()
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAttended {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateAttended)
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

	cmd := m.startAutopilot(f, domain.GateAttended, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
	}
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAttended {
		t.Errorf("gate approval = %q, want %q", got.GateApproval, domain.GateAttended)
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

	// the fake answers in chat and never writes the plan; stand in for it,
	// or the undrafted-sections gate holds this crossing shut.
	draftRequiredSections(t, m)

	f := m.rows[0].F
	m.inbox.add(f.ID, attnGate, "plan critiqued: clean — review & approve")
	plan := m.planAutopilot(f)
	if plan.bucket != "gate" || plan.to != domain.StageImplement {
		t.Fatalf("plan = %+v, want bucket=gate to=implement", plan)
	}

	cmd := m.startAutopilot(f, domain.GateAutopilot, plan)
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

// TestAutopilotDialogConfirmSubmitsAutopilot: with two modes there is
// nothing to navigate — the dialog states what handing the card over
// will do and its confirm is the whole choice. ←/→ still reach Cancel,
// and enter activates whichever button is focused.
func TestAutopilotDialogConfirmSubmitsAutopilot(t *testing.T) {
	var got string
	f := domain.Feature{ID: "FD-001", GateApproval: domain.GateAttended}
	plan := autopilotPlan{bucket: "running"}
	d := newAutopilotDialog(f, plan, "main", func(mode string) tea.Cmd {
		got = mode
		return nil
	})
	// the confirm leads, so enter submits without moving anything
	done, _ := d.HandleKey(tea.KeyPressMsg{Text: "enter"})
	if !done {
		t.Fatal("enter should close the dialog")
	}
	if got != domain.GateAutopilot {
		t.Fatalf("onSubmit mode = %q, want %q", got, domain.GateAutopilot)
	}

	// ←  reaches Cancel, and enter there changes nothing — but it does
	// leave a notice, so cancelling one confirmation never reads like
	// confirming another (see TestAutopilotDialogEscCancelsWithoutSubmitting).
	got = ""
	d = newAutopilotDialog(f, plan, "main", func(mode string) tea.Cmd { got = mode; return nil })
	d.HandleKey(tea.KeyPressMsg{Text: "left"})
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "enter"})
	if !done {
		t.Fatal("enter on Cancel should close the dialog")
	}
	if got != "" {
		t.Fatalf("Cancel submitted %q — it must change nothing", got)
	}
	if cmd == nil {
		t.Fatal("enter on Cancel should leave a notice behind")
	}
	if msg, ok := cmd().(noticeMsg); !ok || !strings.Contains(msg.text, "nothing started") {
		t.Fatalf("Cancel notice = %#v, want a noticeMsg saying nothing started", cmd())
	}
}

// TestAutopilotDialogEscCancelsWithoutSubmitting also covers the notice
// esc leaves behind: closing the overlay used to leave nothing at all,
// so cancelling a confirmation read identically to confirming one that
// happened to do nothing — the miss driving this dialog for real found,
// two screens later, with the card still unstarted and no record of why.
func TestAutopilotDialogEscCancelsWithoutSubmitting(t *testing.T) {
	called := false
	f := domain.Feature{ID: "FD-001"}
	d := newAutopilotDialog(f, autopilotPlan{bucket: "running"}, "main", func(string) tea.Cmd {
		called = true
		return nil
	})
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "esc"})
	if !done || cmd == nil {
		t.Fatalf("esc: done=%v cmd=%v, want done, with a cancel notice", done, cmd)
	}
	if called {
		t.Fatal("esc must not submit")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("esc's cmd = %T, want a noticeMsg", cmd())
	}
	if !strings.Contains(msg.text, "nothing started") {
		t.Errorf("esc notice = %q, want it to say nothing started", msg.text)
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
	m.Overlay.Push(newAutopilotDialog(f, m.planAutopilot(f), m.baseBranch(f), func(string) tea.Cmd { return nil }))
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

// attended and autopilot promise different things, and the dialog is
// what someone reads before leaving the room: autopilot runs the card
// unattended and spends the corrective budget doing it, attended stops
// at every gate and spends nothing. Sharing a sentence between them
// would make one of the two a lie.
func TestAutopilotBodyDistinguishesAttendedFromAutopilot(t *testing.T) {
	f := domain.Feature{
		ID: "FD-051", Num: 51, Title: "rate limits", Slug: "rate-limits",
		Stage: domain.StageTodo,
	}
	// the tail has to hold a stage autopilot may actually run: the two
	// modes differ in how they run one, so a card with nothing runnable
	// ahead of it is the one case where neither promise is made.
	plan := autopilotPlan{bucket: "todo", to: domain.StagePlan, remaining: []domain.Stage{domain.StagePlan, domain.StagePlan, domain.StagePlan}}

	autopilot := strings.Join(autopilotBody(f, plan, domain.GateAutopilot, "main"), " ")
	attended := strings.Join(autopilotBody(f, plan, domain.GateAttended, "main"), " ")

	if !strings.Contains(autopilot, "without you") {
		t.Errorf("autopilot body does not say it runs without you: %q", autopilot)
	}
	if strings.Contains(attended, "without you") {
		t.Errorf("attended body claims autopilot's promise: %q", attended)
	}
	if !strings.Contains(attended, "waits for you") {
		t.Errorf("attended body does not say every gate waits: %q", attended)
	}
	// the corrective budget is autopilot's; naming it under attended would
	// imply a bounce loop that mode never runs.
	if !strings.Contains(autopilot, "corrective rounds") {
		t.Errorf("autopilot body omits the corrective budget: %q", autopilot)
	}
	if strings.Contains(attended, "corrective rounds") {
		t.Errorf("attended body names a budget that does not apply to it: %q", attended)
	}
	// autopilot is the mode that walks away, so it carries the guarantees;
	// attended stops at everything, so it has nothing to guarantee about
	// what it does unsupervised.
	if !strings.Contains(autopilot, "never lands on main") || !strings.Contains(autopilot, "stops and leaves the card in the inbox") {
		t.Errorf("autopilot body drops a guarantee: %q", autopilot)
	}
}
