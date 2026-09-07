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
	m = pump(t, m, m.routeReentry(r, "bounce", miss))

	// A REWIND CONFIRMS. The card has not moved yet.
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Fatalf("the card moved before the confirm (at %s)", got)
	}
	if m.Overlay.Top() == nil {
		t.Fatal("a rewind must ask before it moves the card")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
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

	m = pump(t, m, m.routeReentry(m.rows[0], "bounce", "the chosen approach cannot work offline"))
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = pump(t, m, m.loadRows)

	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Errorf("a cancelled rewind moved the card to %s", got)
	}
	if after := artifactBody(t, m); after != before {
		t.Error("a cancelled rewind still wrote to the artifact")
	}
}

// An in-place re-run just goes: it is exactly what "send it back" has
// always meant at that stage, so a confirm would be asking about
// nothing.
func TestInPlaceReentryDoesNotConfirm(t *testing.T) {
	m := verifyCard(t, classifyingAgent("implementation_wrong"))
	m = pump(t, m, m.routeReentry(m.rows[0], "bounce", "the toggle does not do what step 4 describes"))
	if m.Overlay.Top() == nil {
		t.Fatal("verify→implement is a rewind and must confirm")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Fatalf("the declined rewind moved the card to %s", got)
	}

	// At implement the same complaint re-runs the stage in place — one
	// stage, no edge, nothing to ask about.
	m2, _ := chatWorkspace(t, classifyingAgent("implementation_wrong"))
	m2 = advanceTo(t, m2, domain.StageImplement)
	m2 = pump(t, m2, m2.routeReentry(m2.rows[0], "run", "the toggle does not do what step 4 describes"))
	if m2.Overlay.Top() != nil {
		t.Error("an in-place re-run asked for a confirm")
	}
	if got := m2.rows[0].F.Stage; got != domain.StageImplement {
		t.Errorf("an in-place re-run moved the card to %s", got)
	}
}

// /bounce <reason> and picking "send it back" are one answer typed two
// ways. Asserted on the outcome rather than on the call graph: both must
// reach plan, and both must leave the same open comment behind.
func TestTypedBounceAndTheRowReachTheSameRouter(t *testing.T) {
	const miss = "nothing in the spec ever asked for offline support"

	viaVerb := verifyCard(t, classifyingAgent("requirement_missing"))
	viaVerb.cardOpen = true
	viaVerb = pump(t, viaVerb, viaVerb.fireVerb("bounce", miss))
	viaVerb = press(t, viaVerb, tea.KeyPressMsg{Code: 'y', Text: "y"})
	viaVerb = pump(t, viaVerb, viaVerb.loadRows)

	viaRow := verifyCard(t, classifyingAgent("requirement_missing"))
	d := &threadDecision{actions: []nextAction{sendBackStep("bounce", "b", "send the failures back")}}
	viaRow = pump(t, viaRow, viaRow.deliverDecisionWords(viaRow.rows[0], d, 0, miss))
	viaRow = press(t, viaRow, tea.KeyPressMsg{Code: 'y', Text: "y"})
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
		f: f, note: "send this back", fallback: "bounce", err: engine.ErrNoClassifier,
	}))
	m = pump(t, m, m.loadRows)
	if got := m.rows[0].F.Stage; got != domain.StageImplement {
		t.Errorf("stage = %s, want implement — verify's own fixed route", got)
	}
}

// The design stage never spends a classification turn: the architect is
// live in the very thread the line was typed into, so the line is
// already delivered correctly by being a turn.
func TestDesignStageNeverClassifies(t *testing.T) {
	asked := 0
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			asked++
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
	}}
	m, _ := chatWorkspace(t, ag)
	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Fatalf("fixture is at %s, want plan", got)
	}
	m = pump(t, m, m.routeReentry(m.rows[0], "changes", "the approach ignores the offline case"))
	if asked != 0 {
		t.Errorf("the design stage spent %d classification turns", asked)
	}
	if got := m.rows[0].F.Stage; got != domain.StagePlan {
		t.Errorf("a design-stage line moved the card to %s", got)
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
