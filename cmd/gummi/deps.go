package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// depsEnv bundles the store/workspace wiring the `gummi deps` commands
// share. It mirrors the headless bugs wiring: ensure the workspace, open
// the store, and bind the worktree manager. Only the store is used for the
// actual edge operations — the CLI is a thin shell over
// Add/Remove/ListDependency, never re-implementing dependency policy.
type depsEnv struct {
	store   *state.Store
	wt      *worktree.Manager
	cleanup func()
}

// openDepsEnv sets up the workspace, store, and worktree manager for a
// `gummi deps` invocation, mirroring openBugEnv's headless wiring. deps
// only reads and writes edges, so no engine or agent is constructed.
func openDepsEnv() (*depsEnv, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, err
	}
	wt, err := newManager(context.Background(), cwd, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &depsEnv{
		store: store, wt: wt,
		cleanup: func() { _ = store.Close() },
	}, nil
}

// resolveDepsID resolves a card named by arg to its ID. A canonical
// work-item id (FD-NNN/BG-NNN) is used directly; anything else is looked
// up by title or slug across the whole store, first match in number order
// (mirroring ingest's deterministic first-occurrence rule — duplicates fall
// back to the first). Errors when arg names nothing.
func resolveDepsID(ctx context.Context, store *state.Store, arg string) (domain.FeatureID, error) {
	if id, err := domain.ParseFeatureID(arg); err == nil {
		return id, nil
	}
	feats, err := store.ListFeatures(ctx)
	if err != nil {
		return "", err
	}
	for _, f := range feats {
		if f.Title == arg || f.Slug == arg {
			return f.ID, nil
		}
	}
	return "", fmt.Errorf("no card %q (not an FD-NNN/BG-NNN id, and no feature or bug carries it as a title or slug)", arg)
}

// runDepsAdd implements `gummi deps add <dependent> <depends-on>`: record
// that the first card depends on the second. It surfaces the store's typed
// errors (self-loop, cycle, late attachment, unknown card) verbatim and
// exits non-zero; a successful add prints the edge and exits zero.
func runDepsAdd(args []string) error {
	fs := flag.NewFlagSet("deps add", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi deps add <dependent> <depends-on>")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("deps add needs exactly two cards: <dependent> <depends-on>")
	}
	de, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer de.cleanup()
	ctx := context.Background()
	dep, err := resolveDepsID(ctx, de.store, fs.Arg(0))
	if err != nil {
		return err
	}
	target, err := resolveDepsID(ctx, de.store, fs.Arg(1))
	if err != nil {
		return err
	}
	if err := de.store.AddDependency(ctx, dep, target); err != nil {
		return err
	}
	fmt.Printf("  %s depends on %s\n", dep, target)
	return nil
}

// runDepsRm implements `gummi deps rm <dependent> <depends-on>`: remove
// the edge. Removing an edge that does not exist is an idempotent no-op that
// exits zero (the store owns that semantics).
func runDepsRm(args []string) error {
	fs := flag.NewFlagSet("deps rm", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi deps rm <dependent> <depends-on>")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("deps rm needs exactly two cards: <dependent> <depends-on>")
	}
	de, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer de.cleanup()
	ctx := context.Background()
	dep, err := resolveDepsID(ctx, de.store, fs.Arg(0))
	if err != nil {
		return err
	}
	target, err := resolveDepsID(ctx, de.store, fs.Arg(1))
	if err != nil {
		return err
	}
	if err := de.store.RemoveDependency(ctx, dep, target); err != nil {
		return err
	}
	fmt.Printf("  %s no longer depends on %s\n", dep, target)
	return nil
}

// runDepsList implements `gummi deps list <id>`: print each forward edge
// (what the card depends on) as one FD-NNN per line, in the store's number
// order.
func runDepsList(args []string) error {
	fs := flag.NewFlagSet("deps list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi deps list <id>")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("deps list needs exactly one card: <id>")
	}
	de, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer de.cleanup()
	ctx := context.Background()
	id, err := resolveDepsID(ctx, de.store, fs.Arg(0))
	if err != nil {
		return err
	}
	deps, err := de.store.ListDependencies(ctx, id)
	if err != nil {
		return err
	}
	for _, d := range deps {
		fmt.Println(d)
	}
	return nil
}
