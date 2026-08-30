package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
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
		// the plan writer's completion triggers the critique pass; a
		// clean critique verdict is what raises the approval gate.
		text := "Plan written."
		if opts.Role == agent.RoleReviewer {
			text = "Plan is sound.\nVERDICT: pass"
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: text},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	// this test is about the gate-raising/clearing mechanism itself, not
	// autopilot — pin the card to off so a clean critique still parks the
	// gate for a human, unaffected by gates' own crossing (autopilot_gate_test.go
	// covers that).
	if err := m.store.SetGateApproval(context.Background(), "FD-001", domain.GateOff); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	// advance to plan (autonomous), run it
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m) // run
	settleChat(t, eng)

	// drain events and loop commands: plan done → critique → pass → gate
	m = drainEngineLoop(t, m)
	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want 1 gate item", m.inbox.len())
	}
	if m.inbox.list()[0].Kind != attnGate {
		t.Errorf("item kind = %s, want gate", m.inbox.list()[0].Kind)
	}
	// acting on the feature (advance) clears the item
	m = toKeys(t, m)
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
	m = openAndAttach(t, m) // attach chat (brainstorm)
	m = typeString(t, m, "hi")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pumpEngine(t, m)
	if m.inbox.len() != 1 || m.inbox.list()[0].Kind != attnFailure {
		t.Fatalf("error did not raise a failure item: %+v", m.inbox.list())
	}
}

func TestNoticeClearInbox(t *testing.T) {
	// a notice whose clearInbox names a feature removes its attention
	// item on receipt — the outcome-driven clear, on the Update goroutine.
	m := populatedShell(80, 24)
	m.inbox.add("FD-001", attnGate, "spec approval pending")
	model, _ := m.Update(noticeMsg{text: "FD-001 → plan", clearInbox: "FD-001"})
	m = model.(*Shell)
	if m.inbox.len() != 0 {
		t.Fatalf("inbox len = %d, want 0 after clearInbox notice", m.inbox.len())
	}
}

func TestNoticeClearInboxNoopWithoutField(t *testing.T) {
	// a notice with an empty clearInbox (an error or gate-blocked return)
	// leaves the item in the queue — the bug being fixed.
	m := populatedShell(80, 24)
	m.inbox.add("FD-001", attnGate, "spec approval pending")
	model, _ := m.Update(noticeMsg{text: "boom", isErr: true})
	m = model.(*Shell)
	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want 1 — plain notice cleared the item", m.inbox.len())
	}
}

func TestGateBlockedKeepsInboxItem(t *testing.T) {
	// pressing g on a gate that turns out to be blocked leaves the
	// needs-attention item in the queue: the advance's error notice carries
	// no clearInbox, so the entry survives until the gate is actually
	// attended to.
	m := specWorkspace(t)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // todo → brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm → spec
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "is this the right approach?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	// a needs-attention item awaits the user on this feature
	m.inbox.add("FD-001", attnGate, "spec approval pending")
	// approving is blocked while the annotation is open
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("blocked advance moved the stage to %s, want spec", m.rows[0].F.Stage)
	}
	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want 1 — blocked advance cleared the item", m.inbox.len())
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
