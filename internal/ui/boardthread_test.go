package ui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
)

// openBoardTab drives a real shell onto the agent tab and waits for its
// board session to open, through the real key path (alt+4 -> gotoTab ->
// ensureBoardSession's command) rather than reaching into m.board
// directly — the same reasoning openAndAttach (thread_conversation_test
// .go) gives for driving a card's own attach through real keys instead
// of poking the field. agent.NewFake never actually shells out, so
// ensureBoardSession's command settles synchronously under pump/press.
func openBoardTab(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m = press(t, m, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt})
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
	m = press(t, m, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt})
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

	m = press(t, m, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt}) // -> agent tab
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
	m = press(t, m, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt}) // -> agent tab
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

// composerBottomGap reports how many rows sit between the composer's ┃
// and the bottom of the rendered frame — the status bar plus whatever
// air the surface keeps above it. It reads the glyph rather than the
// placeholder text because the two surfaces word their placeholders
// differently (boardPlaceholderText vs placeholderText) while sharing
// the one marker newThreadInput draws down the composer's left edge.
func composerBottomGap(t *testing.T, view string) int {
	t.Helper()
	rows := strings.Split(ansi.Strip(view), "\n")
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.Contains(rows[i], "┃") {
			return len(rows) - 1 - i
		}
	}
	t.Fatalf("no composer found in:\n%s", view)
	return 0
}

// TestBoardComposerKeepsTheCardThreadsBottomGap: the two surfaces render
// the same composer widget, so it must sit the same distance above the
// status bar on both. The card thread gets its blank row from its page
// wrapper (cardPageView spends cardPageChrome's `blank` around
// threadView); the agent tab has no wrapper, so before boardPageBlank
// the board composer sat flush against the status bar and the two
// chrome-coloured rows read as one control — exactly what that row
// exists to prevent.
func TestBoardComposerKeepsTheCardThreadsBottomGap(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))

	card := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !card.cardOpen {
		t.Fatal("precondition: enter should open the card page")
	}
	want := composerBottomGap(t, card.View().Content)

	board := openBoardTab(t, m)
	if got := composerBottomGap(t, board.View().Content); got != want {
		t.Errorf("board composer sits %d rows above the bottom, card thread %d — the same widget on two surfaces must keep the same air", got, want)
	}
	if want < 2 {
		t.Fatalf("precondition: at this height the card thread should keep a blank row under its composer, got a gap of %d", want)
	}
}

// TestBoardComposerGivesUpItsBlankRowWhenShort: the blank is chrome, and
// chrome yields to the control on a short terminal — the same trade
// cardPageChrome makes. Both surfaces have to make it at the same
// height, or the "same air" guarantee above just moves the mismatch to
// small windows.
func TestBoardComposerGivesUpItsBlankRowWhenShort(t *testing.T) {
	if got := boardPageBlank(composerBlankRows); got != 1 {
		t.Errorf("boardPageBlank(%d) = %d, want 1 — the row is affordable at its own budget", composerBlankRows, got)
	}
	if got := boardPageBlank(composerBlankRows - 1); got != 0 {
		t.Errorf("boardPageBlank(%d) = %d, want 0 — below the budget the composer sits flush", composerBlankRows-1, got)
	}
}

// TestBoardScrollClampMatchesTheRenderedHeight: boardThreadSize feeds
// both the page step and maxBoardScroll, so it has to subtract the same
// row boardThreadView spends — a clamp measured against a taller window
// than the one drawn stops paging one row short of the oldest line.
func TestBoardScrollClampMatchesTheRenderedHeight(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)

	main := m.computeLayout().Main
	if main.Dy() < composerBlankRows {
		t.Skipf("pane too short (%d rows) for the blank row to be in play", main.Dy())
	}
	_, h := m.boardThreadSize()
	if want := main.Dy() - 1; h != want {
		t.Errorf("boardThreadSize height = %d, want %d — the pane less boardPageBlank's row", h, want)
	}
}

// TestBoardClearStartsAFreshSession: "/clear" typed into the board
// composer is answered by the composer itself — the old session is
// closed, a new one opens in its place, and the transcript it had
// accumulated is gone from the tab. The line must not reach the board as
// a message: a person typing it is asking gummi to start over, not
// asking the agent to.
func TestBoardClearStartsAFreshSession(t *testing.T) {
	ag := agent.NewFake("hello from the board")
	// a Responder rather than the bare Reply, so the test can say what
	// the backend was actually asked — "the command was not sent" is the
	// half of this that the transcript alone cannot show.
	var mu sync.Mutex
	var sent []string
	ag.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "hello from the board"},
			{Kind: agent.EventIdle},
		}
	}
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)
	first := m.board

	m = typeString(t, m, "what's on the board?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	m = typeString(t, m, boardClearCommand)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.board == nil {
		t.Fatalf("clear left no session open: boardErr = %q", m.boardErr)
	}
	if m.board == first {
		t.Fatal("clear kept the same session — its context and spend would have survived")
	}
	if n := len(m.board.Snapshot().Transcript); n != 0 {
		t.Fatalf("the fresh session already carries %d transcript entries", n)
	}
	if got := m.boardInput.Value(); got != "" {
		t.Errorf("composer not cleared after the command: %q", got)
	}
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "hello from the board") {
		t.Errorf("the cleared transcript is still on screen:\n%s", view)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0] != "what's on the board?" {
		t.Errorf("the backend was sent %q, want only the one real turn", sent)
	}
}

// TestBoardSlashLineWithMoreWordsIsAMessage: only the whole line matches
// the command. A message can open with a slash — a path, or the very
// word "clear" aimed at the board's own tools — and swallowing those as
// a mistyped command would make the composer unusable for exactly the
// lines a board conversation is for.
func TestBoardSlashLineWithMoreWordsIsAMessage(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("on it"))
	m = openBoardTab(t, m)
	first := m.board

	const line = "/clear the verify backlog"
	m = typeString(t, m, line)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	if m.board != first {
		t.Fatal("a message that merely starts with a slash closed the session")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, line) {
		t.Errorf("the line never reached the board as a message:\n%s", view)
	}
}

// TestBoardComposerCtrlCClearsTheDraft: ctrl+c is hoisted above the
// overlay straight into quitCmd, which left this surface — the one place
// in gummi where a person types paragraphs — with no way to empty the box
// at all: the widget's own ctrl+u deletes to the cursor, and the key
// every coding CLI binds to "clear the line" exited gummi mid-sentence.
func TestBoardComposerCtrlCClearsTheDraft(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "half a thought")

	_, cmd := m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("ctrl+c with a draft in the composer quit gummi")
		}
	}
	if got := m.boardInput.Value(); got != "" {
		t.Fatalf("board composer = %q, want it emptied", got)
	}
	if m.tab != TabAgent {
		t.Fatalf("ctrl+c left the agent tab: tab = %v, want TabAgent", m.tab)
	}
	if !m.boardInput.Focused() {
		t.Fatal("ctrl+c blurred the board composer")
	}
}

// TestBoardComposerCtrlCClosesTheCompletionPopup: the popup is derived
// from the line, so clearing the line has to re-derive it — otherwise a
// "/" cleared away leaves its command list hanging over a composer whose
// text no longer has anything to complete.
func TestBoardComposerCtrlCClosesTheCompletionPopup(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "/")
	if m.boardComplete == nil {
		t.Fatal("typing / did not open the completion popup")
	}

	m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.boardComplete != nil {
		t.Error("the completion popup outlived the line it was derived from")
	}
}

// TestBoardComposerCtrlCOnEmptyLineInterrupts: with nothing typed, the
// turn is what there is to cancel — the same thing esc does, reached by
// the key a person arriving from a coding CLI presses first.
func TestBoardComposerCtrlCOnEmptyLineInterrupts(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := agent.NewFake("working on it")
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	var interrupted bool
	ag.OnInterrupt = func() { interrupted = true }
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)

	m = typeString(t, m, "do a thing")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.board.Snapshot().Busy {
		t.Fatal("the board session is not mid-turn with its responder blocked")
	}
	if m.boardInput.Value() != "" {
		t.Fatalf("the composer should have cleared on send, got %q", m.boardInput.Value())
	}

	_, cmd := m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c on an empty line mid-turn returned no command")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("ctrl+c interrupted nothing and quit gummi mid-turn")
	}
	cmd()
	if !interrupted {
		t.Fatal("ctrl+c did not reach the board session's Interrupt")
	}
}

// TestBoardComposerCtrlCIdleStillQuits: the cancel contract narrows by
// what there is to cancel, and with an empty composer and no turn in
// flight there is nothing — so the key must still be the exit it is
// everywhere else in gummi, not a keystroke that silently does nothing.
func TestBoardComposerCtrlCIdleStillQuits(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)
	if m.boardInput.Value() != "" || m.board.Snapshot().Busy {
		t.Skip("the board session is not idle with an empty composer")
	}

	_, cmd := m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c on an idle, empty composer did nothing at all")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("ctrl+c returned %T, want QuitMsg", cmd())
	}
}

// TestBoardComposerCtrlCLeavesADialogTheHoistToQuit: the hoist above the
// overlay exists so a modal can never trap the one key every terminal
// program answers. A picker over this tab (/profile, /model) is such a
// modal, so the composer's cancel must stand down while one is up.
func TestBoardComposerCtrlCLeavesADialogTheHoistToQuit(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "a draft that must survive the dialog")
	m.Overlay.Push(&confirmDialog{id: "test-modal", question: "?", onConfirm: func() tea.Cmd { return nil }})

	_, cmd := m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c over a dialog returned no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("ctrl+c over a dialog returned %T, want QuitMsg", cmd())
	}
	if m.boardInput.Value() == "" {
		t.Error("the draft was cleared by a ctrl+c that belonged to the dialog")
	}
}

// TestBoardCancelBindingNamesTheNextPress: the bar is read as "what this
// key does now", so the ctrl+c row has to track the three states rather
// than name one of them everywhere.
func TestBoardCancelBindingNamesTheNextPress(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openBoardTab(t, m)

	if b := m.boardCancelBinding(); b.label != "" || b.bar {
		t.Errorf("idle, empty composer: binding = %+v, want an unlabelled help-only row", b)
	}
	m = typeString(t, m, "x")
	b := m.boardCancelBinding()
	if b.label != "clear" || !b.bar {
		t.Errorf("with a draft: binding = %+v, want a barred clear row", b)
	}
	for _, h := range barHints(m.agentBindings()) {
		if h.Key == "ctrl+c" && h.Label == "clear" {
			return
		}
	}
	t.Error("the agent tab's bar never names ctrl+c as the clear key")
}

// TestBoardComposerNewlineKeys: enter sends here, so the composer needs a
// key that means "line break" — and had none: the widget's own
// InsertNewline is bound to the enter this handler claims, so a paragraph
// could only ever be pasted in. All three spellings a terminal might
// deliver have to work, since which one arrives is the terminal's choice.
func TestBoardComposerNewlineKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := agentWorkspace(t, agent.NewFake("hi"))
			m = openBoardTab(t, m)
			m = typeString(t, m, "first")
			m = press(t, m, tc.key)
			m = typeString(t, m, "second")

			if got, want := m.boardInput.Value(), "first\nsecond"; got != want {
				t.Fatalf("composer = %q, want %q", got, want)
			}
			if len(m.board.Snapshot().Transcript) != 0 {
				t.Fatal("the newline key sent the line instead of breaking it")
			}
		})
	}
}

// TestBoardHistoryRecallsSentLines: ↑/↓ walk the lines already sent, the
// recall every shell and every coding CLI has. Without it a sent line
// lived only in the transcript — readable, not re-sendable — so fixing a
// typo in a long message meant typing the whole thing again.
func TestBoardHistoryRecallsSentLines(t *testing.T) {
	ag := agent.NewFake("ok")
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)
	for _, line := range []string{"first thing", "second thing"} {
		m = typeString(t, m, line)
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		settleBoard(t, m.board)
	}
	m = typeString(t, m, "half-written")

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.boardInput.Value(); got != "second thing" {
		t.Fatalf("first ↑ = %q, want the newest sent line", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.boardInput.Value(); got != "first thing" {
		t.Fatalf("second ↑ = %q, want the older sent line", got)
	}
	// off the oldest entry ↑ holds, rather than wrapping or emptying
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.boardInput.Value(); got != "first thing" {
		t.Fatalf("↑ past the oldest = %q, want to stay put", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.boardInput.Value(); got != "half-written" {
		t.Fatalf("↓ back off the newest = %q, want the interrupted draft returned", got)
	}
}

// TestBoardHistoryKeepsMultiLineDraftsNavigable: ↑ recalls only from the
// first row, so the rows of a paragraph the user is still writing stay
// reachable with the same key.
func TestBoardHistoryKeepsMultiLineDraftsNavigable(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "sent line")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	m = typeString(t, m, "one")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m = typeString(t, m, "two")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // into the draft's own first row
	if got, want := m.boardInput.Value(), "one\ntwo"; got != want {
		t.Fatalf("↑ inside a multi-line draft replaced it: %q, want %q", got, want)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // now from row 0: recall
	if got := m.boardInput.Value(); got != "sent line" {
		t.Fatalf("↑ from the first row = %q, want the recalled line", got)
	}
}

// TestBoardHistoryRecordsCommandsAndSurvivesClear: a command is typed the
// same way a sentence is, so ↑ has to bring it back — and /clear ends the
// conversation, not the record of what the user typed, exactly as a
// shell's history outlives `clear`.
func TestBoardHistoryRecordsCommandsAndSurvivesClear(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "a message")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)
	m = typeString(t, m, boardClearCommand)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.boardInput.Value(); got != "" {
		t.Fatalf("the composer should be empty after /clear, got %q", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.boardInput.Value(); got != boardClearCommand {
		t.Fatalf("↑ after /clear = %q, want the command itself recalled", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.boardInput.Value(); got != "a message" {
		t.Fatalf("↑ twice = %q, want the line sent before the clear", got)
	}
}

// TestBoardRefusedTurnKeepsTheLine: the backend refusing a second turn is
// "not now", not a failure. The echo comes back out of the transcript
// (engine's own half), the session is not marked broken, and the line
// returns to the composer it was typed in — the same contract the card
// thread has had since ErrBusy stopped killing stages.
func TestBoardRefusedTurnKeepsTheLine(t *testing.T) {
	ag := agent.NewFake("ok")
	ag.SendErr = agent.ErrBusy
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)

	m = typeString(t, m, "a line the backend will refuse")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.boardInput.Value(); got != "a line the backend will refuse" {
		t.Errorf("composer = %q, want the refused line handed back", got)
	}
	snap := m.board.Snapshot()
	if snap.Err != nil {
		t.Errorf("a refusal was recorded as the session's error: %v", snap.Err)
	}
	for _, e := range snap.Transcript {
		if e.Author == engine.AuthorUser {
			t.Error("the transcript kept the echo of a line the agent never received")
		}
	}
	if !strings.Contains(m.notice.text, "mid-turn") {
		t.Errorf("notice = %q, want it to say the turn is still running", m.notice.text)
	}
}

// TestBoardBusySpinnerNamesTheInterrupt: the hint belongs on the line the
// eye is already on while a turn runs, not in the key bar alone.
func TestBoardBusySpinnerNamesTheInterrupt(t *testing.T) {
	// A blocked responder rather than a settled one: the busy window of a
	// fake that answers immediately is a race, and what is being asserted
	// here is exactly what the screen says DURING it.
	release := make(chan struct{})
	defer close(release)
	ag := agent.NewFake("working on it")
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)
	m = typeString(t, m, "do a thing")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.board.Snapshot().Busy {
		t.Fatal("the board session is not mid-turn with its responder blocked")
	}

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "thinking…") {
		t.Fatalf("no spinner line on screen:\n%s", view)
	}
	if !strings.Contains(view, "esc to interrupt") {
		t.Errorf("the spinner line never names the interrupt key:\n%s", view)
	}
}

// TestBoardEscRowOnlyWhileATurnRuns: the bar says what the next press
// does, so "esc interrupt" over an idle conversation is a key promised
// and not kept.
func TestBoardEscRowOnlyWhileATurnRuns(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := agent.NewFake("working on it")
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	m, _ := agentWorkspace(t, ag)
	m = openBoardTab(t, m)
	if m.boardEscBinding().bar {
		t.Error("idle: the bar offers esc as an interrupt with no turn to interrupt")
	}
	if m.boardEscBinding().help == "" {
		t.Error("esc dropped out of the help table too, where it belongs either way")
	}

	m = typeString(t, m, "do a thing")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.board.Snapshot().Busy {
		t.Fatal("the board session is not mid-turn with its responder blocked")
	}
	if !m.boardEscBinding().bar {
		t.Error("mid-turn: the bar never names the interrupt key")
	}
}
