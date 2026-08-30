package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// The pinned decision's focused tests: visibility at the width the thread
// is actually driven at, parity with the chat pane's picker, the idle and
// todo states it exists to unstrand, answering a live ask_user from the
// thread, and the collapse into history once answered.

// TestThreadDecisionVisibleAt36x9 is the pinning arithmetic (PLAN §2.4):
// at 36×9 the thread's body renders zero rows, so a decision drawn in the
// body would be invisible at exactly the width the thread is driven at.
// Its own region above the composer must show the question and the
// highlighted answer while the head and body yield.
func TestThreadDecisionVisibleAt36x9(t *testing.T) {
	m := populatedShell(36, 9)
	m.sel = 1 // FD-042, implement — nothing running, so a decision is open
	out := ansi.Strip(m.threadView(33, 6))
	if !strings.Contains(out, "nothing is running") {
		t.Errorf("36x9 thread missing the open decision's question:\n%s", out)
	}
	if !strings.Contains(out, "run implement") {
		t.Errorf("36x9 thread missing the highlighted answer:\n%s", out)
	}
	if !strings.Contains(out, "┃") {
		t.Errorf("36x9 thread lost the composer under the decision:\n%s", out)
	}
	// the head yields to the decision: the pinned spec line is the first
	// thing dropped, the masthead the second
	if strings.Contains(out, "Implementation notes") {
		t.Errorf("36x9 thread spent rows on the spec line while the decision showed:\n%s", out)
	}
}

// TestThreadDecisionAdvancesIdleTodo: a card in todo has no key at all
// now that empty-composer enter runs nothing — the idle decision is what
// keeps it reachable. Enter answers the highlighted option ("start"),
// which routes through the same advance verb its accelerator names.
func TestThreadDecisionAdvancesIdleTodo(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	// a fresh second card sits in todo, untouched by the fixture's advance
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Second card")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	selectRow(t, m, "FD-002")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open its card page

	d := m.openDecision(m.rows[m.sel])
	if d == nil || len(d.actions) == 0 || d.actions[0].id != "advance" {
		t.Fatalf("todo card has no idle decision to answer: %+v", d)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // answer it
	if m.rows[m.sel].F.Stage != domain.StageBrainstorm {
		t.Fatalf("answering the idle decision left the card at %s, want brainstorm", m.rows[m.sel].F.Stage)
	}
}

// TestThreadDecisionMatchesTheChatPicker is the parity contract: the
// thread's pinned decision and the chat pane's picker render the same
// control for the same pending ask, because they are the same renderer.
func TestThreadDecisionMatchesTheChatPicker(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // detach; the ask stays pending

	ask := eng.Get("FD-001").Snapshot().PendingAsk
	if ask == nil {
		t.Fatal("precondition: the ask is still pending after detach")
	}
	out := ansi.Strip(m.threadView(100, 30))
	want := pickerView(m0Styles(), "FD-001 asks", ask.Question,
		askPickerOptions(ask), 0, nil, ask.MultiPick, 100)
	for _, line := range strings.Split(ansi.Strip(want), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(out, line) {
			t.Errorf("thread decision missing the picker row %q:\n%s", line, out)
		}
	}
}

// TestThreadDecisionAnswersLiveAsk: the thread itself can answer a
// blocking ask_user — the pane's esc only detaches, and the pinned
// decision keeps the question answerable on the card page. This is the
// capability the chat pane's retirement depends on.
func TestThreadDecisionAnswersLiveAsk(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // detach; the ask stays pending

	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // answer the pinned decision
	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("answering from the thread did not clear the pending ask")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got string
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Author == engine.AuthorUser {
			got = msg.Content
		}
	}
	if got != "per-device" {
		t.Errorf("thread answer recorded as %q, want per-device", got)
	}
}

// TestAnsweredAskCollapsesIntoHistory: once answered, an ask is not a
// pinned control any more — it is one compact line in the body's history
// where it happened, the same as a crossed gate.
func TestAnsweredAskCollapsesIntoHistory(t *testing.T) {
	s := m0Styles()
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ask := state.CardEvent{
		Kind: state.EventAsk, At: at,
		Payload: `{"question":"Persist where?","answer":"per-device","actor":"user"}`,
	}
	gate := state.CardEvent{
		Kind: state.EventGate, At: at,
		Payload: `{"from":"spec","to":"plan","actor":"user"}`,
	}
	askLine := ansi.Strip(stageEventLine(s, ask, 80))
	for _, want := range []string{"you answered", "Persist where?", "per-device"} {
		if !strings.Contains(askLine, want) {
			t.Errorf("answered ask line %q missing %q", askLine, want)
		}
	}
	gateLine := ansi.Strip(stageEventLine(s, gate, 80))
	for _, want := range []string{"you advanced", "spec", "plan"} {
		if !strings.Contains(gateLine, want) {
			t.Errorf("crossed gate line %q missing %q", gateLine, want)
		}
	}
	if strings.Contains(askLine, "\n") || strings.Contains(gateLine, "\n") {
		t.Error("answered decisions must collapse to one line each")
	}
}

// TestThreadDecisionWindowsAroundTheCursor: a decision with more options
// than its row budget keeps the question and the highlighted answer
// visible and states what is hidden, rather than trimming from the bottom
// and leaving the cursor on a row that is no longer on screen.
func TestThreadDecisionWindowsAroundTheCursor(t *testing.T) {
	s := m0Styles()
	lines := []string{"gummi  the question."}
	for _, label := range []string{"one", "two", "three", "four", "five"} {
		lines = append(lines, "  "+label)
	}
	// three rows: the question, one option row, the hidden count
	got := windowDecisionBlock(s, lines, 5, 3, 3)
	if len(got) != 3 {
		t.Fatalf("windowed decision = %q, want 3 rows", got)
	}
	if got[0] != lines[0] {
		t.Errorf("question row lost: %q", got[0])
	}
	if !strings.Contains(ansi.Strip(got[1]), "four") {
		t.Errorf("window did not follow the cursor: %q", got[1])
	}
	if !strings.Contains(ansi.Strip(got[2]), "…4 more") {
		t.Errorf("hidden count not stated: %q", got[2])
	}

	// a budget of two keeps the highlighted answer over the count
	got = windowDecisionBlock(s, lines, 5, 4, 2)
	if len(got) != 2 || !strings.Contains(ansi.Strip(got[1]), "five") {
		t.Fatalf("two-row window = %q, want the question and the cursor's row", got)
	}
}
