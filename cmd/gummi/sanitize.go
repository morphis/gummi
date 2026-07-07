package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// clean strips terminal escape sequences and control characters from
// untrusted text before printing it. Ingest step labels and proposal
// titles/one-liners are model-authored, and imported bug titles come from
// third-party GitHub issues; printed raw with fmt.Printf they could smuggle
// OSC 52 (clipboard write), title-spoofing, or cursor/screen escapes to the
// user's terminal. These are single-line CLI cells, so only tab survives.
func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
