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
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// reposOnlyWorkspace builds a shell over a repos:-only workspace: the
// workspace root is a plain parent of checkouts with no default repo, two
// named repos ("a" and "b"), and one todo feature naming "a".
func reposOnlyWorkspace(t *testing.T) *Shell {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(repo string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	init := func(r string) {
		git(r, "init", "-q", "-b", "main")
		git(r, "config", "user.name", "t")
		git(r, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(r, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(r, "add", ".")
		git(r, "commit", "-q", "-m", "init")
	}
	repoA := filepath.Join(root, "git", "a")
	repoB := filepath.Join(root, "git", "b")
	for _, r := range []string{repoA, repoB} {
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		init(r)
	}

	ws, err := state.Init(root, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), root, "",
		[]worktree.NamedRepo{{Name: "a", Root: repoA}, {Name: "b", Root: repoB}}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("x")), Store: store, Pool: pool, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	m.SetRepoNames(pool.Names())
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	f := domain.Feature{
		ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StageTodo, Profile: "thrifty", Repo: "a",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	return m
}

// TestDeletingACardWithNoRepoInAReposOnlyWorkspace: a card whose repo is
// empty in a workspace with no default has nothing on disk left to reach —
// it never could have cut a worktree, branch, or scratch tree. The delete
// degrades to record-only removal (record and workspace files gone) rather
// than refusing forever, the same way it already handles a named repo
// dropped from `repos:`.
func TestDeletingACardWithNoRepoInAReposOnlyWorkspace(t *testing.T) {
	m := reposOnlyWorkspace(t)
	ctx := context.Background()

	// a card minted before its workspace grew `repos:` entries: an empty
	// repo and no default to resolve it to.
	now := fixedTime
	f := domain.Feature{
		ID: "FD-002", Num: 2, Title: "Legacy card", Slug: "legacy-card",
		Stage: domain.StageTodo, Profile: "thrifty",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	// the workspace files keyed to the record exist — the card reached
	// design before the workspace changed under it.
	artifact, ok := f.ArtifactFile(m.wt.Root())
	if !ok {
		t.Fatal("a feature card has an artifact path")
	}
	if err := os.WriteFile(artifact, []byte("# spec\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
	if err := os.WriteFile(draft, []byte("# draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg, ok := m.deleteFeature(f.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if _, err := m.store.GetFeature(ctx, f.ID); err == nil {
		t.Errorf("%s still has a record", f.ID)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Errorf("artifact survived the delete: %v", err)
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Errorf("draft survived the delete: %v", err)
	}
	// and nothing was ever cut for it: no branch worktree and no scratch
	// tree anywhere in the workspace.
	for _, p := range []string{
		filepath.Join(m.wt.Root(), ".gummi", "worktrees", string(f.ID)),
		filepath.Join(m.wt.Root(), ".gummi", "scratch", string(f.ID)),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists for a card that never cut a tree: %v", p, err)
		}
	}
}

// TestDuplicateThenDeleteInAReposOnlyWorkspace: the reported flow end to
// end. A duplicate used to be born with an empty repo — exactly the card
// mint refuses in a repos:-only workspace — so its delete died on the
// first worktree probe. Carried, the copy is indistinguishable from a
// card minted through the form, and the whole flow succeeds.
func TestDuplicateThenDeleteInAReposOnlyWorkspace(t *testing.T) {
	m := reposOnlyWorkspace(t)
	ctx := context.Background()

	if msg := m.duplicateFeature("FD-001")(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("duplicate failed: %s", nm.text)
		}
	}
	dup, err := m.store.GetFeature(ctx, "FD-002")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Repo != "a" {
		t.Fatalf("duplicate repo = %q, want the source's %q", dup.Repo, "a")
	}

	msg, ok := m.deleteFeature(dup.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if _, err := m.store.GetFeature(ctx, dup.ID); err == nil {
		t.Errorf("%s still has a record", dup.ID)
	}

	// the source is untouched by either action.
	src, err := m.store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if src.Repo != "a" || src.Stage != domain.StageTodo {
		t.Errorf("source changed by duplicate+delete: repo=%q stage=%q", src.Repo, src.Stage)
	}
}
