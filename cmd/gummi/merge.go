package main

import (
	"context"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// runMerge implements `gummi merge <id|ref> -m <message|->`: the headless
// landing verb. It requires the card to be at a verified branch, takes the
// commit message explicitly from the caller (never drafts one), validates it,
// squash-merges the branch onto main (or, with --no-squash, merges it as a
// merge commit that keeps the branch's commits), and moves the card to done —
// streaming a `merged` NDJSON event (with the landed commit sha and method)
// and exiting 0 on success. A missing, malformed, or unverified precondition
// fails loudly with a non-zero exit before any git mutation.
func runMerge(fl cliFlags, args []string) error {
	idArg, err := oneID("merge", args)
	if err != nil {
		return err
	}
	// a goal lands as a merge commit gummi writes from the goal and its
	// cards, so -m is optional for one
	isGoal := strings.HasPrefix(strings.ToUpper(idArg), "GL-")
	message, err := commitMessage(fl, "merge", !isGoal)
	if err != nil {
		return err
	}
	method := domain.LandSquash
	if fl.Bool("no-squash") {
		method = domain.LandMerge
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace, _ *worktree.Pool) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		return d.Merge(ctx, f.ID, message, method)
	})
}

// runClean implements `gummi clean <id|ref>`: the headless cleanup verb. It
// removes a landed card's worktree and branch (keeping the card record),
// streaming a `cleaned` NDJSON event and exiting 0 on success. It refuses
// anything that has not actually landed, or that carries tracked-dirty rework.
func runClean(args []string) error {
	idArg, err := oneID("clean", args)
	if err != nil {
		return err
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace, _ *worktree.Pool) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		state.ReapOrphanAgent(ws, f.ID)
		return d.Clean(ctx, f.ID)
	})
}

// runHandOff implements `gummi handoff <id|ref>`: the headless counterpart of
// the TUI's h key. It ends a verified card WITHOUT landing it — the branch
// stays exactly where it is, the card moves to done, and the caller owns
// whatever happens to the branch next. It refuses anything that is not at a
// verified branch, and every gate floor that holds a landing holds a hand-off
// too: waiving the merge never waived the quality bar.
func runHandOff(args []string) error {
	idArg, err := oneID("handoff", args)
	if err != nil {
		return err
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace, _ *worktree.Pool) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		return d.HandOff(ctx, f.ID)
	})
}

// withLandingWorkspace wires the workspace, store, worktree manager, and a
// minimal driver for the headless landing verbs (merge, handoff, clean), then
// hands it to fn and maps the Outcome to a process exit. It mirrors
// withRunEngine but deliberately starts no agent: none of them run a
// session — they only touch the workspace, store, and worktree manager.
// Each fn resolves its card and holds that card's per-card lock, so landing
// one card never races a drive or another landing of a different card. The driver still needs an engine object for its
// gate-floor checks, so one is built with no agents — the engine is only
// ever read from here, never run.
func withLandingWorkspace(fn func(context.Context, *driver.Driver, *state.Store, state.Workspace, *worktree.Pool) (driver.Outcome, error)) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		return err
	}
	hookd := wireHooks(store, pool, ws)
	defer hookd.Close()
	// no agents: the driver's Merge/HandOff/Clean never run a session.
	eng := engine.New(engine.Config{Store: store, Pool: pool, Workspace: ws})
	defer func() { _ = eng.Close() }()

	d := driver.New(eng, store, ws, os.Stdout, driver.Options{})
	out, derr := fn(context.Background(), d, store, ws, pool)
	if out.Status == "" && derr != nil {
		// the closure failed before the driver produced an outcome (e.g. an
		// unknown id/ref) — a plain setup/usage error to stderr, exit 1.
		return derr
	}
	return driverExit(out, derr)
}
