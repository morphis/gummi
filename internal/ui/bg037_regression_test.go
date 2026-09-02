package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG037NarrowRowKeepsStatusBadges locks in BG-037: cardLine used to
// assemble the row title-first and cut it from the right with a single
// ansi.Truncate, so every operational badge appended after the title
// (landed, the PR link, spend, the worktree mark) died before the title
// gave up a single character. landed and the PR badge are the two that
// change what the user should DO with the card, and were among the first
// lost.
func TestBG037NarrowRowKeepsStatusBadges(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	r := row(2, "Warn when a profile is applied across projects", domain.StageImplement, "", true)
	r.Landed = true
	r.F.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 72, URL: "https://github.com/o/r/pull/72"}
	r.F.Spend.Credits = 1.23

	line := m.cardLine(r, 3, false, true, 62)
	if !strings.Contains(line, "landed") {
		t.Errorf("landed badge dropped at w=62: %q", line)
	}
	if !strings.Contains(line, "PR#72") {
		t.Errorf("PR badge dropped at w=62: %q", line)
	}
}
