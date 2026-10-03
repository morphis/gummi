package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

// watchingCard opens a freeform card whose agent starts a Monitor watch and
// ends its turn, then waits until the turn is over with the watch still open.
func watchingCard(t *testing.T) (*Shell, featureRow) {
	t.Helper()
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "Monitor", Detail: "tail build.log", CallID: "w1"},
			{Kind: agent.EventMessage, Text: "watching the build"},
			{Kind: agent.EventIdle},
		}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	m, eng := agentWorkspace(t, ag)
	r := freeformRow(9, "watch the build", true)
	ctx := context.Background()
	if err := m.store.CreateFeature(ctx, &r.F); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{r}
	ff, err := eng.OpenFreeform(ctx, r.F)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "tell me when it breaks"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ff.Busy() || !ff.Watching() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never ended with the watch open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m, r
}

// webRowStatus is the board row's status for card id, or "" when absent.
func webRowStatus(b webapi.Board, id string) webapi.RowStatus {
	for _, row := range b.Rows {
		if row.ID == id {
			return row.Status
		}
	}
	return ""
}

// An idle freeform card whose agent has a watch open reads as watching on
// the TUI board row and on the web board row, and the thread lists the
// watch by its command.
func TestAnOpenWatchShowsOnTheIdleCard(t *testing.T) {
	m, r := watchingCard(t)

	if line := m.cardLine(r, 1, false, false, 100); !strings.Contains(line, "watching") {
		t.Errorf("TUI board row = %q, want the open watch named", line)
	}
	if got := webRowStatus(m.WebBoard(), string(r.F.ID)); got != webapi.StatusWatching {
		t.Errorf("web board row status = %q, want %q", got, webapi.StatusWatching)
	}
	if conv := m.webFreeform(r); conv == nil || !containsLine(conv.Watches, "tail build.log") {
		t.Errorf("web conversation watches = %v, want the Monitor listed", conv)
	}

	s := theme.New(theme.GummiDark())
	snap := m.engine.Freeform(r.F.ID).Snapshot()
	thread := strings.Join(transcriptLines(s, snap, 120, false), "\n")
	if !strings.Contains(thread, "tail build.log") {
		t.Errorf("TUI thread does not list the open watch:\n%s", thread)
	}
	if !strings.Contains(thread, "watching") {
		t.Errorf("TUI thread does not say it is watching:\n%s", thread)
	}
}

// A card with no watch open reads as plainly idle on both faces.
func TestAnIdleCardWithoutAWatchReadsIdle(t *testing.T) {
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	m, eng := agentWorkspace(t, ag)
	r := freeformRow(10, "plain card", true)
	ctx := context.Background()
	if err := m.store.CreateFeature(ctx, &r.F); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{r}
	ff, err := eng.OpenFreeform(ctx, r.F)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "say done"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ff.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if line := m.cardLine(r, 1, false, false, 100); strings.Contains(line, "watching") {
		t.Errorf("TUI board row = %q, an idle card must not read as watching", line)
	}
	if got := webRowStatus(m.WebBoard(), string(r.F.ID)); got == webapi.StatusWatching {
		t.Errorf("web board row status = %q for a card with no watch", got)
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
