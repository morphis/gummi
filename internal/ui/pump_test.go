package ui

import (
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestPumpWaitsForSlowFiniteCmd proves the pump carries no wall-clock
// deadline: a command that takes much longer than the old 100 ms budget
// (a proxy for a git-heavy chain like the merge flow shells out to) is
// still run to completion and its message reaches the shell.
func TestPumpWaitsForSlowFiniteCmd(t *testing.T) {
	m, _ := newWorkspace(t)
	cmd := func() tea.Msg {
		time.Sleep(300 * time.Millisecond)
		return noticeMsg{text: "slow finite done"}
	}
	m = pump(t, m, cmd)
	if m.notice.text != "slow finite done" {
		t.Fatalf("notice.text = %q, want %q", m.notice.text, "slow finite done")
	}
}

// TestPumpSkipsSubscriptionWrappedCmd proves the tag path: a
// subscription-wrapped command whose body would block forever on a
// channel receive is identified by its code pointer and never invoked.
func TestPumpSkipsSubscriptionWrappedCmd(t *testing.T) {
	m, _ := newWorkspace(t)
	var ran atomic.Bool
	cmd := subscription(func() tea.Msg {
		ran.Store(true)
		<-make(chan struct{})
		return nil
	})
	start := time.Now()
	pump(t, m, cmd)
	if ran.Load() {
		t.Fatal("pump invoked the wrapped subscription body; want it skipped")
	}
	if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
		t.Fatalf("pump took %v to skip a subscription; want well under 50ms", elapsed)
	}
}
