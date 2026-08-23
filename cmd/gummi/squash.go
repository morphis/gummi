package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// squashFlagValues holds the flag pointers `gummi squash` binds.
// registerSquashFlags is the single registration site: runSquash reads it,
// cobra binds the same surface, and the skill's command-grammar generator
// enumerates it.
type squashFlagValues struct {
	message *string
	force   *bool
}

// registerSquashFlags binds `gummi squash`'s flags onto fs and returns their
// pointers. -m/--message carries the collapsed commit's message: inline
// text, or the sentinel "-" to read it from stdin. --force lets the caller
// proceed past a linked PR's open review threads, acknowledging that the
// follow-up force-push will outdate them.
func registerSquashFlags(fs *flag.FlagSet) *squashFlagValues {
	msg := fs.String("m", "", "collapsed commit message (required; - reads from stdin)")
	fs.StringVar(msg, "message", "", "long form of -m")
	force := fs.Bool("force", false, "proceed even if the linked PR has open review threads")
	return &squashFlagValues{message: msg, force: force}
}

// runSquash implements `gummi squash <id|ref> -m <message|->`: it collapses
// the card's branch to a single commit, in place, so any GitHub merge method
// (merge commit, rebase-merge, or the squash button) leaves main clean. It
// never contacts a remote — on success it prints the follow-up
// `git push --force-with-lease` command and exits 0, leaving publishing to
// the operator.
func runSquash(args []string) error {
	fs := flag.NewFlagSet("squash", flag.ContinueOnError)
	sv := registerSquashFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi squash <id|ref> -m <message|-> [--force]")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	message := *sv.message
	if message == "" {
		return fmt.Errorf("squash needs a commit message: pass -m <message> (or -m - to read one from stdin)")
	}
	if message == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading commit message from stdin: %w", err)
		}
		message = strings.TrimRight(string(b), "\n")
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

		if !f.PullRequest.Empty() {
			count, prURL, err := d.OpenReviewThreads(ctx, f.ID)
			if err != nil {
				return driver.Outcome{}, err
			}
			if count > 0 {
				if !*sv.force {
					return driver.Outcome{}, fmt.Errorf("refusing to collapse: %d open review threads on %s; re-run with --force to proceed", count, prURL)
				}
				fmt.Fprintf(os.Stderr, "warning: force-push will outdate %d open review threads on %s\n", count, prURL)
			}
		}

		beforeSHA, err := pool.Head(ctx, &f)
		if err != nil {
			return driver.Outcome{}, err
		}
		out, err := d.Squash(ctx, f.ID, message)
		if err != nil {
			return out, err
		}
		afterSHA, err := pool.Head(ctx, &f)
		if err != nil {
			return out, err
		}
		if afterSHA == beforeSHA {
			fmt.Printf("%s already collapsed, nothing to do\n", f.ID)
			return out, nil
		}
		fmt.Printf("%s squashed to %s\n  git push --force-with-lease origin %s\n", f.ID, afterSHA, f.BranchName())
		return out, nil
	})
}
