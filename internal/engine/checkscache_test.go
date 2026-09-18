package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// The fingerprint answers one question: has anything that decides the
// build changed? A new commit that touches source has not.
func TestChecksFingerprintTracksBuildFilesOnly(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Makefile"), "build:\n\tgo build ./...\n")
	write(t, filepath.Join(root, "main.go"), "package main\n")

	first := checksFingerprint(root)
	if first == "" {
		t.Fatal("a repo with a Makefile has no fingerprint")
	}

	write(t, filepath.Join(root, "main.go"), "package main // edited\n")
	if got := checksFingerprint(root); got != first {
		t.Error("editing source invalidated the survey")
	}

	write(t, filepath.Join(root, "Makefile"), "build:\n\tgo build -tags pin ./...\n")
	if got := checksFingerprint(root); got == first {
		t.Error("changing how the repo builds did NOT invalidate the survey")
	}
}

// A CI definition is the most direct statement a repo makes about its
// checks, so it counts too.
func TestChecksFingerprintCoversCIDefinitions(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module x\n")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := checksFingerprint(root)
	write(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "jobs: {}\n")
	if got := checksFingerprint(root); got == first {
		t.Error("adding a CI workflow did not invalidate the survey")
	}
}

// A repository that states nothing about its own build has nothing stable
// to key on, and is surveyed every time rather than remembered wrongly.
func TestChecksFingerprintEmptyWithoutBuildFiles(t *testing.T) {
	if got := checksFingerprint(t.TempDir()); got != "" {
		t.Errorf("a repo with no build files produced a fingerprint: %q", got)
	}
	if got := checksFingerprint(""); got != "" {
		t.Errorf("an empty root produced a fingerprint: %q", got)
	}
}

// The round trip: the second card in a repo reads the first card's survey
// instead of paying for its own, and stops reading it the moment the
// repo's build changes.
func TestChecksCacheRoundTrip(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	root := t.TempDir()
	write(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	if _, ok := e.cachedChecks(root); ok {
		t.Fatal("an unsurveyed repo reported a cached survey")
	}

	checks := []domain.Check{{Name: "test", Cmd: "go test ./..."}}
	e.rememberChecks(root, checks)

	got, ok := e.cachedChecks(root)
	if !ok {
		t.Fatal("the remembered survey did not come back")
	}
	if len(got) != 1 || got[0].Cmd != "go test ./..." {
		t.Errorf("the survey came back changed: %+v", got)
	}

	write(t, filepath.Join(root, "Makefile"), "test:\n\tgo test -race ./...\n")
	if _, ok := e.cachedChecks(root); ok {
		t.Error("a repo that changed how it tests still served the old survey")
	}
}

// Two repositories in one workspace are remembered separately.
func TestChecksCacheKeyedByRepo(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "go.mod"), "module a\n")
	write(t, filepath.Join(b, "package.json"), "{}\n")

	e.rememberChecks(a, []domain.Check{{Name: "go", Cmd: "go test ./..."}})
	e.rememberChecks(b, []domain.Check{{Name: "js", Cmd: "npm test"}})

	gotA, okA := e.cachedChecks(a)
	gotB, okB := e.cachedChecks(b)
	if !okA || !okB {
		t.Fatal("a repo lost its survey to the other")
	}
	if gotA[0].Cmd == gotB[0].Cmd {
		t.Errorf("the two repos share one survey: %q", gotA[0].Cmd)
	}
}

// The cache is an optimisation. A corrupt file is a survey that has to run
// again, never a card that cannot start.
func TestChecksCacheSurvivesCorruption(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module x\n")
	e.rememberChecks(root, []domain.Check{{Name: "test", Cmd: "go test ./..."}})

	write(t, e.checksCachePath(), "{not json")
	if _, ok := e.cachedChecks(root); ok {
		t.Error("a corrupt cache served a survey")
	}
	e.rememberChecks(root, []domain.Check{{Name: "test", Cmd: "go test ./..."}})
	if _, ok := e.cachedChecks(root); !ok {
		t.Error("a corrupt cache could not be rewritten")
	}
}

// A second card in the same repository is a second PATH — gummi's model
// is a worktree per card — so a cache that can only hit on the path it was
// written at can never help the card it exists for. A goal makes it
// certain: its children resolve their repo root through the goal's own
// worktree while the goal's gate resolves the workspace root. On the lxd
// autopilot drive the cache ended the run holding two entries with
// identical fingerprints and different keys, the second survey having
// re-derived a byte-identical answer for 79.3 credits.
func TestChecksCacheFollowsTheFingerprintAcrossWorktrees(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	repo := t.TempDir()
	write(t, filepath.Join(repo, "go.mod"), "module x\n")
	write(t, filepath.Join(repo, "Makefile"), "test:\n\tgo test ./...\n")

	// the same repository content checked out again, as a card's worktree is
	worktree := filepath.Join(t.TempDir(), ".gummi", "worktrees", "FD-002")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(worktree, "go.mod"), "module x\n")
	write(t, filepath.Join(worktree, "Makefile"), "test:\n\tgo test ./...\n")

	want := []domain.Check{{Name: "test", Cmd: "go test ./..."}}
	e.rememberChecks(repo, want)

	got, ok := e.cachedChecks(worktree)
	if !ok {
		t.Fatal("the second card in one repo surveyed again from scratch")
	}
	if got[0].Cmd != want[0].Cmd {
		t.Errorf("cachedChecks = %q, want the first card's %q — the point of the "+
			"cache is that the second card gets the SAME floor", got[0].Cmd, want[0].Cmd)
	}

	// A repo that really does build differently is still surveyed again.
	other := t.TempDir()
	write(t, filepath.Join(other, "go.mod"), "module x\n")
	write(t, filepath.Join(other, "Makefile"), "test:\n\tgo test -race ./...\n")
	if _, ok := e.cachedChecks(other); ok {
		t.Error("a repo with different build files was served another's survey")
	}
}

// Two entries sharing a fingerprint are two independent surveys that need
// not have agreed — exactly what the drive's cache held. Reuse has to pick
// the same one every time, or the floor a card is held to depends on map
// order.
func TestChecksCachePicksTheSameEntryEveryTime(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	base := t.TempDir()
	seed := func(dir string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "go.mod"), "module x\n")
	}
	shortRoot := filepath.Join(base, "r")
	longRoot := filepath.Join(base, "r", ".gummi", "worktrees", "GL-001")
	seed(shortRoot)
	seed(longRoot)
	e.rememberChecks(longRoot, []domain.Check{{Name: "a", Cmd: "go test ./x/..."}})
	e.rememberChecks(shortRoot, []domain.Check{{Name: "b", Cmd: "go test ./..."}})

	fresh := filepath.Join(base, "other")
	seed(fresh)
	first, ok := e.cachedChecks(fresh)
	if !ok {
		t.Fatal("no entry reused")
	}
	for i := 0; i < 20; i++ {
		got, ok := e.cachedChecks(fresh)
		if !ok || got[0].Cmd != first[0].Cmd {
			t.Fatalf("reuse is not deterministic: %q then %q", first[0].Cmd, got[0].Cmd)
		}
	}
	if first[0].Cmd != "go test ./..." {
		t.Errorf("reused %q, want the root nearest the workspace (%q)",
			first[0].Cmd, "go test ./...")
	}
}

// The measured miss. A repository's own .gitignore can hide a file the
// fingerprint reads — yq ignores `test*.yml`, which covers its own
// .github/workflows/test-yq.yml — and a fresh worktree does not have it.
// Hashing what git does not track therefore made the workspace root and
// every worktree of it disagree by construction, so the goal card that
// should have reused its workspace's survey paid for a second one.
func TestChecksFingerprintIgnoresWhatGitDoesNotTrack(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "go.mod"), "module x\n")
	write(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")
	write(t, filepath.Join(root, ".gitignore"), "test*.yml\n")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "jobs: {}\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "repo")

	tracked := checksFingerprint(root)
	if tracked == "" {
		t.Fatal("a repo with a Makefile has no fingerprint")
	}
	// the repo's own ignore rule hides this one from git, and from any
	// worktree checked out of it
	write(t, filepath.Join(root, ".github", "workflows", "test-it.yml"), "jobs: {}\n")
	if got := checksFingerprint(root); got != tracked {
		t.Error("a file git does not track changed what the repo is fingerprinted as")
	}

	tree := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "-b", "goal/x", tree)
	if got := checksFingerprint(tree); got != tracked {
		t.Errorf("a worktree of the repo fingerprints differently from the repo:\n %s\n %s", got, tracked)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
