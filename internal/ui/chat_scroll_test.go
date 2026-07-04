package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatScrollbackPaging(t *testing.T) {
	// simulate a render having recorded the layout: 100 transcript lines
	// in a 10-line viewport.
	c := &chatPane{bodyH: 10, totalLines: 100}

	pgup := tea.KeyPressMsg{Code: tea.KeyPgUp}
	pgdn := tea.KeyPressMsg{Code: tea.KeyPgDown}

	// pgup scrolls up by a page (bodyH-1 = 9); no detach/send
	if detach, send, _, _ := c.handleKey(pgup); detach || send != "" {
		t.Fatalf("pgup detach=%v send=%q, want false/empty", detach, send)
	}
	if c.scroll != 9 {
		t.Errorf("after one pgup scroll = %d, want 9", c.scroll)
	}
	// paging up clamps at maxScroll = totalLines-bodyH = 90
	for i := 0; i < 30; i++ {
		c.handleKey(pgup)
	}
	if c.scroll != 90 {
		t.Errorf("scroll clamped to %d, want 90", c.scroll)
	}
	// pgdown walks back toward the latest
	c.handleKey(pgdn)
	if c.scroll != 81 {
		t.Errorf("after pgdown scroll = %d, want 81", c.scroll)
	}
	// paging down clamps at the bottom (0)
	for i := 0; i < 30; i++ {
		c.handleKey(pgdn)
	}
	if c.scroll != 0 {
		t.Errorf("scroll clamped to %d, want 0 (bottom)", c.scroll)
	}
}

func TestChatSendJumpsToLatest(t *testing.T) {
	c := &chatPane{bodyH: 10, totalLines: 100}
	c.input = newChatInput()
	c.input.SetValue("a question")
	c.scroll = 40 // scrolled up into history
	detach, send, _, _ := c.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if detach || send != "a question" {
		t.Fatalf("enter detach=%v send=%q", detach, send)
	}
	if c.scroll != 0 {
		t.Errorf("send left scroll at %d, want 0 (jump to latest)", c.scroll)
	}
}
