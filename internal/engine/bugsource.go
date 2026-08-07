package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

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
// filters to triaged bugs; State defaults to open. FetchComments pulls
// each issue's top-level comments into the report's Discussion section.
type GitHubSource struct {
	Repo  string // "owner/repo"; empty = auto-detect from Dir's remote
	Label string // filter to this label; empty = no label filter
	State string // open|closed|all; empty = open
	Dir   string // working directory for gh (the repo root)
	Limit int    // max issues; 0 = ghDefaultLimit

	// FetchComments fetches each issue's comments via `gh issue view` and
	// appends them to the report's Discussion section. Opt-in (default
	// false) so the common no-comments import stays a single gh call.
	FetchComments bool

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
// body is split into the report's symptom sections by parseBodySections;
// when FetchComments is on, each issue's top-level comments are appended
// to Discussion. The import stays deterministic — no coding agent is
// involved.
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
		report := parseBodySections(is.Body)
		if g.FetchComments {
			discussion, err := g.fetchComments(ctx, g.Dir, is.Number, g.Repo)
			if err != nil {
				// Comments are bonus signal; a transient gh failure (network,
				// auth, deleted issue) must not drop the issue — import it with
				// parsed body but no Discussion.
				log.Printf("fetching comments for issue #%d: %v", is.Number, err)
			} else {
				report.Discussion = discussion
			}
		}
		proposals = append(proposals, domain.BugProposal{
			Title:       title,
			Source:      "github",
			ExternalRef: is.URL,
			Severity:    severityFromLabels(is.Labels),
			Report:      report,
		})
	}
	return proposals, nil
}

// maxComments bounds the number of comments kept per issue, and
// maxCommentChars truncates an overlong comment. Comments are bonus
// context, not a transcript, so both stay small and fixed.
const (
	maxComments     = 5
	maxCommentChars = 2000
)

// ghIssueComments is the subset of `gh issue view --json comments` we
// consume: top-level comments only, oldest first.
type ghIssueComments struct {
	Comments []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
}

// fetchComments pulls a single issue's comments and joins them into a
// Discussion block, newest-last. Each comment is prefixed with its
// author's login and truncated to maxCommentChars; at most maxComments
// are kept. repo is threaded through to `gh issue view --repo` when set,
// matching the `gh issue list` conditional in Fetch.
func (g GitHubSource) fetchComments(ctx context.Context, dir string, issueNum int, repo string) (string, error) {
	args := []string{"issue", "view", strconv.Itoa(issueNum)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	args = append(args, "--json", "comments")
	run := g.run
	if run == nil {
		run = execGH
	}
	out, err := run(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	var data ghIssueComments
	if err := json.Unmarshal(out, &data); err != nil {
		return "", fmt.Errorf("parsing gh comments output: %w", err)
	}
	sort.SliceStable(data.Comments, func(i, j int) bool {
		return data.Comments[i].CreatedAt.Before(data.Comments[j].CreatedAt)
	})
	if len(data.Comments) > maxComments {
		data.Comments = data.Comments[:maxComments]
	}
	parts := make([]string, 0, len(data.Comments))
	for _, c := range data.Comments {
		parts = append(parts, fmt.Sprintf("**%s:** %s", c.Author.Login, truncateComment(c.Body)))
	}
	return strings.Join(parts, "\n\n"), nil
}

// truncateComment cuts an overlong comment body at maxCommentChars, noting
// how many characters were dropped so the reader knows the tail is gone.
func truncateComment(s string) string {
	if len(s) <= maxCommentChars {
		return s
	}
	return s[:maxCommentChars] + fmt.Sprintf("… [truncated %d chars]", len(s)-maxCommentChars)
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
