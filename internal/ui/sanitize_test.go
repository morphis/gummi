package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
)

func TestSanitizeStripsEscapes(t *testing.T) {
	cases := map[string]string{
		"plain text":                  "plain text",
		"keeps\nnewlines\tand tabs":   "keeps\nnewlines\tand tabs",
		"clip\x1b]52;c;ZWvil\x07jack": "clipjack",          // OSC 52 clipboard write
		"title\x1b]0;pwned\x07here":   "titlehere",         // OSC title spoof
		"wipe\x1b[2Jscreen":           "wipescreen",        // CSI clear
		"bell\x07 and null\x00byte":   "bell and nullbyte", // lone C0 controls
		"cursor\x1b[6nquery":          "cursorquery",       // device status report
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChatRendersHostileModelOutputSafely proves a malicious provider
// reply cannot smuggle escape sequences to the terminal through the
// chat transcript.
func TestChatRendersHostileModelOutputSafely(t *testing.T) {
	hostile := "Sure!\x1b]52;c;bWFsaWNpb3Vz\x07 done."
	m, eng := chatWorkspace(t, agent.NewFake(hostile))
	m = openAndAttach(t, m)
	m = typeString(t, m, "hi")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	view := m.View().Content
	if strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x07") {
		t.Fatal("hostile OSC sequence reached the rendered screen")
	}
	// the readable text survives
	if !strings.Contains(view, "done.") {
		t.Error("sanitizer ate the legible text")
	}
}
