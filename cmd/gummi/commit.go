package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// commitFlagValues holds the flag pointer `gummi commit` binds.
// registerCommitFlags is the single registration site: runCommit reads it,
// cobra binds the same surface, and the skill's command-grammar generator
// enumerates it.
type commitFlagValues struct {
	message *string
}

// registerCommitFlags binds `gummi commit`'s flags onto fs and returns their
// pointers. A single -m/--message flag carries the commit message: inline
// text, or the sentinel "-" to read it from stdin.
func registerCommitFlags(fs *flag.FlagSet) *commitFlagValues {
	msg := fs.String("m", "", "commit message for the card's uncommitted worktree changes (required; - reads from stdin)")
	fs.StringVar(msg, "message", "", "long form of -m")
	return &commitFlagValues{message: msg}
}

// runCommit implements `gummi commit <id|ref> -m <message|->`: it commits
// exactly the target card's own uncommitted worktree changes onto the
// card's own branch, using the caller-supplied message. It never touches a
// linked PR, a remote, or main, and never transitions the card's stage —
// available on any card regardless of PR-linked status or stage. It
// composes with `gummi squash` to replace the raw-git "commit stray changes,
// then collapse" workaround a PR-linked card with a dirty worktree otherwise
// requires. A clean worktree is a no-op, reported as such, not an error.
func runCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	cv := registerCommitFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi commit <id|ref> -m <message|->")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	message := *cv.message
	if message == "" {
		return fmt.Errorf("commit needs a commit message: pass -m <message> (or -m - to read one from stdin)")
	}
	if message == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading commit message from stdin: %w", err)
		}
		message = string(b)
	}
	return withLandingWorkspace(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace, pool *worktree.Pool) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()

		beforeSHA, err := pool.Head(ctx, &f)
		if err != nil {
			return driver.Outcome{}, err
		}
		out, err := d.Commit(ctx, f.ID, message)
		if err != nil {
			return out, err
		}
		afterSHA, err := pool.Head(ctx, &f)
		if err != nil {
			return out, err
		}
		if afterSHA == beforeSHA {
			fmt.Printf("%s clean, nothing to commit\n", f.ID)
			return out, nil
		}
		fmt.Printf("%s committed %s\n", f.ID, afterSHA)
		return out, nil
	})
}
