package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// depsFixture builds a temp repo with two todo cards (FD-001 "Payments",
// FD-002 "Retries"), chdirs the test into the repo root where the runDeps*
// commands expect to run, and returns the store so tests can assert edges
// directly. Cards sit at todo (not at/past coding) so the store's
// late-attachment guard does not reject new edges.
func depsFixture(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
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
	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Now()
	for i, title := range []string{"Payments", "Retries"} {
		id, _ := domain.NewFeatureID(i + 1)
		slug, _ := domain.Slugify(title)
		f := domain.Feature{
			ID: id, Num: i + 1, Kind: domain.KindFeature, Title: title, Slug: slug,
			Stage: domain.StageTodo, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.CreateFeature(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// resolveDepsID accepts a canonical id, a title, or a slug, and falls back
// to the first match in number order on duplicate titles.
func TestResolveDepsID(t *testing.T) {
	store := depsFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		arg  string
		want domain.FeatureID
	}{
		{"FD-002", "FD-002"},  // canonical id
		{"Retries", "FD-002"}, // title
		{"retries", "FD-002"}, // slug
	} {
		id, err := resolveDepsID(ctx, store, tc.arg)
		if err != nil || id != tc.want {
			t.Errorf("resolveDepsID(%q) = %s, %v; want %s", tc.arg, id, err, tc.want)
		}
	}
	// a second card with a duplicate title resolves to the first (lowest number).
	dup := domain.Feature{
		ID: "FD-003", Num: 3, Kind: domain.KindFeature, Title: "Retries",
		Slug: "retries", Stage: domain.StageTodo, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateFeature(ctx, &dup); err != nil {
		t.Fatal(err)
	}
	if id, err := resolveDepsID(ctx, store, "Retries"); err != nil || id != "FD-002" {
		t.Errorf("duplicate title resolved to %s, %v; want first FD-002", id, err)
	}
	// an arg that is neither an id nor a title/slug errors.
	if _, err := resolveDepsID(ctx, store, "nope"); err == nil {
		t.Error("resolved an arg that names nothing")
	}
}

// deps add/rm/list round-trip against real cards: add records the forward
// edge, list prints it, rm removes it, and removing a missing edge is a
// no-op that exits zero.
func TestDepsAddRmListRoundTrip(t *testing.T) {
	store := depsFixture(t)
	ctx := context.Background()

	if err := runDepsAdd([]string{"FD-002", "FD-001"}); err != nil {
		t.Fatalf("runDepsAdd: %v", err)
	}
	deps, err := store.ListDependencies(ctx, "FD-002")
	if err != nil || len(deps) != 1 || deps[0] != "FD-001" {
		t.Fatalf("after add: deps=%v err=%v, want [FD-001]", deps, err)
	}
	out := captureStdout(t, func() {
		if err := runDepsList([]string{"FD-002"}); err != nil {
			t.Fatalf("runDepsList: %v", err)
		}
	})
	if !strings.Contains(out, "FD-001") {
		t.Errorf("deps list output missing the forward edge:\n%s", out)
	}

	if err := runDepsRm([]string{"FD-002", "FD-001"}); err != nil {
		t.Fatalf("runDepsRm: %v", err)
	}
	if deps, _ := store.ListDependencies(ctx, "FD-002"); len(deps) != 0 {
		t.Fatalf("after rm: deps=%v, want none", deps)
	}
	// removing an edge that does not exist is an idempotent no-op (exit 0).
	if err := runDepsRm([]string{"FD-002", "FD-001"}); err != nil {
		t.Fatalf("runDepsRm(missing): %v", err)
	}
}

// deps add surfaces the store's typed error verbatim and fails — the CLI
// re-derives no dependency policy.
func TestDepsAddSurfacesStoreError(t *testing.T) {
	depsFixture(t)
	if err := runDepsAdd([]string{"FD-001", "FD-001"}); err == nil {
		t.Fatal("self-dependency add accepted")
	} else if !strings.Contains(err.Error(), "self-dependency") {
		t.Fatalf("error = %q, want it to carry the store's self-dependency text", err)
	}
}
