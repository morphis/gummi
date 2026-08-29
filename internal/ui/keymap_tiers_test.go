package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
)

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

	// no session: newChatPane would store a typed-nil *engine.Session,
	// which passes every nil check and then panics on Snapshot (chat.go).
	// These tests never read the transcript, only the keyboard.
	withChat := populatedShell(100, 30)
	withChat.chat = &chatPane{feature: id, input: newChatInput()}

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

// TestTabCycleIncludesTheAgentTab is severity 4. The cycle used to skip
// the agent tab because tab belongs to the hosted CLI once you are
// there; with alt+1/2/3 answered above every surface there is always a
// way back out, so skipping it just hid a third of the tab bar.
func TestTabCycleIncludesTheAgentTab(t *testing.T) {
	m := populatedShell(100, 30)
	want := []Tab{TabInbox, TabAgent, TabBoard}
	for i, w := range want {
		m.nextTab()
		if m.tab != w {
			t.Fatalf("cycle step %d: tab = %v, want %v", i+1, m.tab, w)
		}
	}
}
