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

// sum is the hashing primitive Anchor routes through. It exists as a
// package-level var (rather than a direct sha256.Sum256 call) as a test
// seam so tests can count how many times the diff is hashed.
var sum = func(b []byte) [32]byte { return sha256.Sum256(b) }

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
	digest := sum([]byte(b.String()))
	return hex.EncodeToString(digest[:])
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

// LocateAll resolves many anchors against a diff in a single pass, hashing
// each line at most once. Returns a slice parallel to wants; element i is
// the index of the line in lines whose anchor equals wants[i], or -1 when
// no line matches (orphaned). For an anchor matching more than one line it
// resolves to the lowest index, identical to Locate's first-match-wins.
func LocateAll(lines []string, wants []string) []int {
	m := make(map[string]int, len(lines))
	for i := range lines {
		a := Anchor(lines, i)
		if _, ok := m[a]; !ok {
			m[a] = i
		}
	}
	out := make([]int, len(wants))
	for i, w := range wants {
		out[i] = -1
		if w != "" {
			if idx, ok := m[w]; ok {
				out[i] = idx
			}
		}
	}
	return out
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
