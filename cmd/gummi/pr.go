package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// prEnv bundles the store/worktree wiring the `gummi pr` verbs share —
// mirroring openDepsEnv's headless wiring (deps.go). Linking/unlinking/
// status are deterministic store + gh operations, so no engine or agent is
// constructed.
type prEnv struct {
	store   *state.Store
	wt      *worktree.Pool
	cleanup func()
}

func openPREnv() (*prEnv, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return nil, err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return nil, err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, err
	}
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &prEnv{
		store: store, wt: pool,
		cleanup: func() { _ = store.Close() },
	}, nil
}

// repoDir resolves the directory gh should run from for f's own configured
// repo — used so gh can auto-detect "owner/repo" from that directory's git
// remote for the bare-number and --auto resolution forms (see
// internal/pr.Resolve).
func (pe *prEnv) repoDir(ctx context.Context, f *domain.Feature) (string, error) {
	mgr, err := pe.wt.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return mgr.RepoRoot(), nil
}

// runPRLink implements `gummi pr link <card> <url|number> [--auto]`:
// preflight gh, refuse a card that is already linked, resolve the PR
// (by URL, by number against the card's own repo, or by --auto matching the
// card's branch), and persist it. Prints the linked repo#number, URL, and
// head SHA on success.
func runPRLink(args []string) error {
	fs := flag.NewFlagSet("pr link", flag.ContinueOnError)
	auto := fs.Bool("auto", false, "resolve the PR whose head branch matches the card's branch (via gh pr list --head)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi pr link <card> <url|number> [--auto]")
		fs.PrintDefaults()
	}
	// pull a leading non-flag id out first (mirroring idFirstArg in
	// read.go), since this grammar has a second, optional positional
	// (<url|number>) that idFirstArg's fixed one-positional shape can't
	// express; a card-name-first line ("FD-001 --auto") and a
	// flags-reconstructed one ("--auto FD-001", what buildFlagArgs emits)
	// both need to resolve to the same id + spec.
	var idArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		idArg, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if idArg == "" && len(rest) > 0 {
		idArg, rest = rest[0], rest[1:]
	}
	var spec string
	switch {
	case idArg == "":
		fs.Usage()
		return fmt.Errorf("pr link needs a card and either <url|number> or --auto")
	case *auto && len(rest) == 0:
		spec = ""
	case !*auto && len(rest) == 1:
		spec = rest[0]
	default:
		fs.Usage()
		return fmt.Errorf("pr link needs a card and either <url|number> or --auto")
	}

	ghBinary := pr.GHBinary()
	if err := pr.Available(ghBinary); err != nil {
		return err
	}

	pe, err := openPREnv()
	if err != nil {
		return err
	}
	defer pe.cleanup()

	ctx := context.Background()
	f, err := resolveFeatureID(ctx, pe.store, idArg)
	if err != nil {
		return err
	}
	if !f.PullRequest.Empty() {
		return fmt.Errorf("%s is already linked to %s#%d (%s); unlink it first with `gummi pr unlink %s`",
			f.ID, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, f.ID)
	}
	dir, err := pe.repoDir(ctx, &f)
	if err != nil {
		return err
	}
	ref, err := pr.Resolve(ctx, ghBinary, spec, dir, f.BranchName())
	if err != nil {
		return fmt.Errorf("resolving PR for %s: %w", f.ID, err)
	}
	if err := pe.store.SetPullRequest(ctx, f.ID, ref); err != nil {
		return err
	}
	fmt.Printf("%s linked to %s#%d\n  %s\n  head %s\n", f.ID, ref.Repo, ref.Number, ref.URL, ref.HeadSHA)
	return nil
}

// runPRUnlink implements `gummi pr unlink <card>`: clears a linked PR,
// refusing when the card carries none.
func runPRUnlink(args []string) error {
	fs := flag.NewFlagSet("pr unlink", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi pr unlink <card>")
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	if err := pr.Available(pr.GHBinary()); err != nil {
		return err
	}
	pe, err := openPREnv()
	if err != nil {
		return err
	}
	defer pe.cleanup()

	ctx := context.Background()
	f, err := resolveFeatureID(ctx, pe.store, idArg)
	if err != nil {
		return err
	}
	if f.PullRequest.Empty() {
		return fmt.Errorf("%s has no linked PR", f.ID)
	}
	prev := f.PullRequest
	if err := pe.store.SetPullRequest(ctx, f.ID, domain.PullRequestRef{}); err != nil {
		return err
	}
	fmt.Printf("%s unlinked from %s#%d\n", f.ID, prev.Repo, prev.Number)
	return nil
}

// prStatusView is `gummi pr status`'s payload: the live PR state and
// comment count, alongside the persisted snapshot fields.
type prStatusView struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
	HeadSHA  string `json:"head_sha"`
	State    string `json:"state"`
	Comments int    `json:"comments"`
}

// runPRStatus implements `gummi pr status <card> [--json]`: a live query of
// the linked PR's state and comment count.
func runPRStatus(args []string) error {
	fs := flag.NewFlagSet("pr status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text summary")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi pr status <card> [--json]")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}

	ghBinary := pr.GHBinary()
	if err := pr.Available(ghBinary); err != nil {
		return err
	}

	pe, err := openPREnv()
	if err != nil {
		return err
	}
	defer pe.cleanup()

	ctx := context.Background()
	f, err := resolveFeatureID(ctx, pe.store, idArg)
	if err != nil {
		return err
	}
	if f.PullRequest.Empty() {
		return fmt.Errorf("%s has no linked PR", f.ID)
	}
	state, comments, err := pr.LiveStatus(ctx, ghBinary, f.PullRequest)
	if err != nil {
		return fmt.Errorf("querying PR status for %s: %w", f.ID, err)
	}
	view := prStatusView{
		ID: string(f.ID), Repo: f.PullRequest.Repo, Number: f.PullRequest.Number,
		URL: f.PullRequest.URL, HeadSHA: f.PullRequest.HeadSHA,
		State: state, Comments: comments,
	}
	if *jsonOut {
		b, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("%s  %s#%d\n", view.ID, view.Repo, view.Number)
	fmt.Printf("  URL:      %s\n", view.URL)
	fmt.Printf("  State:    %s\n", strings.ToLower(view.State))
	fmt.Printf("  Comments: %d\n", view.Comments)
	return nil
}
