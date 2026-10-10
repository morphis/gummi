package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/branchlog"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// runLog implements `gummi log <id|ref> [--json]`: the card's own commits,
// oldest first, as the board's log tab reads them (branchlog). Read-only:
// it holds no lock and moves nothing.
func runLog(fl cliFlags, args []string) error {
	idArg, err := oneID("log", args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, pool *worktree.Pool, _ state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		// no session runs in this process; a board driving the card holds
		// its lock, which a rewrite (not a read) is refused by
		l, err := branchlog.Env{Store: store, Pool: pool}.Read(ctx, f, false)
		if err != nil {
			return err
		}
		if fl.Bool("json") {
			b, err := json.MarshalIndent(ui.WebLog(cliBase(ctx, pool, f), l), "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, string(b))
			return err
		}
		printLog(os.Stdout, f, l)
		return nil
	})
}

// cliBase names the branch the card forks from, for the reader: its
// chosen base, or its repository's trunk.
func cliBase(ctx context.Context, pool *worktree.Pool, f domain.Feature) string {
	if f.Base != "" && (f.StackID == "" || f.StackPos == 0) {
		return f.Base
	}
	return pool.BaseBranch(ctx, f.Repo)
}

func printLog(w io.Writer, _ domain.Feature, l branchlog.Log) {
	for _, r := range l.Rows {
		var tags []string
		if r.Checkpoint {
			tags = append(tags, "checkpoint")
		}
		if r.Pushed {
			tags = append(tags, "pushed")
		}
		if r.Signed {
			tags = append(tags, "signed")
		}
		if r.Warning != "" {
			tags = append(tags, "attribution")
		}
		line := r.Short + "  " + clean(r.Subject)
		if len(tags) > 0 {
			line += "  [" + strings.Join(tags, ", ") + "]"
		}
		_, _ = fmt.Fprintf(w, "%s\n    %s · %s · %d file%s +%d -%d\n", line,
			clean(r.Author), r.At.Local().Format("2006-01-02 15:04"), r.Files, cardPlural(r.Files), r.Add, r.Del)
	}
	switch {
	case l.Why != "" && len(l.Rows) == 0:
		_, _ = fmt.Fprintln(w, l.Why)
	case l.Why != "":
		_, _ = fmt.Fprintln(w, "read-only: "+l.Why)
	}
}

// runRewrite implements `gummi rewrite <id|ref> --plan <file|->`: the
// card's commits rewritten to a plan — contiguous runs of them, each
// becoming one commit with the message given — under the card's lock.
// The plan is the shape the web page sends (webapi.RewriteRequest), with
// commits named by the SHAs (or 7+ character prefixes) `gummi log`
// prints. The branch's content cannot change: reordering and dropping are
// not expressible. gummi never pushes; a rewrite of pushed commits needs
// --allow-pushed and prints the push to run.
//
// --sign makes again every commit from the first unsigned one up, signed,
// and needs no plan of its own: without one every commit keeps its
// message and only the signatures are new.
func runRewrite(fl cliFlags, args []string) error {
	idArg, err := oneID("rewrite", args)
	if err != nil {
		return err
	}
	sign := fl.Bool("sign")
	var req webapi.RewriteRequest
	if !sign || fl.String("plan") != "" {
		if req, err = readRewritePlan(fl.String("plan")); err != nil {
			return err
		}
	}
	plan := worktree.RewritePlan{Head: req.Head, Sign: sign || req.Sign}
	for _, g := range req.Groups {
		plan.Groups = append(plan.Groups, worktree.RewriteGroup{Commits: g.Commits, Message: g.Message})
	}
	allowPushed := fl.Bool("allow-pushed") || req.AcknowledgePushed
	return withWriteWorkspace(func(ctx context.Context, store *state.Store, pool *worktree.Pool, ws state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return err
		}
		defer release()
		env := branchlog.Env{Store: store, Pool: pool}
		if len(plan.Groups) == 0 {
			// --sign alone: the branch as it reads now
			l, err := env.Read(ctx, f, false)
			if err != nil {
				return err
			}
			plan.Groups = branchlog.PlanGroups(l.Rows, nil, nil)
			if n := len(l.Rows); n > 0 {
				plan.Head = l.Rows[n-1].SHA
			}
		}
		if fl.Bool("dry-run") {
			prev, err := env.Plan(ctx, f, false, plan)
			if err != nil {
				return err
			}
			if prev.Noop {
				fmt.Printf("%s: the plan leaves the branch as it is\n", f.ID)
				return nil
			}
			printLog(os.Stdout, f, branchlog.Log{Rows: branchlog.Rows(prev.Entries)})
			fmt.Printf("%d commit%s would change · content unchanged\n", prev.Changed, cardPlural(prev.Changed))
			if prev.Pushed {
				fmt.Println("replaces pushed commits: needs --allow-pushed, then a force push")
			}
			return nil
		}
		tip, push, err := env.Apply(ctx, f, false, plan, allowPushed)
		if errors.Is(err, worktree.ErrPushedNotAcknowledged) {
			return fmt.Errorf("%w; re-run with --allow-pushed", err)
		}
		if err != nil {
			return err
		}
		if tip == "" {
			fmt.Printf("%s: the plan leaves the branch as it is\n", f.ID)
			return nil
		}
		fmt.Printf("%s history rewritten to %s — content unchanged\n", f.ID, tip)
		if push != "" {
			fmt.Println("  " + push)
		}
		return nil
	})
}

// readRewritePlan reads the --plan JSON from a file, or stdin for "-".
func readRewritePlan(src string) (webapi.RewriteRequest, error) {
	var req webapi.RewriteRequest
	if src == "" {
		return req, errors.New("rewrite needs a plan: --plan <file> (or --plan - for stdin), or --sign alone; see `gummi log <id> --json` for the commits it names")
	}
	var (
		raw []byte
		err error
	)
	if src == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(src)
	}
	if err != nil {
		return req, fmt.Errorf("reading the plan: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("the plan is not a rewrite plan: %w", err)
	}
	if len(req.Groups) == 0 {
		return req, errors.New("the plan has no groups: list every commit, oldest first, each in exactly one group")
	}
	return req, nil
}

// withWriteWorkspace is withReadWorkspace for a verb that changes a card's
// branch without driving it: the workspace must exist, and the pool runs
// the .gummi exclusion pass, as every mutating command's does.
func withWriteWorkspace(fn func(context.Context, *state.Store, *worktree.Pool, state.Workspace) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := state.Open(wsRoot, defaultRoot)
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
	return fn(context.Background(), store, pool, ws)
}
