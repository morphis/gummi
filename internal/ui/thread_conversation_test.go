package ui

import (
	"context"
	"encoding/json"
	"errors"
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

// agentWorkspace builds a shell wired to a real workspace and a
// Fake-backed engine, with one brainstorm feature created.
func agentWorkspace(t *testing.T, ag agent.Agent) (*Shell, *engine.Engine) {
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

// chatWorkspace is agentWorkspace, kept under the old name so the
// conversation battery reads unchanged. New tests should use
// agentWorkspace.
func chatWorkspace(t *testing.T, ag agent.Agent) (*Shell, *engine.Engine) {
	return agentWorkspace(t, ag)
}

// settleChat waits for the first feature's (FD-001) session to finish a
// turn — every conversation test creates FD-001 as its subject.
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

// toKeys hands the keyboard from the card page's composer back to its
// single-letter accelerators, so a test can drive the page with bare keys
// (p, q, J, g) the way boardVerb sees them.
//
// It blurs directly rather than pressing esc. esc leaves the page now —
// the accelerator layer has no surface of its own and is reached only by
// a card another process drives (threadinput.go) — so a keypress is no
// longer the honest way in, and a helper that pretended otherwise would
// have every one of these tests asserting a route that does not exist.
func toKeys(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.blurThreadInput()
	return m
}

// openAndAttach opens the selected card and starts its conversation.
//
// The first enter opens the card page. The second answers the card's
// pinned decision: at an interactive stage with no live session the
// decision's highlighted option is "start the architect" — what enter
// does now that an empty composer's enter only ever answers what the
// screen is offering (DESIGN §10.19). Attach spawns the backend in a
// command; chatAttachedMsg lands back on the card page with the composer
// ready, and the thread is the conversation.
func openAndAttach(t *testing.T, m *Shell) *Shell {
	t.Helper()
	open := tea.KeyPressMsg{Code: tea.KeyEnter}
	m = press(t, m, open)
	m = press(t, m, open)
	return m
}

// TestThreadAttachAndSend is the retirement's core contract: the card
// page is the conversation. Attach starts the architect, the composer
// sends a turn, the thread shows the whole exchange, and esc — which
// used to detach a separate pane — now just leaves the page while the
// session keeps running, with the conversation waiting where it was.
func TestThreadAttachAndSend(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("Two options: localStorage or synced account."))

	// enter opens the card page, enter again answers the idle decision's
	// "start the architect"; gummi's kickoff turn runs first
	m = openAndAttach(t, m)
	if m.sessionFor("FD-001") == nil {
		t.Fatal("enter did not attach a session")
	}
	settleChat(t, eng) // kickoff reply lands

	// type and send from the composer, exactly as the bare composer
	// always routed prose
	m = typeString(t, m, "how should it persist?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	// kickoff (system) + reply, then the user turn + reply
	snap := m.sessionFor("FD-001").Snapshot()
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

	// esc leaves the page; the session stays alive and the draft with it
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc did not leave the card page")
	}
	if eng.Get("FD-001") == nil {
		t.Fatal("leaving the page killed the engine session")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // back onto the card

	// the thread holds the whole conversation: every one of the four
	// turns is in the body, not just the last assistant message
	view := ansi.Strip(m.threadView(100, 30))
	for _, want := range []string{"gummi", "how should it persist?", "Two options: localStorage or synced account."} {
		if !strings.Contains(view, want) {
			t.Errorf("thread missing %q:\n%s", want, view)
		}
	}

	// Talking again reuses the same session rather than starting a second
	// one: the transcript grows, it does not restart. The composer is the
	// whole door — there is nothing to re-attach to, because the card
	// never stopped being the conversation.
	// reopening the card found the composer focused, as it always is
	m = typeString(t, m, "synced, then")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	if s := m.sessionFor("FD-001"); s == nil || len(s.Snapshot().Transcript) != 6 {
		t.Fatalf("talking again did not continue the same session: %+v", m.sessionFor("FD-001"))
	}

	// and now that the architect has stopped, the thread offers the way
	// on: a decision with the stage's own legal set (DESIGN §10.19 — a
	// bare composer means an agent is working, so an idle one must not be
	// bare). It offers no "start the architect" row, because the
	// architect is already here.
	d := m.openDecision(m.rows[m.sel])
	if d == nil {
		t.Fatal("a finished interactive stage offered nothing to continue with")
	}
	for _, a := range d.actions {
		if a.id == "run" {
			t.Errorf("offered to start a conversation that is already live: %+v", d.actions)
		}
	}
	if len(d.actions) == 0 {
		t.Error("the decision carried no options")
	}
}

// TestThreadAttachRespectsStage: an attach never reuses a stale session
// from an earlier stage — the fresh context boundary is the design's
// promise (the spec carries context between stages, not the transcript).
func TestThreadAttachRespectsStage(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("hi"))
	// attach at brainstorm, note the session, blur
	m = openAndAttach(t, m)
	brainstormSess := m.sessionFor("FD-001")
	if brainstormSess == nil {
		t.Fatal("no brainstorm session")
	}
	m = toKeys(t, m)

	// advance brainstorm → spec while the composer is blurred
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("stage = %s, want spec", m.rows[0].F.Stage)
	}

	// re-attach: must NOT reuse the brainstorm session for a spec stage
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionFor("FD-001") == nil {
		t.Fatal("re-attach failed")
	}
	if m.sessionFor("FD-001") == brainstormSess {
		t.Error("reused the stale brainstorm session for the spec stage")
	}
	if a := eng.Get("FD-001"); a == nil || a.Feature.Stage != domain.StageSpec {
		t.Errorf("active session stage = %v, want spec", a)
	}
}

// TestThreadConversationGolden is the thread's conversation review
// surface: after a real two-turn brainstorm the card page carries every
// turn, the kickoff labelled gummi and the user turn labelled you, with
// the composer ready below.
func TestThreadConversationGolden(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("Persist per-device via localStorage; account sync is a follow-up."))
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

// TestThreadDecisionDigitJumpsAndAnswers: the thread's inherited picker
// keys answer a live ask — a digit on an empty line jumps to (and, for a
// single-pick question, answers) the option, the pane's own contract on
// the thread's pinned decision.
func TestThreadDecisionDigitJumpsAndAnswers(t *testing.T) {
	m, eng := agentWorkspace(t, askingFake())
	m = openAndAttach(t, m) // attach; kickoff triggers the ask
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

// TestThreadRunStartsOnAutonomousStage: enter on an autonomous stage
// runs it — the decision's run answer starts the planner; it does not
// open any pane, because there is no pane anymore (DESIGN §10.5).
func TestThreadRunStartsOnAutonomousStage(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("x"))
	// advance brainstorm → spec → plan (needs worktree at spec approval)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan (worktree created)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("stage = %s, want plan", m.rows[0].F.Stage)
	}
	// enter on the page answers the idle decision and starts the run
	m = openAndAttach(t, m)
	settleChat(t, eng)
	if m.sessionFor("FD-001") == nil {
		t.Error("enter on plan did not start an autonomous run")
	}
}

// TestThreadAttachNoEngine: a workspace-attached shell without an engine
// answers the idle decision with a notice rather than a broken pane.
func TestThreadAttachNoEngine(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("x"))
	m.engine = nil
	m = openAndAttach(t, m)
	if !m.notice.isErr {
		t.Error("no notice when attaching without an engine")
	}
}

// The transcript renderer (transcript.go), previously the chat pane's:
// tool lines, their output, the outcome markers, and the dedupe of an
// answer captured into the spec.

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
	fail := "line\n" + strings.Repeat("line\n", 19) + "Error: device eth0 already exists"
	ok := "all green"

	// a failure shows its tail unprompted, elided to failTailLines
	got := toolOutputLines(s, engine.ToolFail, fail, 80, false)
	if len(got) != failTailLines+1 { // "…" + tail
		t.Fatalf("failure shows %d lines, want %d", len(got), failTailLines+1)
	}
	if !strings.Contains(got[len(got)-1], "device eth0 already exists") {
		t.Errorf("failure tail lost the error: %q", got[len(got)-1])
	}
	// successes stay collapsed until alt+o
	if got := toolOutputLines(s, engine.ToolOK, ok, 80, false); got != nil {
		t.Errorf("collapsed success rendered output: %q", got)
	}
	if got := toolOutputLines(s, engine.ToolOK, ok, 80, true); len(got) != 1 || !strings.Contains(got[0], "all green") {
		t.Errorf("expanded success = %q", got)
	}
	// expanded failures show everything, not just the tail
	if got := toolOutputLines(s, engine.ToolFail, fail, 80, true); len(got) != 21 {
		t.Errorf("expanded failure shows %d lines, want all 21", len(got))
	}
}

// TestTranscriptDedupesCapturedAnswer: an ask_user answer captured into
// the spec as a resolved marker shows once — as the answer bubble tagged
// "recorded in the spec" — not as the bubble plus a separate note line.
func TestTranscriptDedupesCapturedAnswer(t *testing.T) {
	s := theme.New(theme.GummiDark())
	snap := engine.Snapshot{
		Role: "architect",
		Transcript: []engine.Message{
			{Author: engine.AuthorUser, Content: "per-device"},
			{Author: engine.AuthorTool, Content: engine.AnswerCapturedNote},
		},
	}
	out := stripANSI(strings.Join(transcriptLines(s, snap, 80, false), "\n"))
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

func TestHumanTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 42: "42", 999: "999", 1200: "1.2k", 45000: "45.0k", 2_000_000: "2.0M"}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSessionMeta(t *testing.T) {
	// full snapshot: model, spend, context with a limit
	snap := engine.Snapshot{
		Spend:   agent.Usage{Model: "gpt-5", InputTokens: 8000, OutputTokens: 2000},
		Context: agent.Context{Tokens: 12000, Limit: 400000},
	}
	got := sessionMeta(snap)
	for _, want := range []string{"gpt-5", "10.0k tok", "12.0k/400.0k ctx", "3%"} {
		if !strings.Contains(got, want) {
			t.Errorf("meta %q missing %q", got, want)
		}
	}

	// no context limit → "ctx" without a fraction
	snap2 := engine.Snapshot{Spend: agent.Usage{Model: "m"}, Context: agent.Context{Tokens: 500}}
	if g := sessionMeta(snap2); !strings.Contains(g, "500 ctx") || strings.Contains(g, "/") {
		t.Errorf("meta without limit = %q, want '500 ctx' and no fraction", g)
	}

	// empty snapshot → empty meta
	if g := sessionMeta(engine.Snapshot{}); g != "" {
		t.Errorf("empty meta = %q, want empty", g)
	}
}

// stripANSI removes ANSI escape sequences for plain-text assertions.
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

// The pane's five capabilities, landed in the thread before it was
// retired (DESIGN §10.5): unbounded scrollback, raw tool-output
// expansion, the failure tail, session errors, and the picker keys.

// TestThreadShowsTheFullTranscript: a multi-turn brainstorm is readable
// in the thread body — every turn, not just the last assistant message.
// This is the capability that made the pane irreplaceable and the whole
// point of retiring it.
func TestThreadShowsTheFullTranscript(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("Persist per-device via localStorage; account sync is a follow-up."))
	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = typeString(t, m, "per-device or synced?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	m = typeString(t, m, "account sync later then")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	// the whole exchange is in the body: the kickoff, both user turns
	// and both replies — labelled gummi/you like the pane labelled them
	view := ansi.Strip(m.threadView(100, 30))
	for _, want := range []string{
		"gummi",
		"per-device or synced?",
		"account sync later then",
		"Persist per-device via localStorage",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("thread body missing %q:\n%s", want, view)
		}
	}
}

// TestThreadFailureTailShowsWithoutExpansion: a failed tool call's
// output tail renders inline under the tool line, expanded or not — the
// failure's diagnosis is the point, and the tail is what carries it.
func TestThreadFailureTailShowsWithoutExpansion(t *testing.T) {
	m, eng := agentWorkspace(t, &agent.Fake{
		Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventToolCall, Tool: "bash", Detail: "go test ./...", CallID: "c1"},
				{
					Kind: agent.EventToolResult, CallID: "c1",
					Result: &agent.ToolResult{OK: false, Output: "--- FAIL: TestX\n" + strings.Repeat("frame\n", 20) + "FAIL\tgummi/internal/x"},
				},
				{Kind: agent.EventMessage, Text: "a test failed"},
				{Kind: agent.EventIdle},
			}
		},
	})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // plan→implement
	m = openAndAttach(t, m)
	settleChat(t, eng)

	view := ansi.Strip(m.threadView(100, 30))
	if !strings.Contains(view, "✗") {
		t.Errorf("failed tool not marked ✗:\n%s", view)
	}
	if !strings.Contains(view, "FAIL") {
		t.Errorf("failure tail not shown inline:\n%s", view)
	}
	if !strings.Contains(view, "go test ./...") {
		t.Errorf("tool line missing:\n%s", view)
	}
}

// TestThreadAltOExpandsToolOutput: alt+o is not text, so it works
// mid-draft and from the accelerator layer alike, expanding a success's
// captured output that the compact view had folded away.
func TestThreadAltOExpandsToolOutput(t *testing.T) {
	m, eng := agentWorkspace(t, &agent.Fake{
		Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventToolCall, Tool: "bash", Detail: "go test ./...", CallID: "c1"},
				{Kind: agent.EventToolResult, CallID: "c1", Result: &agent.ToolResult{OK: true, Output: "ok 1.234s"}},
				{Kind: agent.EventIdle},
			}
		},
	})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // plan→implement
	m = openAndAttach(t, m)
	settleChat(t, eng)

	// collapsed: a success's output is folded away
	if v := ansi.Strip(m.threadView(100, 30)); strings.Contains(v, "1.234s") {
		t.Fatalf("success output shown while collapsed:\n%s", v)
	}

	// alt+o expands it from the focused composer
	m = press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	if v := ansi.Strip(m.threadView(100, 30)); !strings.Contains(v, "1.234s") {
		t.Errorf("alt+o did not expand the output:\n%s", v)
	}

	// ... and alt+o folds it back from the accelerator layer
	m = toKeys(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	if v := ansi.Strip(m.threadView(100, 30)); strings.Contains(v, "1.234s") {
		t.Errorf("second alt+o did not fold the output:\n%s", v)
	}
}

// TestThreadShowsSessionError: a session that errored renders its
// wrapped diagnosis in the live stage block — a failure is never silent
// on the card that owns it.
func TestThreadShowsSessionError(t *testing.T) {
	m, _ := agentWorkspace(t, &agent.Fake{
		Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{{Kind: agent.EventError, Err: errors.New("backend refused: the model is not authorized")}}
		},
	})
	m = openAndAttach(t, m)
	deadline := time.After(testWaitTimeout)
	for m.sessionFor("FD-001") == nil || m.sessionFor("FD-001").Snapshot().Err == nil {
		select {
		case <-deadline:
			t.Fatal("session error never surfaced")
		case <-time.After(10 * time.Millisecond):
		}
	}
	view := ansi.Strip(m.threadView(100, 30))
	for _, want := range []string{"✗", "backend refused", "not authorized"} {
		if !strings.Contains(view, want) {
			t.Errorf("thread missing the session error's %q:\n%s", want, view)
		}
	}
}

// TestThreadOArmsFreeForm is the picker-key inheritance: with a question
// that allows free form, 'o' on an empty line arms the composer as the
// answer channel — the pane's own 'o'. While armed the picker keys stand
// down, so prose that starts with a digit types instead of jumping to an
// option, and enter delivers the line verbatim as the answer.
func TestThreadOArmsFreeForm(t *testing.T) {
	m, eng := agentWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	// arm the free-form channel, then type an answer that begins with a
	// digit — the keystroke that would otherwise jump to an option
	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if !m.threadFreeForm {
		t.Fatal("'o' did not arm the free-form channel")
	}
	m = typeString(t, m, "2.4 to 1 is the floor")
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("the free-form answer did not clear the pending ask")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got string
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Author == engine.AuthorUser {
			got = msg.Content
		}
	}
	if got != "2.4 to 1 is the floor" {
		t.Errorf("ask answered with %q, want the whole armed line", got)
	}
}

// TestThreadEscDisarmsFreeFormBeforeLeaving: esc leaves the card page in
// one press, with one exception — an armed free-form channel is a state
// the user just entered deliberately, and the decision above the line
// shows it, so esc backs out of that first. It is not the old blur: the
// keyboard stays on the composer and the draft stays with it. A second
// esc leaves, because by then there is nothing pending to cancel.
func TestThreadEscDisarmsFreeFormBeforeLeaving(t *testing.T) {
	m, eng := agentWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if !m.threadFreeForm {
		t.Fatal("'o' did not arm the free-form channel")
	}
	m = typeString(t, m, "2.4 to 1")

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadFreeForm {
		t.Fatal("esc did not disarm the free-form channel")
	}
	if !m.cardOpen {
		t.Fatal("esc left the page instead of disarming the pending channel first")
	}
	if !m.threadInput.Focused() {
		t.Fatal("disarming blurred the composer — it should keep the keyboard")
	}
	if got := m.threadInput.Value(); got != "2.4 to 1" {
		t.Fatalf("disarming discarded the draft: %q", got)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc with nothing pending should leave the card page")
	}
}

// TestTranscriptViewExpandsEveryStage: t opens the thread's transcript
// view — every stage segment renders its events in the body, so a card's
// whole log reads end to end; t again folds it back to the receipts.
func TestTranscriptViewExpandsEveryStage(t *testing.T) {
	m := populatedShell(100, 30)
	id := m.rows[m.sel].F.ID
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "m"})
	exit, _ := json.Marshal(map[string]any{"verdict": "pass"})
	// a finished brainstorm carries a real conversation; the implement
	// stage is the open (current) one
	m.cardEvents[id] = []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StageBrainstorm, At: at, Payload: string(enter)},
		{
			Kind: state.EventMessage, Stage: domain.StageBrainstorm, At: at,
			Payload: `{"author":"architect","content":"persist per-device, sync later"}`,
		},
		{Kind: state.EventStageExit, Stage: domain.StageBrainstorm, At: at, Payload: string(exit)},
		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: at, Payload: string(enter)},
	}
	if !m.cardOpen {
		m.cardOpen = true // the page is open, as a real openCard leaves it
	}
	m.openTranscript(m.rows[m.sel].F)
	if !m.threadTranscript {
		t.Fatal("t did not open the transcript view")
	}
	// the finished stage's own conversation is laid out, not one folded
	// line — the part only the transcript view can reach
	view := ansi.Strip(m.threadView(100, 30))
	if !strings.Contains(view, "persist per-device, sync later") {
		t.Errorf("transcript view missing the expanded brainstorm conversation:\n%s", view)
	}

	// t folds it back
	m.openTranscript(m.rows[m.sel].F)
	if m.threadTranscript {
		t.Fatal("t did not fold the transcript view back")
	}
	if v := ansi.Strip(m.threadView(100, 30)); strings.Contains(v, "persist per-device, sync later") {
		t.Errorf("folded view still shows the expanded conversation:\n%s", v)
	}
}
