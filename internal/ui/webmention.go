package ui

import (
	"context"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/webapi"
)

// mentionMax bounds how many files one "@word" offers: a picker, not a
// listing of the tree.
const mentionMax = 10

// mentionTimeout bounds the ls-files a keystroke waits on.
const mentionTimeout = 2 * time.Second

// mentionWord is the "@word" a line ends with, without its "@", and
// whether it ends with one at all: the token after the last blank, so a
// mention can sit anywhere in a sentence the person is still typing.
func mentionWord(text string) (string, bool) {
	tok := text
	if i := strings.LastIndexAny(text, " \t\n"); i >= 0 {
		tok = text[i+1:]
	}
	return strings.CutPrefix(tok, "@")
}

// fileCompletions offers the worktree's files a partly typed "@word"
// could name — tracked and untracked alike, ignored ones never — best
// first: a file whose name starts with the word, then one whose path
// does, then one whose path merely holds it. Picking one puts "@<path> "
// in place of the word.
func fileCompletions(dir, word string) []webapi.Completion {
	if dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mentionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil
	}
	lower := strings.ToLower(word)
	type hit struct {
		p    string
		rank int
	}
	var hits []hit
	seen := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		lp := strings.ToLower(p)
		switch {
		case strings.HasPrefix(strings.ToLower(path.Base(p)), lower):
			hits = append(hits, hit{p, 0})
		case strings.HasPrefix(lp, lower):
			hits = append(hits, hit{p, 1})
		case strings.Contains(lp, lower):
			hits = append(hits, hit{p, 2})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if len(hits[i].p) != len(hits[j].p) {
			return len(hits[i].p) < len(hits[j].p)
		}
		return hits[i].p < hits[j].p
	})
	if len(hits) > mentionMax {
		hits = hits[:mentionMax]
	}
	res := make([]webapi.Completion, 0, len(hits))
	for _, h := range hits {
		res = append(res, webapi.Completion{Text: "@" + h.p + " ", Detail: "file", Group: "file"})
	}
	return res
}
