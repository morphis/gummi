package engine

import (
	"context"
	"fmt"
	"os/exec"
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
	// maxRepoCardEntries is the card's budget, in lines of tree. A
	// repository small enough to enumerate spends it on its files; a large
	// one spends it on the directories that hold the most of them.
	//
	// The budget is spent by DEPTH, not by breadth at depth one. A card
	// that stops at the top level of a large repository ("lxd (952 files),
	// doc (386 files), shared (171 files)") costs the same tokens as one
	// that reaches `shared/units` and is worth none of them: it names no
	// place a session could go, so the session goes and finds it anyway.
	// Expanding the biggest directories until the budget is gone turns the
	// same lines into a map.
	maxRepoCardEntries = 200
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
	if len(files) <= maxRepoCardEntries {
		sort.Strings(files)
		for _, f := range files {
			b.WriteString("  " + f + "\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%d tracked files, too many to list, so the directories holding "+
		"the most of them are expanded and the rest are counted (a line ending in "+
		"/ is a directory, with the number of files beneath it):\n", len(files))
	for _, line := range summarizeTree(files, maxRepoCardEntries) {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// repoNode is one node of the tracked-file tree: a directory with the
// number of files beneath it, or a file (leaf) counting one.
type repoNode struct {
	path     string
	files    int // every file beneath this node
	direct   int // files sitting in this directory itself
	leaf     bool
	children map[string]*repoNode
}

// childDirs returns n's subdirectories ordered by path, so every rendering
// of the same tree is byte-identical.
func (n *repoNode) childDirs() []*repoNode {
	out := make([]*repoNode, 0, len(n.children))
	for _, c := range n.children {
		if c.leaf {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// childFiles returns the files sitting directly in n, ordered by path.
func (n *repoNode) childFiles() []*repoNode {
	out := make([]*repoNode, 0, len(n.children))
	for _, c := range n.children {
		if c.leaf {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// buildRepoTree folds a path list into a tree, each node carrying the
// number of files beneath it and the number sitting in it.
func buildRepoTree(files []string) *repoNode {
	root := &repoNode{children: map[string]*repoNode{}}
	for _, f := range files {
		cur := root
		cur.files++
		parts := strings.Split(f, "/")
		for i, p := range parts {
			child, ok := cur.children[p]
			if !ok {
				child = &repoNode{
					path:     strings.Join(parts[:i+1], "/"),
					leaf:     i == len(parts)-1,
					children: map[string]*repoNode{},
				}
				cur.children[p] = child
			}
			child.files++
			if i == len(parts)-1 {
				cur.direct++
			}
			cur = child
		}
	}
	return root
}

// cardEntry is one line of the summarized card: a directory (with the
// subtree beneath it), a directory's own files once its subdirectories
// have been broken out (self), or a top-level file.
type cardEntry struct {
	node *repoNode
	self bool
}

// weight orders expansion: the directory holding the most files is the one
// worth resolving further.
func (e cardEntry) weight() int {
	if e.self || e.node.leaf {
		return 0
	}
	return e.node.files
}

// expandable reports whether spending more lines on this entry would say
// anything new. A directory with no subdirectories would only be restated
// in different words, so it is left alone however many files it holds —
// which is what keeps a flat directory of 400 documents from eating a
// budget meant for the code.
func (e cardEntry) expandable() bool {
	return !e.self && !e.node.leaf && len(e.node.childDirs()) > 0
}

// expand replaces a directory with its subdirectories, plus one line for
// the files it holds itself. Files below the top level are never listed
// individually: the card is a map, and a map names places, not pages.
func (e cardEntry) expand() []cardEntry {
	out := make([]cardEntry, 0, len(e.node.children)+1)
	if e.node.direct > 0 {
		out = append(out, cardEntry{node: e.node, self: true})
	}
	for _, d := range e.node.childDirs() {
		out = append(out, cardEntry{node: d})
	}
	return out
}

func (e cardEntry) line() string {
	switch {
	case e.node.leaf:
		return e.node.path
	case e.self:
		return fmt.Sprintf("%s/  (%d files here)", e.node.path, e.node.direct)
	default:
		return fmt.Sprintf("%s/  (%d files)", e.node.path, e.node.files)
	}
}

// sortKey keeps a directory's own files directly under its subdirectories
// rather than sorting away from them.
func (e cardEntry) sortKey() string {
	if e.self {
		return e.node.path + "/"
	}
	return e.node.path
}

// summarizeTree spends an entry budget on the tree, deepest where the code
// is: it starts at the top level and repeatedly expands whichever
// directory holds the most files, as long as its subdirectories still fit.
// What comes back is one line per entry — a top-level file, a directory
// with the count beneath it, or a directory's own file count once its
// subdirectories have been named — for a repository too large to
// enumerate.
func summarizeTree(files []string, budget int) []string {
	if budget < 1 {
		return nil
	}
	root := buildRepoTree(files)
	frontier := make([]cardEntry, 0, len(root.children))
	for _, f := range root.childFiles() {
		frontier = append(frontier, cardEntry{node: f})
	}
	for _, d := range root.childDirs() {
		frontier = append(frontier, cardEntry{node: d})
	}
	// A repository whose top level alone overflows the budget cannot be
	// expanded at all; keep the largest entries and say what was dropped,
	// rather than silently showing a slice of the tree.
	var dropped int
	if len(frontier) > budget {
		sort.SliceStable(frontier, func(i, j int) bool { return frontier[i].weight() > frontier[j].weight() })
		dropped = len(frontier) - budget + 1
		frontier = frontier[:budget-1]
	}
	reserve := 0
	if dropped > 0 {
		reserve = 1
	}
	for {
		best := -1
		for i, e := range frontier {
			if !e.expandable() {
				continue
			}
			if len(frontier)-1+len(e.expand())+reserve > budget {
				continue
			}
			if best == -1 || e.weight() > frontier[best].weight() ||
				(e.weight() == frontier[best].weight() && e.node.path < frontier[best].node.path) {
				best = i
			}
		}
		if best == -1 {
			break
		}
		grown := frontier[best].expand()
		next := make([]cardEntry, 0, len(frontier)-1+len(grown))
		next = append(next, frontier[:best]...)
		next = append(next, grown...)
		next = append(next, frontier[best+1:]...)
		frontier = next
	}
	sort.Slice(frontier, func(i, j int) bool { return frontier[i].sortKey() < frontier[j].sortKey() })
	out := make([]string, 0, len(frontier)+1)
	for _, e := range frontier {
		out = append(out, e.line())
	}
	if dropped > 0 {
		out = append(out, fmt.Sprintf("… and %d more top-level entries", dropped))
	}
	return out
}
