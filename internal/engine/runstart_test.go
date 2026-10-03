package engine

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// attendedFeature builds a feature whose gate-approval mode is
// domain.GateAttended.
func attendedFeature(num int, title string, stage domain.Stage) domain.Feature {
	f := feature(num, title, stage)
	f.GateApproval = domain.GateAttended
	return f
}

// autopilotFeature builds a feature whose gate-approval mode is
// domain.GateAutopilot.
func autopilotFeature(num int, title string) domain.Feature {
	f := feature(num, title, domain.StageImplement)
	f.GateApproval = domain.GateAutopilot
	return f
}

// TestEveryRunStartsAtOnce: nothing caps how many autonomous runs execute
// at once. Three attended and three autopilot cards are run back to back
// while every agent is still busy, and all six are running together.
func TestEveryRunStartsAtOnce(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	feats := []domain.Feature{
		attendedFeature(1, "att one", domain.StageImplement),
		attendedFeature(2, "att two", domain.StageImplement),
		attendedFeature(3, "att three", domain.StageImplement),
		autopilotFeature(4, "auto one"),
		autopilotFeature(5, "auto two"),
		autopilotFeature(6, "auto three"),
	}
	for _, f := range feats {
		withWorktree(t, wt, f)
	}
	for _, f := range feats {
		if err := e.Run(f); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range feats {
		waitState(t, e, f.ID, StateRunning)
	}
}

// TestAnAskLeavesOtherRunsRunning: an attended run blocked on a question
// neither gives up its place nor holds back another attended card. The
// asker stays running with the question open, and a second attended card
// started meanwhile is running as well.
func TestAnAskLeavesOtherRunsRunning(t *testing.T) {
	release := make(chan struct{})
	args := []byte(`{"changes_section":"Problem","question":"Persist where?","options":[{"label":"per-device","detail":"localStorage"},{"label":"synced","detail":"account"}]}`)
	ag := &agent.Fake{Caps: agent.Capabilities{ClientTools: true, Interrupt: true}, Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.FeatureID == "FD-001" {
			return []agent.Event{{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: args}}}
		}
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	asker := attendedFeature(1, "asker", domain.StagePlan)
	other := attendedFeature(2, "other", domain.StageImplement)
	for _, f := range []domain.Feature{asker, other} {
		if err := store.CreateFeature(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
		withWorktree(t, wt, f)
	}
	if err := e.Run(asker); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(other); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, func() bool { return e.Get("FD-001").Snapshot().PendingAsk != nil })
	waitState(t, e, "FD-001", StateRunning)
	waitState(t, e, "FD-002", StateRunning)
}

// waitUntil polls cond until it holds or the test's patience runs out.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
