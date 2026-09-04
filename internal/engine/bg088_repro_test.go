package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// researchFeature builds a research-kind work item (RS-003) parked at an
// autonomous stage — review, the first stage on the research route that
// runs without a human in the loop and so reaches checkpoint at all.
func researchFeature(stage domain.Stage) domain.Feature {
	const num = 3
	id, _ := domain.NewID(domain.KindResearch, num)
	slug, _ := domain.Slugify("quota accounting")
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindResearch, Title: "quota accounting",
		Slug: slug, Stage: stage, CreatedAt: now, UpdatedAt: now,
	}
}

// TestBG088CheckpointSaysNothingOnAWorktreeLessStage: a research card has
// no worktree by design, so its autonomous stages have nothing to commit.
// checkpoint must say nothing at all about that — it used to let
// CommitAll fail with ErrNoWorktree and write "checkpoint commit failed:
// … no worktree" into the session's activity, which the card thread
// renders as a receipt under every research stage the agent runs.
//
// Asserted over the canonical list rather than the one stage the drive
// saw: every stage on the research route that reaches checkpoint must be
// silent, and every stage that does need a worktree must still be able to
// report a real failure.
func TestBG088CheckpointSaysNothingOnAWorktreeLessStage(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	for _, stage := range domain.Stages {
		if workflow.NeedsWorktree(domain.KindResearch, stage) {
			t.Fatalf("%s: a research card is never supposed to need a worktree", stage)
		}
		if interactiveStage(stage) || stage == domain.StageDone {
			continue // never reaches checkpoint
		}
		f := researchFeature(stage)
		if err := store.CreateFeature(context.Background(), &f); err != nil && stage != domain.StageTodo {
			// one row per test; recreating the same id is expected to fail
			_ = err
		}
		s := &Session{Feature: f}
		if err := e.checkpoint(s); err != nil {
			t.Errorf("%s: checkpoint returned %v on a worktree-less stage", stage, err)
		}
		for _, a := range s.Snapshot().Activity {
			if strings.Contains(a, "checkpoint") {
				t.Errorf("%s: research card got checkpoint activity %q", stage, a)
			}
		}
	}
}

// TestBG088CheckpointStillReportsARealFailure guards the other half: the
// silence above is scoped to stages that are worktree-less by design, not
// to a missing worktree in general. A feature card at implement still has
// its lost worktree reported back, so the run cannot read as a clean
// finish (the invariant TestCheckpointWorktreeGoneFailsRunWithoutIdle
// covers end to end).
func TestBG088CheckpointStillReportsARealFailure(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement) // no worktree created
	s := &Session{Feature: f}
	if err := e.checkpoint(s); err == nil {
		t.Fatal("a stage that needs a worktree must report a missing one, not swallow it")
	}
	if !containsSubstring(s.Snapshot().Activity, "checkpoint commit failed") {
		t.Errorf("activity missing the failure line: %v", s.Snapshot().Activity)
	}
}
