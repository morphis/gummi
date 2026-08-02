package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// runDiff implements `gummi diff <id|ref>` (DESIGN §3): a read-only dump of
// the feature's worktree diff against main. It drives nothing and holds no
// lock. Before a worktree exists (the item is still in a design stage), the
// manager reports that clearly.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi diff <id|ref>")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, wt *worktree.Manager, ws state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		out, err := wt.Diff(ctx, &f)
		if err != nil {
			return err
		}
		_, err = os.Stdout.WriteString(out)
		return err
	})
}
