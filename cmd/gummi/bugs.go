package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
	eng, agents, names, _ := newEngineFromEnv(store, pool, ws)
	if eng == nil {
		eng = engine.New(engine.Config{Store: store, Pool: pool, Workspace: ws})
	}
	hookd := wireHooks(store, pool, ws)
	prof := profile
	if prof == "" && len(names) > 0 {
		prof = names[0]
	}
	env := envelope
	if env == 0 {
		env = envEnvelope()
	}
	return &bugEnv{
		eng: eng, profile: prof, env: env,
		cleanup: func() {
			hookd.Close()
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
// --issue N switches to single-issue mode: the same fetch is resolved
// against N (via selectIssue) and only that one bug is materialized; every
// other flag keeps its current meaning. Without --issue this is the batch
// path, unchanged.
func runBugIngest(fl cliFlags) error {
	be, err := openBugEnv(fl.String("profile"), fl.Budget("envelope"))
	if err != nil {
		return err
	}
	defer be.cleanup()

	cwd, _ := os.Getwd()
	src := ingestGitHubSource(fl.String("repo"), fl.String("label"), fl.String("state"), fl.Bool("comments"), cwd)
	ctx := context.Background()
	target := fl.String("repo")
	if target == "" {
		target = "origin"
	}
	fmt.Printf("Importing GitHub issues from %s (label %q, state %s) …\n", target, fl.String("label"), fl.String("state"))
	res, err := be.eng.IngestBugs(ctx, src)
	if err != nil {
		return err
	}

	if fl.Int("issue") != 0 {
		prop, err := selectIssue(res, fl.Int("issue"), target, fl.String("label"), fl.String("state"))
		if err != nil {
			return err
		}
		fmt.Printf("\nSelected issue #%d: %s\n", prop.Number, clean(prop.Title))
		if prop.ExternalRef != "" {
			fmt.Printf("      %s\n", clean(prop.ExternalRef))
		}
		if !fl.Bool("yes") {
			if !confirm(os.Stdin, os.Stdout, "Materialize this bug into todo?") {
				fmt.Println("Aborted — nothing created.")
				return nil
			}
		}
		return materializeBugs(ctx, be, []domain.BugProposal{prop}, fl.String("target-repo"), "", adoption{})
	}

	renderBugProposals(os.Stdout, res)
	if len(res.Proposals) == 0 {
		fmt.Println("Nothing new to import.")
		return nil
	}

	if !fl.Bool("yes") {
		if !confirm(os.Stdin, os.Stdout, fmt.Sprintf("Create %d bug%s in todo?", len(res.Proposals), cardPlural(len(res.Proposals)))) {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}
	return materializeBugs(ctx, be, res.Proposals, fl.String("target-repo"), "", adoption{})
}

// selectIssue resolves a GitHub issue number against a single
// already-fetched IngestBugs result — it never triggers a second, narrower
// fetch. A match in Proposals is the selection. A match in Skipped means the
// issue is already on the board, reported by local ID so a dedup exclusion
// is never mistaken for a missing issue. No match names the active filters
// so a filter exclusion isn't mistaken for a genuinely missing issue.
func selectIssue(res engine.BugIngestResult, issue int, repo, label, state string) (domain.BugProposal, error) {
	for _, p := range res.Proposals {
		if p.Number == issue {
			return p, nil
		}
	}
	for _, s := range res.Skipped {
		if s.Proposal.Number == issue {
			return domain.BugProposal{}, fmt.Errorf("issue %d already on the board as %s", issue, s.LocalID)
		}
	}
	return domain.BugProposal{}, fmt.Errorf("issue %d not in the fetched set: repo=%s label=%s state=%s", issue, repo, label, state)
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

// runBugNew implements `gummi bugs new`: one hand-entered bug straight
// into the todo backlog with a seeded report.
func runBugNew(fl cliFlags) error {
	if strings.TrimSpace(fl.String("title")) == "" {
		return fmt.Errorf("bugs new needs a --title")
	}
	adopted, err := resolveAdoption(fl.String("adopt"), fl.String("pr"), fl.String("repo"))
	if err != nil {
		return err
	}

	be, err := openBugEnv(fl.String("profile"), fl.Budget("envelope"))
	if err != nil {
		return err
	}
	defer be.cleanup()

	prop := domain.BugProposal{
		Title:    strings.TrimSpace(fl.String("title")),
		OneLiner: strings.TrimSpace(fl.String("one-liner")),
		Source:   "manual",
		Severity: domain.NormalizeSeverity(fl.String("severity")),
		Report: domain.BugReport{
			Description:  strings.TrimSpace(fl.String("desc")),
			Reproduction: strings.TrimSpace(fl.String("repro")),
			Expected:     strings.TrimSpace(fl.String("expected")),
			Actual:       strings.TrimSpace(fl.String("actual")),
			Environment:  strings.TrimSpace(fl.String("env")),
		},
	}
	ctx := context.Background()
	res, err := be.eng.IngestBugs(ctx, engine.ManualSource{Bug: prop})
	if err != nil {
		return err
	}
	renderBugProposals(os.Stdout, res)
	if !fl.Bool("yes") {
		if !confirm(os.Stdin, os.Stdout, "Create this bug in todo?") {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}
	return materializeBugs(ctx, be, res.Proposals, fl.String("repo"), fl.String("base"), adopted)
}

// materializeBugs mints the proposals and prints what was created.
func materializeBugs(ctx context.Context, be *bugEnv, props []domain.BugProposal, repo, base string, adopt adoption) error {
	opts := engine.MaterializeOpts{Profile: be.profile, Envelope: be.env, Repo: repo, Base: base, Adopt: adopt.Branch}
	if adopt.Branch != "" {
		// The same inspection cardmint's callers run, asked here because
		// this mint path does not go through cardmint: the branch has to
		// exist and carry work before a bug is minted onto it.
		w, ierr := be.eng.AdoptInspector(ctx, repo, base)(adopt.Branch)
		if ierr != nil {
			return ierr
		}
		opts.AdoptedWork = w
	}
	created, err := be.eng.MaterializeBugs(ctx, props, opts)
	for _, f := range created {
		fmt.Printf("  %s  %s\n", f.ID, clean(f.Title))
	}
	if err != nil {
		return fmt.Errorf("materialize (created %d before failing): %w", len(created), err)
	}
	if len(created) > 0 {
		fmt.Printf("Created %d bug%s in todo. Open gummi to run them.\n", len(created), cardPlural(len(created)))
	}
	return nil
}

// renderBugProposals prints the fresh proposals and a note of any skipped
// (already-imported) ones, so a re-ingest shows what it already had.
func renderBugProposals(w io.Writer, res engine.BugIngestResult) {
	fmt.Fprintf(w, "\nProposed %d bug%s:\n", len(res.Proposals), cardPlural(len(res.Proposals)))
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
		if p.Author != "" {
			tags = append(tags, "by @"+clean(p.Author))
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
