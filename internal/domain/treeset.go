package domain

import (
	"sort"
	"strings"
)

// A workflow run snapshots the session's trees (DESIGN §25.6, §27): the
// primary one and each attached repository's. The snapshot of several is
// one string, kept in the same columns the snapshot of one always was: a
// bare tree hash for a session that spans nothing (every row stored before
// attachments existed), and for one that does, the primary's hash on the
// first line and one "<repo>=<hash>" line per attached tree, sorted.

// EncodeTrees joins the snapshot of the primary tree and of each attached
// one into the string a run or step stores.
func EncodeTrees(primary string, attached map[string]string) string {
	if len(attached) == 0 {
		return primary
	}
	repos := make([]string, 0, len(attached))
	for r := range attached {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	var b strings.Builder
	b.WriteString(primary)
	for _, r := range repos {
		b.WriteString("\n" + r + "=" + attached[r])
	}
	return b.String()
}

// DecodeTrees is EncodeTrees's reverse. A bare hash is a primary tree
// with nothing attached.
func DecodeTrees(s string) (primary string, attached map[string]string) {
	first, rest, _ := strings.Cut(s, "\n")
	for _, line := range strings.Split(rest, "\n") {
		// a hash holds no "=", a repository's name may: cut at the last
		if i := strings.LastIndex(line, "="); i > 0 {
			repo, hash := line[:i], line[i+1:]
			if attached == nil {
				attached = map[string]string{}
			}
			attached[repo] = hash
		}
	}
	return first, attached
}

// PrimaryTree is the primary tree's hash in a stored snapshot.
func PrimaryTree(s string) string {
	p, _ := DecodeTrees(s)
	return p
}
