package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
)

// ThreadComment is one comment (root or reply) within a review thread.
type ThreadComment struct {
	Id          string
	AuthorLogin string
	Body        string
}

// ReviewThread is a GitHub PR review thread, flattened from the GraphQL
// `reviewThreads` connection. DiffHunk is a decode-time convenience — GitHub
// carries `diffHunk` per-comment (on PullRequestReviewComment), not on the
// thread itself — populated from Comments[0].diffHunk so consumers see one
// authoritative value per thread and never re-derive it.
type ReviewThread struct {
	Id         string
	Path       string
	DiffHunk   string
	IsResolved bool
	IsOutdated bool
	Comments   []ThreadComment
}

// TopLevelComment is a PR-body comment or review summary — no file/line, so
// it never becomes a DiffAnnotation (DiffAnnotation.File is required).
type TopLevelComment struct {
	AuthorLogin string
	Body        string
}

// reviewThreadsQuery fetches a PR's review threads (paginated via
// $endCursor, which --paginate manages automatically) alongside the PR's
// top-level body comments and review summaries. diffHunk is queried under
// comments.nodes (PullRequestReviewComment), not reviewThreads.nodes — it is
// not a field of PullRequestReviewThread in GitHub's schema.
const reviewThreadsQuery = `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      reviewThreads(first:100,after:$endCursor){
        nodes{
          id
          path
          isResolved
          isOutdated
          comments(first:100){
            nodes{ id author{login} body diffHunk }
          }
        }
        pageInfo{ hasNextPage endCursor }
      }
      comments(first:100){ nodes{ author{login} body } }
      reviews(first:100){ nodes{ author{login} body state } }
    }
  }
}`

type ghReviewThreadsPage struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						Path       string `json:"path"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Comments   struct {
							Nodes []struct {
								ID     string `json:"id"`
								Author struct {
									Login string `json:"login"`
								} `json:"author"`
								Body     string `json:"body"`
								DiffHunk string `json:"diffHunk"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
				Comments struct {
					Nodes []struct {
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
						Body string `json:"body"`
					} `json:"nodes"`
				} `json:"comments"`
				Reviews struct {
					Nodes []struct {
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
						Body  string `json:"body"`
						State string `json:"state"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchReviewThreads fetches ref's unresolved review threads and top-level
// comments via `gh api graphql --paginate`. reviewThreads.nodes accumulate
// across every page `--paginate` decodes; comments/reviews are non-paginated
// siblings that `--paginate` repeats verbatim on every page, so
// TopLevelComment folds from the first decoded page only. isResolved=true
// threads are dropped here and never surface to a caller.
func FetchReviewThreads(ctx context.Context, ghBinary string, ref domain.PullRequestRef) ([]ReviewThread, []TopLevelComment, error) {
	owner, repoName, ok := strings.Cut(ref.Repo, "/")
	if !ok || owner == "" || repoName == "" {
		return nil, nil, fmt.Errorf("pull request repo %q is not in owner/repo form", ref.Repo)
	}

	out, err := run(ctx, ghBinary, "", "api", "graphql", "--paginate",
		"-f", "query="+reviewThreadsQuery,
		"-F", "owner="+owner,
		"-F", "repo="+repoName,
		"-F", "number="+strconv.Itoa(ref.Number))
	if err != nil {
		return nil, nil, err
	}

	var threads []ReviewThread
	var topLevel []TopLevelComment
	first := true
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var page ghReviewThreadsPage
		if err := dec.Decode(&page); err != nil {
			return nil, nil, fmt.Errorf("parsing gh api graphql output: %w", err)
		}
		p := page.Data.Repository.PullRequest
		for _, n := range p.ReviewThreads.Nodes {
			if n.IsResolved {
				continue
			}
			t := ReviewThread{Id: n.ID, Path: n.Path, IsResolved: n.IsResolved, IsOutdated: n.IsOutdated}
			for _, c := range n.Comments.Nodes {
				t.Comments = append(t.Comments, ThreadComment{Id: c.ID, AuthorLogin: c.Author.Login, Body: c.Body})
			}
			if len(n.Comments.Nodes) > 0 {
				t.DiffHunk = n.Comments.Nodes[0].DiffHunk
			}
			threads = append(threads, t)
		}
		if first {
			for _, c := range p.Comments.Nodes {
				topLevel = append(topLevel, TopLevelComment{AuthorLogin: c.Author.Login, Body: c.Body})
			}
			for _, r := range p.Reviews.Nodes {
				if r.Body == "" {
					continue
				}
				topLevel = append(topLevel, TopLevelComment{AuthorLogin: r.Author.Login, Body: r.Body})
			}
			first = false
		}
	}
	return threads, topLevel, nil
}

// formatBody renders a thread's comments as per-comment `@login: body`
// blocks, root first then replies in reply order, joined by a blank line.
// An outdated thread is prefixed with a leading "[outdated]" line and a
// blank line.
func formatBody(t ReviewThread) string {
	blocks := make([]string, len(t.Comments))
	for i, c := range t.Comments {
		blocks[i] = "@" + c.AuthorLogin + ": " + c.Body
	}
	body := strings.Join(blocks, "\n\n")
	if t.IsOutdated {
		return "[outdated]\n\n" + body
	}
	return body
}

// stripMarker strips a unified-diff line's leading +/-/space marker, same
// rule as diffannot's own payload stripping, so hunk lines and worktree-diff
// lines compare on text alone.
func stripMarker(line string) string {
	if line == "" {
		return ""
	}
	switch line[0] {
	case '+', '-', ' ':
		return line[1:]
	default:
		return line
	}
}

// locateHunkTail finds the worktree-diff line index whose trailing content
// matches hunkLines' trailing payload (the commented line and up to 3 lines
// of leading context), under the same path's `+++ b/<path>` section.
// hunkLines[0] is GitHub's "@@ -a,b +c,d @@" header and never participates
// in matching. Returns -1 on an empty payload, no match, or more than one
// match (multi-match protection: ambiguous hunks degrade to a file-level
// orphan rather than guessing).
func locateHunkTail(worktreeLines []string, path string, hunkLines []string) int {
	if len(hunkLines) == 0 {
		return -1
	}
	payload := hunkLines[1:]
	if len(payload) == 0 {
		return -1
	}
	n := min(4, len(payload))
	want := make([]string, n)
	for i, l := range payload[len(payload)-n:] {
		want[i] = stripMarker(l)
	}

	file := ""
	found := -1
	for i, l := range worktreeLines {
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			file = strings.TrimPrefix(l, "+++ b/")
		case strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(l, "+++ ")
		case strings.HasPrefix(l, "diff --git "):
			if j := strings.Index(l, " b/"); j >= 0 {
				file = l[j+3:]
			}
		}
		if file != path || i-n+1 < 0 {
			continue
		}
		match := true
		for j := 0; j < n; j++ {
			if stripMarker(worktreeLines[i-n+1+j]) != want[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if found != -1 {
			return -1
		}
		found = i
	}
	return found
}

// AnnotationFor builds the DiffAnnotation for a review thread: located onto
// worktreeLines via locateHunkTail, with the anchor hash derived from
// worktreeLines at the found position (the same "lines" shape the render
// path's diffannot.Locate recomputes against, per internal/ui/diffview.go).
// On a miss, Anchor stays "" and the existing orphaned-anchor contract
// degrades the row to file-level at render time.
func AnnotationFor(f domain.FeatureID, t ReviewThread, worktreeLines []string) domain.DiffAnnotation {
	hunkLines := strings.Split(t.DiffHunk, "\n")
	ann := domain.DiffAnnotation{
		Feature:   f,
		File:      t.Path,
		SourceRef: t.Id,
		Comment:   formatBody(t),
	}
	if payload := hunkLines[1:]; len(payload) > 0 {
		ann.Excerpt = strings.TrimSpace(payload[len(payload)-1])
	}
	if idx := locateHunkTail(worktreeLines, t.Path, hunkLines); idx >= 0 {
		ann.Anchor = diffannot.Anchor(worktreeLines, idx)
	}
	return ann
}
