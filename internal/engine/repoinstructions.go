package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The repository's own instructions, quoted into the session that runs
// inside it.
//
// Every stage session is told, in repoInstructionsPrecedenceHint, that the
// repo carries its own instructions and that they govern craft. That was
// an assertion about a file nobody put in front of the model. On a backend
// whose CLI auto-loads only its own convention (Claude Code reads
// CLAUDE.md), a repository that states its rules in AGENTS.md states them
// to no one: across two full drives of canonical/lxd — 23 sessions — not a
// single tool call opened its AGENTS.md, and both branches broke the
// commit-message convention that repo documents, because the convention
// was never visible. Discovery separately spent minutes re-deriving a CGO
// split that AGENTS.md states in one line.
//
// So the card quotes the entry-point files verbatim. It deliberately does
// NOT try to gather every rule in the repository: a repo's entry-point
// instructions are written to point onwards ("see COMMITS.md for the
// commit prefix table"), and a session that can read the pointer is in the
// worktree and can open what it names. Getting the entry point in front of
// the model is the whole job.
const (
	// maxRepoInstructions caps the card. Instruction files are prose a
	// human wrote for contributors, and the card rides every request of
	// every session, so an unbounded file would be a per-turn tax on every
	// stage of every card. Past the cap the card is cut at a line boundary
	// and says so, naming the file to open for the rest.
	maxRepoInstructions = 12 << 10
	// maxRepoInstructionFile caps any single file's contribution, so one
	// very long CONTRIBUTING-style document cannot crowd out the file that
	// was written for agents specifically.
	maxRepoInstructionFile = 8 << 10
)

// repoInstructionFiles names the entry-point instruction files a
// repository may carry at its root, in the order they are quoted.
//
// AGENTS.md first: it is the file written for this reader. CLAUDE.md
// second, since a repo that has both usually keeps the Claude-specific
// notes there. Nothing else is read — in particular not CONTRIBUTING.md,
// which is written for humans opening pull requests and is mostly process
// a stage session cannot act on. The two entry points name whatever else
// matters.
var repoInstructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// repoInstructionsCard returns the instruction card for the repository
// rooted at root, computed at most once per root per Engine lifetime.
// A repository that carries none of the files yields "" and the session
// opens exactly as it used to.
func (e *Engine) repoInstructionsCard(root string) string {
	if root == "" {
		return ""
	}
	e.repoCardMu.Lock()
	defer e.repoCardMu.Unlock()
	if card, done := e.repoInstructions[root]; done {
		return card
	}
	card := buildRepoInstructionsCard(root)
	if e.repoInstructions == nil {
		e.repoInstructions = map[string]string{}
	}
	e.repoInstructions[root] = card
	return card
}

// buildRepoInstructionsCard reads the entry-point instruction files under
// root and renders them as one card. Unreadable files are skipped: a
// session that cannot be told the rules is no worse off than one that was
// never told them, and a hard error here would fail a stage over a
// permissions problem in a file the repo did not have to have.
func buildRepoInstructionsCard(root string) string {
	var quoted []string
	var names []string
	budget := maxRepoInstructions
	for _, name := range repoInstructionFiles {
		if budget <= 0 {
			break
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(raw))
		if body == "" {
			continue
		}
		cap := min(budget, maxRepoInstructionFile)
		body, cut := clipLines(body, cap)
		if cut {
			body += fmt.Sprintf("\n\n[…%s continues — open it in the worktree for the rest]", name)
		}
		quoted = append(quoted, fmt.Sprintf("--- %s ---\n%s", name, body))
		names = append(names, name)
		budget -= len(body)
	}
	if len(quoted) == 0 {
		return ""
	}
	header := fmt.Sprintf("This repository's own instructions (%s), quoted in full below so you "+
		"do not have to go and find them. They govern craft — build commands, style, commit "+
		"format, test and review conventions — and where they name another file for a detail "+
		"(a commit-format table, a list of generated files), that file is in this worktree and "+
		"you can open it. gummi still governs process; see the precedence rule in these hints.",
		strings.Join(names, ", "))
	return header + "\n\n" + strings.Join(quoted, "\n\n")
}

// clipLines truncates s to at most n bytes at a line boundary, reporting
// whether anything was dropped. Cutting mid-sentence in a rules document
// is how a rule becomes its own opposite ("never commit" / "never"), so
// the cut lands between lines or not at all.
func clipLines(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, "\n"), true
}
