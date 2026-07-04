package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/agent"
)

func TestInboxOps(t *testing.T) {
	b := newInbox()
	if b.len() != 0 || b.next("") != "" {
		t.Fatal("empty inbox misbehaves")
	}
	b.add("FD-001", attnGate, "plan ready")
	b.add("FD-002", attnFailure, "boom")
	b.add("FD-001", attnGate, "plan ready v2") // upsert keeps order, one entry
	if b.len() != 2 {
		t.Fatalf("len = %d, want 2", b.len())
	}
	if got := b.list(); got[0].Feature != "FD-001" || got[1].Feature != "FD-002" {
		t.Errorf("order wrong: %+v", got)
	}
	if b.items["FD-001"].Text != "plan ready v2" {
		t.Error("upsert did not replace text")
	}
	// cycling wraps
	if n := b.next("FD-001"); n != "FD-002" {
		t.Errorf("next(FD-001) = %s, want FD-002", n)
	}
	if n := b.next("FD-002"); n != "FD-001" {
		t.Errorf("next(FD-002) wrap = %s, want FD-001", n)
	}
	if n := b.next("FD-999"); n != "FD-001" {
		t.Errorf("next(unknown) = %s, want first", n)
	}
	b.remove("FD-001")
	if b.len() != 1 || b.list()[0].Feature != "FD-002" {
		t.Errorf("after remove: %+v", b.list())
	}
	b.remove("FD-404") // no-op
}

func TestCompletedRunRaisesGate(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Plan written."},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	// advance to plan (autonomous), run it
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // run
	settleChat(t, eng)

	// drain the engine idle event into the shell so the inbox fills
	m = pumpEngine(t, m)
	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want 1 gate item", m.inbox.len())
	}
	if m.inbox.list()[0].Kind != attnGate {
		t.Errorf("item kind = %s, want gate", m.inbox.list()[0].Kind)
	}
	// acting on the feature (advance) clears the item
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.inbox.len() != 0 {
		t.Error("advancing did not clear the gate item")
	}
}

func TestInboxErrorItem(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: errFake}}
	}}
	m, _ := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // attach chat (brainstorm)
	m = typeString(t, m, "hi")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pumpEngine(t, m)
	if m.inbox.len() != 1 || m.inbox.list()[0].Kind != attnFailure {
		t.Fatalf("error did not raise a failure item: %+v", m.inbox.list())
	}
}

func TestInboxOverlayGolden(t *testing.T) {
	m := populatedShell(100, 30)
	m.inbox.add("FD-042", attnGate, "implement finished — review & advance")
	m.inbox.add("FD-049", attnFailure, "provider rate-limited")
	m.openInbox()
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestTabCyclesAttention(t *testing.T) {
	m := populatedShell(120, 34)
	m.inbox.add("FD-044", attnGate, "review ready")
	m.inbox.add("FD-047", attnQuestion, "which approach?")
	// tab jumps selection to the first attention feature, then cycles
	m.cycleAttention()
	first := m.rows[m.sel].F.ID
	m.cycleAttention()
	second := m.rows[m.sel].F.ID
	if first == second {
		t.Error("tab did not advance through the queue")
	}
	if first != "FD-044" && second != "FD-044" {
		t.Errorf("cycle never hit FD-044: %s then %s", first, second)
	}
}

// errFake is a control-char-free error for inbox tests.
var errFake = fakeErr("model unavailable")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// pumpEngine drains pending engine events into the shell (in tests the
// listenEngine command isn't looping through the tea runtime). It stops
// when no event arrives within a short window.
func pumpEngine(t *testing.T, m *Shell) *Shell {
	t.Helper()
	for i := 0; i < 50; i++ {
		select {
		case ev := <-m.engine.Events():
			m.handleEngineEvent(ev)
		case <-time.After(40 * time.Millisecond):
			return m
		}
	}
	return m
}
