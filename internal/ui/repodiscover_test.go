package ui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// initTestRepo makes dir a git repository with one commit on main.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@e.invalid"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// discoveringShell builds a shell over a workspace whose named repositories
// are the git checkouts under <root>/git, found by a rescan (SetDiscover), and
// one todo card. calls counts the rescans the pool has run.
func discoveringShell(t *testing.T) (m *Shell, root string, calls *int) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initTestRepo(t, root)
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), root, root, nil, store, false)
	if err != nil {
		t.Fatal(err)
	}
	calls = new(int)
	gitDir := filepath.Join(root, "git")
	if err := pool.SetDiscover(func() ([]worktree.NamedRepo, map[string][]string, error) {
		*calls++
		entries, err := os.ReadDir(gitDir)
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		var named []worktree.NamedRepo
		for _, e := range entries {
			if e.IsDir() {
				named = append(named, worktree.NamedRepo{Name: e.Name(), Root: filepath.Join(gitDir, e.Name())})
			}
		}
		return named, nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("x")), Store: store, Pool: pool, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })
	m = NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
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
	return m, root, calls
}

// pickerCandidates is the repository names the open picker offers.
func pickerCandidates(t *testing.T, m *Shell) []string {
	t.Helper()
	d, ok := m.Overlay.Top().(*repoPickerDialog)
	if !ok {
		t.Fatalf("top overlay = %T, want the repository picker", m.Overlay.Top())
	}
	return d.candidates
}

func TestRepoPickerOffersACloneWithReposKnown(t *testing.T) {
	m, root, _ := discoveringShell(t)
	initTestRepo(t, filepath.Join(root, "git", "a"))
	m = pump(t, m, m.rescanRepos(""))
	if len(m.repoNames) != 1 || m.repoNames[0] != "a" {
		t.Fatalf("precondition: repoNames = %v, want [a]", m.repoNames)
	}

	initTestRepo(t, filepath.Join(root, "git", "b"))
	// the press opens the picker on [a] and returns a rescan; press pumps
	// that rescan and feeds its result back, so the picker is re-offered
	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if got := pickerCandidates(t, m); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("picker candidates after the rescan = %v, want [a b]", got)
	}
	if m.Overlay.Len() != 1 {
		t.Errorf("overlay has %d dialogs, want the one picker", m.Overlay.Len())
	}
}

func TestRepoPickerAfterRescanFindsAClone(t *testing.T) {
	m, root, _ := discoveringShell(t)
	if len(m.repoNames) != 0 {
		t.Fatalf("precondition: repoNames = %v, want none", m.repoNames)
	}
	initTestRepo(t, filepath.Join(root, "git", "a"))

	m = press(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if got := pickerCandidates(t, m); len(got) != 1 || got[0] != "a" {
		t.Errorf("picker candidates = %v, want [a]", got)
	}
}

func TestAnOlderRepoSnapshotIsDropped(t *testing.T) {
	m, root, _ := discoveringShell(t)
	ctx := context.Background()
	wt := m.wt

	older := readRepos(ctx, wt, m.nextRepoSeq())
	initTestRepo(t, filepath.Join(root, "git", "a"))
	newer := readRepos(ctx, wt, m.nextRepoSeq())

	m.landRepos(reposReadMsg{snap: newer})
	m.landRepos(reposReadMsg{snap: older})
	if len(m.repoNames) != 1 || m.repoNames[0] != "a" {
		t.Errorf("repoNames = %v after the older snapshot landed last, want [a]", m.repoNames)
	}
	if m.repoInstalled != newer.seq {
		t.Errorf("installed seq = %d, want the newer %d", m.repoInstalled, newer.seq)
	}
}

func TestAFixedRepoSetSendsNoRescan(t *testing.T) {
	m := repoWorkspace(t)
	if cmd := m.rescanRepos("FD-001"); cmd != nil {
		t.Errorf("rescanRepos on a fixed set returned a command; want nil")
	}
}

func TestDialogOpenRescanOffTheLoop(t *testing.T) {
	m, root, calls := discoveringShell(t)
	initTestRepo(t, filepath.Join(root, "git", "a"))
	m = pump(t, m, m.rescanRepos(""))
	before := *calls

	model, cmd := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = model.(*Shell)
	if *calls != before {
		t.Fatalf("rescans ran on the loop: %d → %d", before, *calls)
	}
	if cmd == nil {
		t.Fatal("o on a discovered set returned no rescan command")
	}
	pump(t, m, cmd)
	if *calls != before+1 {
		t.Errorf("rescans after the command ran = %d, want %d", *calls, before+1)
	}
}

// TestWebRepoActionOffersARepoClonedSinceLaunch: a board that started with no
// repositories still offers a card's repository action once a clone exists,
// and running the action sets the card's repository to that clone.
func TestWebRepoActionOffersARepoClonedSinceLaunch(t *testing.T) {
	m, root, _ := discoveringShell(t)
	b := NewHeadless(m, tea.WithOutput(io.Discard))
	go func() { _ = b.Run() }()
	t.Cleanup(b.Stop)
	ctx := context.Background()

	initTestRepo(t, filepath.Join(root, "git", "a"))
	card, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var choices []string
	for _, a := range card.Actions {
		if a.ID == "repo" {
			for _, c := range a.Choices {
				choices = append(choices, c.Value)
			}
		}
	}
	if len(choices) != 1 || choices[0] != "a" {
		t.Fatalf("repo action choices = %v, want [a]", choices)
	}

	if _, err := b.Action(ctx, "FD-001", "repo", webapi.ActionRequest{Repo: "a"}, "tester"); err != nil {
		t.Fatal(err)
	}
	var repo string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { repo = m.rows[0].F.Repo; return nil }); err != nil {
		t.Fatal(err)
	}
	if repo != "a" {
		t.Errorf("card repo after the action = %q, want %q", repo, "a")
	}
}

// gitBranch cuts branch in the checkout at dir.
func gitBranch(t *testing.T, dir, branch string) {
	t.Helper()
	if out, err := exec.CommandContext(context.Background(), "git", "-C", dir, "branch", branch).CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", branch, err, out)
	}
}

// openCardFormOverTwoRepos opens the new-card form on a workspace whose repos
// are a and d (d has a feat branch besides its checked-out main), with the
// repository row still unchosen.
func openCardFormOverTwoRepos(t *testing.T) (*Shell, *cardForm, string) {
	t.Helper()
	m, root, _ := discoveringShell(t)
	initTestRepo(t, filepath.Join(root, "git", "a"))
	initTestRepo(t, filepath.Join(root, "git", "d"))
	gitBranch(t, filepath.Join(root, "git", "d"), "feat")
	m = pump(t, m, m.rescanRepos(""))
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	d, ok := m.Overlay.Top().(*cardForm)
	if !ok {
		t.Fatalf("n opened %T, want the new-card form", m.Overlay.Top())
	}
	return m, d, root
}

// cloneAfterOpen makes a third repo e and runs the rescan a dialog open
// would, so every open dialog is re-offered its choices.
func cloneAfterOpen(t *testing.T, m *Shell, root string) {
	t.Helper()
	initTestRepo(t, filepath.Join(root, "git", "e"))
	pump(t, m, m.rescanRepos(""))
}

// TestADialogOffersARepoClonedSinceLaunch: the new-card form, opened after a
// clone was made, offers that clone with its own branches. Picking it by hand
// offers those branches too, with its checked-out branch as the default base.
func TestADialogOffersARepoClonedSinceLaunch(t *testing.T) {
	m, d, _ := openCardFormOverTwoRepos(t)
	if !slices.Contains(d.repo.options(), "d") {
		t.Fatalf("form repo options = %v, want d among them", d.repo.options())
	}
	press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	if d.repo.name() != "d" {
		t.Fatalf("repo after pressing 2 = %q, want d", d.repo.name())
	}
	if !slices.Contains(d.baseCands, "feat") || !slices.Contains(d.baseCands, "main") {
		t.Errorf("base candidates for d = %v, want main and feat", d.baseCands)
	}
	if got := d.baseCands[d.baseCursor]; got != "main" {
		t.Errorf("default base for d = %q, want its checked-out main", got)
	}
}

// TestARepoChoiceSurvivesARescan: a rescan that lands while the new-card form
// is open keeps what the person chose: the repo by name, the base cursor on
// its branch, an explicit base, and an unset repo unset.
func TestARepoChoiceSurvivesARescan(t *testing.T) {
	t.Run("the base cursor stays on its branch", func(t *testing.T) {
		m, d, root := openCardFormOverTwoRepos(t)
		m = press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
		d.baseCursor = slices.Index(d.baseCands, "feat")
		cloneAfterOpen(t, m, root)
		if d.repo.name() != "d" {
			t.Errorf("repo = %q after the rescan, want d", d.repo.name())
		}
		if got := d.baseCands[d.baseCursor]; got != "feat" {
			t.Errorf("base after the rescan = %q, want feat", got)
		}
	})
	t.Run("an explicit base is kept", func(t *testing.T) {
		m, d, root := openCardFormOverTwoRepos(t)
		m = press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
		d.base = "feat"
		cloneAfterOpen(t, m, root)
		if d.base != "feat" {
			t.Errorf("explicit base after the rescan = %q, want feat", d.base)
		}
	})
	t.Run("an unset repo stays unset", func(t *testing.T) {
		m, d, root := openCardFormOverTwoRepos(t)
		cloneAfterOpen(t, m, root)
		if d.repo.chosen() {
			t.Errorf("repo chosen after the rescan (%q), want it still unset", d.repo.name())
		}
		if !slices.Contains(d.repo.options(), "e") {
			t.Errorf("repo options = %v, want e among them", d.repo.options())
		}
	})
}

// TestIngestAndScheduleReofferAClone: the ingest and schedule dialogs, open
// when a clone lands, offer that clone.
func TestIngestAndScheduleReofferAClone(t *testing.T) {
	m, root, _ := discoveringShell(t)
	initTestRepo(t, filepath.Join(root, "git", "a"))
	m = pump(t, m, m.rescanRepos(""))
	m.Overlay.Push(newIngestForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.startIngest))
	m.Overlay.Push(newScheduleForm(m, nil))

	initTestRepo(t, filepath.Join(root, "git", "d"))
	m = pump(t, m, m.rescanRepos(""))
	ingest, ok := m.Overlay.At(0).(*ingestForm)
	if !ok || !slices.Contains(ingest.repo.options(), "d") {
		t.Errorf("ingest form repo options = %v, want d re-offered", ingest.repo.options())
	}
	sched, ok := m.Overlay.At(1).(*scheduleForm)
	if !ok || !slices.Contains(sched.repo.options(), "d") {
		t.Errorf("schedule form repo options = %v, want d re-offered", sched.repo.options())
	}
}

// TestAClashedNameIsRefusedWithItsPaths: a folder name two checkouts share is
// refused as the clash, listing both paths, not as an unconfigured repository.
func TestAClashedNameIsRefusedWithItsPaths(t *testing.T) {
	m, _, _ := discoveringShell(t)
	clash := map[string][]string{"a": {"/x/a", "/y/a"}}
	if err := m.wt.SetDiscover(func() ([]worktree.NamedRepo, map[string][]string, error) {
		return nil, clash, nil
	}); err != nil {
		t.Fatal(err)
	}
	err := m.requireRepo("a")
	if err == nil || !strings.Contains(err.Error(), "/x/a, /y/a") || strings.Contains(err.Error(), "not configured") {
		t.Errorf("requireRepo(a) = %v, want the clash listing /x/a and /y/a", err)
	}
}

// TestWebFormOffersARepoClonedSinceLaunch: the web's new-card form, asked for
// after a clone was made, names that clone and lists its branches on the first
// open.
func TestWebFormOffersARepoClonedSinceLaunch(t *testing.T) {
	m, root, _ := discoveringShell(t)
	b := NewHeadless(m, tea.WithOutput(io.Discard))
	go func() { _ = b.Run() }()
	t.Cleanup(b.Stop)
	ctx := context.Background()

	clone := filepath.Join(root, "git", "d")
	initTestRepo(t, clone)
	if out, err := exec.CommandContext(context.Background(), "git", "-C", clone, "branch", "feat").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	form, err := b.Form(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(form.Repos, "d") {
		t.Fatalf("form repos = %v, want d on the first open", form.Repos)
	}
	if !slices.Contains(form.Branches, "feat") {
		t.Errorf("form branches for d = %v, want feat on the first open", form.Branches)
	}
}
