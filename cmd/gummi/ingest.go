package main

import (
	"bufio"
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

// runIngest implements `gummi ingest <spec-file>` (DESIGN §11): an
// architect pass decomposes the document into feature proposals, gummi
// prints them plus a coverage map, and — after confirmation (or with
// --yes) — materializes them into the todo backlog with seeded drafts.
func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile the new features adopt (default: first configured)")
	envelope := fs.Int("envelope", 0, "credit envelope per feature (0 = none; falls back to GUMMI_ENVELOPE)")
	yes := fs.Bool("yes", false, "materialize without the confirmation prompt")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi ingest [--profile p] [--envelope n] [--yes] <spec-file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("ingest needs exactly one spec file")
	}
	source := fs.Arg(0)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	wt, err := newManager(context.Background(), cwd, store)
	if err != nil {
		return err
	}
	eng, agents, names := newEngineFromEnv(store, wt, ws)
	if eng == nil {
		return fmt.Errorf("no coding agent is configured; ingestion needs one (GitHub Copilot, or set GUMMI_AGENT_CMD)")
	}
	defer func() { _ = eng.Close(); closeAgents(agents) }()

	prof := *profile
	if prof == "" && len(names) > 0 {
		prof = names[0]
	}
	env := *envelope
	if env == 0 {
		if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				env = n
			}
		}
	}

	ctx := context.Background()
	fmt.Printf("Ingesting %s (architect / profile %q) …\n", source, cmpOrDefault(prof))
	// stream the pass's discrete steps (milestones + tool calls) so the
	// wait isn't silent; the architect's prose commentary stays quiet.
	res, err := eng.Ingest(ctx, source, prof, func(st engine.IngestStep) {
		switch st.Kind {
		case engine.IngestStepNote:
			fmt.Printf("  · %s\n", clean(st.Text))
		case engine.IngestStepTool:
			fmt.Printf("  ✓ %s\n", clean(st.Text))
		}
	})
	if err != nil {
		return err
	}
	renderProposal(os.Stdout, res)

	if !*yes {
		prompt := fmt.Sprintf("Materialize %d feature(s) into todo?", len(res.Proposals))
		if len(res.Unmapped()) > 0 {
			prompt = fmt.Sprintf("%d requirement(s) are UNMAPPED. %s", len(res.Unmapped()), prompt)
		}
		if !confirm(os.Stdin, os.Stdout, prompt) {
			fmt.Println("Aborted — nothing created.")
			return nil
		}
	}

	created, err := eng.Materialize(ctx, res, engine.MaterializeOpts{Profile: prof, Envelope: env})
	for _, f := range created {
		fmt.Printf("  %s  %s\n", f.ID, clean(f.Title))
	}
	if err != nil {
		return fmt.Errorf("materialize (created %d before failing): %w", len(created), err)
	}
	fmt.Printf("Created %d feature(s) in todo. Open gummi to run them.\n", len(created))
	return nil
}

func cmpOrDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

// renderProposal prints the ingest result: each feature with its
// one-liner, source refs, dependencies, and open-question count, then the
// coverage map with any unmapped requirements flagged loudly.
func renderProposal(w io.Writer, res domain.IngestResult) {
	fmt.Fprintf(w, "\nProposed %d feature(s):\n", len(res.Proposals))
	for i, p := range res.Proposals {
		fmt.Fprintf(w, "\n  %2d. %s\n", i+1, clean(p.Title))
		if p.OneLiner != "" {
			fmt.Fprintf(w, "      %s\n", clean(p.OneLiner))
		}
		if len(p.SourceRefs) > 0 {
			fmt.Fprintf(w, "      from: %s\n", clean(strings.Join(p.SourceRefs, ", ")))
		}
		if len(p.DependsOn) > 0 {
			fmt.Fprintf(w, "      needs: %s\n", clean(strings.Join(p.DependsOn, ", ")))
		}
		var tags []string
		if p.Skip.Brainstorm {
			tags = append(tags, "skip brainstorm")
		}
		if p.Skip.Plan {
			tags = append(tags, "skip plan")
		}
		if n := len(p.Draft.OpenQuestions); n > 0 {
			tags = append(tags, fmt.Sprintf("%d open question(s)", n))
		}
		if len(tags) > 0 {
			fmt.Fprintf(w, "      [%s]\n", strings.Join(tags, " · "))
		}
	}

	if len(res.Coverage) == 0 {
		fmt.Fprintln(w)
		return
	}
	var mapped, oos int
	for _, c := range res.Coverage {
		switch c.Status {
		case domain.CoverageMapped:
			mapped++
		case domain.CoverageOutOfScope:
			oos++
		}
	}
	fmt.Fprintf(w, "\nCoverage: %d mapped · %d out-of-scope · %d unmapped\n", mapped, oos, len(res.Unmapped()))
	for _, c := range res.Unmapped() {
		line := "  ! UNMAPPED: " + c.Requirement
		if c.Note != "" {
			line += " — " + c.Note
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
}

// confirm reads a y/N answer from in, writing the prompt to out. Anything
// other than an affirmative is a decline, so the gate fails closed.
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
