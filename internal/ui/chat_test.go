package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// chatWorkspace builds a shell wired to a real workspace and a
// Fake-backed engine, with one brainstorm feature created.
func chatWorkspace(t *testing.T, ag agent.Agent) (*Shell, *engine.Engine) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	eng := engine.New(engine.Config{Agents: singleAgent(ag), Store: store, Pool: pool, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	// create a brainstorm feature
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// advance todo → brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	return m, eng
}

// settleChat waits for the first feature's (FD-001) session to finish a
// turn — every chat/run test creates FD-001 as its subject.
func settleChat(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if a := eng.Get("FD-001"); a != nil {
			snap := a.Snapshot()
			if !snap.Busy && len(snap.Transcript) > 0 {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("session did not settle")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// openAndAttach opens the selected card's page and runs its highlighted
// action — enter's first job is opening the card now that the split
// board is gone (backlog.go), so attaching from the closed backlog list
// takes two presses: this helper is the first attach in a test. A later
// re-attach after esc only needs one more press, because esc detaches
// the chat pane without closing the card page underneath it.
// toKeys hands the keyboard from the card page's composer back to its
// single-letter accelerators. The composer is focused whenever a card is
// open, so a test driving the page with bare keys (p, q, J, g) has to
// step out of the line first — exactly as a user does.
func toKeys(t *testing.T, m *Shell) *Shell {
	t.Helper()
	if !m.threadInput.Focused() {
		return m
	}
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
}

// openAndAttach opens the selected card and starts its conversation.
//
// The second step answers the card's pinned decision: at an interactive
// stage with no live session the decision's highlighted option is "start
// the architect" — what enter does now that an empty composer's enter
// only ever answers what the screen is offering (DESIGN §10.19). Once
// the session is live the decision is gone (the thread IS the
// conversation), and re-attaching after an esc goes through the action
// pop-over instead.
func openAndAttach(t *testing.T, m *Shell) *Shell {
	t.Helper()
	open := tea.KeyPressMsg{Code: tea.KeyEnter}
	m = press(t, m, open)
	m = press(t, m, open)
	return m
}

func TestChatAttachAndSend(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("Two options: localStorage or synced account."))

	// enter opens the card page, enter again attaches the chat pane;
	// gummi's kickoff turn runs first
	m = openAndAttach(t, m)
	if m.chat == nil {
		t.Fatal("enter did not attach the chat pane")
	}
	if m.chat.feature != "FD-001" {
		t.Fatalf("attached to wrong feature: %s", m.chat.feature)
	}
	settleChat(t, eng) // kickoff reply lands

	// type and send
	m = typeString(t, m, "how should it persist?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	// kickoff (system) + reply, then the user turn + reply
	snap := m.chat.session.Snapshot()
	if len(snap.Transcript) != 4 {
		t.Fatalf("transcript = %+v", snap.Transcript)
	}
	if snap.Transcript[0].Author != engine.AuthorSystem {
		t.Errorf("kickoff turn wrong: %+v", snap.Transcript[0])
	}
	if snap.Transcript[2].Author != engine.AuthorUser || snap.Transcript[2].Content != "how should it persist?" {
		t.Errorf("user turn wrong: %+v", snap.Transcript[2])
	}
	if snap.Transcript[3].Content != "Two options: localStorage or synced account." {
		t.Errorf("assistant turn wrong: %+v", snap.Transcript[3])
	}

	// esc detaches; the session stays alive
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.chat != nil {
		t.Fatal("esc did not detach")
	}
	if eng.Get("FD-001") == nil {
		t.Fatal("detach killed the engine session")
	}

	// re-attach reuses the same session (transcript preserved). With the
	// conversation already live the decision is gone, so the inventory is
	// the door: ↑ opens it, and its highlighted "chat" action attaches.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil || len(m.chat.session.Snapshot().Transcript) != 4 {
		t.Fatal("re-attach lost the transcript")
	}
}

func TestChatReuseRespectsStage(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("hi"))
	// attach at brainstorm, note the session, detach
	m = openAndAttach(t, m)
	brainstormSess := m.chat.session
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// advance brainstorm → spec while detached
	m = toKeys(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("stage = %s, want spec", m.rows[0].F.Stage)
	}

	// re-attach: must NOT reuse the brainstorm session for a spec stage
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatal("re-attach failed")
	}
	if m.chat.session == brainstormSess {
		t.Error("reused the stale brainstorm session for the spec stage")
	}
	if a := eng.Get("FD-001"); a == nil || a.Feature.Stage != domain.StageSpec {
		t.Errorf("active session stage = %v, want spec", a)
	}
}

func TestChatViewGolden(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("Persist per-device via localStorage; account sync is a follow-up."))
	m = openAndAttach(t, m)
	settleChat(t, eng) // kickoff reply lands before the user types
	m = typeString(t, m, "per-device or synced?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	golden.RequireEqual(t, []byte(m.View().Content))
}

// askingFake advertises client tools and puts an ask_user question on
// its first turn, then acknowledges later turns.
func askingFake() *agent.Fake {
	f := agent.NewFake("")
	f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true, UsageEvents: true}
	args := []byte(`{"question":"Persist where?","options":[{"label":"per-device","detail":"localStorage"},{"label":"synced","detail":"account"}],"allow_free_form":true}`)
	first := true
	f.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		if first {
			first = false
			return []agent.Event{{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: args}}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ack: " + msg}, {Kind: agent.EventIdle}}
	}
	return f
}

// waitAsk blocks until FD-001 has an open ask (the picker is showing).
func waitAsk(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := eng.Get("FD-001"); s != nil && s.Snapshot().PendingAsk != nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no pending ask surfaced")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestChatPickerGolden(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m) // attach; kickoff triggers the ask
	waitAsk(t, eng)
	m = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"}) // move cursor to the second option
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestChatPickerAnswers(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	// selecting option 1 (per-device) answers the question
	press(t, m, tea.KeyPressMsg{Code: '1', Text: "1"})
	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("answer did not clear the pending ask")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// the choice is recorded as a user turn
	var got string
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Author == engine.AuthorUser {
			got = msg.Content
		}
	}
	if got != "per-device" {
		t.Errorf("answer recorded as %q, want per-device", got)
	}
}

func TestChatNotOpenedForAutonomousStage(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("x"))
	// advance brainstorm → spec → plan (needs worktree at spec approval)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan (worktree created)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("stage = %s, want plan", m.rows[0].F.Stage)
	}
	// enter on an autonomous stage runs it, it does not open a chat pane
	m = openAndAttach(t, m)
	settleChat(t, eng)
	if m.chat != nil {
		t.Fatal("chat attached for a non-interactive stage")
	}
	if m.sessionFor("FD-001") == nil {
		t.Error("enter on plan did not start an autonomous run")
	}
}

func TestChatNoEngine(t *testing.T) {
	// a workspace-attached shell without an engine: enter yields a notice
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	m.engine = nil
	m = openAndAttach(t, m)
	if m.chat != nil {
		t.Fatal("chat attached without an engine")
	}
	if !m.notice.isErr {
		t.Error("no notice when attaching without an engine")
	}
}

func TestToolLineView(t *testing.T) {
	s := theme.New(theme.GummiDark())

	// a composed tool line splits at the double space: name Muted,
	// detail Faint
	got := toolLineView(s, "bash  go test ./...", 80)
	want := s.Muted.Render("bash") + "  " + s.Faint.Render("go test ./...")
	if got != want {
		t.Errorf("tool line = %q, want %q", got, want)
	}

	// non-tool activity (check results, notes — spaces before any double
	// space, or none at all) stays a single Faint line
	for _, plain := range []string{"check gofmt: ok", "budget exhausted — stage stopped for review"} {
		if got := toolLineView(s, plain, 80); got != s.Faint.Render(plain) {
			t.Errorf("plain line %q = %q, want single Faint", plain, got)
		}
	}

	// long lines truncate ANSI-aware to the given width
	if w := ansi.StringWidth(toolLineView(s, "bash  "+strings.Repeat("x", 100), 20)); w != 20 {
		t.Errorf("truncated width = %d, want 20", w)
	}
}

func TestToolMarkerHonest(t *testing.T) {
	s := theme.New(theme.GummiDark())
	if got := toolMarker(s, engine.ToolOK); got != s.Success.Render("✓ ") {
		t.Errorf("ok marker = %q", got)
	}
	if got := toolMarker(s, engine.ToolFail); got != s.Error.Render("✗ ") {
		t.Errorf("fail marker = %q", got)
	}
	// unknown outcomes must not claim success
	if got := toolMarker(s, engine.ToolPending); got != s.Faint.Render("· ") {
		t.Errorf("pending marker = %q", got)
	}
}

func TestToolOutputLines(t *testing.T) {
	s := theme.New(theme.GummiDark())
	c := &chatPane{}
	fail := engine.Message{
		Author: engine.AuthorTool, ToolStatus: engine.ToolFail,
		ToolOutput: strings.Repeat("line\n", 20) + "Error: device eth0 already exists",
	}
	okMsg := engine.Message{Author: engine.AuthorTool, ToolStatus: engine.ToolOK, ToolOutput: "all green"}

	// a failure shows its tail unprompted, elided to failTailLines
	got := c.toolOutputLines(s, fail, 80)
	if len(got) != failTailLines+1 { // "…" + tail
		t.Fatalf("failure shows %d lines, want %d", len(got), failTailLines+1)
	}
	if !strings.Contains(got[len(got)-1], "device eth0 already exists") {
		t.Errorf("failure tail lost the error: %q", got[len(got)-1])
	}
	// successes stay collapsed until ctrl+o
	if got := c.toolOutputLines(s, okMsg, 80); got != nil {
		t.Errorf("collapsed success rendered output: %q", got)
	}
	c.showOutput = true
	if got := c.toolOutputLines(s, okMsg, 80); len(got) != 1 || !strings.Contains(got[0], "all green") {
		t.Errorf("expanded success = %q", got)
	}
	// expanded failures show everything, not just the tail
	if got := c.toolOutputLines(s, fail, 80); len(got) != 21 {
		t.Errorf("expanded failure shows %d lines, want all 21", len(got))
	}
}

// TestChatDedupesCapturedAnswer: an ask_user answer captured into the
// spec as a resolved marker shows once — as the answer bubble tagged
// "recorded in the spec" — not as the bubble plus a separate note line.
func TestChatDedupesCapturedAnswer(t *testing.T) {
	s := theme.New(theme.GummiDark())
	c := &chatPane{}
	snap := engine.Snapshot{
		Role: "architect",
		Transcript: []engine.Message{
			{Author: engine.AuthorUser, Content: "per-device"},
			{Author: engine.AuthorTool, Content: engine.AnswerCapturedNote},
		},
	}
	out := stripANSI(c.transcript(s, snap, 80, 40))
	// the answer appears once, and the standalone full "recorded your
	// answer in the spec" note line is gone — folded into the bubble's
	// label as a short "recorded in the spec" tag
	if strings.Count(out, "per-device") != 1 {
		t.Fatalf("answer should appear exactly once:\n%s", out)
	}
	if strings.Contains(out, engine.AnswerCapturedNote) {
		t.Errorf("standalone capture note not deduped:\n%s", out)
	}
	if !strings.Contains(out, "recorded in the spec") {
		t.Errorf("captured answer not tagged on its bubble:\n%s", out)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && (r == 'm'):
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
