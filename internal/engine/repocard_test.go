package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoCardRepo(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	return root
}

// TestRepoCardListsTrackedFiles: the card exists so a session does not open
// by running `find`. On a small repo it is the file list itself.
func TestRepoCardListsTrackedFiles(t *testing.T) {
	root := repoCardRepo(t, "cmd/tasks/main.go", "internal/store/store.go", "README.md")
	card := buildRepoCard(root)
	for _, want := range []string{"cmd/tasks/main.go", "internal/store/store.go", "README.md"} {
		if !strings.Contains(card, want) {
			t.Errorf("card omits %q:\n%s", want, card)
		}
	}
}

// Untracked files are exactly what a session does not need: build output,
// scratch data, anything .gitignore already rules out.
func TestRepoCardSkipsUntracked(t *testing.T) {
	root := repoCardRepo(t, "main.go")
	if err := os.WriteFile(filepath.Join(root, "binary-artifact"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if card := buildRepoCard(root); strings.Contains(card, "binary-artifact") {
		t.Errorf("card names an untracked file:\n%s", card)
	}
}

// Past the listing cap the card summarizes by directory rather than
// enumerating: a manifest of a large repository is one nobody reads, and it
// would crowd out the rest of the prompt.
func TestRepoCardSummarizesALargeRepo(t *testing.T) {
	var files []string
	for i := 0; i < maxRepoCardFiles+40; i++ {
		files = append(files, fmt.Sprintf("internal/pkg/f%03d.go", i))
	}
	files = append(files, "README.md")
	root := repoCardRepo(t, files...)
	card := buildRepoCard(root)
	if strings.Contains(card, "too many to list") == false {
		t.Fatalf("large repo was enumerated instead of summarized:\n%s", card)
	}
	if !strings.Contains(card, "internal  (") {
		t.Errorf("summary does not name the biggest directory:\n%s", card)
	}
	if n := strings.Count(card, "\n"); n > maxRepoCardDirs+8 {
		t.Errorf("summary is %d lines; it must stay bounded", n)
	}
}

// A non-git directory yields no card at all — the session then opens
// exactly as it did before the card existed.
func TestRepoCardAbsentOutsideGit(t *testing.T) {
	if card := buildRepoCard(t.TempDir()); card != "" {
		t.Errorf("non-git root produced a card:\n%s", card)
	}
	if card := buildRepoCard(""); card != "" {
		t.Errorf("empty root produced a card:\n%s", card)
	}
}

// The card is computed once per repository root, not once per session.
func TestRepoCardCachedPerRoot(t *testing.T) {
	root := repoCardRepo(t, "main.go")
	e := &Engine{}
	first := e.repoCard(root)
	if first == "" {
		t.Fatal("no card for a git repo")
	}
	// a file added after the first call must not appear: the card is a
	// per-Engine snapshot, and recomputing it per session is the cost this
	// exists to avoid
	if err := os.WriteFile(filepath.Join(root, "late.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", root, "add", "-A").Run()
	if second := e.repoCard(root); second != first {
		t.Error("card recomputed on the second call; it must be cached per root")
	}
}
