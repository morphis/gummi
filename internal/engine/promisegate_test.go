package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// promiseRepo makes a git repo with one tracked file, so git grep has
// something to answer with.
func promiseRepo(t *testing.T, tracked map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for name, body := range tracked {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	return dir
}

func promiseSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The card's own invariant, unanswered by verify, is the shape that
// shipped a branch contradicting the card description while every stage
// reported success.
func TestPromisesBlockOnAnUnansweredInvariant(t *testing.T) {
	repo := promiseRepo(t, map[string]string{"a.go": "package a\n"})
	sp := promiseSpec(t, "## Plan claims\n- invariant: every filter valid today still parses.\n")

	rep := checkPromises(context.Background(), sp, repo)
	if !rep.blocks() {
		t.Fatal("an unanswered invariant did not block")
	}
	if len(rep.Unanswered) != 1 || rep.Unanswered[0].ID != "INV-1" {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(rep.reason(), "INV-1") {
		t.Errorf("the reason does not name the invariant: %s", rep.reason())
	}
}

// Answered pass, and the floor has no opinion.
func TestPromisesPassWhenVerifyAnswers(t *testing.T) {
	repo := promiseRepo(t, map[string]string{"a.go": "package a\n"})
	sp := promiseSpec(t, "## Plan claims\n- invariant: every filter valid today still parses.\n\n"+
		"## Verification plan\nINV-1: pass — re-ran the whole pre-existing table.\n")

	if rep := checkPromises(context.Background(), sp, repo); rep.blocks() {
		t.Errorf("an answered invariant still blocked: %+v", rep)
	}
}

// Answered fail blocks: an invariant the card asked for is either still
// true of the branch or it is a fail, and "documented as a caveat" is
// what the last two drives did instead.
func TestPromisesBlockOnAFailedInvariant(t *testing.T) {
	repo := promiseRepo(t, map[string]string{"a.go": "package a\n"})
	sp := promiseSpec(t, "## Plan claims\n- invariant: literal parens in values keep working.\n\n"+
		"## Verification plan\nINV-1: fail — three such filters now 400.\n")

	rep := checkPromises(context.Background(), sp, repo)
	if len(rep.Failed) != 1 {
		t.Fatalf("a failed invariant did not block: %+v", rep)
	}
	if !strings.Contains(rep.reason(), "FAIL") {
		t.Errorf("the reason does not say what happened: %s", rep.reason())
	}
}

// The golden half: a plan pins an input, the branch contains it, nothing
// blocks — and when the implementation drops it, the card stops.
func TestPromisesCheckGoldensAgainstTheBranch(t *testing.T) {
	repo := promiseRepo(t, map[string]string{
		"parse_test.go": "package p\n\nvar cases = map[string]string{\n\t\"name eq c1)\": \"unbalanced parentheses\",\n}\n",
	})
	pinned := "## Plan claims\n- golden `TestParse_Error[\"name eq c1)\"] = \"unbalanced parentheses\"` because the stack is at its root.\n"
	if rep := checkPromises(context.Background(), promiseSpec(t, pinned), repo); rep.blocks() {
		t.Fatalf("a golden the branch pins still blocked: %+v", rep)
	}

	dropped := "## Plan claims\n- golden `TestParse_Error[\"name eq c9)\"] = \"unbalanced parentheses\"` because the stack is at its root.\n"
	rep := checkPromises(context.Background(), promiseSpec(t, dropped), repo)
	if len(rep.Unpinned) != 1 {
		t.Fatalf("a golden nothing on the branch contains did not block: %+v", rep)
	}
	if !strings.Contains(rep.reason(), "name eq c9)") {
		t.Errorf("the reason does not name the missing input: %s", rep.reason())
	}
}

// A golden that quotes nothing is prose. The floor holds a card to
// promises it can read, and never to ones it cannot.
func TestPromisesIgnoreAnUnquotedGolden(t *testing.T) {
	repo := promiseRepo(t, map[string]string{"a.go": "package a\n"})
	sp := promiseSpec(t, "## Plan claims\n- golden the parser rejects bad input\n")
	if rep := checkPromises(context.Background(), sp, repo); rep.blocks() {
		t.Errorf("an unquotable golden blocked a card: %+v", rep)
	}
}

// A card whose plan promised nothing is untouched — this floor adds no
// requirement of its own.
func TestPromisesSilentWithoutPromises(t *testing.T) {
	repo := promiseRepo(t, map[string]string{"a.go": "package a\n"})
	sp := promiseSpec(t, "## Chosen approach\nJust do it.\n")
	if rep := checkPromises(context.Background(), sp, repo); rep.blocks() {
		t.Errorf("a plan that promised nothing was held to something: %+v", rep)
	}
}

// One direction only: an artifact that cannot be read, or a search that
// cannot run, is no opinion rather than a block.
func TestPromisesNeverBlockOnTheirOwnFailure(t *testing.T) {
	if rep := checkPromises(context.Background(), filepath.Join(t.TempDir(), "gone.md"), t.TempDir()); rep.blocks() {
		t.Error("an unreadable artifact blocked a card")
	}
	sp := promiseSpec(t, "## Plan claims\n- golden `x[\"abc\"] = 1` because.\n")
	// Not a git repo: git grep cannot answer, so the golden gets no verdict.
	if rep := checkPromises(context.Background(), sp, t.TempDir()); rep.blocks() {
		t.Error("a search that could not run blocked a card")
	}
}

// The inventory is what makes the coverage question answerable at all.
func TestChangedFileInventoryNamesTheFilesAndTheRule(t *testing.T) {
	if got := changedFileInventory(nil); got != "" {
		t.Errorf("an empty branch produced an inventory: %q", got)
	}
	got := changedFileInventory([]string{"lxd/images.go", "shared/filter/clause.go"})
	if !strings.Contains(got, "lxd/images.go") || !strings.Contains(got, "shared/filter/clause.go") {
		t.Errorf("the inventory dropped a file:\n%s", got)
	}
	if !strings.Contains(got, "UNPROVEN:") {
		t.Errorf("the inventory does not say what to do with an uncovered file:\n%s", got)
	}
}

func TestChangedFileInventoryIsBounded(t *testing.T) {
	paths := make([]string, maxInventoryPaths+10)
	for i := range paths {
		paths[i] = "pkg/file.go"
	}
	got := changedFileInventory(paths)
	if !strings.Contains(got, "and 10 more") {
		t.Errorf("a long branch's inventory was not summarised:\n%s", got)
	}
}

// The sweep is asked for where the change decides what input is legal,
// and nowhere else.
func TestGrammarSweepHintFiresAtTheSeam(t *testing.T) {
	spec := "## Problem\nThe filter parser folds clauses left to right.\n"
	got := grammarSweepHint(spec, []string{"shared/filter/clause.go"})
	if got == "" {
		t.Fatal("a parser change was not offered the sweep")
	}
	if !strings.Contains(got, "merge-base") {
		t.Errorf("the sweep does not say what to compare against:\n%s", got)
	}

	// The filenames alone said nothing on either lxd branch; the plan's
	// own prose is what named the seam.
	if grammarSweepHint("## Problem\nTokenize the value.\n", nil) == "" {
		t.Error("the seam was missed when only the prose named it")
	}

	quiet := "## Problem\nThe status bar loses its badge on resize.\n"
	if got := grammarSweepHint(quiet, []string{"internal/ui/board.go"}); got != "" {
		t.Errorf("a UI card was asked for a grammar sweep:\n%s", got)
	}
}
