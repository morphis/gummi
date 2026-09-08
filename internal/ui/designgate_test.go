package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// pressG is the raw board keypress, without pressAdvance's answer to the
// confirmation — these tests are about the confirmation itself.
func pressG(t *testing.T, m *Shell) *Shell {
	t.Helper()
	return press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
}

// designGateDialog returns the open design-gate confirmation, failing if
// there isn't one.
func designGateDialog(t *testing.T, m *Shell) *confirmDialog {
	t.Helper()
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || d.id != "confirm-design-gate" {
		t.Fatalf("top overlay = %#v, want the design-gate confirmation (notice %q)", m.Overlay.Top(), m.notice.text)
	}
	return d
}

// TestBoardGAtTheDesignGateAsksFirst: g on the board no longer walks a
// card out of the design phase on one keypress. The board's 1..9 jumps
// are positional and positions shift as cards move between sections, so
// the card under the cursor is not necessarily the card the reader meant
// — and plan → implement has no undo.
func TestBoardGAtTheDesignGateAsksFirst(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("ok")) // FD-001 sits at plan
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("fixture stage = %s, want plan", m.rows[0].F.Stage)
	}

	m = pressG(t, m)
	d := designGateDialog(t, m)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatal("g crossed the gate before the confirmation was answered")
	}
	// it names the card and the edge it is about to cross
	if !strings.Contains(d.question, "FD-001") {
		t.Errorf("question = %q, does not name the card", d.question)
	}
	if !strings.Contains(d.question, "plan → implement") {
		t.Errorf("question = %q, does not name the edge", d.question)
	}
	if d.confirmLabel != "Advance" || d.cancelLabel != "Stay" {
		t.Errorf("buttons = %q/%q, want Stay/Advance", d.cancelLabel, d.confirmLabel)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Errorf("confirming did not cross the gate (at %s, notice %q)", m.rows[0].F.Stage, m.notice.text)
	}
}

// TestBoardGAtTheDesignGateCancelsOnN: the answer that costs nothing is
// the default one — declining leaves the card exactly where it was.
func TestBoardGAtTheDesignGateCancelsOnN(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("ok"))
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)

	m = pressG(t, m)
	designGateDialog(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Errorf("declining the confirmation still crossed the gate (at %s)", m.rows[0].F.Stage)
	}
	if m.Overlay.Top() != nil {
		t.Errorf("the confirmation stayed open after n: %#v", m.Overlay.Top())
	}
}

// TestBoardGOutsideTheDesignGateDoesNotAsk: todo → plan starts a stage
// rather than closing one, and the verify gate already has its own
// confirmation (the landing dialog). Neither grows a second one.
func TestBoardGOutsideTheDesignGateDoesNotAsk(t *testing.T) {
	m, root := newWorkspace(t)
	_ = root
	m = pump(t, m, m.Init())
	if msg := m.createFeature(formResult{Desc: "a todo card"})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StageTodo {
		t.Fatalf("fixture stage = %s, want todo", m.rows[0].F.Stage)
	}
	m = pressG(t, m)
	if d, ok := m.Overlay.Top().(*confirmDialog); ok && d.id == "confirm-design-gate" {
		t.Error("todo → plan asked for a confirmation")
	}
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Errorf("g on a todo card did not start the design stage (at %s)", m.rows[0].F.Stage)
	}
}

// TestCardPageApproveDoesNotAskAgain: the card page's own surfaces route
// through the same boardVerb case, and each already made the reader name
// the card and choose the act — so the confirmation lives on the board's
// key layer alone, and a typed /approve crosses in one step as before.
func TestCardPageApproveDoesNotAskAgain(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("ok"))
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)
	m = openCardPage(t, m)

	m.threadInput.SetValue("/approve")
	m = pump(t, m, m.submitThreadInput(m.rows[0]))
	if d, ok := m.Overlay.Top().(*confirmDialog); ok && d.id == "confirm-design-gate" {
		t.Fatal("a typed /approve on the card page raised the board's confirmation")
	}
	m = pump(t, m, m.loadRows)
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Errorf("/approve did not cross the gate (at %s, notice %q)", m.rows[0].F.Stage, m.notice.text)
	}
}
