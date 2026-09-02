package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestAskArmsWithEmptyLine: bare `ask` arms the composer against the
// card's consult session without delivering anything — no consult
// session opens on the strength of arming alone.
func TestAskArmsWithEmptyLine(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("hi"))
	m = openAndAttach(t, m)
	settleChat(t, eng)

	m = typeString(t, m, "ask")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.threadAsk {
		t.Fatal("bare `ask` did not arm the composer")
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("input not cleared after arming: %q", m.threadInput.Value())
	}
	if eng.Consult("FD-001") != nil {
		t.Fatal("arming alone must not open a consult session")
	}
}

// TestAskWithRemainderArmsAndDelivers: `ask <question>` both arms the
// composer and delivers the question as the first consult turn in one
// motion — never a confirm chip (ask is chip-free).
func TestAskWithRemainderArmsAndDelivers(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("looks fine"))
	m = openAndAttach(t, m)
	settleChat(t, eng)

	m = typeString(t, m, "ask is the envelope close to the cap?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.threadAsk {
		t.Fatal("`ask <question>` did not arm the composer")
	}
	if m.threadChip != nil {
		t.Fatalf("ask raised a confirm chip: %+v", m.threadChip)
	}
	c := eng.Consult("FD-001")
	if c == nil {
		t.Fatal("`ask <question>` did not open the consult session")
	}
	var saw bool
	for _, msg := range c.Snapshot().Transcript {
		if msg.Content == "is the envelope close to the cap?" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("consult transcript = %+v, missing the delivered question", c.Snapshot().Transcript)
	}
}

// TestAskFollowUpReachesConsultNotLiveStage: while armed, a plain
// follow-up line reaches the consult session even though a live,
// attached stage session exists for the same card — arming overrides
// steering, which is the whole point of the channel.
func TestAskFollowUpReachesConsultNotLiveStage(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("architect reply"))
	m = openAndAttach(t, m) // a live, attached architect session on FD-001
	settleChat(t, eng)
	if !eng.Get("FD-001").Live() {
		t.Fatal("setup: want a live, attached stage session")
	}
	stageBefore := len(eng.Get("FD-001").Snapshot().Transcript)

	m = typeString(t, m, "ask")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // arm, empty line

	m = typeString(t, m, "what did you just do?")
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // deliver to consult

	c := eng.Consult("FD-001")
	if c == nil {
		t.Fatal("armed follow-up did not open/reach the consult session")
	}
	var saw bool
	for _, msg := range c.Snapshot().Transcript {
		if msg.Content == "what did you just do?" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("consult transcript = %+v, missing the follow-up question", c.Snapshot().Transcript)
	}
	stageAfter := eng.Get("FD-001").Snapshot().Transcript
	if len(stageAfter) != stageBefore {
		t.Errorf("the live stage session's transcript grew from %d to %d entries — the armed follow-up steered it instead of asking", stageBefore, len(stageAfter))
	}
}

// TestAskEscDisarmsAndRestoresSteering: esc drops the consult channel
// (draft kept) and a subsequent plain line goes back to steering the
// live stage session.
func TestAskEscDisarmsAndRestoresSteering(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("architect reply"))
	m = openAndAttach(t, m)
	settleChat(t, eng)

	m = typeString(t, m, "ask")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // arm
	if !m.threadAsk {
		t.Fatal("setup: expected the composer armed")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadAsk {
		t.Fatal("esc did not disarm the consult channel")
	}

	m = typeString(t, m, "please add a loading spinner")
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	var steered bool
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Content == "please add a loading spinner" {
			steered = true
		}
	}
	if !steered {
		t.Error("after esc disarmed the channel, a plain line did not steer the live stage session again")
	}
}

// TestDrivenAbroadComposerAsksNotSteers: a card another gummi process
// drives accepts a question and answers it, and a verb-shaped line
// (e.g. "approve") is sent to consult verbatim rather than firing any
// local action against the card the other process holds.
func TestDrivenAbroadComposerAsksNotSteers(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("consult reply"))
	m.rows[0].DrivenAbroad = true
	stageBefore := m.rows[0].F.Stage

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.threadInput.Focused() {
		t.Fatal("the composer withheld focus on a driven-abroad card")
	}

	m = typeString(t, m, "approve")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := eng.Get("FD-001"); got != nil {
		t.Fatalf("a verb line on a driven-abroad card reached the local engine: %+v", got.Snapshot())
	}
	if m.rows[0].F.Stage != stageBefore {
		t.Fatalf("a driven-abroad card's stage changed to %s", m.rows[0].F.Stage)
	}
	c := eng.Consult("FD-001")
	if c == nil {
		t.Fatal("the driven-abroad composer's line never reached the consult session")
	}
	var saw bool
	for _, msg := range c.Snapshot().Transcript {
		if msg.Content == "approve" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("consult transcript = %+v, missing the verbatim verb-shaped line", c.Snapshot().Transcript)
	}
}

// TestThreadConsultBlockGolden: a card carrying both a finished live
// stage block and a consult exchange renders the two as visually
// distinguishable segments — a captioned, dash-dot bordered block for
// the consult exchange, appended after the stage's own boundary-ruled
// one, never interleaved with it.
func TestThreadConsultBlockGolden(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleConsult {
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "the envelope has plenty of headroom left."},
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, OutputTokens: 20}},
				{Kind: agent.EventIdle},
			}
		}
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit", Detail: "palette.go"},
			{Kind: agent.EventMessage, Text: "Done: toggle wired, tests green."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 88}},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	// tall enough that the consult block's own caption survives the
	// thread's bottom-anchored scroll alongside everything above it
	// (the folded spec receipt, the finished stage's tool call, the
	// pinned decision) — a short window would just scroll the caption
	// off-screen, which is a viewport artifact this golden isn't for.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = model.(*Shell)
	if err := m.store.SetGateApproval(context.Background(), "FD-001", domain.GateOff); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	m = typeString(t, m, "ask is the envelope close to the cap?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainEngineLoop(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // disarm — steering resumes for the next line

	golden.RequireEqual(t, []byte(m.View().Content))
}
