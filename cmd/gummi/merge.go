package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// mergeFlagValues holds the flag pointer `gummi merge` binds. registerMergeFlags
// is the single registration site: runMerge reads it, cobra binds the same
// surface, and the skill's command-grammar generator enumerates it.
type mergeFlagValues struct {
	message *string
}

// registerMergeFlags binds `gummi merge`'s flags onto fs and returns their
// pointers. A single -m/--message flag carries the landing message: inline
// text, or the sentinel "-" to read it from stdin.
func registerMergeFlags(fs *flag.FlagSet) *mergeFlagValues {
	msg := fs.String("m", "", "landing commit message (required; - reads from stdin)")
	fs.StringVar(msg, "message", "", "long form of -m")
	return &mergeFlagValues{message: msg}
}

// runMerge implements `gummi merge <id|ref> -m <message|->`: the headless
// landing verb. It requires the card to be at a verified branch, takes the
// commit message explicitly from the caller (never drafts one), validates it,
// squash-merges the branch onto main, and moves the card to done — streaming
// a `merged` NDJSON event (with the landed commit sha) and exiting 0 on
// success. A missing, malformed, or unverified precondition fails loudly with
// a non-zero exit before any git mutation.
func runMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	mv := registerMergeFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi merge <id|ref> -m <message|->")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	message := *mv.message
	if message == "" {
		return fmt.Errorf("merge needs a commit message: pass -m <message> (or -m - to read one from stdin)")
	}
	if message == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading commit message from stdin: %w", err)
		}
		message = string(b)
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		return d.Merge(ctx, f.ID, message)
	})
}

// runClean implements `gummi clean <id|ref>`: the headless cleanup verb. It
// removes a landed card's worktree and branch (keeping the card record),
// streaming a `cleaned` NDJSON event and exiting 0 on success. It refuses
// anything that has not actually landed, or that carries tracked-dirty rework.
func runClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi clean <id|ref>")
		fmt.Fprintln(os.Stderr, "  remove a landed card's worktree and branch")
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		return d.Clean(ctx, f.ID)
	})
}

// withLandingWorkspace wires the workspace, exclusive lock, store, worktree
// manager, and a minimal driver for the headless merge/clean verbs, then
// hands it to fn and maps the Outcome to a process exit. It mirrors
// withRunEngine but deliberately starts no agent: merge/clean only touch the
// workspace, store, worktree manager, and the exclusive lock (so they cannot
// race the TUI or another run). The driver still needs an engine object for
// its gate-floor checks, so one is built with no agents — the engine is only
// ever read from here, never run.
func withLandingWorkspace(fn func(context.Context, *driver.Driver, *state.Store) (driver.Outcome, error)) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return err
	}
	release, err := state.AcquireLock(ws.LockFile())
	if err != nil {
		return err
	}
	defer release()
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	wt, err := newManager(context.Background(), cwd, store)
	if err != nil {
		return err
	}
	// no agents: the driver's Merge/Clean never run a session.
	eng := engine.New(engine.Config{Store: store, Worktrees: wt, Workspace: ws})
	defer func() { _ = eng.Close() }()

	d := driver.New(eng, store, ws, os.Stdout, driver.Options{})
	out, derr := fn(context.Background(), d, store)
	if out.Status == "" && derr != nil {
		// the closure failed before the driver produced an outcome (e.g. an
		// unknown id/ref) — a plain setup/usage error to stderr, exit 1.
		return derr
	}
	return driverExit(out, derr)
}
