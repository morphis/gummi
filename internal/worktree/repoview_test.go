package worktree

import (
	"errors"
	"path/filepath"
	"testing"
)

// withOrigin gives root a bare `origin` holding main, tracked, and a
// second clone to push from, so a test can move the remote behind root.
func withOrigin(t *testing.T, root string) (other string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "origin.git")
	mustGit(t, dir, "clone", "-q", "--bare", root, bare)
	mustGit(t, root, "remote", "add", "origin", bare)
	mustGit(t, root, "fetch", "-q", "origin")
	mustGit(t, root, "branch", "-q", "--set-upstream-to=origin/main", "main")
	other = filepath.Join(dir, "other")
	mustGit(t, dir, "clone", "-q", bare, other)
	mustGit(t, other, "config", "user.name", "other")
	mustGit(t, other, "config", "user.email", "other@example.invalid")
	return other
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	writeFile(t, dir, name, msg+"\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-q", "-m", msg)
}

func branchNamed(t *testing.T, m *Manager, name string) BranchInfo {
	t.Helper()
	branches, err := m.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no branch %s in %+v", name, branches)
	return BranchInfo{}
}

func TestBranchesStandAgainstTheBaseAndTheirUpstream(t *testing.T) {
	root := newRepo(t)
	other := withOrigin(t, root)
	m := newManager(t, root)

	mustGit(t, root, "checkout", "-q", "-b", "topic")
	commitFile(t, root, "a.txt", "topic work")
	mustGit(t, root, "push", "-q", "-u", "origin", "topic")
	commitFile(t, root, "b.txt", "more topic work")
	mustGit(t, root, "checkout", "-q", "main")
	commitFile(t, root, "c.txt", "main work")
	mustGit(t, root, "branch", "-q", "idle")
	wt := filepath.Join(filepath.Dir(other), "elsewhere")
	mustGit(t, root, "worktree", "add", "-q", wt, "idle")

	topic := branchNamed(t, m, "topic")
	if topic.AheadBase != 2 || topic.BehindBase != 1 {
		t.Errorf("topic against main = +%d -%d, want +2 -1", topic.AheadBase, topic.BehindBase)
	}
	if topic.Upstream != "origin/topic" || topic.Ahead != 1 || topic.Behind != 0 || topic.UpstreamGone {
		t.Errorf("topic against its upstream = %+v", topic)
	}
	if topic.Subject != "more topic work" || topic.Committed.IsZero() || len(topic.SHA) != 40 {
		t.Errorf("topic tip = %+v", topic)
	}
	main := branchNamed(t, m, "main")
	if main.AheadBase != 0 || main.BehindBase != 0 || main.Ahead != 1 || main.Worktree != root {
		t.Errorf("main = %+v", main)
	}
	idle := branchNamed(t, m, "idle")
	if idle.Upstream != "" || idle.Worktree != wt || idle.AheadBase != 0 {
		t.Errorf("idle = %+v", idle)
	}

	// the remote branch goes; a pruning fetch is what notices
	mustGit(t, other, "push", "-q", "origin", "--delete", "topic")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if topic = branchNamed(t, m, "topic"); !topic.UpstreamGone {
		t.Errorf("topic after its remote branch was deleted = %+v, want its upstream gone", topic)
	}
	if st := m.Status(ctx); st.Base != "main" || st.Origin == "" || len(st.Remotes) != 1 || st.Fetched.IsZero() || st.Dirty {
		t.Errorf("status = %+v", st)
	}
}

func TestFetchNeedsARemote(t *testing.T) {
	m := newManager(t, newRepo(t))
	if err := m.Fetch(ctx); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Fetch with no remote = %v, want ErrNoRemote", err)
	}
	if _, _, err := m.FastForwardBase(ctx); !errors.Is(err, ErrNoUpstream) {
		t.Fatalf("FastForwardBase with no upstream = %v, want ErrNoUpstream", err)
	}
}

func TestFastForwardBaseOnlyEverFastForwards(t *testing.T) {
	root := newRepo(t)
	other := withOrigin(t, root)
	m := newManager(t, root)

	if from, to, err := m.FastForwardBase(ctx); err != nil || from != to {
		t.Fatalf("level with upstream: %s → %s, %v; want no move", from, to, err)
	}

	commitFile(t, other, "remote.txt", "remote work")
	mustGit(t, other, "push", "-q", "origin", "main")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if b := branchNamed(t, m, "main"); b.Behind != 1 || b.Ahead != 0 {
		t.Fatalf("main after the fetch = %+v, want one behind", b)
	}

	// uncommitted tracked work in the main checkout holds it
	writeFile(t, root, "README.md", "edited\n")
	if _, _, err := m.FastForwardBase(ctx); !errors.Is(err, ErrMainDirty) {
		t.Fatalf("over a dirty checkout = %v, want ErrMainDirty", err)
	}
	mustGit(t, root, "checkout", "-q", "--", "README.md")

	from, to, err := m.FastForwardBase(ctx)
	if err != nil || from == to {
		t.Fatalf("FastForwardBase = %s → %s, %v", from, to, err)
	}
	if head := mustGit(t, root, "rev-parse", "HEAD"); head != to || head != mustGit(t, root, "rev-parse", "origin/main") {
		t.Fatalf("main is at %s, want origin/main (%s)", head, to)
	}

	// commits on both sides: not this verb's to reconcile
	commitFile(t, root, "local.txt", "local work")
	commitFile(t, other, "remote2.txt", "more remote work")
	mustGit(t, other, "push", "-q", "origin", "main")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	before := mustGit(t, root, "rev-parse", "HEAD")
	if _, _, err := m.FastForwardBase(ctx); !errors.Is(err, ErrDiverged) {
		t.Fatalf("diverged = %v, want ErrDiverged", err)
	}
	if head := mustGit(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatalf("a refused fast-forward moved main: %s → %s", before, head)
	}
}

func TestDeleteBranchNamedKeepsWhatWouldBeLost(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	mustGit(t, root, "branch", "-q", "merged")
	mustGit(t, root, "checkout", "-q", "-b", "spike")
	commitFile(t, root, "s.txt", "spike")
	mustGit(t, root, "checkout", "-q", "main")
	wt := filepath.Join(t.TempDir(), "out")
	mustGit(t, root, "branch", "-q", "out")
	mustGit(t, root, "worktree", "add", "-q", wt, "out")

	if err := m.DeleteBranchNamed(ctx, "spike", false); !errors.Is(err, ErrBranchUnmerged) {
		t.Fatalf("unmerged, unforced = %v, want ErrBranchUnmerged", err)
	}
	if err := m.DeleteBranchNamed(ctx, "out", true); !errors.Is(err, ErrBranchInUse) {
		t.Fatalf("checked out = %v, want ErrBranchInUse", err)
	}
	if err := m.DeleteBranchNamed(ctx, "main", true); !errors.Is(err, ErrBranchInUse) {
		t.Fatalf("the base = %v, want ErrBranchInUse", err)
	}
	if err := m.DeleteBranchNamed(ctx, "nope", false); err == nil {
		t.Fatal("deleting a branch that does not exist succeeded")
	}
	for _, c := range []struct {
		name  string
		force bool
	}{{"merged", false}, {"spike", true}} {
		if err := m.DeleteBranchNamed(ctx, c.name, c.force); err != nil {
			t.Fatalf("deleting %s: %v", c.name, err)
		}
	}
	names, err := m.ListBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "main" || names[1] != "out" {
		t.Fatalf("branches left = %v, want main and out", names)
	}
}

func TestRemotesAreListedAndEditedInTheRepositorysConfig(t *testing.T) {
	root := newRepo(t)
	withOrigin(t, root)
	m := newManager(t, root)
	origin := m.RemoteList(ctx)[0].URL

	if err := m.AddRemote(ctx, "fork", "https://simon:hunter2@example.invalid/simon/board.git"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRemote(ctx, "fork", origin); !errors.Is(err, ErrRemoteExists) {
		t.Errorf("adding a taken name = %v, want ErrRemoteExists", err)
	}
	for _, name := range []string{"", "-x", "a b", "a/b", "a..b", "x.lock"} {
		if err := m.AddRemote(ctx, name, origin); !errors.Is(err, ErrRemoteName) {
			t.Errorf("adding remote %q = %v, want ErrRemoteName", name, err)
		}
	}
	for _, url := range []string{"", "--upload-pack=touch x", "ext::sh -c 'touch pwned'", "a\nb"} {
		if err := m.AddRemote(ctx, "bad", url); !errors.Is(err, ErrRemoteURL) {
			t.Errorf("adding URL %q = %v, want ErrRemoteURL", url, err)
		}
	}

	remotes := m.RemoteList(ctx)
	if len(remotes) != 2 || remotes[0].Name != "origin" || remotes[0].Tracking != 1 || remotes[1].Name != "fork" || remotes[1].Tracking != 0 {
		t.Fatalf("remotes = %+v, want origin tracked by main, then fork", remotes)
	}
	if masked, secret := MaskRemoteURL(remotes[1].URL); !secret || masked != "https://•••@example.invalid/simon/board.git" {
		t.Errorf("masked fork URL = %q (secret %v)", masked, secret)
	}

	if err := m.RenameRemote(ctx, "origin", "upstream"); err != nil {
		t.Fatal(err)
	}
	if b := branchNamed(t, m, "main"); b.Upstream != "upstream/main" || b.UpstreamRemote != "upstream" {
		t.Errorf("main after the rename = %+v, want it tracking upstream/main", b)
	}
	if err := m.RenameRemote(ctx, "upstream", "fork"); !errors.Is(err, ErrRemoteExists) {
		t.Errorf("renaming onto a taken name = %v, want ErrRemoteExists", err)
	}
	if err := m.SetRemoteURL(ctx, "nope", origin); !errors.Is(err, ErrRemoteUnknown) {
		t.Errorf("pointing an unknown remote = %v, want ErrRemoteUnknown", err)
	}
	if err := m.SetRemoteURL(ctx, "fork", origin); err != nil {
		t.Fatal(err)
	}
	if err := m.FetchRemote(ctx, "fork"); err != nil {
		t.Fatal(err)
	}
	if got := m.RemoteBranches(ctx); len(got) != 2 || got[0] != "fork/main" || got[1] != "upstream/main" {
		t.Errorf("remote branches = %v, want fork/main and upstream/main", got)
	}

	before := mustGit(t, root, "rev-parse", "main")
	if err := m.RemoveRemote(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	if b := branchNamed(t, m, "main"); b.Upstream != "" || b.SHA != before {
		t.Errorf("main after its remote went = %+v, want it tracking nothing and where it was", b)
	}
	if left := m.RemoteList(ctx); len(left) != 1 || left[0].Name != "fork" {
		t.Errorf("remotes left = %+v", left)
	}
}

func TestMaskRemoteURLHidesOnlyCredentials(t *testing.T) {
	for raw, want := range map[string]string{
		"git@github.com:o/r.git":             "git@github.com:o/r.git",
		"ssh://git@github.com/o/r":           "ssh://git@github.com/o/r",
		"https://github.com/o/r.git":         "https://github.com/o/r.git",
		"/srv/git/r.git":                     "/srv/git/r.git",
		"https://ghp_token@github.com/o/r":   "https://•••@github.com/o/r",
		"https://u:p@ss@github.com/o/r@v1":   "https://•••@github.com/o/r@v1",
		"ssh://deploy:secret@host:2222/o/r":  "ssh://deploy:•••@host:2222/o/r",
		"https://user:tok@host.invalid":      "https://•••@host.invalid",
		"https://github.com/o/r@refs/tags/x": "https://github.com/o/r@refs/tags/x",
	} {
		got, secret := MaskRemoteURL(raw)
		if got != want || secret != (got != raw) {
			t.Errorf("MaskRemoteURL(%q) = %q (secret %v), want %q", raw, got, secret, want)
		}
	}
}

func TestSetUpstreamPicksWhatABranchTracksAndMovesNothing(t *testing.T) {
	root := newRepo(t)
	withOrigin(t, root)
	m := newManager(t, root)
	mustGit(t, root, "branch", "-q", "topic")
	before := branchNamed(t, m, "topic").SHA

	if err := m.SetUpstream(ctx, "topic", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if b := branchNamed(t, m, "topic"); b.Upstream != "origin/main" || b.SHA != before {
		t.Errorf("topic = %+v, want it tracking origin/main and unmoved", b)
	}
	// a local branch is not an upstream this offers, and neither is one
	// no fetch has seen
	for _, bad := range []string{"main", "origin/nope"} {
		if err := m.SetUpstream(ctx, "topic", bad); !errors.Is(err, ErrNoUpstream) {
			t.Errorf("tracking %s = %v, want ErrNoUpstream", bad, err)
		}
	}
	if err := m.SetUpstream(ctx, "nope", "origin/main"); err == nil {
		t.Error("setting the upstream of a branch that is not there succeeded")
	}
	for range 2 { // clearing twice: the second has nothing to clear
		if err := m.SetUpstream(ctx, "topic", ""); err != nil {
			t.Fatal(err)
		}
	}
	if b := branchNamed(t, m, "topic"); b.Upstream != "" {
		t.Errorf("topic = %+v, want it tracking nothing", b)
	}
}
