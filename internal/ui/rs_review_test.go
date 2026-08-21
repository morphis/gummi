package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// researchWorkspace builds a shell wired to a real workspace and a
// Fake-backed engine, with one research card (RS-001) created directly at
// stage — mirroring chatWorkspace's setup, but for a research card, which
// gummi mints through its own creation form rather than the feature "n"
// flow this package's other tests drive.
func researchWorkspace(t *testing.T, ag agent.Agent, stage domain.Stage) (*Shell, *engine.Engine) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	eng := engine.New(engine.Config{Agents: singleAgent(ag), Store: store, Pool: pool, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	slug, _ := domain.Slugify("research card")
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: stage, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return m, eng
}

// An RS work-leg completion (Investigate finishing) with a review round
// already burned — the review→investigate bounce — auto-continues to
// Shape: onAutonomousDone's investigate case mirrors the Implement/Fix
// case, so the RS review loop turns in the TUI exactly like the feature
// loop, instead of parking as a generic gate item.
func TestRSInvestigateAutoStepsToShape(t *testing.T) {
	ag := verdictAgent(func(agent.SessionOpts) string { return "shaped" })
	// research stages run read-only in the main checkout; only a backend
	// that can structurally enforce that is allowed to drive them.
	ag.Caps.ReadOnlyEnforce = true
	m, eng := researchWorkspace(t, ag, domain.StageInvestigate)
	m.setRound("RS-001", domain.RoundKindReview, 1)

	handled, cmd := m.onAutonomousDone("RS-001", domain.StageInvestigate)
	if !handled {
		t.Fatal("onAutonomousDone(investigate, round>0) not handled — the RS work leg should auto-continue")
	}
	if cmd == nil {
		t.Fatal("onAutonomousDone(investigate, round>0) returned a nil command")
	}
	m = pump(t, m, cmd)

	f, err := m.store.GetFeature(context.Background(), "RS-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Stage != domain.StageShape {
		t.Fatalf("feature at %s, want Shape (investigate auto-continued)", f.Stage)
	}
	// shape is interactive: the loop only clears the way to it, it never
	// auto-runs an agent turn there — that happens on the human's own
	// attach/Enter, so no session should have started.
	if s := eng.Get("RS-001"); s != nil && (s.State() == engine.StateRunning || s.State() == engine.StateQueued) {
		t.Error("shape session auto-started; want it to wait for the human's attach")
	}
}

// A fresh, loop-free Investigate completion (no review round burned) is
// NOT auto-continued — it raises the generic gate instead, matching
// Implement/Fix's behavior for a first-time work-stage completion.
func TestRSInvestigateNoLoopNotAutoStepped(t *testing.T) {
	m, _ := researchWorkspace(t, verdictAgent(func(agent.SessionOpts) string { return "shaped" }), domain.StageInvestigate)

	handled, cmd := m.onAutonomousDone("RS-001", domain.StageInvestigate)
	if handled {
		t.Fatal("onAutonomousDone(investigate, round==0) was handled — want the generic gate instead")
	}
	if cmd != nil {
		t.Fatal("onAutonomousDone(investigate, round==0) returned a non-nil command")
	}
}
