package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The card exists because a repository that states its rules in AGENTS.md
// was stating them to nobody: the file has to be quoted, not referred to.
func TestRepoInstructionsCardQuotesTheFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "AGENTS.md"), "# Rules\n\nCommit prefixes: see COMMITS.md.\n")

	card := buildRepoInstructionsCard(root)
	if card == "" {
		t.Fatal("a repo with an AGENTS.md produced no instruction card")
	}
	if !strings.Contains(card, "Commit prefixes: see COMMITS.md.") {
		t.Errorf("the card does not carry the file's own words:\n%s", card)
	}
	if !strings.Contains(card, "AGENTS.md") {
		t.Errorf("the card does not name the file it quotes:\n%s", card)
	}
}

// Both entry points are quoted, in a fixed order, so a repo that keeps
// Claude-specific notes beside its agent rules loses neither.
func TestRepoInstructionsCardQuotesBothEntryPoints(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "AGENTS.md"), "agents-rule\n")
	write(t, filepath.Join(root, "CLAUDE.md"), "claude-rule\n")

	card := buildRepoInstructionsCard(root)
	iAgents := strings.Index(card, "agents-rule")
	iClaude := strings.Index(card, "claude-rule")
	if iAgents < 0 || iClaude < 0 {
		t.Fatalf("a file was dropped from the card:\n%s", card)
	}
	if iAgents > iClaude {
		t.Error("CLAUDE.md was quoted ahead of the file written for this reader")
	}
}

// CONTRIBUTING.md is written for humans opening pull requests. The entry
// points name whatever part of it matters, and quoting it would spend the
// card's budget on process a stage session cannot act on.
func TestRepoInstructionsCardIgnoresContributing(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "CONTRIBUTING.md"), "how to file an issue\n")

	if card := buildRepoInstructionsCard(root); card != "" {
		t.Errorf("CONTRIBUTING.md alone produced a card:\n%s", card)
	}
}

// A repo that states nothing yields nothing: the session opens exactly as
// it did before the card existed.
func TestRepoInstructionsCardEmptyWithoutFiles(t *testing.T) {
	if card := buildRepoInstructionsCard(t.TempDir()); card != "" {
		t.Errorf("a repo with no instruction files produced a card:\n%s", card)
	}
	if card := buildRepoInstructionsCard(""); card != "" {
		t.Errorf("an empty root produced a card:\n%s", card)
	}
}

// A long file is cut at a line boundary and says so — a rules document
// truncated mid-sentence can state the opposite of the rule.
func TestRepoInstructionsCardTruncatesAtALineBoundary(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; b.Len() < maxRepoInstructionFile*2; i++ {
		b.WriteString("never commit generated files by hand\n")
	}
	write(t, filepath.Join(root, "AGENTS.md"), b.String())

	card := buildRepoInstructionsCard(root)
	if len(card) > maxRepoInstructions+1024 {
		t.Errorf("card is %d bytes, past its budget", len(card))
	}
	if !strings.Contains(card, "continues — open it in the worktree") {
		t.Errorf("a truncated card does not say it was cut:\n%s", card[len(card)-300:])
	}
	body := card[strings.Index(card, "--- AGENTS.md ---"):]
	if strings.Contains(body, "never commit generated files by han\n") {
		t.Error("the card cut a rule mid-line")
	}
}

// The commit scribe is the pass most often judged against a written
// convention, so it gets the card and an explicit no-trailers rule.
func TestCommitScribeHintsCarryTheCard(t *testing.T) {
	if got := commitScribeRepoHints(""); got != nil {
		t.Errorf("a repo that states no conventions still added hints: %v", got)
	}
	got := commitScribeRepoHints("--- AGENTS.md ---\nprefix table: api:")
	if len(got) != 2 || !strings.Contains(got[0], "prefix table") {
		t.Fatalf("the card did not reach the commit scribe: %v", got)
	}
	if !strings.Contains(got[1], "Signed-off-by") {
		t.Errorf("the scribe is not told to leave sign-off to the human: %s", got[1])
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
