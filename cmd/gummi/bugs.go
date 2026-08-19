package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// bugEnv bundles the store/engine wiring bug commands share.
type bugEnv struct {
	eng     *engine.Engine
	profile string
	env     int
	cleanup func()
}

// openBugEnv sets up the workspace, store, worktree manager, and engine,
// resolving the default profile and credit envelope the same way feature
// ingestion does.
func openBugEnv(profile string, envelope int) (*bugEnv, error) {
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
	// Bug ingestion and materialization are deterministic (gh + the store),
	// so they don't need a coding agent — construct a bare engine when none
	// is configured. Running the bugs later needs an agent; creating them
	// does not.
	eng, agents, names := newEngineFromEnv(store, pool, ws)
	if eng == nil {
		eng = engine.New(engine.Config{Store: store, Pool: pool, Workspace: ws})
	}
	prof := profile
	if prof == "" && len(names) > 0 {
		prof = names[0]
	}
	env := envelope
	if env == 0 {
		if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				env = n
			}
		}
	}
	return &bugEnv{
		eng: eng, profile: prof, env: env,
		cleanup: func() {
			_ = eng.Close()
			closeAgents(agents)
			_ = store.Close()
		},
	}, nil
}

// closeAgents closes every distinct agent in the map exactly once (the
// "" default alias points at one of the concrete-name entries).
func closeAgents(agents map[string]agent.Agent) {
	seen := map[agent.Agent]struct{}{}
	for _, a := range agents {
		if a == nil {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		_ = a.Close()
	}
}

// runBugIngest implements `gummi bugs ingest`: pull open issues from a
// GitHub repo (default: this repo's origin remote), print them, and —
// after confirmation — materialize the fresh ones into the todo backlog.
func runBugIngest(args []string) error {
	fs := flag.NewFlagSet("bugs ingest", flag.ContinueOnError)
	repo := fs.String("repo", "", "owner/repo to import from (default: this repo's origin remote)")
	label := fs.String("label", "bug", "issue label filter (\"\" imports all issues)")
	stateFilter := fs.String("state", "open", "issue state: open|closed|all")
	profile := fs.String("profile", "", "profile the new bugs adopt (default: first configured)")
	envelope := fs.Int("envelope", 0, "credit envelope per bug (0 = none; falls back to GUMMI_ENVELOPE)")
	yes := fs.Bool("yes", false, "materialize without the confirmation prompt")
	comments := fs.Bool("comments", false, "fetch issue comments into the report's Discussion section")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi bugs ingest [--repo owner/repo] [--label bug] [--state open] [--profile p] [--envelope n] [--comments] [--yes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	be, err := openBugEnv(*profile, *envelope)
	if err != nil {
		return err
	}
	defer be.cleanup()

	cwd, _ := os.Getwd()
	src := ingestGitHubSource(*repo, *label, *stateFilter, *comments, cwd)
	ctx := context.Background()
	target := *repo
	if target == "" {
		target = "origin"
	}
	fmt.Printf("Importing GitHub issues from %s (label %q, state %s) …\n", target, *label, *stateFilter)
	res, err := be.eng.IngestBugs(ctx, src)
	if err != nil {
		return err
	}
	renderBugProposals(os.Stdout, res)
	if len(res.Proposals) == 0 {
		fmt.Println("Nothing new to import.")
		return nil
	}

	if !*yes {
		if !confirm(os.Stdin, os.Stdout, fmt.Sprintf("Materialize %d bug(s) into todo?", len(res.Proposals))) {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}
	return materializeBugs(ctx, be, res.Proposals, "")
}

// ingestGitHubSource builds the GitHub source from parsed ingest flags.
// Kept as a small helper so the flag-to-source mapping is testable without
// standing up a workspace and shelling out to gh.
func ingestGitHubSource(repo, label, state string, comments bool, dir string) engine.GitHubSource {
	return engine.GitHubSource{
		Repo:          repo,
		Label:         label,
		State:         state,
		Dir:           dir,
		FetchComments: comments,
	}
}

// bugNewFlagValues holds the pointers registerBugsNewFlags binds, so
// runBugNew and the cobra adapter share one flag grammar.
type bugNewFlagValues struct {
	title, oneLiner, severity, repro, expected, actual, env, desc *string
	profile, repo                                                 *string
	envelope                                                      *int
	yes                                                           *bool
}

// registerBugsNewFlags binds `gummi bugs new`'s flags onto fs and returns
// their pointers. It defines the flags only — parsing and validation stay in
// runBugNew — so a throwaway FlagSet can be handed here purely to enumerate
// the grammar (and the cobra adapter stays in lockstep with it).
func registerBugsNewFlags(fs *flag.FlagSet) *bugNewFlagValues {
	return &bugNewFlagValues{
		title:    fs.String("title", "", "bug title (required)"),
		oneLiner: fs.String("one-liner", "", "short one-line summary"),
		severity: fs.String("severity", "", "severity: critical|high|medium|low"),
		repro:    fs.String("repro", "", "reproduction steps"),
		expected: fs.String("expected", "", "expected behavior"),
		actual:   fs.String("actual", "", "actual behavior"),
		env:      fs.String("env", "", "environment (versions, OS, config)"),
		desc:     fs.String("desc", "", "summary of what's broken"),
		profile:  fs.String("profile", "", "profile the bug adopts (default: first configured)"),
		envelope: fs.Int("envelope", 0, "credit envelope (0 = none; falls back to GUMMI_ENVELOPE)"),
		repo:     fs.String("repo", "", "managed repository to create the bug in (a configured `repos:` name; default: the workspace default repo)"),
		yes:      fs.Bool("yes", false, "create without the confirmation prompt"),
	}
}

// runBugNew implements `gummi bugs new`: one hand-entered bug straight
// into the todo backlog with a seeded report.
func runBugNew(args []string) error {
	fs := flag.NewFlagSet("bugs new", flag.ContinueOnError)
	f := registerBugsNewFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi bugs new --title T [--severity S] [--repro …] [--expected …] [--actual …] [--env …] [--desc …] [--profile p] [--repo r] [--envelope n] [--yes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*f.title) == "" {
		fs.Usage()
		return fmt.Errorf("bugs new needs a --title")
	}

	be, err := openBugEnv(*f.profile, *f.envelope)
	if err != nil {
		return err
	}
	defer be.cleanup()

	prop := domain.BugProposal{
		Title:    strings.TrimSpace(*f.title),
		OneLiner: strings.TrimSpace(*f.oneLiner),
		Source:   "manual",
		Severity: domain.NormalizeSeverity(*f.severity),
		Report: domain.BugReport{
			Description:  strings.TrimSpace(*f.desc),
			Reproduction: strings.TrimSpace(*f.repro),
			Expected:     strings.TrimSpace(*f.expected),
			Actual:       strings.TrimSpace(*f.actual),
			Environment:  strings.TrimSpace(*f.env),
		},
	}
	ctx := context.Background()
	res, err := be.eng.IngestBugs(ctx, engine.ManualSource{Bug: prop})
	if err != nil {
		return err
	}
	renderBugProposals(os.Stdout, res)
	if !*f.yes {
		if !confirm(os.Stdin, os.Stdout, "Create this bug in todo?") {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}
	return materializeBugs(ctx, be, res.Proposals, *f.repo)
}

// materializeBugs mints the proposals and prints what was created.
func materializeBugs(ctx context.Context, be *bugEnv, props []domain.BugProposal, repo string) error {
	created, err := be.eng.MaterializeBugs(ctx, props, engine.MaterializeOpts{Profile: be.profile, Envelope: be.env, Repo: repo})
	for _, f := range created {
		fmt.Printf("  %s  %s\n", f.ID, clean(f.Title))
	}
	if err != nil {
		return fmt.Errorf("materialize (created %d before failing): %w", len(created), err)
	}
	if len(created) > 0 {
		fmt.Printf("Created %d bug(s) in todo. Open gummi to run them.\n", len(created))
	}
	return nil
}

// renderBugProposals prints the fresh proposals and a note of any skipped
// (already-imported) ones, so a re-ingest shows what it already had.
func renderBugProposals(w io.Writer, res engine.BugIngestResult) {
	fmt.Fprintf(w, "\nProposed %d bug(s):\n", len(res.Proposals))
	for i, p := range res.Proposals {
		fmt.Fprintf(w, "\n  %2d. %s\n", i+1, clean(p.Title))
		if p.OneLiner != "" {
			fmt.Fprintf(w, "      %s\n", clean(p.OneLiner))
		}
		var tags []string
		if p.Severity != "" {
			tags = append(tags, "severity "+string(p.Severity))
		}
		if p.ExternalRef != "" {
			tags = append(tags, clean(p.ExternalRef))
		}
		if len(tags) > 0 {
			fmt.Fprintf(w, "      [%s]\n", strings.Join(tags, " · "))
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(w, "\nSkipped %d already on the board:\n", len(res.Skipped))
		for _, s := range res.Skipped {
			fmt.Fprintf(w, "  → %s  %s\n", s.LocalID, clean(s.Proposal.Title))
		}
	}
	fmt.Fprintln(w)
}
