package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// uiRepo wires a throwaway repo, store, and worktree manager for tests
// that need direct store access (chatWorkspace hides the store).
func uiRepo(t *testing.T) (state.Workspace, *state.Store, *worktree.Pool) {
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
	return ws, store, worktree.WrapSingle(wt)
}

func mkFeature(t *testing.T, store *state.Store, num int, title string, stage domain.Stage) domain.Feature {
	t.Helper()
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	f := domain.Feature{ID: id, Num: num, Title: title, Slug: slug, Stage: stage, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestReconstructInboxAfterRestart rebuilds the needs-attention queue
// from durable session state: a parked budget stop, a plain gate, and a
// failed run all reappear after a restart, so the top-up path for a
// parked card isn't stranded (the drive's FD-002 finding).
func TestReconstructInboxAfterRestart(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()

	fBudget := mkFeature(t, store, 1, "budget park", domain.StageImplement)
	fGate := mkFeature(t, store, 2, "plain gate", domain.StageReview)
	fFail := mkFeature(t, store, 3, "failed run", domain.StageVerify)

	// persist the sessions the way a pre-restart engine would have
	save := func(f domain.Feature, stateStr, role, errText string, activity ...string) {
		if err := store.SaveSession(ctx, state.SessionSnapshot{
			Feature: f.ID, Stage: f.Stage, Role: role, State: stateStr,
			Activity: activity, Error: errText,
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(fBudget, "done", "implementer", "", "worktree committed: FD-001 implement checkpoint", "budget exhausted — stage stopped for review")
	save(fGate, "done", "reviewer", "")
	save(fFail, "paused", "implementer", "provider boom")

	eng := engine.New(engine.Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.reconstructInbox()

	if m.inbox.len() != 3 {
		t.Fatalf("reconstructed inbox len = %d, want 3: %+v", m.inbox.len(), m.inbox.list())
	}
	got := map[domain.FeatureID]attnKind{}
	for _, it := range m.inbox.list() {
		got[it.Feature] = it.Kind
	}
	if got[fBudget.ID] != attnBudget {
		t.Errorf("budget park reconstructed as %q, want budget", got[fBudget.ID])
	}
	if got[fGate.ID] != attnGate {
		t.Errorf("plain gate reconstructed as %q, want gate", got[fGate.ID])
	}
	if got[fFail.ID] != attnFailure {
		t.Errorf("failed run reconstructed as %q, want failure", got[fFail.ID])
	}
}

// TestCreateFeatureDerivesTitle: a long creation description becomes a
// concise card title with the full text kept as the one-liner, so the
// title slot isn't the whole first line (the drive's F12 finding).
func TestCreateFeatureDerivesTitle(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	long := "Add a healthz endpoint. It returns status and version so the load balancer can check liveness."
	if msg := m.createFeature(formResult{Desc: long})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Add a healthz endpoint" {
		t.Errorf("card title = %q, want the concise first sentence", f.Title)
	}
	if f.OneLiner != strings.Join(strings.Fields(long), " ") {
		t.Errorf("one-liner lost the full description: %q", f.OneLiner)
	}
	if f.Slug != "add-a-healthz-endpoint" {
		t.Errorf("slug = %q, want it derived from the concise title", f.Slug)
	}
}
