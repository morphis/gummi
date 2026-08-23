// Package pr wires gummi's outbound-PR verbs (link/unlink/status) to the gh
// CLI. It runs gh deterministically off a working directory (mirroring
// internal/engine/bugsource.go's execGH: gh auto-detects owner/repo from a
// directory's git remote when --repo is omitted) and never guesses when gh
// itself is missing — Available fails loudly, naming exactly what is
// absent. Authentication is left entirely to gh.
package pr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// GHBinary reads the GUMMI_GH_CMD test seam, defaulting to "" (meaning the
// caller should fall back to plain "gh" on PATH). Production callers pass
// its result as every function's ghBinary argument; tests set GUMMI_GH_CMD
// to a fake shim's path so no real gh CLI or network is ever touched.
func GHBinary() string { return os.Getenv("GUMMI_GH_CMD") }

func binOrDefault(ghBinary string) string {
	if ghBinary != "" {
		return ghBinary
	}
	return "gh"
}

// Available reports whether gh can be run at all: the binary is on PATH (or
// at the configured ghBinary path). Authentication is gh's own concern — it
// may come from GH_TOKEN, GITHUB_TOKEN, or a `gh auth login` keyring/config,
// and an unauthenticated gh surfaces its own error on the first real call,
// same as internal/engine/bugsource.go's execGH.
func Available(ghBinary string) error {
	bin := binOrDefault(ghBinary)
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("gh CLI not on PATH (looked for %q)", bin)
	}
	return nil
}

// run executes gh with args, in dir when non-empty (letting gh auto-detect
// --repo from that directory's git remote), returning its stdout. A stderr
// carried by the process's exit error is surfaced verbatim — the same shape
// as internal/engine/bugsource.go's execGH.
func run(ctx context.Context, ghBinary, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binOrDefault(ghBinary), args...) //nolint:gosec // ghBinary is operator config (GUMMI_GH_CMD), args are gummi-built
	if dir != "" {
		cmd.Dir = dir
	}
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

var prURLRe = regexp.MustCompile(`^https://github\.com/([^/\s]+/[^/\s]+)/pull/([0-9]+)/?$`)

// ParsePullRefURL parses a GitHub PR URL ("https://github.com/o/r/pull/42")
// into its repo ("o/r") and number (42). It fails on anything else,
// including a github.com URL of another shape.
func ParsePullRefURL(u string) (repo string, number int, err error) {
	m := prURLRe.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return "", 0, fmt.Errorf("not a github.com PR URL: %q", u)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, fmt.Errorf("not a github.com PR URL: %q", u)
	}
	return m[1], n, nil
}

// prJSONFields is what Resolve asks gh for: enough to fill a
// domain.PullRequestRef. url is the authoritative source for Repo (parsed
// back out via ParsePullRefURL) so a fork/upstream PR's real repo is always
// recorded, never assumed from the caller's own directory.
const prJSONFields = "number,url,headRefOid"

type ghPR struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	HeadRefOid string `json:"headRefOid"`
}

func refFromGHPR(p ghPR) (domain.PullRequestRef, error) {
	repo, _, err := ParsePullRefURL(p.URL)
	if err != nil {
		return domain.PullRequestRef{}, fmt.Errorf("gh returned an unparseable PR url %q: %w", p.URL, err)
	}
	return domain.PullRequestRef{Repo: repo, Number: p.Number, URL: p.URL, HeadSHA: p.HeadRefOid}, nil
}

// Resolve resolves spec to a fully populated domain.PullRequestRef with the
// PR's head commit as of right now. spec is one of:
//   - a "https://github.com/owner/repo/pull/N" URL (any repo, fork or not);
//   - a bare PR number, resolved against repoDir's own repo (gh auto-detects
//     owner/repo from that directory's git remote — the card's own
//     configured repo, per the chosen approach);
//   - "" (the --auto form), resolved via `gh pr list --head branch` against
//     repoDir's repo, requiring exactly one match.
func Resolve(ctx context.Context, ghBinary, spec, repoDir, branch string) (domain.PullRequestRef, error) {
	if spec == "" {
		return resolveAuto(ctx, ghBinary, repoDir, branch)
	}
	if repo, number, err := ParsePullRefURL(spec); err == nil {
		return resolveByRepoOrDir(ctx, ghBinary, "", repo, number)
	}
	number, err := strconv.Atoi(strings.TrimSpace(spec))
	if err != nil {
		return domain.PullRequestRef{}, fmt.Errorf("%q is neither a github.com PR URL nor a bare PR number", spec)
	}
	return resolveByRepoOrDir(ctx, ghBinary, repoDir, "", number)
}

// resolveByRepoOrDir fetches one PR by number, either against an explicit
// "owner/repo" (repoFlag set, the URL form) or by letting gh auto-detect the
// repo from dir (the bare-number form).
func resolveByRepoOrDir(ctx context.Context, ghBinary, dir, repoFlag string, number int) (domain.PullRequestRef, error) {
	args := []string{"pr", "view", strconv.Itoa(number), "--json", prJSONFields}
	if repoFlag != "" {
		args = append(args, "--repo", repoFlag)
	}
	out, err := run(ctx, ghBinary, dir, args...)
	if err != nil {
		return domain.PullRequestRef{}, err
	}
	var p ghPR
	if err := json.Unmarshal(out, &p); err != nil {
		return domain.PullRequestRef{}, fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return refFromGHPR(p)
}

// resolveAuto finds the PR whose head branch is branch, in the repo gh
// auto-detects from dir, requiring exactly one match.
func resolveAuto(ctx context.Context, ghBinary, dir, branch string) (domain.PullRequestRef, error) {
	out, err := run(ctx, ghBinary, dir, "pr", "list", "--head", branch, "--json", prJSONFields)
	if err != nil {
		return domain.PullRequestRef{}, err
	}
	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return domain.PullRequestRef{}, fmt.Errorf("parsing gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return domain.PullRequestRef{}, fmt.Errorf("--auto found no open PR with head branch %q", branch)
	}
	if len(prs) > 1 {
		return domain.PullRequestRef{}, fmt.Errorf("--auto found %d PRs with head branch %q; link one explicitly by URL or number", len(prs), branch)
	}
	return refFromGHPR(prs[0])
}

// LiveStatus queries ref's PR right now for its state (e.g. "OPEN",
// "CLOSED", "MERGED") and its comment count.
func LiveStatus(ctx context.Context, ghBinary string, ref domain.PullRequestRef) (state string, comments int, err error) {
	out, err := run(ctx, ghBinary, "", "pr", "view", strconv.Itoa(ref.Number), "--repo", ref.Repo, "--json", "state,comments")
	if err != nil {
		return "", 0, err
	}
	var data struct {
		State    string            `json:"state"`
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return "", 0, fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return data.State, len(data.Comments), nil
}
