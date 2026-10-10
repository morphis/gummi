package web

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/webapi"
)

func (b *boardHarness) git(dir string, args ...string) string {
	b.t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		b.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (b *boardHarness) commit(dir, name, msg string) {
	b.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o600); err != nil {
		b.t.Fatal(err)
	}
	b.git(dir, "add", ".")
	b.git(dir, "-c", "user.name=t", "-c", "user.email=t@e.invalid", "commit", "-q", "-m", msg)
}

func (b *boardHarness) repoBranches() map[string]webapi.RepoBranch {
	b.t.Helper()
	var repos webapi.Repos
	b.must(http.StatusOK, http.MethodGet, "/api/repos", nil, &repos)
	if len(repos.Repos) != 1 || repos.Repos[0].Name != "" || repos.Repos[0].Path != "." {
		b.t.Fatalf("repos = %+v, want the workspace's one default repository", repos.Repos)
	}
	out := map[string]webapi.RepoBranch{}
	for _, br := range repos.Repos[0].Branches {
		out[br.Name] = br
	}
	return out
}

// The view's one destructive write deletes a branch nobody holds, and
// nothing else: a card's branch, an adopted one, a branch a card forks
// from and the base are each refused whatever the request says.
func TestReposViewDeletesOnlyBranchesNoCardHolds(t *testing.T) {
	b := newBoardHarness(t)
	held := b.card(1, "Loader", domain.StageImplement)
	adopted := domain.Feature{
		ID: "FD-002", Num: 2, Title: "Auth rework", Slug: "auth-rework", Kind: domain.KindFeature,
		Stage: domain.StagePlan, BranchScheme: domain.BranchSchemeAdopted, Branch: "pr/412",
	}
	forking := domain.Feature{
		ID: "FD-003", Num: 3, Title: "Backport", Slug: "backport", Kind: domain.KindFeature,
		Stage: domain.StageTodo, BranchScheme: domain.BranchSchemeKind, Base: "release",
	}
	for _, f := range []*domain.Feature{&adopted, &forking} {
		if err := b.store.CreateFeature(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	b.reload()
	for _, name := range []string{held.BranchName(), "pr/412", "release", "spike/old"} {
		b.git(b.root, "branch", "-q", name)
	}
	b.git(b.root, "checkout", "-q", "-b", "wip/simon")
	b.commit(b.root, "wip.txt", "wip")
	b.git(b.root, "checkout", "-q", "main")

	got := b.repoBranches()
	for name, want := range map[string]struct{ group, del, card string }{
		"main":            {"base", "", ""},
		held.BranchName(): {"cards", "", "FD-001"},
		"pr/412":          {"held", "", "FD-002"},
		"release":         {"unowned", "", ""},
		"spike/old":       {"unowned", "ok", ""},
		"wip/simon":       {"unowned", "confirm", ""},
	} {
		br, ok := got[name]
		card := ""
		if br.Card != nil {
			card = br.Card.ID
		}
		if !ok || br.Group != want.group || br.Delete != want.del || card != want.card {
			t.Errorf("%s = %+v (card %q), want group %q delete %q card %q", name, br, card, want.group, want.del, want.card)
		}
	}
	if got["wip/simon"].AheadBase != 1 || got["release"].Why == "" || len(got["release"].ForkedBy) != 1 {
		t.Errorf("wip/simon = %+v, release = %+v", got["wip/simon"], got["release"])
	}

	del := func(branch string, force bool) int {
		return b.call(http.MethodPost, "/api/repos/branches/delete", webapi.RepoRequest{Branch: branch, Force: force}, nil)
	}
	for _, name := range []string{"main", held.BranchName(), "pr/412", "release"} {
		if code := del(name, true); code != http.StatusConflict {
			t.Errorf("deleting %s = %d, want a refusal", name, code)
		}
	}
	if code := del("wip/simon", false); code != http.StatusConflict {
		t.Errorf("deleting unmerged wip/simon unforced = %d, want a refusal", code)
	}
	if code := del("nope", false); code != http.StatusNotFound {
		t.Errorf("deleting a branch that is not there = %d, want 404", code)
	}
	if code := del("", false); code != http.StatusBadRequest {
		t.Errorf("deleting with no branch named = %d, want 400", code)
	}
	if code := b.call(http.MethodPost, "/api/repos/branches/delete", webapi.RepoRequest{Repo: "elsewhere", Branch: "spike/old"}, nil); code != http.StatusNotFound {
		t.Errorf("deleting in an unknown repository = %d, want 404", code)
	}
	var out webapi.Outcome
	b.must(http.StatusOK, http.MethodPost, "/api/repos/branches/delete", webapi.RepoRequest{Branch: "spike/old"}, &out)
	if !strings.Contains(out.Text, "spike/old") {
		t.Errorf("outcome = %+v", out)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/repos/branches/delete", webapi.RepoRequest{Branch: "wip/simon", Force: true}, nil)

	left := b.repoBranches()
	if len(left) != 4 {
		t.Fatalf("branches left = %v, want the base, the card's, the adopted one and the forked-from one", left)
	}
}

// Fetch reads the remote and moves no local branch; fast-forward then
// moves the base up to it, and is the only thing that does.
func TestReposViewFetchesThenFastForwardsTheBase(t *testing.T) {
	b := newBoardHarness(t)
	if code := b.call(http.MethodPost, "/api/repos/fetch", webapi.RepoRequest{}, nil); code != http.StatusConflict {
		t.Fatalf("fetch with no remote = %d, want a refusal", code)
	}
	var out webapi.Outcome
	b.must(http.StatusOK, http.MethodPost, "/api/repos/fetch", webapi.RepoRequest{All: true}, &out)
	if !strings.Contains(out.Text, "nothing to fetch") {
		t.Fatalf("fetch all with no remote anywhere = %+v", out)
	}

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare, other := filepath.Join(dir, "origin.git"), filepath.Join(dir, "other")
	b.git(dir, "clone", "-q", "--bare", b.root, bare)
	b.git(b.root, "remote", "add", "origin", bare)
	b.git(b.root, "fetch", "-q", "origin")
	b.git(b.root, "branch", "-q", "--set-upstream-to=origin/main", "main")
	b.git(dir, "clone", "-q", bare, other)
	b.commit(other, "remote.txt", "remote work")
	b.git(other, "push", "-q", "origin", "main")

	before := b.git(b.root, "rev-parse", "main")
	b.must(http.StatusOK, http.MethodPost, "/api/repos/fetch", webapi.RepoRequest{}, nil)
	if at := b.git(b.root, "rev-parse", "main"); at != before {
		t.Fatalf("a fetch moved main: %s → %s", before, at)
	}
	if main := b.repoBranches()["main"]; main.Behind != 1 || main.Upstream != "origin/main" {
		t.Fatalf("main after the fetch = %+v, want one behind origin/main", main)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/repos/fastforward", webapi.RepoRequest{}, &out)
	if !strings.Contains(out.Text, "fast-forwarded") {
		t.Errorf("outcome = %+v", out)
	}
	if at := b.git(b.root, "rev-parse", "main"); at != b.git(b.root, "rev-parse", "origin/main") || at == before {
		t.Fatalf("main is at %s after the fast-forward", at)
	}
	if main := b.repoBranches()["main"]; main.Behind != 0 {
		t.Fatalf("main after the fast-forward = %+v", main)
	}

	// commits on both sides are not this verb's to reconcile
	b.commit(b.root, "local.txt", "local work")
	b.commit(other, "remote2.txt", "more remote work")
	b.git(other, "push", "-q", "origin", "main")
	b.must(http.StatusOK, http.MethodPost, "/api/repos/fetch", webapi.RepoRequest{}, nil)
	if code := b.call(http.MethodPost, "/api/repos/fastforward", webapi.RepoRequest{}, nil); code != http.StatusConflict {
		t.Fatalf("fast-forwarding a diverged base = %d, want a refusal", code)
	}
}

// A repository's remotes are added, renamed, pointed elsewhere and
// removed from the view, a branch is told what to track, and a
// credential in a remote's URL never reaches the page.
func TestReposViewManagesRemotesAndUpstreams(t *testing.T) {
	b := newBoardHarness(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "origin.git")
	b.git(dir, "clone", "-q", "--bare", b.root, bare)
	post := func(route string, req webapi.RepoRequest) int {
		return b.call(http.MethodPost, "/api/repos/"+route, req, nil)
	}
	repo := func() webapi.Repo {
		var repos webapi.Repos
		b.must(http.StatusOK, http.MethodGet, "/api/repos", nil, &repos)
		return repos.Repos[0]
	}

	for route, req := range map[string]webapi.RepoRequest{
		"remotes/add":       {URL: bare},
		"remotes/rename":    {NewName: "x"},
		"remotes/remove":    {},
		"branches/upstream": {Upstream: "origin/main"},
	} {
		if code := post(route, req); code != http.StatusBadRequest {
			t.Errorf("%s naming nothing = %d, want 400", route, code)
		}
	}
	if code := post("remotes/add", webapi.RepoRequest{Remote: "evil", URL: "ext::sh -c 'touch pwned'"}); code != http.StatusBadRequest {
		t.Errorf("adding a transport-helper URL = %d, want 400", code)
	}
	if code := post("remotes/remove", webapi.RepoRequest{Remote: "origin"}); code != http.StatusNotFound {
		t.Errorf("removing a remote that is not there = %d, want 404", code)
	}

	var out webapi.Outcome
	b.must(http.StatusOK, http.MethodPost, "/api/repos/remotes/add", webapi.RepoRequest{Remote: "origin", URL: "https://simon:hunter2@example.invalid/o/r.git"}, &out)
	if strings.Contains(out.Text, "hunter2") {
		t.Errorf("the outcome quotes the credential: %q", out.Text)
	}
	got := repo()
	if len(got.Remotes) != 1 || !got.Remotes[0].Secret || strings.Contains(got.Remotes[0].URL+got.Origin, "hunter2") || !strings.Contains(got.Origin, "example.invalid") {
		t.Fatalf("remotes = %+v, origin = %q: want the credential masked", got.Remotes, got.Origin)
	}
	if code := post("remotes/add", webapi.RepoRequest{Remote: "origin", URL: bare}); code != http.StatusConflict {
		t.Errorf("adding a taken name = %d, want a refusal", code)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/repos/remotes/seturl", webapi.RepoRequest{Remote: "origin", URL: bare}, nil)
	b.must(http.StatusOK, http.MethodPost, "/api/repos/fetch", webapi.RepoRequest{Remote: "origin"}, nil)
	if code := post("fetch", webapi.RepoRequest{Remote: "nope"}); code != http.StatusConflict {
		t.Errorf("fetching a remote that is not there = %d, want a refusal", code)
	}
	if got = repo(); len(got.RemoteBranches) != 1 || got.RemoteBranches[0] != "origin/main" || got.Remotes[0].Secret || got.Remotes[0].URL != bare {
		t.Fatalf("after the fetch: remotes %+v, remote branches %v", got.Remotes, got.RemoteBranches)
	}

	before := b.git(b.root, "rev-parse", "main")
	b.must(http.StatusOK, http.MethodPost, "/api/repos/branches/upstream", webapi.RepoRequest{Branch: "main", Upstream: "origin/main"}, nil)
	if main := b.repoBranches()["main"]; main.Upstream != "origin/main" || main.Remote != "origin" {
		t.Fatalf("main = %+v, want it tracking origin/main", main)
	}
	if code := post("branches/upstream", webapi.RepoRequest{Branch: "main", Upstream: "origin/nope"}); code != http.StatusConflict {
		t.Errorf("tracking a branch no fetch has seen = %d, want a refusal", code)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/repos/remotes/rename", webapi.RepoRequest{Remote: "origin", NewName: "upstream"}, nil)
	if main := b.repoBranches()["main"]; main.Upstream != "upstream/main" {
		t.Fatalf("main after the rename = %+v, want it tracking upstream/main", main)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/repos/remotes/remove", webapi.RepoRequest{Remote: "upstream"}, nil)
	got = repo()
	if got.Remote || len(got.Remotes) != 0 || len(got.RemoteBranches) != 0 {
		t.Fatalf("after the remove: %+v", got)
	}
	if main := b.repoBranches()["main"]; main.Upstream != "" || b.git(b.root, "rev-parse", "main") != before {
		t.Fatalf("main after its remote went = %+v, want it tracking nothing and where it was", main)
	}
}
