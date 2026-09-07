package ui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/spec"
)

// classifyingAgent answers the re-entry classification turn with intent
// and every other turn with "ok". The classification prompt is spotted
// by the reply contract it asks for, which is the one thing no stage
// prompt in the codebase says.
func classifyingAgent(intent string) *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		reply := "ok"
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			reply = "INTENT: " + intent
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}}
}

// verifyCard walks a fresh card to the verify stage, where the
// interesting re-entries live: everything upstream of it has been
// approved once already, so "this was never asked for" is a claim about
// a decision somebody made rather than about work in progress.
func verifyCard(t *testing.T, ag agent.Agent) *Shell {
	t.Helper()
	m, _ := chatWorkspace(t, ag)
	return advanceTo(t, m, domain.StageVerify)
}

func artifactBody(t *testing.T, m *Shell) string {
	t.Helper()
	f := m.rows[0].F
	path := m.artifactFile(&f)
	if path == "" {
		t.Fatal("the card has no artifact")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func transitions(t *testing.T, m *Shell) [][2]domain.Stage {
	t.Helper()
	hist, err := m.store.History(context.Background(), m.rows[0].F.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([][2]domain.Stage, 0, len(hist))
	for _, tr := range hist {
		out = append(out, [2]domain.Stage{tr.From, tr.To})
	}
	return out
}

// The whole phase in one test: a sentence saying the spec never asked
// for the thing walks the card from verify back to plan — two real
// edges, both recorded — after writing the miss into the artifact as an
// open comment, and only after the reader confirms.
func TestSendItBackWalksToPlanAfterConfirming(t *testing.T) {
	m := verifyCard(t, classifyingAgent("requirement_missing"))
	const miss = "the persistence step was never in the spec"

	r := m.rows[0]
	m = openCardPage(t, m)
	m = pump(t, m, m.routeReentry(r, "bounce", miss))

	// A REWIND CONFIRMS. The card has not moved yet; the chip is up.
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Fatalf("the card moved before the confirm (at %s)", got)
	}
	p := m.reentryPending
	if p == nil || p.out.Action != reentry.Rewind {
		t.Fatalf("no rewind chip: %+v", p)
	}
	if !p.goOnEnter {
		t.Fatal("a rewind spends nothing now, so it goes on enter")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pump(t, m, m.loadRows)

	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Fatalf("stage = %s, want plan — a missing requirement belongs upstream of the build", got)
	}

	// BOTH transitions are recorded: the graph has no verify→plan edge,
	// and history must say the card walked through implement rather than
	// claiming an edge that does not exist.
	got := transitions(t, m)
	want := [2][2]domain.Stage{
		{domain.StageVerify, domain.StageImplement},
		{domain.StageImplement, domain.StagePlan},
	}
	if len(got) < 2 || got[len(got)-2] != want[0] || got[len(got)-1] != want[1] {
		t.Errorf("history tail = %v, want the walk %v", got, want)
	}

	// THE ARTIFACT EDIT IS THE POINT. Not the note — the note is carried
	// too, but a note dies with the kickoff that reads it.
	body := artifactBody(t, m)
	if !strings.Contains(body, miss) || !strings.Contains(body, "%% @user") {
		t.Fatalf("the miss was not written into the artifact:\n%s", body)
	}
	open := spec.Parse(body).UserOpenThreads()
	if len(open) != 1 {
		t.Fatalf("UserOpenThreads = %d, want 1 — the miss must hold the plan gate shut", len(open))
	}
	head, ok := spec.HeadingLine(body, "Problem")
	if !ok || open[0].Anchor != head {
		t.Errorf("the miss landed at line %d, want the Problem heading at %d", open[0].Anchor, head)
	}
	if m.bounceNotes[m.rows[0].F.ID] != miss {
		t.Errorf("the line does not ride the next kickoff: %q", m.bounceNotes[m.rows[0].F.ID])
	}
}

// Cancelling leaves everything exactly as it was — no transition, and
// no half-written artifact.
func TestRewindCancelledChangesNothing(t *testing.T) {
	m := verifyCard(t, classifyingAgent("plan_wrong"))
	before := artifactBody(t, m)

	m = openCardPage(t, m)
	m = pump(t, m, m.routeReentry(m.rows[0], "bounce", "the chosen approach cannot work offline"))
	if m.reentryPending == nil {
		t.Fatal("no chip to take back")
	}
	// esc is the take-it-back gesture: the chip goes, the line is sent
	// as a plain message, and the page stays open
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = pump(t, m, m.loadRows)

	if m.reentryPending != nil {
		t.Error("esc left the chip up")
	}
	if !m.cardOpen {
		t.Error("esc on a chip left the card page")
	}
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Errorf("a cancelled rewind moved the card to %s", got)
	}
	if after := artifactBody(t, m); after != before {
		t.Error("a cancelled rewind still wrote to the artifact")
	}
}

// An in-place re-run starts a session that spends credits now, so it
// confirms like every other spend — and on y, never on enter (§9.2).
func TestInPlaceReentryConfirmsOnYNotEnter(t *testing.T) {
	m, _ := chatWorkspace(t, classifyingAgent("implementation_wrong"))
	m = advanceTo(t, m, domain.StageImplement)
	m = openCardPage(t, m)
	m = pump(t, m, m.routeReentry(m.rows[0], "run", "the toggle does not do what step 4 describes"))
	p := m.reentryPending
	if p == nil || p.out.Action != reentry.RerunInPlace {
		t.Fatalf("no re-run chip: %+v", p)
	}
	if p.goOnEnter {
		t.Fatal("a re-run spends now; it must not go on enter")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.reentryPending == nil {
		t.Fatal("enter took a chip that spends")
	}
	if !strings.Contains(m.notice.text, "press y") {
		t.Errorf("enter on a spend did not say what key goes: %+v", m.notice)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.reentryPending != nil {
		t.Error("y did not take the chip")
	}
	if got := m.rows[0].F.Stage; got != domain.StageImplement {
		t.Errorf("an in-place re-run moved the card to %s", got)
	}
	if m.engine.Get(m.rows[0].F.ID) == nil {
		t.Error("y did not start the run")
	}
}

// /bounce <reason> and picking "send it back" are one answer typed two
// ways. Asserted on the outcome rather than on the call graph: both must
// reach plan, and both must leave the same open comment behind.
func TestTypedBounceAndTheRowReachTheSameRouter(t *testing.T) {
	const miss = "nothing in the spec ever asked for offline support"

	viaVerb := verifyCard(t, classifyingAgent("requirement_missing"))
	viaVerb = openCardPage(t, viaVerb)
	viaVerb = pump(t, viaVerb, viaVerb.fireVerb("bounce", miss))
	viaVerb = press(t, viaVerb, tea.KeyPressMsg{Code: tea.KeyEnter})
	viaVerb = pump(t, viaVerb, viaVerb.loadRows)

	// the composer's own path: prose typed at the stop, no row picked
	viaRow := verifyCard(t, classifyingAgent("requirement_missing"))
	viaRow = openCardPage(t, viaRow)
	viaRow = pump(t, viaRow, viaRow.submitThreadLine(viaRow.rows[0], miss))
	viaRow = press(t, viaRow, tea.KeyPressMsg{Code: tea.KeyEnter})
	viaRow = pump(t, viaRow, viaRow.loadRows)

	for name, m := range map[string]*Shell{"/bounce": viaVerb, "the row": viaRow} {
		if got := m.rows[0].F.Stage; got != domain.StagePlan {
			t.Errorf("%s landed at %s, want plan", name, got)
		}
		if !strings.Contains(artifactBody(t, m), miss) {
			t.Errorf("%s did not record the miss in the artifact", name)
		}
	}
}

// A sentence the classifier could not read is still a sentence someone
// meant. It reaches the card as prose and the card does not move —
// DESIGN §6.3's safety property, which this whole path is built around.
func TestUnreadableSentenceBecomesATurn(t *testing.T) {
	m := verifyCard(t, classifyingAgent("none"))
	m = pump(t, m, m.routeReentry(m.rows[0], "bounce", "hmm"))
	if m.Overlay.Top() != nil {
		t.Error("an unclassified sentence asked to move the card")
	}
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Errorf("an unclassified sentence moved the card to %s", got)
	}
}

// No classifier is not a statement about the sentence. The row already
// offered "send it back", so the fixed route it declares is what
// happens — never a silent downgrade to a chat message.
func TestNoClassifierKeepsTheRowsFixedRoute(t *testing.T) {
	m := verifyCard(t, classifyingAgent("requirement_missing"))
	f := m.rows[0].F
	m = pump(t, m, m.applyReentry(reentryClassifiedMsg{
		f: f, note: "send this back", fallback: "bounce", err: engine.ErrNoScribe,
	}))
	m = pump(t, m, m.loadRows)
	if got := m.rows[0].F.Stage; got != domain.StageImplement {
		t.Errorf("stage = %s, want implement — verify's own fixed route", got)
	}
}

// A live session is a conversation: a line typed into one is the next
// thing said in it, never something to read first. The design chat is
// the everyday case — an attached architect sitting at its own gate.
func TestLiveSessionIsNeverRead(t *testing.T) {
	asked := 0
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			asked++
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	// an attached interactive session is the everyday live conversation
	if _, err := eng.Attach(context.Background(), m.rows[0].F); err != nil {
		t.Fatal(err)
	}
	if s := m.sessionFor(m.rows[0].F.ID); s == nil || !s.Live() {
		t.Fatal("fixture has no live session")
	}
	m = pump(t, m, m.routeReentry(m.rows[0], "changes", "the approach ignores the offline case"))
	if asked != 0 {
		t.Errorf("a line into a live conversation spent %d classification turns", asked)
	}
	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Errorf("a design-chat line moved the card to %s", got)
	}
	if m.reentryPending != nil {
		t.Error("a line into a live conversation raised a chip")
	}
}

// A rewind whose artifact edit cannot land does not happen. This is the
// failure the phase exists to prevent, stated as a test: a note that
// rides a kickoff and nothing else leaves the artifact still wrong.
func TestRewindRefusesWithoutItsArtifactEdit(t *testing.T) {
	m := verifyCard(t, classifyingAgent("requirement_missing"))
	f := m.rows[0].F
	out := reentry.Outcome{
		Action: reentry.Rewind, Target: domain.StagePlan,
		Path: []domain.Stage{domain.StageImplement, domain.StagePlan},
		Edit: reentry.Edit{Section: "No Such Heading", Text: "x"},
		Note: "x",
	}
	m = pump(t, m, m.commitRewind(f, out))
	m = pump(t, m, m.loadRows)
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Errorf("the card moved to %s despite the edit failing", got)
	}
	if !m.notice.isErr {
		t.Errorf("no error notice explaining the refusal: %+v", m.notice)
	}
}

// A verb with its reason attached is an instruction, not a bare word:
// "/bounce <reason>" reaches the router whether or not a bounce row is
// on screen, instead of degrading to the "/" menu and dropping the
// reason. The bare verb still degrades — it has nothing to lose.
func TestBounceWithAReasonNeverDropsIt(t *testing.T) {
	asked := 0
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		reply := "ok"
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			asked++
			reply = "INTENT: implementation_wrong"
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}}
	m, _ := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageVerify)
	m.cardOpen = true
	// a verify that has not run offers "run verify" and no bounce row, so
	// the bare verb is valid-but-off-screen: exactly the case that used
	// to open the menu on the reason too
	in := m.nextInputFor(m.rows[0])
	for _, a := range stageActions(in) {
		if a.id == "bounce" {
			t.Fatal("fixture offers a bounce row; the test needs it off screen")
		}
	}

	m.threadInput.SetValue("/bounce the jq bootstrap never ran")
	m = pump(t, m, m.submitThreadInput(m.rows[0]))
	if asked != 1 {
		t.Fatalf("the reason never reached the router (asked=%d)", asked)
	}
	if _, isMenu := m.Overlay.Top().(*commandMenu); isMenu {
		t.Fatal("/bounce with a reason opened the menu instead of routing")
	}

	// the bare verb keeps the on-screen rule: nothing to lose, so the
	// menu, pre-filtered
	if m.Overlay.Top() != nil {
		m.Overlay.Pop()
	}
	m.threadInput.SetValue("/bounce")
	m = pump(t, m, m.submitThreadInput(m.rows[0]))
	if _, isMenu := m.Overlay.Top().(*commandMenu); !isMenu {
		t.Errorf("a bare /bounce with no row on screen did not open the menu (top=%T)", m.Overlay.Top())
	}
	if asked != 1 {
		t.Errorf("a bare /bounce spent a classification turn")
	}
}

// openCardPage opens the selected card's page from the backlog with the
// key that does it, so the composer is focused and chip keys reach it.
func openCardPage(t *testing.T, m *Shell) *Shell {
	t.Helper()
	if m.cardOpen {
		return m
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter on the backlog did not open the card page")
	}
	return m
}
