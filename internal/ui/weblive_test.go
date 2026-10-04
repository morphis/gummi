package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// A tool line whose backend never reported back is not running once the
// session has stopped: only the newest pending call of a session mid-turn
// is in flight.
func TestWebTranscriptOnlyTheCallInFlightRuns(t *testing.T) {
	tr := []engine.Message{
		{Author: engine.AuthorTool, Content: "read  a.go"},
		{Author: engine.AuthorAssistant, Content: "done"},
		{Author: engine.AuthorTool, Content: "edit  b.go"},
	}
	turns, _, tool := webTranscript(engine.Snapshot{Transcript: tr})
	if tool != nil {
		t.Fatalf("a stopped session has no call in flight, got %+v", tool)
	}
	for _, tn := range turns {
		if tn.Tool != nil && tn.Tool.Status != "" {
			t.Fatalf("%q: status %q, want none", tn.Tool.Label, tn.Tool.Status)
		}
	}

	turns, _, tool = webTranscript(engine.Snapshot{Transcript: tr, Busy: true})
	if tool == nil || tool.Status != "running" || tool.Label != "edit  b.go" {
		t.Fatalf("the newest call of a busy session is in flight, got %+v", tool)
	}
	if st := turns[0].Tool.Status; st != "" {
		t.Fatalf("an older pending call: status %q, want none", st)
	}
	if st := turns[2].Tool.Status; st != "running" {
		t.Fatalf("the call in flight: status %q, want running", st)
	}
}

// A Monitor watch (Claude Code's background-watch tool) never gets a
// reported outcome while it runs, and it keeps running past the turn
// that started it going idle: unlike an ordinary pending call, it must
// still read as active rather than as "outcome unknown", whether or not
// the session is busy or this is its newest call.
func TestWebTranscriptAMonitorWatchKeepsWatching(t *testing.T) {
	tr := []engine.Message{
		{Author: engine.AuthorTool, Tool: "Monitor", Detail: "tail -f build.log", Content: "Monitor  tail -f build.log"},
		{Author: engine.AuthorAssistant, Content: "started the watch"},
	}
	turns, _, tool := webTranscript(engine.Snapshot{Transcript: tr})
	if tool != nil {
		t.Fatalf("an idle session has no call in flight, got %+v", tool)
	}
	if st := turns[0].Tool.Status; st != "watching" {
		t.Fatalf("an idle session's outstanding watch: status %q, want watching", st)
	}
}

// A message a pause (or a failure) cut off is not still being written:
// the session is not mid-turn, and the page drew it under "writing" with a
// spinner for as long as the paused session stayed around.
func TestWebTranscriptACutOffMessageIsNotStreaming(t *testing.T) {
	tr := []engine.Message{{Author: engine.AuthorAssistant, Content: "Converged on the", Streaming: true}}
	turns, streaming, _ := webTranscript(engine.Snapshot{Transcript: tr, State: engine.StatePaused})
	if streaming != "" {
		t.Fatalf("a paused session streams %q", streaming)
	}
	if len(turns) != 1 || turns[0].Text != "Converged on the" {
		t.Fatalf("the cut-off message is not kept as the turn it stopped as: %+v", turns)
	}
	if _, streaming, _ = webTranscript(engine.Snapshot{Transcript: tr, Busy: true}); streaming != "Converged on the" {
		t.Fatalf("a busy session's tail is what it is writing, got %q", streaming)
	}
}

// A card whose busy word moves — a check that finished, a scribe pass
// that settled — tells the open pages, whatever message moved it: the
// page kept "implementer is checking" with a spinner for a card that had
// long gone idle, because the pass's end was on no list of changes.
func TestABusyWordThatEndsIsPushed(t *testing.T) {
	b, log, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m.baselining[f.ID] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return bd.Rows[0].Status == webapi.StatusRunning })
	log.mu.Lock()
	mark := len(log.all)
	log.mu.Unlock()
	// the check ends on a message the change hook has no arm for, and the
	// row it reloads is the row it was
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		return func() tea.Msg { return baselineDoneMsg{id: f.ID} }
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		log.mu.Lock()
		var live, card bool
		for _, c := range log.all[mark:] {
			live = live || (c.Kind == webapi.ChangeLive && c.ID == "FD-001")
			card = card || (c.Kind == webapi.ChangeCard && c.ID == "FD-001")
		}
		log.mu.Unlock()
		if live && card {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the check ended and no page was told: its spinner stays up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTheWebConversationNamesTheDraftingBrief: while gummi's handoff-brief
// turn runs on a freeform card, the web conversation's busy verb names the
// drafting — the same in-flight flag the TUI thread's busy line reads —
// and goes back to the ordinary working word once it has ended.
func TestTheWebConversationNamesTheDraftingBrief(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	once := &sync.Once{}
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			once.Do(func() { close(started) })
			<-release
			return []agent.Event{{Kind: agent.EventMessage, Text: "the brief"}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "on it"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	m, eng, _ := freeformAgentWorkspace(t, ag)
	ctx := context.Background()

	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	fs, err := m.store.ListFeatures(ctx)
	if err != nil || len(fs) != 1 {
		t.Fatalf("features = %v, err = %v", fs, err)
	}
	f := fs[0]
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked pty fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, eng, f.ID)
	m = pump(t, m, m.loadRows)

	done := make(chan error, 1)
	go func() {
		_, _, err := eng.SessionHandoffBrief(ctx, f.ID)
		done <- err
	}()
	<-started
	r, ok := m.rowByID(f.ID)
	if !ok {
		t.Fatal("the board lost the card")
	}
	conv := m.webFreeform(r)
	if conv == nil || conv.Verb != engine.BriefDrafting {
		t.Fatalf("the conversation's busy verb = %+v, want %q", conv, engine.BriefDrafting)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("SessionHandoffBrief: %v", err)
	}
	if conv := m.webFreeform(r); conv == nil || conv.Busy || conv.Verb != "" {
		t.Fatalf("after the brief turn the conversation still reads busy: %+v", conv)
	}
}
