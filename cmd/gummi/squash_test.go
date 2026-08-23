package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// squashCLIRepo builds a temp repo with a feature worktree holding three
// checkpoint commits, the card parked at StageImplement (not done, not
// verified — squash needs neither), and refs/remotes/origin/main faked to
// the repo's initial commit so the zero-dependency collapse-base arm
// resolves without a real remote. It chdirs the test process into the repo
// root, matching every other headless-command test in this package.
func squashCLIRepo(t *testing.T) (*state.Store, domain.Feature) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cliGit(t, root, "init", "-q", "-b", "main")
	cliGit(t, root, "config", "user.name", "t")
	cliGit(t, root, "config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-q", "-m", "init")
	cliGit(t, root, "update-ref", "refs/remotes/origin/main", cliGit(t, root, "rev-parse", "HEAD"))

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	id, _ := domain.NewFeatureID(9)
	slug, _ := domain.Slugify("JSON export")
	f := domain.Feature{
		ID: id, Num: 9, Kind: domain.KindFeature, Title: "JSON export", Slug: slug,
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	p, err := wt.Create(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		name := filepath.Join(p, "cp.txt")
		if err := os.WriteFile(name, []byte(strings.Repeat("x", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		cliGit(t, p, "add", ".")
		cliGit(t, p, "commit", "-q", "-m", "checkpoint")
	}
	return store, f
}

// A card with checkpoint commits collapses to one commit off origin/main and
// exits 0, printing the follow-up force-with-lease push hint.
func TestSquashCommand_Happy(t *testing.T) {
	_, f := squashCLIRepo(t)
	base := cliGit(t, ".", "rev-parse", "origin/main")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSquash([]string{string(f.ID), "-m", "feat(export): collapsed"})
	})
	if runErr != nil {
		t.Fatalf("runSquash: %v", runErr)
	}
	if !strings.Contains(out, "squashed to") || !strings.Contains(out, "git push --force-with-lease origin "+f.BranchName()) {
		t.Fatalf("stdout = %q, want the squashed + push-hint lines", out)
	}
	if n := cliGit(t, ".", "rev-list", "--count", base+".."+f.BranchName()); n != "1" {
		t.Fatalf("commits beyond base = %s, want 1", n)
	}
	if msg := cliGit(t, filepath.Join(".gummi", "worktrees", string(f.ID)), "log", "-1", "--format=%s"); msg != "feat(export): collapsed" {
		t.Fatalf("collapsed subject = %q", msg)
	}
}

// The cobra layer accepts the documented -m shorthand and routes through to
// runSquash end-to-end (guards against the flag being registered without a
// shorthand, the same regression TestMergeCobraShorthandFlag guards for merge).
func TestSquashCobraShorthandFlag(t *testing.T) {
	_, f := squashCLIRepo(t)
	rootCmd.SetArgs([]string{"squash", string(f.ID), "-m", "feat(export): collapsed"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute(squash -m): %v", err)
	}
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	if msg := cliGit(t, wtPath, "log", "-1", "--format=%s"); msg != "feat(export): collapsed" {
		t.Fatalf("collapsed subject = %q", msg)
	}
}

// -m - reads the message from stdin, trimmed of its trailing newline.
func TestSquashCommand_StdinMessage(t *testing.T) {
	_, f := squashCLIRepo(t)

	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("feat(export): from stdin\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	if err := runSquash([]string{string(f.ID), "-m", "-"}); err != nil {
		t.Fatalf("runSquash: %v", err)
	}
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	if msg := cliGit(t, wtPath, "log", "-1", "--format=%s"); msg != "feat(export): from stdin" {
		t.Fatalf("collapsed subject = %q", msg)
	}
}

// A missing -m fails before touching git.
func TestSquashCommand_RequiresMessage(t *testing.T) {
	if err := runSquash([]string{"FD-009"}); err == nil {
		t.Fatal("squash without -m accepted")
	}
}

// A linked PR with an open review thread refuses without --force and
// proceeds (with a stderr warning) when --force is passed.
func TestSquashOpenThreadsGate(t *testing.T) {
	store, f := squashCLIRepo(t)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 3, URL: "https://github.com/o/r/pull/3", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := store.SetPullRequest(context.Background(), f.ID, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddDiffAnnotation(context.Background(), domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "a", Excerpt: "line", Comment: "please fix",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := runSquash([]string{string(f.ID), "-m", "feat(export): collapsed"}); err == nil {
		t.Fatal("squash accepted despite open review threads")
	} else if !strings.Contains(err.Error(), "open review threads") {
		t.Fatalf("error %q does not name the open-threads refusal", err)
	}
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	if n := cliGit(t, wtPath, "rev-list", "--count", "HEAD"); n != "4" {
		t.Fatalf("branch mutated by a refused squash: %s commits, want 4 (init + 3 checkpoints)", n)
	}

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	runErr := runSquash([]string{string(f.ID), "-m", "feat(export): collapsed", "--force"})
	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	io.Copy(&buf, r)

	if runErr != nil {
		t.Fatalf("runSquash --force: %v", runErr)
	}
	if !strings.Contains(buf.String(), "outdate") {
		t.Fatalf("stderr = %q, want an outdating warning", buf.String())
	}
}
