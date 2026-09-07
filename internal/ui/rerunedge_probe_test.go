package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/workflow"
)

// The workflow declares two rerun edges — verify→implement ("send it
// back as rework") and implement→plan ("the plan was wrong"). A declared
// edge no surface offers is unreachable in practice, so this probe pins
// each edge's offer separately: the bounce action must appear in the
// action inventory at both stages. Each assertion stands alone — the
// original implication between the two went green the moment one offer
// vanished, and would have let a later removal re-land an unreachable
// edge behind a passing lock.

func TestImplementPlanRerunEdgeReachability(t *testing.T) {
	// the edges are still declared; a dropped edge shows up here by name
	// before the offer assertions below can even run
	next := workflow.Next(domain.StageImplement)
	if !slices.Contains(next, domain.StagePlan) {
		t.Fatalf("graph no longer declares implement→plan: Next = %v", next)
	}

	t.Run("verify re-offers implement", func(t *testing.T) {
		acts := cardActionsFor(nextInput{stage: domain.StageVerify, kind: domain.KindFeature}, featureRow{})
		bounceOffersEdge(t, acts, domain.StageVerify, domain.StageImplement)
	})

	t.Run("implement re-offers plan", func(t *testing.T) {
		acts := cardActionsFor(nextInput{stage: domain.StageImplement, kind: domain.KindFeature}, featureRow{})
		bounceOffersEdge(t, acts, domain.StageImplement, domain.StagePlan)
	})
}

// bounceOffersEdge asserts the action inventory at from offers a bounce
// whose why names the declared rewind target to.
func bounceOffersEdge(t *testing.T, acts []cardAction, from, to domain.Stage) {
	t.Helper()
	i := slices.IndexFunc(acts, func(a cardAction) bool { return a.id == "bounce" })
	if i < 0 {
		t.Fatalf("%s→%s is declared in the workflow but the action inventory at %s offers no bounce: %q",
			from, to, from, actionIDs(acts))
	}
	if !strings.Contains(acts[i].why, string(to)) {
		t.Errorf("bounce why at %s does not name the rewind target %s: %q", from, to, acts[i].why)
	}
}

func actionIDs(acts []cardAction) []string {
	ids := make([]string, 0, len(acts))
	for _, a := range acts {
		ids = append(ids, a.id)
	}
	return ids
}

// TestBounceAtImplementRewindsToPlan: the b key on an implement-stage
// card takes the graph's implement→plan rerun edge — the same rewiring
// bounceStage already does for verify→implement.
func TestBounceAtImplementRewindsToPlan(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("on it"))
	m = advanceTo(t, m, domain.StageImplement)
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement", m.rows[0].F.Stage)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("bounce at implement did not rewind to plan (at %s)", m.rows[0].F.Stage)
	}
}

// TestBounceNoteRidesReplanKickoff: a bounce's note from the work stage
// has nowhere to land until the replan runs — it waits in bounceNotes
// and rides the plan kickoff, the same delivery a verify bounce gives
// the reborn implement run.
func TestBounceNoteRidesReplanKickoff(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("on it"))
	m = advanceTo(t, m, domain.StageImplement)

	m = pump(t, m, m.bounceStage("FD-001", "the plan ignored the streaming constraint"))
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("bounce landed at %s, want plan", m.rows[0].F.Stage)
	}

	// the run the stash waits for: enter answers the plan stage's idle
	// decision and starts the replan
	m = openAndAttach(t, m)
	settleChat(t, eng)

	snap := eng.Get("FD-001").Snapshot()
	if len(snap.Transcript) == 0 || snap.Transcript[0].Author != engine.AuthorSystem {
		t.Fatalf("kickoff missing from the transcript: %+v", snap.Transcript)
	}
	if !strings.Contains(snap.Transcript[0].Content, "the plan ignored the streaming constraint") {
		t.Errorf("bounce note did not ride the replan kickoff: %q", snap.Transcript[0].Content)
	}
	if _, ok := m.bounceNotes["FD-001"]; ok {
		t.Error("the bounce note survived the run that consumed it")
	}
}
