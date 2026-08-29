package ui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// --- wording helpers ---

func TestAutopilotQuitQuestionWording(t *testing.T) {
	if got := autopilotQuitQuestion([]string{"FD-047"}); got != "1 card is running on autopilot — FD-047." {
		t.Errorf("singular wording = %q", got)
	}
	if got := autopilotQuitQuestion([]string{"FD-044", "FD-047"}); got != "2 cards are running on autopilot — FD-044, FD-047." {
		t.Errorf("plural wording = %q", got)
	}
}

func TestResumeLabelWording(t *testing.T) {
	cases := map[int]string{1: "Resume", 2: "Resume both", 3: "Resume all 3"}
	for n, want := range cases {
		if got := resumeLabel(n); got != want {
			t.Errorf("resumeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- quitCmd wording ---

// TestQuitWithAutopilotLiveSessionWordsDialog: a live autonomous session
// on a card that is not GateOff gets the autopilot wording — it names
// the card, says it stops and picks back up, and never implies it keeps
// going once the terminal closes.
func TestQuitWithAutopilotLiveSessionWordsDialog(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm → spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec → plan
	m = openAndAttach(t, m)                                // run plan (autonomous)
	waitLive(t, eng, "FD-001")

	cmd := m.quitCmd()
	if cmd != nil {
		t.Fatal("quit with a live autopilot session returned a command, want the dialog")
	}
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || d.id != "confirm-quit" {
		t.Fatalf("top overlay = %v, want the confirm-quit dialog", m.Overlay.Top())
	}
	if d.question != "1 card is running on autopilot — FD-001." {
		t.Errorf("question = %q", d.question)
	}
	if d.detail != "they stop where they are and pick up when you reopen." {
		t.Errorf("detail = %q", d.detail)
	}
	if d.confirmLabel != "Stop them and quit" || d.cancelLabel != "Cancel" {
		t.Errorf("buttons = %q/%q, want %q/%q", d.cancelLabel, d.confirmLabel, "Cancel", "Stop them and quit")
	}
}

// TestQuitWithGateOffLiveSessionKeepsOriginalWording: a card driven by
// hand (GateOff) is not "on autopilot" for this wording — quitCmd's
// pre-existing sentence about a discarded in-flight turn is unchanged.
func TestQuitWithGateOffLiveSessionKeepsOriginalWording(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m.rows[0].F.GateApproval = domain.GateOff // set before the run starts, matching the session's own snapshot
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	cmd := m.quitCmd()
	if cmd != nil {
		t.Fatal("quit with a live session returned a command, want the dialog")
	}
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || d.id != "confirm-quit" {
		t.Fatalf("top overlay = %v, want the confirm-quit dialog", m.Overlay.Top())
	}
	if d.question != "quit with live sessions FD-001 (plan)?" {
		t.Errorf("question = %q, want the unchanged plain-session wording", d.question)
	}
	if d.confirmLabel != "Quit" || d.cancelLabel != "Stay" {
		t.Errorf("buttons = %q/%q, want the unchanged Stay/Quit pair", d.cancelLabel, d.confirmLabel)
	}
}

// TestQuitConfirmParksAutopilotSessionOnConfirm: confirming the autopilot
// quit dialog calls StopForQuit, which pauses the session and leaves a
// quit park behind in the card-event log — the marker a reopen reads.
func TestQuitConfirmParksAutopilotSessionOnConfirm(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	m.quitCmd()
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok {
		t.Fatalf("top overlay = %v, want confirmDialog", m.Overlay.Top())
	}
	cmd := d.onConfirm()
	if cmd == nil {
		t.Fatal("onConfirm returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("onConfirm should still quit")
	}

	if st := eng.Get("FD-001").State(); st != engine.StatePaused {
		t.Errorf("session state after confirm = %s, want paused", st)
	}
	evs, err := m.store.Events(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind != state.EventPark {
			continue
		}
		var p state.ParkPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err == nil && p.Reason == state.ParkReasonQuit {
			found = true
		}
	}
	if !found {
		t.Error("no quit park event was written")
	}
}

// --- quitResumeDialog: keys ---

func TestQuitResumeDialogDefaultsToNotNow(t *testing.T) {
	called := false
	d := newQuitResumeDialog([]engine.QuitStoppedCard{{Feature: domain.Feature{ID: "FD-001"}}}, "7h0m ago", func() tea.Cmd {
		called = true
		return nil
	})
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "enter"})
	if !done || cmd != nil {
		t.Fatalf("enter on default focus: done=%v cmd=%v, want done, no cmd", done, cmd)
	}
	if called {
		t.Fatal("default enter (Not now) must not resume")
	}
}

func TestQuitResumeDialogRightThenEnterResumes(t *testing.T) {
	called := false
	d := newQuitResumeDialog([]engine.QuitStoppedCard{{Feature: domain.Feature{ID: "FD-001"}}}, "7h0m ago", func() tea.Cmd {
		called = true
		return nil
	})
	d.HandleKey(tea.KeyPressMsg{Text: "right"})
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "enter"})
	if !done {
		t.Fatal("enter should close the dialog")
	}
	if cmd != nil {
		cmd()
	}
	if !called {
		t.Fatal("the resume button did not call onResume")
	}
}

// TestQuitResumeDialogEscNeverResumes: esc must leave every card alone
// no matter where the button focus sits — nothing may restart on its own.
func TestQuitResumeDialogEscNeverResumes(t *testing.T) {
	called := false
	d := newQuitResumeDialog([]engine.QuitStoppedCard{{Feature: domain.Feature{ID: "FD-001"}}}, "7h0m ago", func() tea.Cmd {
		called = true
		return nil
	})
	d.HandleKey(tea.KeyPressMsg{Text: "right"}) // focus the resume button
	done, cmd := d.HandleKey(tea.KeyPressMsg{Text: "esc"})
	if !done || cmd != nil {
		t.Fatalf("esc: done=%v cmd=%v, want done, no cmd", done, cmd)
	}
	if called {
		t.Fatal("esc must not resume even with the resume button focused")
	}
}

// --- maybeOfferQuitResume: the startup trigger ---

// seedQuitStopped persists a feature parked exactly the way
// engine.Engine.StopForQuit would have left it: a paused session
// snapshot plus a quit park event, so engine.Restore + QuitStoppedCards
// see the same shape a real quit-then-reopen produces.
func seedQuitStopped(t *testing.T, store *state.Store, num int, title string, stage domain.Stage, gate string, corrective int, parkedAt time.Time) domain.Feature {
	t.Helper()
	ctx := context.Background()
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug, Stage: stage,
		GateApproval: gate, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, state.SessionSnapshot{
		Feature: f.ID, Stage: f.Stage, Role: "implementer", State: "paused",
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(state.ParkPayload{Reason: state.ParkReasonQuit})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(ctx, state.CardEvent{
		Feature: f.ID, Stage: f.Stage, Kind: state.EventPark, At: parkedAt, Payload: string(payload),
	}); err != nil {
		t.Fatal(err)
	}
	for range corrective {
		if err := store.IncrementRounds(ctx, f.ID, domain.RoundKindCorrective); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// TestMaybeOfferQuitResumePushesDialogWithBothCards: Init's own trigger,
// end to end — two quit-stopped autopilot cards seeded the way a real
// quit leaves them, restored by a fresh engine, offered back sorted by
// id with what each was doing.
func TestMaybeOfferQuitResumePushesDialogWithBothCards(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	parked := time.Now().Add(-7 * time.Hour)
	fSearch := seedQuitStopped(t, store, 44, "search", domain.StageVerify, domain.GateGates, 0, parked)
	fCSV := seedQuitStopped(t, store, 47, "csv export", domain.StageImplement, domain.GateFull, 2, parked)

	eng := engine.New(engine.Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.maybeOfferQuitResume()

	d, ok := m.Overlay.Top().(*quitResumeDialog)
	if !ok {
		t.Fatalf("top overlay = %T, want *quitResumeDialog", m.Overlay.Top())
	}
	if len(d.cards) != 2 {
		t.Fatalf("got %d cards, want 2: %+v", len(d.cards), d.cards)
	}
	// "FD-044" sorts before "FD-047".
	if d.cards[0].Feature.ID != fSearch.ID || d.cards[1].Feature.ID != fCSV.ID {
		t.Fatalf("cards = [%s %s], want [%s %s]", d.cards[0].Feature.ID, d.cards[1].Feature.ID, fSearch.ID, fCSV.ID)
	}
	if d.cards[0].Corrective != 0 {
		t.Errorf("search corrective = %d, want 0", d.cards[0].Corrective)
	}
	if d.cards[1].Corrective != 2 {
		t.Errorf("csv export corrective = %d, want 2", d.cards[1].Corrective)
	}
}

// TestMaybeOfferQuitResumeNothingToOffer: no quit park in the log means
// nothing is offered — a plain paused/exhausted card must not trigger
// this prompt.
func TestMaybeOfferQuitResumeNothingToOffer(t *testing.T) {
	ws, store, wt := uiRepo(t)
	eng := engine.New(engine.Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.maybeOfferQuitResume()

	if m.Overlay.Contains("quit-resume") {
		t.Fatal("nothing was quit-stopped; the reopen prompt should not appear")
	}
}

// TestResumeCardRestartsMidStageSession: resumeCard's real job — a
// paused, quit-stopped mid-stage card (StopForQuit's only shape: see its
// own doc comment) restarts through the ordinary resume-in-place path,
// not a no-op mode write.
func TestResumeCardRestartsMidStageSession(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := seedQuitStopped(t, store, 1, "csv export", domain.StageImplement, domain.GateFull, 0, time.Now())
	if _, err := wt.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	eng := engine.New(engine.Config{Agents: singleAgent(ag), Store: store, Pool: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if st := eng.Get(f.ID).State(); st != engine.StatePaused {
		t.Fatalf("precondition: restored state = %s, want paused", st)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)

	cmd := m.resumeCard(f)
	if cmd == nil {
		t.Fatal("resumeCard returned no command")
	}
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("resumeCard failed: %s", nm.text)
		}
	}
	waitLive(t, eng, f.ID)
}

// --- golden ---

// TestQuitResumeDialogGolden renders the reopen prompt over two offered
// cards, matching the design mock's shape: a stage line with corrections
// spent, and one without.
func TestQuitResumeDialogGolden(t *testing.T) {
	m := populatedShell(100, 30)
	cards := []engine.QuitStoppedCard{
		{Feature: domain.Feature{ID: "FD-047", Title: "csv export", Stage: domain.StageImplement}, Corrective: 2},
		{Feature: domain.Feature{ID: "FD-044", Title: "search", Stage: domain.StageVerify}},
	}
	m.Overlay.Push(newQuitResumeDialog(cards, "7h0m ago", func() tea.Cmd { return nil }))
	golden.RequireEqual(t, []byte(m.View().Content))
}
