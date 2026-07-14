package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// runBugs implements `gummi bugs …`: bug ingestion from GitHub issues and
// manual entry (DESIGN §11, bug variant). Both funnel into the same gate
// + materialize path as feature ingestion.
func runBugs(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gummi bugs <ingest|new> [flags]")
		return fmt.Errorf("bugs needs a subcommand")
	}
	switch args[0] {
	case "ingest":
		return runBugIngest(args[1:])
	case "new":
		return runBugNew(args[1:])
	default:
		return fmt.Errorf("unknown bugs subcommand %q (want ingest|new)", args[0])
	}
}

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
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, err
	}
	wt, err := newManager(context.Background(), cwd)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	// Bug ingestion and materialization are deterministic (gh + the store),
	// so they don't need a coding agent — construct a bare engine when none
	// is configured. Running the bugs later needs an agent; creating them
	// does not.
	eng, ag, names := newEngineFromEnv(store, wt, ws)
	if eng == nil {
		eng = engine.New(engine.Config{Store: store, Worktrees: wt, Workspace: ws})
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
			if ag != nil {
				_ = ag.Close()
			}
			_ = store.Close()
		},
	}, nil
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
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi bugs ingest [--repo owner/repo] [--label bug] [--state open] [--profile p] [--envelope n] [--yes]")
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
	src := engine.GitHubSource{Repo: *repo, Label: *label, State: *stateFilter, Dir: cwd}
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
	return materializeBugs(ctx, be, res.Proposals)
}

// runBugNew implements `gummi bugs new`: one hand-entered bug straight
// into the todo backlog with a seeded report.
func runBugNew(args []string) error {
	fs := flag.NewFlagSet("bugs new", flag.ContinueOnError)
	title := fs.String("title", "", "bug title (required)")
	oneLiner := fs.String("one-liner", "", "short one-line summary")
	severity := fs.String("severity", "", "severity: critical|high|medium|low")
	repro := fs.String("repro", "", "reproduction steps")
	expected := fs.String("expected", "", "expected behavior")
	actual := fs.String("actual", "", "actual behavior")
	env := fs.String("env", "", "environment (versions, OS, config)")
	desc := fs.String("desc", "", "summary of what's broken")
	profile := fs.String("profile", "", "profile the bug adopts (default: first configured)")
	envelope := fs.Int("envelope", 0, "credit envelope (0 = none; falls back to GUMMI_ENVELOPE)")
	yes := fs.Bool("yes", false, "create without the confirmation prompt")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi bugs new --title T [--severity S] [--repro …] [--expected …] [--actual …] [--env …] [--desc …] [--profile p] [--envelope n] [--yes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		fs.Usage()
		return fmt.Errorf("bugs new needs a --title")
	}

	be, err := openBugEnv(*profile, *envelope)
	if err != nil {
		return err
	}
	defer be.cleanup()

	prop := domain.BugProposal{
		Title:    strings.TrimSpace(*title),
		OneLiner: strings.TrimSpace(*oneLiner),
		Source:   "manual",
		Severity: domain.NormalizeSeverity(*severity),
		Report: domain.BugReport{
			Description:  strings.TrimSpace(*desc),
			Reproduction: strings.TrimSpace(*repro),
			Expected:     strings.TrimSpace(*expected),
			Actual:       strings.TrimSpace(*actual),
			Environment:  strings.TrimSpace(*env),
		},
	}
	ctx := context.Background()
	res, err := be.eng.IngestBugs(ctx, engine.ManualSource{Bug: prop})
	if err != nil {
		return err
	}
	renderBugProposals(os.Stdout, res)
	if !*yes {
		if !confirm(os.Stdin, os.Stdout, "Create this bug in todo?") {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}
	return materializeBugs(ctx, be, res.Proposals)
}

// materializeBugs mints the proposals and prints what was created.
func materializeBugs(ctx context.Context, be *bugEnv, props []domain.BugProposal) error {
	created, err := be.eng.MaterializeBugs(ctx, props, engine.MaterializeOpts{Profile: be.profile, Envelope: be.env})
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
		fmt.Fprintf(w, "\nSkipped %d already on the board.\n", len(res.Skipped))
	}
	fmt.Fprintln(w)
}
