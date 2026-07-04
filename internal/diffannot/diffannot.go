// Package diffannot anchors line comments to a unified diff by the
// content around the line rather than a line number, so a comment stays
// attached across minor rebases (DESIGN §6.1). An annotation whose anchor
// no longer matches any line has orphaned; the caller degrades it to a
// file-level comment rather than dropping it.
package diffannot

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// context is how many lines on each side of the target are folded into
// its anchor hash. Two each side (DESIGN §6.1) balances stability against
// uniqueness.
const context = 2

// payload strips a unified-diff line's leading marker (+, -, space) so the
// anchor is the same whether a line is added or already context — what
// matters is the text, not its diff role.
func payload(line string) string {
	if line == "" {
		return ""
	}
	switch line[0] {
	case '+', '-', ' ':
		return line[1:]
	default:
		return line // headers (@@, diff --git) hash whole
	}
}

// Anchor hashes the target line plus up to `context` lines on each side,
// by content (markers stripped). Out-of-range neighbors contribute an
// empty slot so the window size is stable at the diff's edges.
func Anchor(lines []string, idx int) string {
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	var b strings.Builder
	for i := idx - context; i <= idx+context; i++ {
		if i >= 0 && i < len(lines) {
			b.WriteString(payload(lines[i]))
		}
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Locate returns the index of the line whose Anchor matches want, or -1
// if none does (the annotation has orphaned). The first match wins.
func Locate(lines []string, want string) int {
	if want == "" {
		return -1
	}
	for i := range lines {
		if Anchor(lines, i) == want {
			return i
		}
	}
	return -1
}

// FileAt returns the new-side file path (from the nearest preceding
// `+++ b/<path>` header, falling back to the `diff --git` header) that the
// line at idx belongs to, or "" before the first header.
func FileAt(lines []string, idx int) string {
	file := ""
	for i := 0; i <= idx && i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			file = strings.TrimPrefix(l, "+++ b/")
		case strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(l, "+++ ")
		case strings.HasPrefix(l, "diff --git "):
			// diff --git a/x b/x — take the b/ side as a provisional name
			if j := strings.Index(l, " b/"); j >= 0 {
				file = l[j+3:]
			}
		}
	}
	return file
}
