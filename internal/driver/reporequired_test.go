package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// reposOnlyDriver builds a driver over a workspace configured the way
// `repos:` with no `repo:` resolves it: two named repositories and **no
// default root at all**. This is the shape config.ResolveRepos produces,
// and the one the pool cannot satisfy an empty repo name from.
func reposOnlyDriver(t *testing.T, opts Options) (*Driver, state.Workspace, *state.Store) {
	t.Helper()
	wsRoot := t.TempDir()
	wsRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	var named []worktree.NamedRepo
	for _, name := range []string{"a", "b"} {
		r := filepath.Join(wsRoot, "git", name)
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) {
			t.Helper()
			if out, err := exec.CommandContext(context.Background(), "git",
				append([]string{"-C", r}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q", "-b", "main")
		git("config", "user.name", "t")
		git("config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(r, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", "init")
		named = append(named, worktree.NamedRepo{Name: name, Root: r})
	}
	ws, err := state.Init(wsRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	// defaultRoot "" is the whole point: a repos:-only workspace.
	pool, err := worktree.NewPool(context.Background(), ws.Root, "", named, store, false)
	if err != nil {
		t.Fatal(err)
	}
	fake := agent.NewFake("")
	eng := engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": fake, fake.Name(): fake},
		Store:  store, Pool: pool, Workspace: ws, Model: "test-model",
	})
	t.Cleanup(func() { eng.Close(); fake.Close() })
	return New(eng, store, ws, os.Stderr, opts), ws, store
}

// TestCreateFeatureRequiresRepoWhenNoDefault: with `repos:` configured and
// no default, `gummi run` without --repo must be refused outright. It used
// to be let through — the guard only checked a *named* repo — so the card
// was minted against the empty name and only fell over later at worktree
// creation, with its FD number already spent.
func TestCreateFeatureRequiresRepoWhenNoDefault(t *testing.T) {
	d, ws, _ := reposOnlyDriver(t, Options{})

	seqBefore, err := os.ReadFile(ws.SeqFile())
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.createFeature(context.Background(), domain.KindFeature, "a card with nowhere to live")
	if err == nil {
		t.Fatal("createFeature succeeded with no repo named and no default configured")
	}
	if !strings.Contains(err.Error(), "no default repository configured") {
		t.Errorf("error = %q, want it to say there is no default", err)
	}

	seqAfter, err := os.ReadFile(ws.SeqFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(seqAfter) != string(seqBefore) {
		t.Errorf("the refusal burned a feature number: seq %q -> %q", seqBefore, seqAfter)
	}
}

// TestCreateFeatureAcceptsNamedRepo: naming one of the configured repos is
// all it takes — the refusal above is about the *absence* of a choice.
func TestCreateFeatureAcceptsNamedRepo(t *testing.T) {
	d, _, _ := reposOnlyDriver(t, Options{Repo: "b"})

	f, err := d.createFeature(context.Background(), domain.KindFeature, "a card that names its repo")
	if err != nil {
		t.Fatalf("createFeature with --repo b: %v", err)
	}
	if f.Repo != "b" {
		t.Errorf("created repo = %q, want b", f.Repo)
	}
}

// TestCreateFeatureRejectsUnknownRepoWithoutPromisingADefault: the typo
// path still fails before minting, and in a workspace with no default it
// must not advise omitting --repo — that advice leads straight into the
// "no default repository configured" error.
func TestCreateFeatureRejectsUnknownRepoWithoutPromisingADefault(t *testing.T) {
	d, _, _ := reposOnlyDriver(t, Options{Repo: "nope"})

	_, err := d.createFeature(context.Background(), domain.KindFeature, "a card in a repo that isn't there")
	if err == nil {
		t.Fatal("createFeature succeeded with an unconfigured repo")
	}
	if !strings.Contains(err.Error(), `"nope" is not configured`) {
		t.Errorf("error = %q, want it to name the unknown repo", err)
	}
	if strings.Contains(err.Error(), "omit --repo") {
		t.Errorf("error advises omitting --repo in a workspace with no default: %q", err)
	}
}
