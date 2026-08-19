package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// runSpec implements `gummi spec <id|ref>` (DESIGN §3): a read-only dump of
// the item's current design artifact (a feature's spec or a bug's report) as
// markdown, wherever it lives right now — its workspace home, its draft, or
// a mid-flight worktree copy. It drives nothing and holds no lock.
func runSpec(args []string) error {
	fs := flag.NewFlagSet("spec", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi spec <id|ref>")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, wt *worktree.Pool, ws state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		path := artifactPath(wt, ws, &f)
		if path == "" {
			return fmt.Errorf("%s has no spec yet — it is created when the spec/brainstorm stage first runs", f.ID)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	})
}
