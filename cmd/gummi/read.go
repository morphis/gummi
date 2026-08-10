package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// The read-only commands — status, spec, diff — inspect a feature without
// driving it. They open the store + worktree manager only (no engine, no
// agent) and, deliberately, take NO exclusive .gummi lock: the lock is
// exclusive-only, so acquiring it would block on a live `gummi run`/TUI —
// the opposite of what a status check is for. Concurrent reads are safe
// because the store is WAL (readers never block a writer) and the git
// queries are read-only; a diff captured mid-write is a fine snapshot and
// never corrupts anything.

// withReadWorkspace wires an existing workspace, store, and worktree
// manager for a read-only command, then hands them to fn. It requires the
// .gummi workspace to already exist (state.Open, not Init — a read has
// nothing to scaffold) and binds the worktree manager directly, skipping
// newManager's EnsureGummiExcluded so a read never mutates the git index.
func withReadWorkspace(fn func(context.Context, *state.Store, *worktree.Manager, state.Workspace) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := state.Open(cwd)
	if err != nil {
		return err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	wt, err := worktree.NewManager(context.Background(), cwd, store)
	if err != nil {
		return err
	}
	return fn(context.Background(), store, wt, ws)
}

// resolveFeatureID loads the work item named by arg, which is either a
// canonical id (FD-NNN/BG-NNN, the primary handle) or an external
// correlation ref persisted at creation (--ref, D11). FD-NNN stays primary:
// a well-formed id is looked up directly; anything else is tried as a ref.
func resolveFeatureID(ctx context.Context, store *state.Store, arg string) (domain.Feature, error) {
	if id, err := domain.ParseFeatureID(arg); err == nil {
		return store.GetFeature(ctx, id)
	}
	f, err := store.FeatureByExternalRef(ctx, arg)
	if err != nil {
		return domain.Feature{}, fmt.Errorf("no work item %q (not an FD-NNN/BG-NNN id, and no feature carries it as --ref): %w", arg, err)
	}
	return f, nil
}

// idFirstArg parses an id-first command line (`status FD-042 --json`),
// pulling a leading non-flag id out before flag parsing (Go's flag package
// stops at the first positional) and tolerating a trailing one as a
// flags-first fallback. Shared by resume/status/spec/diff so they accept
// the same grammar.
func idFirstArg(fs *flag.FlagSet, args []string) (string, error) {
	var idArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		idArg, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if idArg == "" {
		idArg = fs.Arg(0)
	}
	if idArg == "" || fs.NArg() > 1 {
		fs.Usage()
		return "", fmt.Errorf("%s needs exactly one work-item id", fs.Name())
	}
	return idArg, nil
}

// artifactPath resolves where an item's design artifact lives right now,
// using the same precedence and helper the engine does (spec.LocateArtifact)
// so the read commands and the gate floor can never disagree.
func artifactPath(wt *worktree.Manager, ws state.Workspace, f *domain.Feature) string {
	root := wt.Root()
	return spec.LocateArtifact(
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(ws.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
	)
}

// gateBlockers reports the open %%-thread and diff-annotation counts that
// would block advancing f's current gate — the same two floor checks
// engine.GateBlockers applies, replicated here because the read commands
// run agent-free (no engine to build). A missing/unreadable artifact or a
// store error reads as zero, exactly as the engine degrades.
func gateBlockers(ctx context.Context, store *state.Store, wt *worktree.Manager, ws state.Workspace, f *domain.Feature) (specOpen, diffOpen int) {
	if p := artifactPath(wt, ws, f); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			specOpen = len(spec.Parse(string(raw)).UserOpenThreads())
		}
	}
	if anns, err := store.ListDiffAnnotations(ctx, f.ID); err == nil {
		for _, a := range anns {
			if !a.Resolved {
				diffOpen++
			}
		}
	}
	return specOpen, diffOpen
}
