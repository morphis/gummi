package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// A BugSource yields candidate bugs to ingest. gummi ships GitHub and
// manual sources; the interface is the seam for future ones (Sentry,
// Linear, …). Sources are deterministic and agent-free — they structure
// what already exists; per-bug reproduction and root-cause enrichment is
// the triage/diagnose stages' job, not the source's (DESIGN §11's "tool
// owns mechanics, model owns content", applied to bugs).
type BugSource interface {
	// Name identifies the source in provenance and prompts ("github").
	Name() string
	// Fetch returns the source's current candidate bugs.
	Fetch(ctx context.Context) ([]domain.BugProposal, error)
}

// ManualSource is a single hand-entered bug. It carries a prepared
// proposal so the same materialize path serves manual entry and imports.
type ManualSource struct{ Bug domain.BugProposal }

func (m ManualSource) Name() string { return "manual" }

func (m ManualSource) Fetch(context.Context) ([]domain.BugProposal, error) {
	b := m.Bug
	b.Source = "manual"
	if strings.TrimSpace(b.Title) == "" {
		return nil, fmt.Errorf("a manual bug needs a title")
	}
	return []domain.BugProposal{b}, nil
}

// ghDefaultLimit bounds a single issue fetch; GitHub bug ingestion is a
// review-gated batch, not an exhaustive mirror, so a high-but-finite cap
// keeps one run bounded. A truncated fetch is surfaced by the caller.
const ghDefaultLimit = 200

// GitHubSource imports open issues from a repository via the `gh` CLI.
// The target defaults to the repo gummi runs in (gh auto-detects the
// origin remote from Dir); Repo overrides it to any "owner/repo". Label
// filters to triaged bugs; State defaults to open.
type GitHubSource struct {
	Repo  string // "owner/repo"; empty = auto-detect from Dir's remote
	Label string // filter to this label; empty = no label filter
	State string // open|closed|all; empty = open
	Dir   string // working directory for gh (the repo root)
	Limit int    // max issues; 0 = ghDefaultLimit

	// run executes gh and returns stdout; injectable so tests never shell
	// out. nil uses the real gh CLI.
	run func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

func (g GitHubSource) Name() string { return "github" }

// ghIssue is the subset of `gh issue list --json` output we consume.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

// Fetch lists issues from the target repo and maps each to a proposal.
// Only issues are imported (gh issue list excludes pull requests). The
// import is verbatim — the whole body seeds the report's Summary; triage
// pulls out repro/expected/actual — so no tokens are spent here.
func (g GitHubSource) Fetch(ctx context.Context) ([]domain.BugProposal, error) {
	limit := g.Limit
	if limit <= 0 {
		limit = ghDefaultLimit
	}
	state := g.State
	if state == "" {
		state = "open"
	}
	args := []string{
		"issue", "list",
		"--state", state,
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "number,title,body,url,state,labels,author",
	}
	if g.Repo != "" {
		args = append(args, "--repo", g.Repo)
	}
	if g.Label != "" {
		args = append(args, "--label", g.Label)
	}

	run := g.run
	if run == nil {
		run = execGH
	}
	out, err := run(ctx, g.Dir, args...)
	if err != nil {
		return nil, err
	}
	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing gh output: %w", err)
	}
	proposals := make([]domain.BugProposal, 0, len(issues))
	for _, is := range issues {
		title := strings.TrimSpace(is.Title)
		if title == "" || strings.TrimSpace(is.URL) == "" {
			continue // unusable: no title to slug, or no ref to dedup on
		}
		proposals = append(proposals, domain.BugProposal{
			Title:       title,
			Source:      "github",
			ExternalRef: is.URL,
			Severity:    severityFromLabels(is.Labels),
			Report:      domain.BugReport{Description: strings.TrimSpace(is.Body)},
		})
	}
	return proposals, nil
}

// severityFromLabels returns the first label that maps to a canonical
// severity, so "P0"/"sev1"/"critical" labels seed the report header.
func severityFromLabels(labels []struct {
	Name string `json:"name"`
},
) domain.Severity {
	for _, l := range labels {
		if s := domain.NormalizeSeverity(l.Name); s != "" {
			return s
		}
	}
	return ""
}

// execGH runs the real gh CLI in dir. gh's stderr is folded into the
// error so an auth or repo problem surfaces verbatim (we assume gh is
// installed and authenticated — a missing or unauthenticated gh fails
// loudly here rather than degrading).
func execGH(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("gh %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("running gh: %w", err)
	}
	return out, nil
}
