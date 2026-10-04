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

// repoWorkspace builds a shell over a workspace with a default repo plus
// two named repos ("a" and "b"), and one todo feature on the board.
func repoWorkspace(t *testing.T) *Shell {
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
	init(root)
	repoA := filepath.Join(root, "git", "a")
	repoB := filepath.Join(root, "git", "b")
	for _, r := range []string{repoA, repoB} {
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		init(r)
	}

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), root, root,
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
		Stage: domain.StageTodo, Profile: "thrifty",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	return m
}

func TestBoardKeyOChangesRepo(t *testing.T) {
	m := repoWorkspace(t)
	if len(m.rows) != 1 || m.rows[0].F.Repo != "" {
		t.Fatalf("precondition: got rows=%d repo=%q, want 1/\"\"", len(m.rows), m.rows[0].F.Repo)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if _, ok := m.Overlay.Top().(*repoPickerDialog); !ok {
		t.Fatalf("o did not open repo picker, got %T", m.Overlay.Top())
	}
	// the picker opens on the first configured repo (there is no "default"
	// candidate), so one step right selects the second.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(m.rows) != 1 {
		t.Fatalf("reload dropped rows: %d", len(m.rows))
	}
	if m.rows[0].F.Repo != "b" {
		t.Errorf("repo after o→right→enter = %q, want %q", m.rows[0].F.Repo, "b")
	}
	if m.notice.text != "FD-001: repo set to b" {
		t.Errorf("notice = %q, want success message", m.notice.text)
	}
}

func TestBoardKeyORefusedWithWorktree(t *testing.T) {
	m := repoWorkspace(t)
	// simulate a card whose worktree already exists; the guard rejects
	// before opening the picker regardless of stage.
	m.rows[0].HasWorktree = true

	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if m.Overlay.Top() != nil {
		t.Fatalf("o opened %T for a worktree card; want no overlay", m.Overlay.Top())
	}
	if !m.notice.isErr || m.notice.text != "FD-001: repo is fixed once a worktree exists" {
		t.Errorf("notice = %q isErr=%v, want error about fixed repo", m.notice.text, m.notice.isErr)
	}
}

// TestDeletingACardWhoseRepoIsNoLongerConfigured: a card minted against a
// named repo that has since been dropped from `repos:` has nothing on disk
// left to reach — the delete still has to take its record off the board
// rather than refusing it forever.
func TestDeletingACardWhoseRepoIsNoLongerConfigured(t *testing.T) {
	m := repoWorkspace(t)
	ctx := context.Background()
	f, err := m.store.GetFeature(ctx, m.rows[0].F.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Repo = "a"
	if err := m.store.UpdateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Ensure(ctx, &f); err != nil {
		t.Fatal(err)
	}

	// "a" drops out of `repos:` between sessions: the pool that comes
	// back up knows nothing of it.
	pool, err := worktree.NewPool(ctx, m.wt.Root(), m.wt.Root(), nil, m.store, false)
	if err != nil {
		t.Fatal(err)
	}
	m.wt = pool

	msg, ok := m.deleteFeature(f.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if _, err := m.store.GetFeature(ctx, f.ID); err == nil {
		t.Errorf("%s still has a record", f.ID)
	}
}

// TestDeletingACardRemovesItsScratchFiles: a card's scratch-files dir —
// where a backend that keeps a per-card config home (antigravity) derives
// it — goes with the card, while a co-resident card's is untouched.
func TestDeletingACardRemovesItsScratchFiles(t *testing.T) {
	m := repoWorkspace(t)
	ctx := context.Background()
	f, err := m.store.GetFeature(ctx, m.rows[0].F.ID)
	if err != nil {
		t.Fatal(err)
	}

	home := filepath.Join(m.ws.ScratchFilesDir(f.ID), "agy-home")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	// what an agent's `go build` leaves under that home: a module cache of
	// read-only directories and files, which plain os.RemoveAll cannot take.
	mod := filepath.Join(home, "go", "pkg", "mod", "x@v1")
	if err := os.MkdirAll(mod, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "a.go"), []byte("package x\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mod, 0o700) })
	other := filepath.Join(m.ws.ScratchFilesDir("FD-999"), "agy-home", ".gemini")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}

	msg, ok := m.deleteFeature(f.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("deleted card's card home still present: stat err = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("co-resident card's scratch files removed: %v", err)
	}
}
