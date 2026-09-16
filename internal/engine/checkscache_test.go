package engine

import (
	"os"
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
