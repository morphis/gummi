package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
)

// emptySession is a chat pane's source with nothing in it — enough for
// the pane to render and answer keys without an engine behind it.
type emptySession struct{}

func (emptySession) Snapshot() engine.Snapshot { return engine.Snapshot{} }

// openSurfaces builds one shell per surface that used to swallow the
// whole keyboard, keyed by the name the ? overlay gives it. The tier-1
// and tier-2 tests below run over all of them, so a seventh surface
// added later is one line here rather than a test nobody writes.
func openSurfaces(t *testing.T) map[string]*Shell {
	t.Helper()
	content := "## Problem\n\nA line to sit on.\n%% @user(2026-07-14): really?\n"
	id, _ := domain.NewFeatureID(1)
	f := domain.Feature{ID: id, Num: 1, Title: "x", Slug: "x", Stage: domain.StageSpec}

	withSpec := populatedShell(100, 30)
	withSpec.spec = &specView{f: f, path: "p.md", content: content, doc: spec.Parse(content), cursor: 1}

	withDiff := populatedShell(100, 30)
	withDiff.diff = newDiffView(f, "diff --git a/x b/x\n@@ -1 +1 @@\n+x\n", nil)

	withDeps := populatedShell(100, 30)
	withDeps.deps = &depPicker{f: f}

	// a stub session rather than newChatPane(id, nil): that would store a
	// typed-nil *engine.Session, which passes every nil check and then
	// panics on Snapshot (chat.go). These tests render the pane.
	withChat := populatedShell(100, 30)
	withChat.chat = &chatPane{feature: id, session: emptySession{}, input: newChatInput()}

	return map[string]*Shell{
		"spec": withSpec,
		"diff": withDiff,
		"deps": withDeps,
		"chat": withChat,
	}
}

// TestAltTabSwitchReachesEveryOpenSurface is the regression for the
// severity-2 conflict: handleKey used to hand the keyboard to chat,
// spec, diff, ingest, bugIngest or deps before any tab key was
// considered, so alt+1/2/3 did nothing at all from inside a view — you
// had to esc out first, which meant discarding whatever you were doing
// just to look at the inbox.
func TestAltTabSwitchReachesEveryOpenSurface(t *testing.T) {
	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			if m.tab != TabBoard {
				t.Fatalf("precondition: tab = %v, want TabBoard", m.tab)
			}
			m.handleKey(tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
			if m.tab != TabInbox {
				t.Fatalf("alt+2 from an open %s: tab = %v, want TabInbox", name, m.tab)
			}
		})
	}
}

// TestHelpReachesEveryOpenSurfaceThatIsNotTyping is the other half of
// severity 2: ? was unreachable from the same six surfaces. It is
// global now — except where the user is typing prose, since a question
// mark is ordinary punctuation and eating it would be the worse bug.
func TestHelpReachesEveryOpenSurfaceThatIsNotTyping(t *testing.T) {
	typing := map[string]bool{"chat": true}
	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})
			opened := m.Overlay.Contains("help")
			if opened == typing[name] {
				t.Fatalf("? on %s: help overlay open = %v, want %v", name, opened, !typing[name])
			}
		})
	}
}

// TestHelpTypesIntoTheBugImportFilter guards the same exception on the
// bug import, whose filter is a text field only while it has focus:
// with the list focused ? must open help, with the filter focused it
// must reach the filter.
func TestHelpTypesIntoTheBugImportFilter(t *testing.T) {
	m := populatedShell(100, 30)
	m.bugIngest = &bugIngestView{filtering: true}
	if m.textEntry() != true {
		t.Fatal("a focused bug-import filter must count as text entry")
	}
	m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.Overlay.Contains("help") {
		t.Fatal("? opened help over a focused filter instead of typing into it")
	}
	m.bugIngest.filtering = false
	m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.Overlay.Contains("help") {
		t.Fatal("? with the list focused must open help")
	}
}

// TestBoardDeleteIsUppercaseOnly is the regression for severity 1. x is
// the reversible key on every other surface — resolve a comment, dismiss
// an inbox item, drop a proposal, remove a dependency — and on the board
// it deleted a feature outright. It must now do nothing there.
func TestBoardDeleteIsUppercaseOnly(t *testing.T) {
	// straight at boardVerb: handleKey refuses every board key on a shell
	// with no store attached, and what is under test is which letter the
	// verb answers to, not the attach guard above it.
	m := populatedShell(100, 30)
	m.boardVerb("x")
	if m.Overlay.Contains("confirm-delete") {
		t.Fatal("x still raises the board's delete confirm")
	}
	m.boardVerb("D")
	if !m.Overlay.Contains("confirm-delete") {
		t.Fatal("D did not raise the board's delete confirm")
	}
}

// TestReversibleXNeverDestroys states the rule the board was breaking,
// over the tables themselves rather than one handler: wherever x is
// bound, its help text must describe something undoable. A future
// surface that spends x on a destructive verb fails here.
func TestReversibleXNeverDestroys(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	tables := map[string][]binding{
		"board": m.boardBindings(),
		"inbox": m.inboxBindings(),
	}
	for name, bs := range tables {
		for _, b := range bs {
			if b.key != "x" {
				continue
			}
			if strings.Contains(b.help+b.label, "delete") {
				t.Errorf("%s binds x to a destructive verb (%q) — destructive verbs are uppercase", name, b.label)
			}
		}
	}
}

// TestTabCycleReachesEveryOpenSurface is severity 3: tab meant five
// things, and the two that had to give were spec/diff's mode toggle
// (gone with the modes) and the bug import's filter focus (now /). It
// cycles tabs from every surface, exactly like alt+N.
func TestTabCycleReachesEveryOpenSurface(t *testing.T) {
	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
			if m.tab != TabInbox {
				t.Fatalf("tab from an open %s: tab = %v, want TabInbox", name, m.tab)
			}
		})
	}
}

// TestNoSurfaceRebindsTab states the rule rather than one instance: tab
// is a tier-2 grammar key, so no surface's table may claim it. The
// hosted CLI is the deliberate exception and declares as much in prose
// (agentBindings), which is why it is checked by name here.
func TestNoSurfaceRebindsTab(t *testing.T) {
	m := populatedShell(100, 30)
	id, _ := domain.NewFeatureID(1)
	f := domain.Feature{ID: id, Num: 1, Title: "x", Slug: "x", Stage: domain.StageSpec}
	content := "## Problem\n\nA line.\n"
	tables := map[string][]binding{
		"board":  m.boardBindings(),
		"inbox":  m.inboxBindings(),
		"spec":   (&specView{f: f, content: content, doc: spec.Parse(content), cursor: 1}).bindings(),
		"diff":   newDiffView(f, "diff --git a/x b/x\n@@ -1 +1 @@\n+x\n", nil).bindings(),
		"deps":   (&depPicker{f: f}).bindings(),
		"ingest": ingestRunBindings,
	}
	for name, bs := range tables {
		for _, b := range bs {
			if b.key == "tab" && name != "board" && name != "inbox" {
				t.Errorf("%s binds tab to %q — tab cycles the tabs", name, b.label)
			}
		}
	}
	// board and inbox may list it, but only as the cycle itself.
	for _, name := range []string{"board", "inbox"} {
		for _, b := range tables[name] {
			if b.key == "tab" && !strings.Contains(b.help, "cycle") {
				t.Errorf("%s documents tab as %q, not the tab cycle", name, b.help)
			}
		}
	}
}

// TestOpenSurfacesAreScopedToTheBoardTab is severity 5. mainView and
// handleKey tested m.chat/m.spec/m.diff before m.tab, so with a chat
// open, switching to the inbox still rendered the chat and still fed it
// the keyboard. It was unreachable until the tab keys went global.
func TestOpenSurfacesAreScopedToTheBoardTab(t *testing.T) {
	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			onBoard := m.mainView(90, 24)
			m.setTab(TabInbox)
			offBoard := m.mainView(90, 24)
			if offBoard == onBoard {
				t.Fatalf("the %s surface still renders on the inbox tab", name)
			}
			if !strings.Contains(stripANSI(offBoard), "NEEDS YOU") {
				t.Fatalf("the inbox tab did not get its own view:\n%s", offBoard)
			}
			// parked, not discarded: a chat holds an unsent input buffer.
			m.setTab(TabBoard)
			if got := m.mainView(90, 24); got != onBoard {
				t.Fatalf("the %s surface was not restored on return to the board", name)
			}
		})
	}
}

// lockedAgentShell is a shell parked on the agent tab with a live hosted
// child, in the given lock state. The child is a stub: these tests are
// about who *receives* a key, which agentKey answers before any pty is
// involved.
func lockedAgentShell(t *testing.T, locked bool) *Shell {
	t.Helper()
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	if m.agent == nil {
		t.Fatal("the agent tab did not spawn a child")
	}
	m.locked = locked
	return m
}

// TestTabCycleCoversEveryTab: the cycle skipped the agent tab for a
// while, because the hosted CLI held tab unconditionally and cycling
// onto a tab that will not cycle you off it is a one-way door. The lock
// removes the reason rather than the tab — unlocked, which is how you
// arrive, tab is always gummi's.
func TestTabCycleCoversEveryTab(t *testing.T) {
	m := populatedShell(100, 30)
	want := []Tab{TabInbox, TabAgent, TabBoard, TabInbox}
	for i, w := range want {
		m.nextTab()
		if m.tab != w {
			t.Fatalf("cycle step %d: tab = %v, want %v", i+1, m.tab, w)
		}
	}
}

// TestUnlockedAgentTabKeepsOnlyTheTabSwitches: arriving at the agent tab
// must not cost the user a keystroke or a mode. gummi claims tab and
// alt+N there and nothing else, so typing works immediately and ? , esc
// and ctrl+c reach the CLI the user is looking at.
func TestUnlockedAgentTabKeepsOnlyTheTabSwitches(t *testing.T) {
	m := lockedAgentShell(t, false)
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.tab != TabBoard {
		t.Fatalf("unlocked, tab should cycle off the agent tab, got %v", m.tab)
	}
	m = lockedAgentShell(t, false)
	m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.Overlay.Contains("help") {
		t.Error("unlocked, ? must reach the hosted CLI — it is ordinary punctuation there")
	}
}

// TestLockedAgentTabKeepsNothingButCtrlG is the lock's whole contract.
// Every key the user could otherwise use to leave goes to the CLI, which
// is the point: it is how its own tab completion is reached.
func TestLockedAgentTabKeepsNothingButCtrlG(t *testing.T) {
	m := lockedAgentShell(t, true)
	for _, msg := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: '1', Mod: tea.ModAlt},
		{Code: '2', Mod: tea.ModAlt},
		{Code: '?', Text: "?"},
		{Code: 'q', Text: "q"},
	} {
		m.handleKey(msg)
		if m.tab != TabAgent {
			t.Fatalf("%v left the agent tab while locked", msg)
		}
		if m.Overlay.HasDialogs() {
			t.Fatalf("%v raised a gummi dialog while locked", msg)
		}
	}
}

// TestCtrlGAlwaysUnlocks: a lock you can enter but not leave is the trap
// this mechanism exists to remove, so ctrl+g is answered above the
// overlay stack and in both states.
func TestCtrlGAlwaysUnlocks(t *testing.T) {
	m := lockedAgentShell(t, true)
	m.Overlay.Push(m.helpOverlay()) // even with a dialog in the way
	model, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if model.(*Shell).locked {
		t.Fatal("ctrl+g did not unlock")
	}
	model, _ = model.(*Shell).Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !model.(*Shell).locked {
		t.Fatal("ctrl+g did not lock again")
	}
}

// TestLockIsInertWithoutAHostedChild: a lock left set over a dead child
// would swallow every key with nothing to receive them — unrecoverable
// rather than modal. On a tab with nothing to lock, ctrl+g says what it
// is for instead of doing nothing.
func TestLockIsInertWithoutAHostedChild(t *testing.T) {
	m := populatedShell(100, 30)
	m.locked = true
	if m.keyboardLocked() {
		t.Error("the lock must be inert on a tab with no hosted child")
	}
	m.locked = false
	m.toggleLock()
	if m.locked {
		t.Error("ctrl+g locked a tab with nothing to lock")
	}
	if !strings.Contains(m.notice.text, "ctrl+g") {
		t.Errorf("ctrl+g on the board should explain itself, got %q", m.notice.text)
	}
}

// TestAltSlashOpensHelpWhereQuestionMarkCannot: ? is the convenient help
// key, but it is also ordinary punctuation, so it has to yield wherever
// the user types prose — the chat box, the bug-import filter, the hosted
// CLI. Those are the surfaces whose key rules are least guessable, which
// left help unreachable in the three places it was most wanted.
func TestAltSlashOpensHelpWhereQuestionMarkCannot(t *testing.T) {
	altSlash := tea.KeyPressMsg{Code: '/', Mod: tea.ModAlt}

	agent := hostedShell(t, "sleep 30")
	pressAlt(agent, '3')
	agent.handleKey(altSlash)
	if !agent.Overlay.Contains("help") {
		t.Error("alt+/ must open help on the agent tab, where ? is the CLI's")
	}

	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			m.handleKey(altSlash)
			if !m.Overlay.Contains("help") {
				t.Errorf("alt+/ did not open help over an open %s", name)
			}
		})
	}
}

// TestAltSlashYieldsToALockedAgent: it is a tier-1 key, not a second
// ctrl+g. Locked means gummi keeps exactly one key, and adding a quiet
// second one would make the lock's contract a lie again.
func TestAltSlashYieldsToALockedAgent(t *testing.T) {
	m := lockedAgentShell(t, true)
	m.handleKey(tea.KeyPressMsg{Code: '/', Mod: tea.ModAlt})
	if m.Overlay.Contains("help") {
		t.Error("alt+/ must reach the hosted CLI while locked")
	}
}

// TestADeadAgentTabAnswersNothing: a foreign tab whose child never
// started still has gummi holding the keyboard, and the answer has to be
// "nothing". It used to fall through to the inbox's keymap, so from a
// tab showing "starting your agent…" an x silently dismissed an inbox
// item, enter jumped to a card and switched tabs, and u spent budget.
func TestADeadAgentTabAnswersNothing(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.setTab(TabAgent)
	if m.agent != nil {
		t.Fatal("precondition: expected no hosted child")
	}
	for _, k := range []tea.KeyPressMsg{
		{Code: 'x', Text: "x"},
		{Code: 'u', Text: "u"},
		{Code: 'i', Text: "i"},
		{Code: tea.KeyEnter},
	} {
		m.setTab(TabAgent)
		m.inbox.add("FD-001", attnGate, "spec approval pending")
		m.handleKey(k)
		if m.tab != TabAgent {
			t.Errorf("%v moved off the agent tab", k)
		}
		if m.inbox.len() != 1 {
			t.Errorf("%v reached the inbox from the agent tab", k)
		}
		m.inbox.remove("FD-001")
	}
}

// TestTheLockIsOfferedOnArrival: a lock nobody knows about is the same
// as no lock. The moment worth naming it is just before the user reaches
// for a key gummi is holding — which is when they land on the tab.
func TestTheLockIsOfferedOnArrival(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	if m.notice.text != lockOfferNotice {
		t.Fatalf("arriving at the agent tab: notice = %q, want the lock offer", m.notice.text)
	}
	// and by the cycle, not only by alt+3 — both routes go through gotoTab
	// precisely so they cannot drift apart on this.
	m2 := hostedShell(t, "sleep 30")
	for m2.tab != TabAgent {
		m2.nextTab()
	}
	if m2.notice.text != lockOfferNotice {
		t.Fatalf("cycling onto the agent tab: notice = %q, want the lock offer", m2.notice.text)
	}
}

// TestTabLeavingTheAgentExplainsItself is the other moment: tab pressed
// at a CLI prompt means completion, and it moved the user instead. That
// surprise is the strongest reason anyone ever wants the lock, so it is
// the strongest place to name it.
func TestTabLeavingTheAgentExplainsItself(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.tab == TabAgent {
		t.Fatal("tab should still cycle — teaching must not cost the keypress")
	}
	if m.notice.text != lockLeftNotice {
		t.Fatalf("after tab left the agent: notice = %q, want the explanation", m.notice.text)
	}
}

// TestTheLockStopsTeachingOnceUsed: it is an offer, not a nag. Having
// worked it once is proof it landed, so the notices retire — while a
// user who never tries it keeps being told, since they never learned.
func TestTheLockStopsTeachingOnceUsed(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	m.toggleLock()
	m.toggleLock()
	if !m.lockUsed {
		t.Fatal("working the lock should retire the lesson")
	}
	m.notice = noticeMsg{}
	pressAlt(m, '1')
	pressAlt(m, '3')
	if m.notice.text != "" {
		t.Errorf("re-arriving after using the lock: notice = %q, want silence", m.notice.text)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.notice.text != "" {
		t.Errorf("tab away after using the lock: notice = %q, want silence", m.notice.text)
	}
}

// TestTheLockHintNamesThePayoff: "lock" says nothing to someone who does
// not already know what is locked, which is exactly the person the hint
// is for. It has to name the trade.
func TestTheLockHintNamesThePayoff(t *testing.T) {
	m := lockedAgentShell(t, false)
	hints := stripANSI(m.tabBarView(120)) + stripANSI(m.statusView(120))
	if !strings.Contains(hints, "tab→agent") {
		t.Errorf("the unlocked hint must say what ctrl+g buys:\n%s", hints)
	}
}

// TestTheLockIsVisible: the lock changes what every other key does, so
// it cannot be silent. It has to be legible from the other tabs too —
// hence the tab-bar badge, not just the hint.
func TestTheLockIsVisible(t *testing.T) {
	m := lockedAgentShell(t, false)
	unlocked := stripANSI(m.tabBarView(120)) + stripANSI(m.statusView(120))
	if !strings.Contains(unlocked, "ctrl+g") {
		t.Errorf("unlocked, the bar should offer the lock:\n%s", unlocked)
	}
	if strings.Contains(unlocked, "locked") {
		t.Errorf("unlocked, nothing should claim otherwise:\n%s", unlocked)
	}

	m.locked = true
	bar, status := stripANSI(m.tabBarView(120)), stripANSI(m.statusView(120))
	if !strings.Contains(bar, "locked") {
		t.Errorf("the tab bar must show the lock:\n%s", bar)
	}
	if !strings.Contains(status, "locked") || !strings.Contains(status, "ctrl+g") {
		t.Errorf("the status bar must show the lock and its way out:\n%s", status)
	}
	// legible from another tab: the lock survives the switch, so the badge
	// has to as well.
	m.setTab(TabBoard)
	if b := stripANSI(m.tabBarView(120)); !strings.Contains(b, "locked") {
		t.Errorf("the agent tab's lock badge must show from other tabs:\n%s", b)
	}
}

// TestAgentTabIsStillReachable: alt+3 goes straight there from anywhere,
// which is what makes the tab the user is on never a dead end.
func TestAgentTabIsStillReachable(t *testing.T) {
	for name, m := range openSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			m.handleKey(tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt})
			if m.tab != TabAgent {
				t.Fatalf("alt+3 from an open %s: tab = %v, want TabAgent", name, m.tab)
			}
		})
	}
}
