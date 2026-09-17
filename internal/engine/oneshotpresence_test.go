package engine

import (
	"context"
	"testing"

	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
)

// TestOneShotPassIsVisibleWhileItRuns locks the fix for the drive's
// most-repeated failure. Check discovery and its baseline hold a feature
// rather than a Session, so for the minutes they run — 4.6 of them on
// canonical/lxd — every question about whether the card was working got
// the wrong answer: the board drew "autopilot stopped without saying so"
// next to a live spend, and a goal declared its own healthy child stuck
// and paid a lead turn to restart it.
func TestOneShotPassIsVisibleWhileItRuns(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	f := feature(1, "Dark mode", domain.StagePlan)
	putFeature(t, e.cfg.Store, f)

	if e.OneShotRunning(f.ID) {
		t.Fatal("a card with nothing running reports a pass in flight")
	}
	end := e.beginOneShot(f, "scribe")
	if !e.OneShotRunning(f.ID) {
		t.Error("a pass in flight is invisible to the engine — this is what told " +
			"a goal its working card was stuck")
	}
	// and visible across processes, which is what the board and `gummi
	// status` read. The header is written by the writer's own goroutine,
	// so give it a moment to land.
	live := false
	for i := 0; i < 200 && !live; i++ {
		live = state.CardIsLive(e.cfg.Workspace, f.ID)
		if !live {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !live {
		t.Error("a pass in flight leaves no live file — the board reads the card as dead")
	}

	// nested: baseline follows discovery on the same card
	end2 := e.beginOneShot(f, "scribe")
	end()
	if !e.OneShotRunning(f.ID) {
		t.Error("the first pass ending cleared a second that is still running")
	}
	end2()
	if e.OneShotRunning(f.ID) {
		t.Error("the pass never cleared")
	}
	st, err := livelog.Stat(e.cfg.Workspace.LiveFile(f.ID))
	if err != nil {
		t.Fatalf("stat live file: %v", err)
	}
	if !st.Stopped {
		t.Error("the pass ended without marking its live file stopped — a follower " +
			"waits forever on a run that is over")
	}
	_ = context.Background()
}
