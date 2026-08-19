package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestDoctorMultiRepoForkDrift: fork drift is grouped per card's own repo —
// a drifted card in a named repo is listed under that repo's name, and a
// clean card in the default repo is not flagged.
func TestDoctorMultiRepoForkDrift(t *testing.T) {
	clearDoctorEnv(t)
	ws := t.TempDir()
	ws, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(ws, "init", "-q", "-b", "main")
	git(ws, "config", "user.name", "t")
	git(ws, "config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("ws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(ws, "add", ".")
	git(ws, "commit", "-q", "-m", "ws init")
	wsHead := git(ws, "rev-parse", "HEAD")

	incus := filepath.Join(ws, "git", "incus")
	if err := os.MkdirAll(incus, 0o750); err != nil {
		t.Fatal(err)
	}
	git(incus, "init", "-q", "-b", "main")
	git(incus, "config", "user.name", "t")
	git(incus, "config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(incus, "README.md"), []byte("incus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(incus, "add", ".")
	git(incus, "commit", "-q", "-m", "incus init")
	incusHead := git(incus, "rev-parse", "HEAD")

	// config: default is the workspace root; named repo "incus".
	writeConfig(t, ws, "repos:\n  incus: git/incus\n")

	wsInfo, err := state.Init(ws, ws)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.OpenStore(wsInfo.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := time.Now()
	mk := func(num int, title, repo, fork string) domain.Feature {
		t.Helper()
		id, _ := domain.NewFeatureID(num)
		slug, _ := domain.Slugify(title)
		f := domain.Feature{
			ID: id, Num: num, Title: title, Slug: slug,
			Stage: domain.StageSpec, Repo: repo, CreatedAt: now, UpdatedAt: now,
		}
		if err := st.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if err := st.SetForkPoint(ctx, id, fork); err != nil {
			t.Fatal(err)
		}
		return f
	}
	cleanDefault := mk(1, "Clean default", "", wsHead)
	driftedIncus := mk(2, "Drifted incus", "incus", incusHead)

	// rewind the incus repo's main to an unrelated lineage so incusHead is
	// no longer an ancestor of its main HEAD.
	if err := os.WriteFile(filepath.Join(incus, "rewound.ts"), []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(incus, "add", ".")
	git(incus, "checkout", "-q", "--orphan", "tmp")
	git(incus, "commit", "-q", "-m", "rewound")
	git(incus, "branch", "-M", "tmp", "main")

	r := buildDoctorReport(ws, doctorOpts{})
	c := checkByName(r, "fork-drift")
	if c.Status != statusWarn {
		t.Fatalf("fork-drift = %+v, want warn", c)
	}
	if !strings.Contains(c.Detail, string(driftedIncus.ID)) || !strings.Contains(c.Detail, "[incus]") {
		t.Errorf("detail %q should name the drifted incus card and its repo", c.Detail)
	}
	if strings.Contains(c.Detail, string(cleanDefault.ID)) {
		t.Errorf("detail %q should not flag the clean default-repo card", c.Detail)
	}

	// each configured repo is reported separately.
	repoIncus := checkByName(r, "repo:incus")
	if repoIncus.Status != statusOK {
		t.Errorf("repo:incus check = %+v, want ok", repoIncus)
	}
}
