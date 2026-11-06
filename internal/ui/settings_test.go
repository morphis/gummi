package ui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// settingsHarness is a settings dialog whose writes are recorded rather
// than stored.
type settingsHarness struct {
	d         *settingsDialog
	name      string
	coAuthors [][]string
	refuse    error
	workflows []bool
}

func newSettingsHarness(name string, coAuthors []string) *settingsHarness {
	h := &settingsHarness{}
	h.d = newSettingsDialog(name, coAuthors,
		func(n string) error { h.name = n; return nil },
		func(list []string) error {
			if h.refuse != nil {
				return h.refuse
			}
			h.coAuthors = append(h.coAuthors, list)
			return nil
		},
		false,
		func(on bool) error { h.workflows = append(h.workflows, on); return nil })
	return h
}

func (h *settingsHarness) typeText(s string) {
	for _, r := range s {
		h.d.HandleKey(key(r))
	}
}

func TestSettingsDialogSavesCoAuthorsTypedInTheBox(t *testing.T) {
	h := newSettingsHarness("laptop", nil)
	h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	h.typeText("Ada <ada@example.com>")
	// enter is a new line in the box, not a save
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); closed {
		t.Fatal("enter in the co-authors box closed the dialog")
	}
	h.typeText("Grace <grace@example.com>")
	closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !closed {
		t.Fatalf("ctrl+s did not save; problem %q", h.d.problem)
	}
	want := []string{"Ada <ada@example.com>", "Grace <grace@example.com>"}
	if h.name != "laptop" || len(h.coAuthors) != 1 || !slices.Equal(h.coAuthors[0], want) {
		t.Fatalf("saved name %q co-authors %q, want laptop %q", h.name, h.coAuthors, want)
	}
}

func TestSettingsDialogLeavesAnUntouchedCoAuthorListAlone(t *testing.T) {
	h := newSettingsHarness("", []string{"Ada <ada@example.com>"})
	h.typeText("east")
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !closed {
		t.Fatalf("enter in the name field did not save; problem %q", h.d.problem)
	}
	if h.name != "east" || h.coAuthors != nil {
		t.Fatalf("saved name %q co-authors %q, want east and no co-author write", h.name, h.coAuthors)
	}
}

func TestSettingsDialogShowsARefusedCoAuthorList(t *testing.T) {
	h := newSettingsHarness("", nil)
	h.refuse = errors.New("bad entry")
	h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	h.typeText("nobody")
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); closed {
		t.Fatal("a refused list closed the dialog")
	}
	if h.d.problem != "co-authors: bad entry" || h.d.focus != settingsFieldCoAuthors {
		t.Fatalf("problem %q focus %d, want the refusal on the co-authors box", h.d.problem, h.d.focus)
	}
}

func TestSettingsDialogClearsCoAuthors(t *testing.T) {
	h := newSettingsHarness("", []string{"Ada <ada@example.com>"})
	h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	h.d.coAuthors.SetValue("")
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); !closed {
		t.Fatalf("ctrl+s did not save; problem %q", h.d.problem)
	}
	if len(h.coAuthors) != 1 || !slices.Equal(h.coAuthors[0], []string{""}) {
		t.Fatalf("co-author writes %q, want one clearing write", h.coAuthors)
	}
}

func TestSettingsDialogTogglesWorkflows(t *testing.T) {
	h := newSettingsHarness("", nil)
	h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	h.d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if h.d.focus != settingsFieldWorkflows {
		t.Fatalf("focus %d", h.d.focus)
	}
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: ' ', Text: " "}); closed {
		t.Fatal("space closed the dialog")
	}
	if !h.d.workflows {
		t.Fatal("space did not flip the toggle")
	}
	if closed, _ := h.d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); !closed {
		t.Fatalf("not saved: %q", h.d.problem)
	}
	if len(h.workflows) != 1 || !h.workflows[0] {
		t.Fatalf("workflows writes = %v", h.workflows)
	}
	// an untouched toggle is not written
	h2 := newSettingsHarness("x", nil)
	h2.d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if h2.workflows != nil {
		t.Fatalf("untouched toggle written: %v", h2.workflows)
	}
}

// TestShellSetWorkflowsWritesConfigAndEngine: the one write both faces make.
func TestShellSetWorkflowsWritesConfigAndEngine(t *testing.T) {
	b, eng, _ := sessionSpecBoard(t, "open")
	var cfgPath string
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd {
		cfgPath = m.ws.ConfigFile()
		if err := m.setWorkflows(false); err != nil {
			t.Error(err)
		}
		if eng.WorkflowsDefault() {
			t.Error("off did not reach the engine")
		}
		if err := m.setWorkflows(true); err != nil {
			t.Error(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "workflows: on") || !eng.WorkflowsDefault() {
		t.Fatalf("config %q, engine %v", raw, eng.WorkflowsDefault())
	}
}
