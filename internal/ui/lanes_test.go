package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// TestBoardFooterHasNoLaneCounts: the board footer reports cards and their
// state, never a concurrency cap. Cards running in both gate modes at once
// leave no "attended n/m" or "autopilot n/m" text in it, because nothing
// caps or queues them.
func TestBoardFooterHasNoLaneCounts(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := agentWorkspace(t, ag)
	t.Cleanup(func() {
		close(release)
		eng.Close()
	})
	ctx := context.Background()

	attended := mkFeature(t, m.store, 20, "attended card", domain.StageImplement)
	auto := mkFeature(t, m.store, 21, "autopilot card", domain.StageImplement)
	if err := m.store.SetGateApproval(ctx, auto.ID, domain.GateAutopilot); err != nil {
		t.Fatal(err)
	}
	for _, f := range []domain.Feature{attended, auto} {
		ff := f
		if _, err := m.wt.Create(ctx, &ff); err != nil {
			t.Fatal(err)
		}
		if err := eng.Run(ff); err != nil {
			t.Fatal(err)
		}
	}
	waitForState(t, eng, attended.ID, engine.StateRunning)
	waitForState(t, eng, auto.ID, engine.StateRunning)

	var rows []featureRow
	for _, id := range []domain.FeatureID{attended.ID, auto.ID} {
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, featureRow{F: f})
	}
	m.rows = rows

	got := m.boardCounts()
	if got == "" {
		t.Fatal("the board footer is empty with two cards running")
	}
	for _, word := range []string{"attended", "autopilot"} {
		if strings.Contains(got, word) {
			t.Errorf("board footer %q carries lane text %q; nothing caps the runs", got, word)
		}
	}
}
