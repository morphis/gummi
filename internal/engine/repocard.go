package engine

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

// The repository orientation card: the tracked-file shape of the repo a
// stage runs in, stated in the system prompt so a session does not have to
// discover it with tool calls.
//
// Every gummi session is a fresh context. That is the design — the
// artifact, not a transcript, is what carries meaning between stages — but
// it has a cost the artifact cannot pay: the artifact says what the work
// IS, never what the repository looks like. So each new session opened by
// running some variant of `find . -name '*.go'` followed by two or three
// reads before it could do anything, and a card that stops four times pays
// that four times over. The plan stage, which stops most, paid it most.
//
// This is deliberately only the SHAPE — which files exist, and where.
// Build, test and lint commands are not named here: discovering those is
// check discovery's job, it writes them into the artifact's own checks
// block, and a second unreviewed copy in the system prompt would be a
// source of truth nobody agreed to.
const (
	// maxRepoCardFiles is where a listing stops being a listing. Past it
	// the card summarizes by directory instead: a manifest that enumerates
	// a large repository is one no session reads, and it would crowd out
	// the rest of the prompt.
	maxRepoCardFiles = 200
	// maxRepoCardDirs bounds the summary form the same way.
	maxRepoCardDirs = 40
	// repoCardTimeout bounds the one git call. A repository large or slow
	// enough to exceed it yields no card at all, which is the behaviour
	// every session had before this existed.
	repoCardTimeout = 5 * time.Second
)

// repoCard returns the orientation card for the repository rooted at root,
// computing it at most once per root per Engine lifetime. An unreadable or
// non-git root yields "", and the session simply opens as it used to.
func (e *Engine) repoCard(root string) string {
	if root == "" {
		return ""
	}
	e.repoCardMu.Lock()
	defer e.repoCardMu.Unlock()
	if card, done := e.repoCards[root]; done {
		return card
	}
	card := buildRepoCard(root)
	if e.repoCards == nil {
		e.repoCards = map[string]string{}
	}
	e.repoCards[root] = card
	return card
}

// buildRepoCard renders the card from the repository's tracked files.
// Tracked, not on-disk: build output, vendored trees and scratch files are
// exactly what a session does not need, and .gitignore already says which
// those are.
func buildRepoCard(root string) string {
	// An empty root is not "the current directory": `git -C "" ls-files`
	// would happily describe whatever tree gummi itself was started in,
	// and a session would be handed a map of the wrong repository. The
	// caller guards this too; it is repeated here because this function is
	// the one that shells out.
	if root == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), repoCardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	files := strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 })
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The repository you are working in, as its tracked files. " +
		"This is the shape of the tree, so you do not have to go and find it; " +
		"it says nothing about what any file contains, and it is current as of " +
		"the start of this run. Read what you need from it and go straight to the work.\n\n")
	if len(files) <= maxRepoCardFiles {
		sort.Strings(files)
		for _, f := range files {
			b.WriteString("  " + f + "\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%d tracked files, too many to list, so by directory:\n", len(files))
	for _, line := range summarizeDirs(files) {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// summarizeDirs folds a file list into "<dir>/  (<n> files)" lines, one per
// top-level directory, largest first. It is the form the card takes for a
// repository too large to enumerate: still enough to tell a session where
// the code lives, and bounded no matter how large the tree is.
func summarizeDirs(files []string) []string {
	counts := map[string]int{}
	for _, f := range files {
		dir := path.Dir(f)
		if dir == "." {
			counts["(top level)"]++
			continue
		}
		if i := strings.Index(dir, "/"); i >= 0 {
			dir = dir[:i]
		}
		counts[dir]++
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	// largest first, then alphabetical, so the order is stable across runs
	sort.Slice(dirs, func(i, j int) bool {
		if counts[dirs[i]] != counts[dirs[j]] {
			return counts[dirs[i]] > counts[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	if len(dirs) > maxRepoCardDirs {
		dirs = dirs[:maxRepoCardDirs]
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, fmt.Sprintf("%s  (%d files)", d, counts[d]))
	}
	return out
}
