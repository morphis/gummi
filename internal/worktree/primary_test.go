package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainSnapshotIgnoresGummi(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !base.Clean() || base.Head == "" || !strings.HasSuffix(base.Branch, "/main") {
		t.Fatalf("baseline snapshot wrong: %+v", base)
	}
	// .gummi churn is machinery, not main-checkout state
	writeFile(t, root, ".gummi/seq", "7\n")
	writeFile(t, root, ".gummi/state/gummi.db", "db\n")
	after, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Equal(base) {
		t.Errorf(".gummi writes changed the snapshot: %+v vs %+v", after, base)
	}
	// anything else is
	writeFile(t, root, "stray.txt", "x\n")
	dirty, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Clean() || !strings.Contains(dirty.Status, "stray.txt") {
		t.Errorf("untracked file missing from snapshot: %+v", dirty)
	}
}

func TestRestoreMainRevertsCommitAndUntracked(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gen := m.MainGen()
	writeFile(t, root, ".gummi/state/gummi.db", "db\n")

	// the escape: a commit on main plus untracked droppings
	writeFile(t, root, "rogue.txt", "rogue\n")
	mustGit(t, root, "add", "rogue.txt")
	mustGit(t, root, "commit", "-q", "-m", "rogue")
	writeFile(t, root, "junk/dropping.txt", "junk\n")

	if chains, err := m.MainChainsFrom(ctx, base.Head); err != nil || !chains {
		t.Fatalf("escape commit should chain from the snapshot HEAD: %v %v", chains, err)
	}
	if err := m.RestoreMain(ctx, base, gen); err != nil {
		t.Fatal(err)
	}
	if head := mustGit(t, root, "rev-parse", "HEAD"); head != base.Head {
		t.Errorf("HEAD not restored: %s want %s", head, base.Head)
	}
	for _, gone := range []string{"rogue.txt", "junk"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the revert", gone)
		}
	}
	// gummi's workspace is spared by the clean
	if _, err := os.Stat(filepath.Join(root, ".gummi", "state", "gummi.db")); err != nil {
		t.Errorf(".gummi was not spared: %v", err)
	}
	if m.MainGen() == gen {
		t.Error("revert did not advance the mutation generation")
	}
}

func TestRestoreMainRefusesStaleGeneration(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gen := m.MainGen()
	m.BumpMainGen() // a sanctioned mutation happened since the snapshot
	if err := m.RestoreMain(ctx, base, gen); err == nil {
		t.Fatal("RestoreMain accepted a stale generation")
	}
}

func TestRestoreMainRefusesDirtySnapshot(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := m.MainSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap.Status = " M README.md"
	if err := m.RestoreMain(ctx, snap, m.MainGen()); err == nil {
		t.Fatal("RestoreMain accepted a dirty snapshot")
	}
}

func TestSquashMergeBumpsMainGen(t *testing.T) {
	m, f, _ := committedFeature(t, newRepo(t))
	gen := m.MainGen()
	if err := m.SquashMerge(ctx, f, "land"); err != nil {
		t.Fatal(err)
	}
	if m.MainGen() == gen {
		t.Error("a land did not advance the mutation generation")
	}
}
