package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// handedOffCard walks a card to done and stamps the hand-off on it — the
// ending whose closing block offers "open a bug from this" beside "land
// it after all". The classifier is wired anyway: the point of the tests
// below is that the follow-up never reaches it.
func handedOffCard(t *testing.T) *Shell {
	t.Helper()
	m, _ := chatWorkspace(t, classifyingAgent("implementation_wrong"))
	m = advanceTo(t, m, domain.StageDone)
	m.rows[0].F.HandedOffAt = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	m.rows[0].Landed = false
	// back to the board and into the card's own page, where the closing
	// block's picker and the composer share the keyboard
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// TestFollowUpTakesTheTypedLine is the bug this file's fourth delivery
// fixes: the closing block aimed the composer at "open a bug from this",
// relabelled the row "with your words" and let the bar name it — and then
// enter routed the line into the re-entry reader instead, which has no
// route out of done to give it. The line cleared the composer and became
// a consult question nobody asked for. Nothing on screen moved.
func TestFollowUpTakesTheTypedLine(t *testing.T) {
	m := handedOffCard(t)

	d := m.openDecision(m.rows[m.sel])
	if d == nil {
		t.Fatal("a handed-off card offers no decision")
	}
	if i := d.wordConsumer(); i < 0 || d.actions[i].id != "newbug" {
		t.Fatalf("word consumer = %d, want the follow-up (actions %v)", i, d.actions)
	}

	m = typeString(t, m, "the export drops the last row")
	if out := ansi.Strip(m.threadView(100, 30)); !strings.Contains(out, "open a bug from this with your words") {
		t.Fatalf("typing did not aim and relabel the follow-up:\n%s", out)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	dlg, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || dlg.id != "confirm-bug-from-card" {
		t.Fatalf("enter did not raise the follow-up confirm (overlay %v, notice %q)",
			m.Overlay.Top(), m.notice.text)
	}
	if !strings.Contains(dlg.detail, "the export drops the last row") {
		t.Errorf("the confirm does not carry the typed line:\n%s", dlg.detail)
	}
	// the line stays in the composer until the mint is asked for, so
	// cancelling costs nothing
	if got := strings.TrimSpace(m.threadInput.Value()); got != "the export drops the last row" {
		t.Errorf("composer = %q, want the line kept until the mint", got)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = drainEngineLoop(t, m)

	var bug *domain.Feature
	for i := range m.rows {
		if m.rows[i].F.Kind == domain.KindBug {
			bug = &m.rows[i].F
		}
	}
	if bug == nil {
		t.Fatalf("no bug card was minted (notice %q)", m.notice.text)
	}
	if !strings.Contains(bug.Title, "export") {
		t.Errorf("bug title = %q, want the typed line", bug.Title)
	}
	if bug.FoundBy != "FD-001" {
		t.Errorf("bug FoundBy = %q, want the card it came from", bug.FoundBy)
	}

	// the reader is on the new card, not on the finished one they filed
	// it from: the sentence they typed is this card's whole content, and
	// the page they were on cannot act on it
	if !m.cardOpen {
		t.Error("the mint left the card page")
	}
	if got := m.selectedID(); got != bug.ID {
		t.Errorf("selected %s after the mint, want the new card %s", got, bug.ID)
	}
}

// The jump waits for the reload that puts the card on the board, and it
// is spent there: a card that never appeared must not have every later
// reload yank the cursor off whatever the reader has since selected.
func TestFollowUpJumpIsSpentOnce(t *testing.T) {
	m := handedOffCard(t)
	m.openOnLoad = "BG-404" // a card no reload will ever carry

	model, _ := m.Update(m.loadRows())
	m = model.(*Shell)
	if m.openOnLoad != "" {
		t.Errorf("openOnLoad survived the load it was meant for: %q", m.openOnLoad)
	}
	if got := m.selectedID(); got != "FD-001" {
		t.Errorf("a jump to a card that is not there moved the cursor to %s", got)
	}
}

// The follow-up never spends a classification turn. There is no rerun
// edge out of done, so every intent the reader could name routes nowhere
// — the read would cost seconds and a scribe pass to arrive back at the
// row the line was already aimed at.
func TestFollowUpIsNotRead(t *testing.T) {
	m := handedOffCard(t)
	m = typeString(t, m, "the export drops the last row")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.reentryRead != nil {
		t.Errorf("the follow-up sent the line out to be read: %+v", m.reentryRead)
	}
	if _, ok := m.Overlay.Top().(*confirmDialog); !ok {
		t.Fatalf("enter did not raise the follow-up confirm (notice %q)", m.notice.text)
	}
}

// A "/verb" line belongs to the parser, and a bare enter on the row has
// nothing to mint — both keep the answers they always gave rather than
// the silence the missing delivery produced.
func TestFollowUpRefusesACommandAndAsksForALine(t *testing.T) {
	m := handedOffCard(t)
	r := m.rows[m.sel]

	if cmd := m.fixedSendBack(r, "newbug", ""); cmd != nil || !strings.Contains(m.notice.text, "type what is wrong") {
		t.Errorf("a bare follow-up = %q, want it to ask for a line", m.notice.text)
	}
	if cmd := m.fixedSendBack(r, "newbug", "/verify"); cmd != nil || !strings.Contains(m.notice.text, "that line is a command") {
		t.Errorf("a verb follow-up = %q, want it refused", m.notice.text)
	}
	if m.Overlay.Top() != nil {
		t.Errorf("a refused follow-up raised a confirm: %v", m.Overlay.Top())
	}
}
