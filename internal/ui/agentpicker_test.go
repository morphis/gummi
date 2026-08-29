package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"
)

// fixedAgentSet is a deterministic stand-in for detectAgentCLIs' result:
// tests must not depend on what happens to be installed on the machine
// running them.
func fixedAgentSet() []AgentCLI {
	return []AgentCLI{
		{Name: "copilot", Bin: "copilot", Installed: true},
		{Name: "claude", Bin: "claude", Installed: false},
		{Name: "codex", Bin: "codex", Installed: false},
		{Name: "opencode", Bin: "opencode", Installed: false},
		{Name: "zz", Bin: "zz", Installed: false},
	}
}

func TestAgentPickerPreselectsInstalledWhenNoneConfigured(t *testing.T) {
	agents := []AgentCLI{
		{Name: "copilot", Bin: "copilot", Installed: false},
		{Name: "claude", Bin: "claude", Installed: true},
		{Name: "codex", Bin: "codex", Installed: false},
	}
	d := newAgentPickerDialog(agents, "", func(string) tea.Cmd { return nil })
	if d.idx != 1 {
		t.Fatalf("idx = %d, want 1 (the only installed candidate)", d.idx)
	}
}

func TestAgentPickerPreselectsConfiguredOverInstalled(t *testing.T) {
	agents := fixedAgentSet()
	d := newAgentPickerDialog(agents, "opencode", func(string) tea.Cmd { return nil })
	if d.idx != 3 {
		t.Fatalf("idx = %d, want 3 (opencode), current config value should win over installed-ness", d.idx)
	}
}

func TestAgentPickerCyclesAndSubmits(t *testing.T) {
	agents := fixedAgentSet()
	var submitted string
	var called bool
	d := newAgentPickerDialog(agents, "", func(name string) tea.Cmd {
		submitted, called = name, true
		return nil
	})
	if d.idx != 0 {
		t.Fatalf("initial idx = %d, want 0 (copilot, the only installed one)", d.idx)
	}
	if _, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}); d.idx != 1 {
		t.Fatalf("forward idx = %d, want 1", d.idx)
	}
	// wrap backward past the start
	for i := 0; i < 2; i++ {
		d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if d.idx != len(agents)-1 {
		t.Fatalf("backward wrap idx = %d, want %d", d.idx, len(agents)-1)
	}

	// land back on claude (idx 1) and submit
	d.idx = 1
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !closed || !called || submitted != "claude" {
		t.Fatalf("submit: closed=%v called=%v submitted=%q, want true/true/claude", closed, called, submitted)
	}
}

func TestAgentPickerEscCancelsWithoutSubmitting(t *testing.T) {
	d := newAgentPickerDialog(fixedAgentSet(), "", func(string) tea.Cmd {
		t.Fatal("esc should not submit")
		return nil
	})
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Fatal("esc should close the dialog")
	}
}

func TestAgentPickerDialogGolden(t *testing.T) {
	m := populatedShell(100, 30)
	m.Overlay.Push(newAgentPickerDialog(fixedAgentSet(), "", func(string) tea.Cmd { return nil }))
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestAgentPickerDialogNoneInstalledGolden covers the "say so usefully"
// requirement: every known CLI still gets listed even when none of them
// are on PATH, plus a banner explaining why they're all marked that way.
func TestAgentPickerDialogNoneInstalledGolden(t *testing.T) {
	agents := []AgentCLI{
		{Name: "copilot", Bin: "copilot", Installed: false},
		{Name: "claude", Bin: "claude", Installed: false},
		{Name: "codex", Bin: "codex", Installed: false},
		{Name: "opencode", Bin: "opencode", Installed: false},
		{Name: "zz", Bin: "zz", Installed: false},
	}
	m := populatedShell(100, 30)
	m.Overlay.Push(newAgentPickerDialog(agents, "", func(string) tea.Cmd { return nil }))
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestMaybeShowAgentPickerFirstRun proves the actual production trigger:
// an attached shell with nothing configured (no env var, no persisted
// choice) opens the picker when MaybeShowAgentPicker is called — the
// call cmd/gummi's runBoard makes once, before the program starts.
func TestMaybeShowAgentPickerFirstRun(t *testing.T) {
	m, _ := newWorkspace(t)
	m.MaybeShowAgentPicker()
	if _, ok := m.Overlay.Top().(*agentPickerDialog); !ok {
		t.Fatalf("MaybeShowAgentPicker did not open the picker, got %T", m.Overlay.Top())
	}
}

// TestMaybeShowAgentPickerSkipsWhenConfigured covers every way
// agentConfigured can already be satisfied: an env var, or a persisted
// config choice.
func TestMaybeShowAgentPickerSkipsWhenConfigured(t *testing.T) {
	t.Run("GUMMI_ATTACH_CMD", func(t *testing.T) {
		t.Setenv("GUMMI_ATTACH_CMD", "true")
		m, _ := newWorkspace(t)
		m.MaybeShowAgentPicker()
		if m.Overlay.Top() != nil {
			t.Fatalf("picker opened despite GUMMI_ATTACH_CMD, got %T", m.Overlay.Top())
		}
	})
	t.Run("GUMMI_AGENT", func(t *testing.T) {
		t.Setenv("GUMMI_AGENT", "claude")
		m, _ := newWorkspace(t)
		m.MaybeShowAgentPicker()
		if m.Overlay.Top() != nil {
			t.Fatalf("picker opened despite GUMMI_AGENT, got %T", m.Overlay.Top())
		}
	})
	t.Run("config agent:", func(t *testing.T) {
		m, _ := newWorkspace(t)
		m.SetAgentConfig("claude", "")
		m.MaybeShowAgentPicker()
		if m.Overlay.Top() != nil {
			t.Fatalf("picker opened despite a persisted agent choice, got %T", m.Overlay.Top())
		}
	})
}

// TestBoardKeyAReopensPickerRegardlessOfConfig proves the board's "A" key
// always reopens the dialog, unlike MaybeShowAgentPicker's first-run gate
// — "change later" has to work even after a choice was already made.
func TestAgentCommandReopensPickerRegardlessOfConfig(t *testing.T) {
	m, _ := newWorkspace(t)
	m.SetAgentConfig("claude", "")
	m = pump(t, m, m.Init())
	if m.Overlay.Top() != nil {
		t.Fatalf("precondition: no overlay should be open yet, got %T", m.Overlay.Top())
	}
	// Menu-only by design: A means "approve the gate" in the spec, diff
	// and ingest views, so the board does not also spend it on this. The
	// space menu dispatches the command by id, which is what this drives.
	m = pump(t, m, m.runCommand("agent-cli"))
	if _, ok := m.Overlay.Top().(*agentPickerDialog); !ok {
		t.Fatalf("the agent-cli command did not open the picker, got %T", m.Overlay.Top())
	}
}

// TestBoardDoesNotBindA guards the reason the command above is menu-only:
// A is the approve accelerator elsewhere, and a board that also answered
// it would train the wrong reflex.
func TestBoardDoesNotBindA(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if top := m.Overlay.Top(); top != nil {
		t.Fatalf("A opened %T on the board; it should do nothing there", top)
	}
}

// TestSpaceMenuAgentEntryOpensPicker proves the same dialog is reachable
// through the space-key command menu — filtering to a query only this
// entry matches, then enter — since that is the discoverable path a
// newcomer who doesn't know the "A" accelerator would actually use.
func TestSpaceMenuAgentEntryOpensPicker(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: ' ', Text: " "})
	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf("space did not open the command menu, got %T", m.Overlay.Top())
	}
	m = typeString(t, m, "agent tab")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.Overlay.Top().(*agentPickerDialog); !ok {
		t.Fatalf("command menu filtered to \"agent tab\" then enter did not open the picker, got %T", m.Overlay.Top())
	}
}
