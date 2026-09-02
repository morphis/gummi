package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func TestRunAutonomousStage(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit internal/theme/palette.go"},
			{Kind: agent.EventToolCall, Tool: "run go test ./..."},
			{Kind: agent.EventMessage, Text: "Implemented the toggle and added a test."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3, OutputTokens: 120}},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement", m.rows[0].F.Stage)
	}

	// enter runs the autonomous stage (the thread is the run's surface,
	// not a pane — DESIGN §10.5)
	m = openAndAttach(t, m)
	settleChat(t, eng)

	sess := m.sessionFor("FD-001")
	if sess == nil {
		t.Fatal("no active session for the running feature")
	}
	snap := sess.Snapshot()
	if len(snap.Activity) != 2 || snap.Activity[0] != "edit internal/theme/palette.go" {
		t.Errorf("activity feed wrong: %+v", snap.Activity)
	}
	if snap.Spend.Credits != 3 {
		t.Errorf("spend not metered: %+v", snap.Spend)
	}

	// the thread's live stage shows the activity feed, under a session
	// boundary naming the fresh context it started (thread.go)
	view := m.View().Content
	if !strings.Contains(view, "fresh context") || !strings.Contains(view, "run go test") {
		t.Error("thread missing live activity feed")
	}
}

func TestPauseStopsRun(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("working…"))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m) // run
	settleChat(t, eng)
	if m.sessionFor("FD-001") == nil {
		t.Fatal("run did not start a session")
	}
	// p pauses: the session is stopped and marked paused (kept visible)
	m = toKeys(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	s := m.engine.Get("FD-001")
	if s == nil || s.State() != engine.StatePaused {
		t.Errorf("after pause: session state = %v, want paused", s)
	}
	if !strings.Contains(m.notice.text, "paused") {
		t.Errorf("notice = %q, want paused", m.notice.text)
	}
}

func TestRunRejectsInteractiveViaRunPath(t *testing.T) {
	// enter on a brainstorm feature attaches the conversation, it does
	// not start a run — the thread is the interactive surface
	m, _ := agentWorkspace(t, agent.NewFake("hi"))
	m = openAndAttach(t, m)
	s := m.sessionFor("FD-001")
	if s == nil || !s.Interactive {
		t.Fatal("brainstorm enter should attach an interactive session, not run")
	}
}

// selectRow points the board selection at a feature by ID.
func selectRow(t *testing.T, m *Shell, id domain.FeatureID) {
	t.Helper()
	for i, r := range m.rows {
		if r.F.ID == id {
			m.sel = i
			return
		}
	}
	t.Fatalf("row %s not found in %d rows", id, len(m.rows))
}

func TestBugInteractiveStagesAttachConversation(t *testing.T) {
	// enter on a bug at its interactive stages (triage/diagnose) attaches
	// the architect conversation in the thread, exactly like brainstorm
	// and spec for features.
	m, _ := agentWorkspace(t, agent.NewFake("Can you reproduce it?"))
	m = press(t, m, tea.KeyPressMsg{Code: 'B', Text: "B"})
	m = typeString(t, m, "Login loops")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	selectRow(t, m, "BG-002")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	for _, stage := range []domain.Stage{domain.StageTriage, domain.StageDiagnose} {
		selectRow(t, m, "BG-002")
		// the card page is open, so its composer holds the keyboard; the
		// next stage's attach runs from the action list instead, because
		// the composer blur is where esc leaves you
		m = toKeys(t, m)
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		if m.rows[m.sel].F.Stage != stage {
			t.Fatalf("setup: stage = %s, want %s", m.rows[m.sel].F.Stage, stage)
		}
		selectRow(t, m, "BG-002")
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		s := m.sessionFor("BG-002")
		if s == nil || !s.Interactive {
			t.Fatalf("enter at %s did not attach an interactive session (notice: %q)", stage, m.notice.text)
		}
	}
}

func TestWatchAttachesRunningSession(t *testing.T) {
	// The watched feature's session stays busy (no trailing idle so the
	// run keeps going), but a one-shot scribe pass — fired at the approval
	// gate and driven synchronously by the test pump — must still finish,
	// or the pump waits on it forever. So the scribe role is answered with
	// a bare idle so its pass resolves without disturbing the run itself.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})

	// first enter answers the idle decision and starts the run — the
	// thread shows it live, no pane to open
	m = openAndAttach(t, m)
	if m.sessionFor("FD-001") == nil {
		t.Fatal("starting a run did not start a session")
	}
	waitForActivity(t, eng)

	// empty-composer enter deliberately sends nothing and runs nothing
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.threadInput.Value() != "" {
		t.Fatal("empty-composer enter changed the draft")
	}

	// the thread's live stage block is the watch surface: the running
	// session's full transcript, messages and tools in order
	view := ansi.Strip(m.threadView(100, 30))
	if !strings.Contains(view, "Wiring the toggle.") || !strings.Contains(view, "edit theme.go") {
		t.Errorf("thread missing the running session's transcript:\n%s", view)
	}

	// the tool call is a transcript entry, ordered after the message
	snap := m.sessionFor("FD-001").Snapshot()
	var msgAt, toolAt int
	for i, msg := range snap.Transcript {
		switch {
		case msg.Content == "Wiring the toggle.":
			msgAt = i
		case msg.Author == engine.AuthorTool:
			toolAt = i
		}
	}
	if toolAt <= msgAt {
		t.Errorf("tool call not interleaved after its message: %+v", snap.Transcript)
	}

	// esc leaves the page; the run keeps going — watching is not what
	// keeps it alive
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc did not leave the card page")
	}
	if s := eng.Get("FD-001"); s == nil || s.State() != engine.StateRunning {
		t.Error("leaving the page stopped the run")
	}
}

// waitForActivity polls until FD-001's session has a tool-call line.
func waitForActivity(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := eng.Get("FD-001"); s != nil && len(s.Snapshot().Activity) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no activity arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestThreadActivityGolden(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit", Detail: "palette.go"},
			{Kind: agent.EventToolCall, Tool: "bash", Detail: "go test ./..."},
			{Kind: agent.EventMessage, Text: "Done: toggle wired, tests green."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 88}},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	// off, explicitly: this golden's subject is what a finished stage
	// looks like in the thread — the boundary rule, the tool lines, the
	// reply and the gate it parks at. On any other mode autopilot crosses
	// that gate and runs the stage behind it, which is autopilot's own
	// contract (autopilot_gate_test.go) and not what this frame is for.
	if err := m.store.SetGateApproval(context.Background(), "FD-001", domain.GateOff); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	settleChat(t, eng)
	// settleChat only waits on Busy/Transcript, not the session's own
	// State — the engine clears Busy on EventIdle well before
	// finishRunning() marks the session StateDone (engine.go's idle
	// handler), so without this the thread's "next" card (verbatim
	// nextActions, which reads StateRunning as "still going — say
	// nothing") could race and render as if the run hadn't finished yet.
	// drainEngineLoop (reviewloop_test.go) processes the engine's own
	// idle event, which is sent only after that transition.
	m = drainEngineLoop(t, m)
	golden.RequireEqual(t, []byte(m.View().Content))
}

// waitForBusy polls until FD-001's session reports Busy — used for a
// session with no tool-call activity to wait on (an interactive chat
// turn that only ever emits a message).
func waitForBusy(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := eng.Get("FD-001"); s != nil && s.Snapshot().Busy {
			return
		}
		select {
		case <-deadline:
			t.Fatal("session never went busy")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestCardBusyStateRunning is FD-029's StateRunning half: a card whose
// autonomous run is mid-turn shows the busy marker without losing its
// stage glyph. This half already worked under the old inline switch —
// the point here is pinning the new cardBusy/cardLine contract so a
// future change can't silently regress the case that used to work while
// fixing the ones that didn't.
func TestCardBusyStateRunning(t *testing.T) {
	// no trailing idle event on the architect's turn: the session stays
	// busy/running so the test can inspect it mid-turn (mirrors
	// TestParkVerbPausesRatherThanOpeningDeps). Scribe/discovery calls
	// made while advancing to Implement must still settle normally.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitForActivity(t, eng)

	r := m.rows[0]
	if r.F.ID != "FD-001" || r.F.Stage != domain.StageImplement {
		t.Fatalf("setup: rows[0] = %+v, want FD-001 at implement", r.F)
	}
	if s := m.sessionFor(r.F.ID); s == nil || s.State() != engine.StateRunning || !s.Busy() {
		t.Fatalf("setup: want a busy StateRunning session, got %+v", s)
	}
	if !m.cardBusy(r) {
		t.Fatal("cardBusy false for a StateRunning session mid-turn")
	}
	if word := m.cardBusyWord(r); word != "running" {
		t.Errorf("cardBusyWord = %q, want \"running\"", word)
	}
	line := m.cardLine(r, 1, false, true, 100)
	if !strings.Contains(line, stageGlyph(r.F.Stage)) {
		t.Errorf("busy card line dropped the stage glyph: %q", line)
	}
	if strings.Contains(line, "◔") {
		t.Errorf("busy running card must not also show the queued marker: %q", line)
	}
	if !strings.Contains(line, "running") {
		t.Errorf("busy card line missing the running word: %q", line)
	}
}

// TestCardLineGlyphSelectionGate proves only the selected card's glyph
// tracks the shared clock: a busy non-selected row freezes to the
// spinner's first frame regardless of m.frame, while the selected row's
// glyph advances with it.
func TestCardLineGlyphSelectionGate(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitForActivity(t, eng)

	r := m.rows[0]
	if !m.cardBusy(r) {
		t.Fatal("setup: want a busy card")
	}
	m.frame = 3 // a non-zero, non-first frame

	notSelected := m.cardLine(r, 1, false, true, 100)
	if !strings.Contains(notSelected, spinnerFrames[0]) {
		t.Errorf("non-selected busy card line = %q, want the frozen first frame %q", notSelected, spinnerFrames[0])
	}
	if strings.Contains(notSelected, m.spinner()) && m.spinner() != spinnerFrames[0] {
		t.Errorf("non-selected busy card line = %q, must not show the live frame %q", notSelected, m.spinner())
	}

	selected := m.cardLine(r, 1, true, true, 100)
	if !strings.Contains(selected, m.spinner()) {
		t.Errorf("selected busy card line = %q, want the live frame %q", selected, m.spinner())
	}
}

// TestCardBusyStateInteractive is FD-029's core repro for the chat-session
// half: a StateInteractive session mid-reply never satisfied the old
// inline switch (it only matched StateRunning), so a busy chat card sat
// dead on the board while its own thread view spun for it.
func TestCardBusyStateInteractive(t *testing.T) {
	// no trailing idle event: the architect stays busy mid-reply.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "thinking out loud"}}
	}}
	m, eng := agentWorkspace(t, ag)
	m = openAndAttach(t, m)
	waitForBusy(t, eng)

	r := m.rows[0]
	s := m.sessionFor(r.F.ID)
	if r.F.ID != "FD-001" || s == nil || s.State() != engine.StateInteractive || !s.Busy() {
		t.Fatalf("setup: want a busy StateInteractive session, got row=%+v sess=%+v", r.F, s)
	}
	if !m.cardBusy(r) {
		t.Fatal("cardBusy false for a StateInteractive session mid-reply — this is the bug FD-029 fixes")
	}
	if word := m.cardBusyWord(r); word != "running" {
		t.Errorf("cardBusyWord = %q, want \"running\"", word)
	}
	line := m.cardLine(r, 1, false, true, 100)
	if !strings.Contains(line, stageGlyph(r.F.Stage)) {
		t.Errorf("busy interactive card line dropped the stage glyph: %q", line)
	}
	if strings.Contains(line, "◔") {
		t.Errorf("busy interactive card must not show the queued marker: %q", line)
	}
	if !strings.Contains(line, "running") {
		t.Errorf("busy interactive card line missing the running word: %q", line)
	}

	// a running baseline on the same card takes priority over the live
	// session's own word — it's the more specific foreground action.
	m.baselining[r.F.ID] = true
	if word := m.cardBusyWord(r); word != "checking" {
		t.Errorf("cardBusyWord = %q, want baseline priority \"checking\" over a busy session", word)
	}
}

// TestCardBusyPlanCritique is FD-029's plan-critique busy case: a card
// whose fresh-context reviewer is mid-critique shows the busy marker
// with the same "critiquing plan" word thread.go's own spinner uses
// for the same session (runningLabel), not the generic "running".
func TestCardBusyPlanCritique(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if isReview(opts) {
			// no idle event: the critique session stays busy mid-turn
			return []agent.Event{{Kind: agent.EventMessage, Text: "reviewing the plan"}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "plan written"}, {Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StagePlan)
	m = openAndAttach(t, m) // run the architect's write leg
	settleChat(t, eng)
	m = drainEngineLoop(t, m) // auto-launches the critique session
	waitForBusy(t, eng)

	r := m.rows[0]
	s := m.sessionFor(r.F.ID)
	if r.F.ID != "FD-001" || s == nil || !s.Busy() || !s.Critique {
		t.Fatalf("setup: want a busy critique session, got row=%+v sess=%+v", r.F, s)
	}
	if !m.cardBusy(r) {
		t.Fatal("cardBusy false for a busy plan-critique session")
	}
	if word := m.cardBusyWord(r); word != "critiquing plan" {
		t.Errorf("cardBusyWord = %q, want \"critiquing plan\"", word)
	}
	line := m.cardLine(r, 1, false, true, 100)
	if !strings.Contains(line, stageGlyph(r.F.Stage)) {
		t.Errorf("busy critique card line dropped the stage glyph: %q", line)
	}
	if strings.Contains(line, "◔") {
		t.Errorf("busy critique card must not show the queued marker: %q", line)
	}
	if !strings.Contains(line, "critiquing plan") {
		t.Errorf("busy critique card line missing the critiquing-plan word: %q", line)
	}
}
