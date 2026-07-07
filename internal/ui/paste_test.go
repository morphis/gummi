package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// paste feeds a bracketed-paste message through the shell, as the
// terminal would deliver it.
func paste(t *testing.T, m *Shell, text string) *Shell {
	t.Helper()
	model, cmd := m.Update(tea.PasteMsg{Content: text})
	return pump(t, model.(*Shell), cmd)
}

func TestPasteIntoFeatureForm(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())

	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = paste(t, m, "Dark ")
	m = typeString(t, m, "mo")
	m = paste(t, m, "de")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() {
		t.Fatal("form did not close on submit")
	}
	if len(m.rows) != 1 || m.rows[0].F.Slug != "dark-mode" {
		t.Fatalf("rows after paste+create: %+v", m.rows)
	}

	// paste while the options row is focused must not touch the input
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = paste(t, m, "stray")
	form := m.Overlay.Top().(*featureForm)
	if got := form.desc.Value(); got != "" {
		t.Fatalf("paste landed in a blurred input: %q", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// a dialog without an input swallows the paste instead of leaking it
	m = press(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	m = paste(t, m, "leak")
	if !m.Overlay.HasDialogs() {
		t.Fatal("help dialog gone after paste")
	}
}

func TestPasteIntoBugForm(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())

	m = press(t, m, tea.KeyPressMsg{Code: 'B', Text: "B"})
	m = paste(t, m, "Login loops")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.rows) != 1 || m.rows[0].F.Kind != domain.KindBug {
		t.Fatalf("rows after paste+create: %+v", m.rows)
	}
	if m.rows[0].F.Slug != "login-loops" {
		t.Fatalf("bad slug: %q", m.rows[0].F.Slug)
	}
}

func TestPasteIntoTextPrompt(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// rename via the text prompt, pasting the new title
	m.Overlay.Push(newTextPrompt("rename", "", "title",
		func(s string) error { _, err := domain.Slugify(s); return err },
		func(s string) tea.Cmd { return nil }))
	m = paste(t, m, "Bright mode")
	d := m.Overlay.Top().(*textPromptDialog)
	if got := d.input.Value(); got != "Bright mode" {
		t.Fatalf("prompt value = %q", got)
	}
}

func TestPasteIntoBugIngestFilter(t *testing.T) {
	m := &Shell{}
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)

	// paste while not filtering is dropped
	m.Update(tea.PasteMsg{Content: "stray"})
	if got := m.bugIngest.filter.Value(); got != "" {
		t.Fatalf("paste landed in an unfocused filter: %q", got)
	}

	m.handleBugIngestKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	m.Update(tea.PasteMsg{Content: "crash"})
	if got := m.bugIngest.filter.Value(); got != "crash" {
		t.Fatalf("filter value = %q", got)
	}
	if len(m.bugIngest.visible()) != 1 {
		t.Fatalf("visible after pasted filter = %d, want 1", len(m.bugIngest.visible()))
	}
}

func TestPasteIntoChat(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("Two options."))
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatal("enter did not attach the chat pane")
	}
	settleChat(t, eng)

	m = paste(t, m, "line one\nline two")
	if got := m.chat.input.Value(); got != "line one\nline two" {
		t.Fatalf("chat input = %q", got)
	}
}
