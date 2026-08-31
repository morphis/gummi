package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
)

// openBoardTab drives a real shell onto the agent tab and waits for its
// board session to open, through the real key path (alt+3 -> gotoTab ->
// ensureBoardSession's command) rather than reaching into m.board
// directly — the same reasoning openAndAttach (thread_conversation_test
// .go) gives for driving a card's own attach through real keys instead
// of poking the field. agent.NewFake never actually shells out, so
// ensureBoardSession's command settles synchronously under pump/press.
func openBoardTab(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m = press(t, m, tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt})
	if m.tab != TabAgent {
		t.Fatalf("tab = %v, want TabAgent", m.tab)
	}
	if m.board == nil {
		t.Fatalf("board session did not open: boardErr = %q", m.boardErr)
	}
	if !m.boardInput.Focused() {
		t.Fatal("the board composer should be focused on arrival, like the card thread's own on open")
	}
	return m
}

// settleBoard waits for the board session to finish a turn — the board
// counterpart to thread_conversation_test.go's settleChat.
func settleBoard(t *testing.T, b *engine.BoardSession) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		snap := b.Snapshot()
		if !snap.Busy && len(snap.Transcript) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("board session did not settle")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestBoardThreadRendersTranscript: a turn sent through the board
// composer reaches the board session, and both the user's line and the
// backend's reply show up in the tab's own render — transcriptLines
// (transcript.go), reused as-is rather than a second copy of it.
func TestBoardThreadRendersTranscript(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hello from the board"))
	m = openBoardTab(t, m)

	m = typeString(t, m, "what's on the board?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "what's on the board?") {
		t.Errorf("board view missing the sent turn:\n%s", view)
	}
	if !strings.Contains(view, "hello from the board") {
		t.Errorf("board view missing the reply:\n%s", view)
	}
	if m.boardInput.Value() != "" {
		t.Errorf("composer not cleared after send: %q", m.boardInput.Value())
	}
}

// TestBoardComposerQDoesNotQuit is the regression for the trap the brief
// calls out by name: handleKey's "q quits from the board root" check
// sits right below where this route has to be added, and typing a
// perfectly ordinary "q" into a board message must type a q, never quit
// gummi out from under a live conversation.
func TestBoardComposerQDoesNotQuit(t *testing.T) {
	m := populatedShell(120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt})
	if m.tab != TabAgent {
		t.Fatalf("tab = %v, want TabAgent", m.tab)
	}
	if !m.boardInput.Focused() {
		t.Fatal("the board composer should be focused on arrival")
	}

	_, cmd := m.update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q typed into the board composer quit gummi")
		}
	}
	if got := m.boardInput.Value(); got != "q" {
		t.Fatalf("board composer = %q, want the typed %q", got, "q")
	}
	if m.tab != TabAgent {
		t.Fatalf("q should not have moved off the agent tab, got %v", m.tab)
	}
}

// TestBoardComposerEscInterrupts: esc has no page to leave here — the
// agent tab IS the board conversation — so its one job is interrupting
// whatever the board session is doing, never quitting and never leaving
// the tab (both of which a card thread's own esc can mean, in different
// states — the board composer means neither).
func TestBoardComposerEscInterrupts(t *testing.T) {
	ag := agent.NewFake("hi")
	var interrupted bool
	ag.OnInterrupt = func() { interrupted = true }
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)

	_, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc on the board composer should return the interrupt command")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("esc on the board composer quit gummi")
	}
	if !interrupted {
		t.Fatal("esc did not reach the board session's Interrupt")
	}
	if m.tab != TabAgent {
		t.Fatalf("esc left the agent tab: tab = %v, want TabAgent", m.tab)
	}
	if !m.boardInput.Focused() {
		t.Fatal("esc should not have blurred the board composer")
	}
}

// TestBoardAndCardDraftsAreIndependent: boardInput and threadInput are
// deliberately two separate Shell fields (the Shell struct's own doc
// comment on why) — typing on one and switching tabs must never leave it
// sitting in the other's box.
func TestBoardAndCardDraftsAreIndependent(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.cardOpen {
		t.Fatal("enter should have opened the card page")
	}
	m = typeString(t, m, "card draft")

	m = press(t, m, tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt}) // -> agent tab
	if !m.boardInput.Focused() {
		t.Fatal("the board composer should be focused on arrival")
	}
	if got := m.boardInput.Value(); got != "" {
		t.Fatalf("a fresh board composer already carries text: %q", got)
	}
	m = typeString(t, m, "board draft")

	if got := m.threadInput.Value(); got != "card draft" {
		t.Fatalf("the card's draft changed while typing on the board: %q", got)
	}
	if got := m.boardInput.Value(); got != "board draft" {
		t.Fatalf("board composer = %q, want %q", got, "board draft")
	}

	m = press(t, m, tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt}) // -> board tab
	if m.tab != TabBoard {
		t.Fatalf("tab = %v, want TabBoard", m.tab)
	}
	if !m.cardOpen {
		t.Fatal("the card page did not come back with the board tab")
	}
	if got := m.threadInput.Value(); got != "card draft" {
		t.Fatalf("card draft after the round trip = %q, want %q", got, "card draft")
	}
}

// TestHandleEngineEventBoardIgnoresEmptyFeature: engine.EventBoard is the
// one EventKind whose Feature is always empty (engine.Event's own doc
// comment) — handleEngineEvent must not panic reaching for a row or a
// session keyed by it, and must not raise any follow-up command of its
// own (re-rendering happens for free, back in Update).
func TestHandleEngineEventBoardIgnoresEmptyFeature(t *testing.T) {
	m := populatedShell(120, 34)
	cmd := m.handleEngineEvent(engine.Event{Kind: engine.EventBoard})
	if cmd != nil {
		t.Fatalf("EventBoard should not raise a follow-up command, got %v", cmd)
	}
}

// TestBoardPasteGoesToTheBoardComposer is the paste half of
// TestBoardAndCardDraftsAreIndependent, and it needs its own test
// because the two routes are separate code: handleKey scopes the card
// composer to the board tab, and handlePaste did not.
//
// Both textareas can report Focused() simultaneously — a card page is
// hidden on a tab switch, never closed, and nothing blurs its input,
// while gotoTab focuses the board's. An ungated card branch therefore
// answered every paste on every tab, and text pasted while looking at
// the board landed invisibly in the card's composer.
func TestBoardPasteGoesToTheBoardComposer(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.cardOpen || !m.threadInput.Focused() {
		t.Fatal("enter should have opened the card page with its composer focused")
	}
	m = press(t, m, tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt}) // -> agent tab
	if !m.threadInput.Focused() {
		t.Fatal("precondition: the card composer stays focused across the tab switch")
	}

	model, _ := m.Update(tea.PasteMsg{Content: "pasted on the board"})
	m = model.(*Shell)

	if got := m.boardInput.Value(); got != "pasted on the board" {
		t.Errorf("board composer = %q, want the pasted text", got)
	}
	if got := m.threadInput.Value(); got != "" {
		t.Errorf("the paste landed in the card's hidden composer instead: %q", got)
	}
}

// TestQuitWhileBoardBusyConfirmsFirst: a board turn is not an engine
// session (Sessions() is keyed by card), so the quit path's live-session
// check never saw it. Every other kind of in-flight work in quitCmd
// raises a confirm dialog naming what is about to be lost; a board turn
// — which is spending money and may be part-way through acting on cards
// through its own tools — used to be thrown away in silence.
func TestQuitWhileBoardBusyConfirmsFirst(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("working on it"))
	m = openBoardTab(t, m)

	// a turn in flight: send, and do not settle it.
	m = typeString(t, m, "do a thing")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.board.Snapshot().Busy {
		t.Skip("the fake settled before the assertion could run; nothing to guard here")
	}

	if cmd := m.quitCmd(); cmd != nil {
		t.Fatal("quitCmd returned a command: it quit outright instead of asking")
	}
	if !m.Overlay.Contains("confirm-quit") {
		t.Error("no confirmation was raised while the board agent was mid-turn")
	}
}

// TestAgentChoiceClearsBoardFailure: boardErr is what ensureBoardSession
// refuses to retry past, so a single failed open used to disable the tab
// for the rest of the process. Choosing an agent is the fix for the
// commonest cause of that failure, so it has to clear the flag — the
// same reset agentChosenMsg already did for the pty's agentErr.
func TestAgentChoiceClearsBoardFailure(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m.boardErr = "no agent configured (set a model/provider to enable agents)"

	model, _ := m.Update(agentChosenMsg{name: "claude"})
	m = model.(*Shell)

	if m.boardErr != "" {
		t.Errorf("boardErr = %q, want it cleared so the next visit retries", m.boardErr)
	}
}
