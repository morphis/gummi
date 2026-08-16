package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

func TestCritiqueHintsAndTools(t *testing.T) {
	f := feature(1, "Dark mode", domain.StagePlan)

	joined := strings.Join(stageHints(f, "spec.md", flavorCritique), "\n")
	if !strings.Contains(joined, "Stage: Plan critique") {
		t.Error("critique hints missing the critique stage contract")
	}
	if !strings.Contains(joined, "You are the reviewer") {
		t.Error("critique contract not issued for the reviewer role")
	}
	if !strings.Contains(joined, "VERDICT:") {
		t.Error("critique hints missing the verdict fallback grammar")
	}
	if !strings.Contains(joined, "executability") || !strings.Contains(joined, "[CI-only]") {
		t.Error("critique hints missing the executability lens / allowed-skip tags")
	}
	if !strings.Contains(joined, "Plan claims") {
		t.Error("critique hints missing awareness of the Plan claims table")
	}
	if plain := strings.Join(stageHints(f, "spec.md", flavorStage), "\n"); strings.Contains(plain, "Plan critique") {
		t.Error("plan-writer hints leaked the critique contract")
	}

	var names []string
	for _, td := range stageTools(domain.StagePlan, flavorCritique) {
		names = append(names, td.Name)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, "submit_verdict") || !strings.Contains(got, "spec_annotate") {
		t.Errorf("critique tools = %s, want submit_verdict + spec_annotate", got)
	}
	var writerNames []string
	for _, td := range stageTools(domain.StagePlan, flavorStage) {
		writerNames = append(writerNames, td.Name)
	}
	writer := strings.Join(writerNames, ",")
	if !strings.Contains(writer, "spec_view") || !strings.Contains(writer, "spec_replace_section") {
		t.Errorf("plan writer tools = %s, want spec_view + spec_replace_section", writer)
	}
	if toolHint(domain.StagePlan, flavorStage) == "" {
		t.Error("plan writer tool hint empty")
	}
	if toolHint(domain.StagePlan, flavorCritique) == "" {
		t.Error("critique tool hint empty")
	}
}

func TestRunCritiqueOnlyOnPlanStage(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	for _, stage := range []domain.Stage{domain.StageImplement, domain.StageReview, domain.StageSpec} {
		if err := e.RunCritique(feature(1, "x", stage), ""); err == nil {
			t.Errorf("critique allowed on %s stage", stage)
		}
	}
}

func TestCritiqueRunsAsReviewer(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "Dark mode", domain.StagePlan)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	var got agent.SessionOpts
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			got = opts
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Sound.\nVERDICT: pass"},
				{Kind: agent.EventIdle},
			}
		},
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	if err := e.RunCritique(f, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)

	s := e.Get(f.ID)
	if s == nil || !s.Snapshot().Critique {
		t.Fatal("session not marked as critique")
	}
	if got.Role != agent.RoleReviewer {
		t.Errorf("critique spawned as %s, want reviewer", got.Role)
	}
	if !strings.Contains(strings.Join(got.SystemHints, "\n"), "Stage: Plan critique") {
		t.Error("critique session missing its stage contract")
	}
	var names []string
	for _, td := range got.Tools {
		names = append(names, td.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "submit_verdict") {
		t.Errorf("critique session tools = %v, want submit_verdict", names)
	}
}

func TestRestoreRecoversCritiqueFlag(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "Dark mode", domain.StagePlan)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	e1 := persistEngine(t, agent.NewFake("Sound.\nVERDICT: pass"), ws, store, wt)
	if err := e1.RunCritique(f, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, e1, f.ID, StateDone)
	e1.Close()

	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("critique session not restored")
	}
	snap := s.Snapshot()
	if !snap.Critique || snap.Role != agent.RoleReviewer {
		t.Errorf("restored critique = %v role = %s, want critique reviewer", snap.Critique, snap.Role)
	}
}
