package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shas(t *testing.T, m *Manager, base string, f interface{ BranchName() string }, root string) []string {
	t.Helper()
	out := mustGit(t, root, "rev-list", "--reverse", base+".."+f.BranchName())
	return strings.Fields(out)
}

func TestLogListsTheCardsOwnCommitsOldestFirst(t *testing.T) {
	root := newRepo(t)
	m, f, _, base := checkpointedFeature(t, root)
	log, err := m.Log(ctx, f, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 {
		t.Fatalf("log = %d entries, want 3", len(log))
	}
	if log[0].Subject != "FD-009: checkpoint 1" || log[2].Subject != "FD-009: checkpoint 3" {
		t.Errorf("order = %q … %q", log[0].Subject, log[2].Subject)
	}
	if log[0].Files != 1 || log[0].Add != 1 || log[0].Del != 0 {
		t.Errorf("numstat = %d files +%d -%d, want 1 file +1 -0", log[0].Files, log[0].Add, log[0].Del)
	}
	if log[0].Pushed {
		t.Error("a branch tracking nothing has nothing pushed")
	}
}

func TestRewriteRewordsAndSquashesKeepingTheTree(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	preTree := mustGit(t, p, "rev-parse", "HEAD^{tree}")
	c := shas(t, m, base, f, root)

	tip, err := m.Rewrite(ctx, f, base, RewritePlan{Head: c[2], Groups: []RewriteGroup{
		{Commits: []string{c[0]}, Message: "feat(x): add first"},
		{Commits: []string{c[1], c[2]}, Message: "feat(x): add the rest"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
		t.Errorf("worktree HEAD %s, want returned tip %s", got, tip)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD^{tree}"); got != preTree {
		t.Errorf("tree changed: %s, want %s", got, preTree)
	}
	if n := mustGit(t, root, "rev-list", "--count", base+".."+f.BranchName()); n != "2" {
		t.Errorf("commits = %s, want 2", n)
	}
	if got := mustGit(t, p, "log", "--format=%s", base+"..HEAD"); got != "feat(x): add the rest\nfeat(x): add first" {
		t.Errorf("subjects = %q", got)
	}
	if got := mustGit(t, p, "status", "--porcelain"); got != "" {
		t.Errorf("worktree dirty after rewrite: %q", got)
	}
	// the first commit's tree is its own, not the squashed one
	if got := mustGit(t, p, "ls-tree", "-r", "--name-only", "HEAD~1"); strings.Contains(got, "cp2.txt") {
		t.Errorf("HEAD~1 should not carry cp2.txt: %q", got)
	}
}

func TestRewriteKeepsTheAuthorAndUntouchedCommits(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	mustGit(t, p, "commit", "-q", "--amend", "--no-edit", "--author=Ada <ada@example.com>", "--date=2020-01-02T03:04:05Z")
	c := shas(t, m, base, f, root)

	// reword only the last: the first two survive as the same commits
	_, err := m.Rewrite(ctx, f, base, RewritePlan{Groups: []RewriteGroup{
		{Commits: []string{c[0]}},
		{Commits: []string{c[1]}},
		{Commits: []string{c[2]}, Message: "fix(x): third"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	after := shas(t, m, base, f, root)
	if after[0] != c[0] || after[1] != c[1] || after[2] == c[2] {
		t.Errorf("untouched commits should keep their SHA: %v -> %v", c, after)
	}
	if got := mustGit(t, p, "log", "-1", "--format=%an <%ae> %at"); got != "Ada <ada@example.com> 1577934245" {
		t.Errorf("author lost: %q", got)
	}
}

func TestRewriteNoopTouchesNothing(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	c := shas(t, m, base, f, root)
	before := mustGit(t, p, "rev-parse", "HEAD")
	tip, err := m.Rewrite(ctx, f, base, RewritePlan{Groups: []RewriteGroup{{Commits: c[:1]}, {Commits: c[1:2]}, {Commits: c[2:]}}}, false)
	if err != nil || tip != "" {
		t.Fatalf("Rewrite = %q, %v; want a no-op", tip, err)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != before {
		t.Error("a no-op moved the branch")
	}
}

func TestRewriteRefuses(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	c := shas(t, m, base, f, root)
	before := mustGit(t, p, "rev-parse", "HEAD")
	all := func(msg string) RewritePlan {
		return RewritePlan{Groups: []RewriteGroup{{Commits: c, Message: msg}}}
	}
	for _, tc := range []struct {
		name string
		plan RewritePlan
		want error
	}{
		{"an empty group", RewritePlan{Groups: []RewriteGroup{{}}}, ErrPlanMismatch},
		{"a missing commit", RewritePlan{Groups: []RewriteGroup{{Commits: c[:2], Message: "x"}}}, ErrPlanMismatch},
		{"reordered commits", RewritePlan{Groups: []RewriteGroup{{Commits: []string{c[1]}}, {Commits: []string{c[0]}}, {Commits: []string{c[2]}}}}, ErrPlanMismatch},
		{"an unknown commit", RewritePlan{Groups: []RewriteGroup{{Commits: []string{"deadbeef"}}}}, ErrPlanMismatch},
		{"a stale head", RewritePlan{Head: c[1], Groups: all("x").Groups}, ErrPlanMismatch},
		{"a squash with no message", RewritePlan{Groups: []RewriteGroup{{Commits: c}}}, ErrPlanMismatch},
		{"agent attribution", all("feat: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>"), ErrAttribution},
	} {
		if _, err := m.Rewrite(ctx, f, base, tc.plan, false); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	writeFile(t, p, "stray.txt", "x\n")
	if _, err := m.Rewrite(ctx, f, base, all("x"), false); !errors.Is(err, ErrDirtyWorktree) {
		t.Errorf("dirty worktree: err = %v", err)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != before {
		t.Error("a refused rewrite moved the branch")
	}
}

func TestRewriteOfPushedCommitsNeedsAnAcknowledgement(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	c := shas(t, m, base, f, root)
	branch := f.BranchName()
	// a remote that already has the first two commits
	mustGit(t, root, "remote", "add", "origin", root)
	mustGit(t, root, "update-ref", "refs/remotes/origin/"+branch, c[1])
	mustGit(t, root, "config", "branch."+branch+".remote", "origin")
	mustGit(t, root, "config", "branch."+branch+".merge", "refs/heads/"+branch)

	log, err := m.Log(ctx, f, base)
	if err != nil {
		t.Fatal(err)
	}
	if !log[0].Pushed || !log[1].Pushed || log[2].Pushed {
		t.Errorf("pushed = %v %v %v, want true true false", log[0].Pushed, log[1].Pushed, log[2].Pushed)
	}
	// rewording only the unpushed tip replaces nothing the remote has
	if _, err := m.Rewrite(ctx, f, base, RewritePlan{Groups: []RewriteGroup{
		{Commits: c[:1]}, {Commits: c[1:2]}, {Commits: c[2:], Message: "fix: third"},
	}}, false); err != nil {
		t.Fatalf("unpushed reword refused: %v", err)
	}
	c = shas(t, m, base, f, root)
	plan := RewritePlan{Groups: []RewriteGroup{{Commits: c[:2], Message: "feat: one"}, {Commits: c[2:]}}}
	prev, err := m.PlanRewrite(ctx, f, base, plan)
	if err != nil || !prev.Pushed || prev.Changed != 2 || len(prev.Entries) != 2 {
		t.Fatalf("preview = %+v, %v", prev, err)
	}
	// the squashed group's stat is what the two commits change together
	if e := prev.Entries[0]; e.Files != 2 || e.Add != 2 || e.Del != 0 {
		t.Errorf("squashed stat = %d files +%d -%d, want 2 files +2 -0", e.Files, e.Add, e.Del)
	}
	before := mustGit(t, p, "rev-parse", "HEAD")
	_, err = m.Rewrite(ctx, f, base, plan, false)
	if !errors.Is(err, ErrPushedNotAcknowledged) || !strings.Contains(err.Error(), "force push") {
		t.Fatalf("err = %v, want the pushed refusal naming the force push", err)
	}
	if mustGit(t, p, "rev-parse", "HEAD") != before {
		t.Error("a refused rewrite moved the branch")
	}
	if _, err := m.Rewrite(ctx, f, base, plan, true); err != nil {
		t.Fatalf("acknowledged rewrite: %v", err)
	}
}

// A rewrite builds its commits with commit-tree, which signs only when
// told to: with commit.gpgsign on, the commits it writes are signed like
// the ones they replace.
func TestRewriteSignsWhereCommitsAreSigned(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	signWithStub(t, root)
	c := shas(t, m, base, f, root)

	if _, err := m.Rewrite(ctx, f, base, RewritePlan{Head: c[2], Groups: []RewriteGroup{
		{Commits: c, Message: "feat(x): all of it"},
	}}, false); err != nil {
		t.Fatal(err)
	}
	if raw := mustGit(t, p, "cat-file", "commit", "HEAD"); !strings.Contains(raw, "gpgsig -----BEGIN PGP SIGNATURE-----") {
		t.Fatalf("the rewritten commit is not signed:\n%s", raw)
	}
}

// signWithStub switches commit signing on in root, through a program
// that answers git as gpg does without holding a key.
func signWithStub(t *testing.T, root string) {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "sign")
	script := "#!/bin/sh\ncat >/dev/null\necho '[GNUPG:] SIG_CREATED ' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nstub\\n-----END PGP SIGNATURE-----\\n'\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "config", "gpg.program", stub)
	mustGit(t, root, "config", "commit.gpgsign", "true")
}

// A plan that asks for signatures makes every unsigned commit again,
// signed, with its message, author and tree as they were; a second ask
// finds nothing left to do.
func TestRewriteSignsEveryUnsignedCommit(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	c := shas(t, m, base, f, root)
	plan := func(c []string) RewritePlan {
		pl := RewritePlan{Head: c[len(c)-1], Sign: true}
		for _, sha := range c {
			pl.Groups = append(pl.Groups, RewriteGroup{Commits: []string{sha}})
		}
		return pl
	}

	if _, err := m.Rewrite(ctx, f, base, plan(c), false); !errors.Is(err, ErrNotSigning) {
		t.Fatalf("signing where nothing signs: err = %v, want ErrNotSigning", err)
	}
	if got := shas(t, m, base, f, root); got[2] != c[2] {
		t.Fatal("a refused plan moved the branch")
	}

	signWithStub(t, root)
	if !m.Signing(ctx, f) {
		t.Fatal("Signing is false with commit.gpgsign on")
	}
	preTree := mustGit(t, p, "rev-parse", "HEAD^{tree}")
	before := mustGit(t, p, "log", "--format=%s|%an|%aI|%T", base+"..HEAD")
	prev, err := m.PlanRewrite(ctx, f, base, plan(c))
	if err != nil {
		t.Fatal(err)
	}
	if prev.Noop || prev.Changed != 3 || !prev.Entries[0].Signed {
		t.Fatalf("preview = %+v, want 3 commits changed and signed", prev)
	}
	if _, err := m.Rewrite(ctx, f, base, plan(c), false); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, p, "log", "--format=%s|%an|%aI|%T", base+"..HEAD"); got != before {
		t.Errorf("messages, authors or trees changed:\n%s\nwant\n%s", got, before)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD^{tree}"); got != preTree {
		t.Errorf("tree changed")
	}
	log, err := m.Log(ctx, f, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range log {
		if !e.Signed {
			t.Errorf("%s %q is not signed", e.Short, e.Subject)
		}
	}

	// signed already: kept as they are
	signed := shas(t, m, base, f, root)
	if tip, err := m.Rewrite(ctx, f, base, plan(signed), false); err != nil || tip != "" {
		t.Fatalf("signing a signed branch: tip %q err %v, want a no-op", tip, err)
	}

	// an unsigned commit on top: the signed ones below keep their SHAs
	mustGit(t, p, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "wip: unsigned")
	mixed := shas(t, m, base, f, root)
	if _, err := m.Rewrite(ctx, f, base, plan(mixed), false); err != nil {
		t.Fatal(err)
	}
	after := shas(t, m, base, f, root)
	if after[2] != signed[2] || after[3] == mixed[3] {
		t.Errorf("after = %v: want the signed three kept and the fourth made again", after)
	}
}
