package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/ui/theme"
)

func shellAt(w, h int) *Shell {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return model.(*Shell)
}

func TestShellView80(t *testing.T) {
	golden.RequireEqual(t, []byte(shellAt(80, 24).View().Content))
}

func TestShellView120(t *testing.T) {
	golden.RequireEqual(t, []byte(shellAt(120, 34).View().Content))
}

func TestShellViewNarrow(t *testing.T) {
	// kanban collapses; nothing panics, content still renders
	out := shellAt(50, 12).View().Content
	if out == "" {
		t.Fatal("narrow view rendered empty")
	}
	golden.RequireEqual(t, []byte(out))
}

func TestShellQuitKeys(t *testing.T) {
	m := shellAt(80, 24)
	for _, key := range []string{"q", "ctrl+c"} {
		var msg tea.KeyPressMsg
		if key == "q" {
			msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
		} else {
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		_, cmd := m.Update(msg)
		if cmd == nil {
			t.Errorf("%s did not quit", key)
		}
	}
}

func TestShellZeroSizeSafe(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0")
	if v := m.View(); v.Content != "" {
		t.Error("zero-size view should be empty, not panic")
	}
}

func TestShellResizeRecomputesLayout(t *testing.T) {
	m := shellAt(120, 34)
	wide := m.View().Content
	model, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	narrow := model.(*Shell).View().Content
	if wide == narrow {
		t.Error("resize did not change the rendered layout")
	}
	if strings.Contains(narrow, "BOARD") {
		t.Error("kanban pane should collapse at 50 cols")
	}
}
