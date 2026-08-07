package main

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func TestRenderBugProposals(t *testing.T) {
	res := engine.BugIngestResult{
		Source: "github",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", OneLiner: "SSO bounce", Severity: domain.SeverityHigh, ExternalRef: "https://x/42"},
			{Title: "Typo in footer"},
		},
		Skipped: []engine.SkippedBug{{Proposal: domain.BugProposal{Title: "old one", ExternalRef: "https://x/1"}, LocalID: "BG-041"}},
	}
	var b strings.Builder
	renderBugProposals(&b, res)
	out := b.String()
	for _, want := range []string{
		"Proposed 2 bug(s)",
		"1. Login loops",
		"SSO bounce",
		"severity high",
		"https://x/42",
		"2. Typo in footer",
		"Skipped 1 already on the board:",
		"→ BG-041  old one",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
}

func TestBugIngest_CommentsFlag(t *testing.T) {
	withComments := ingestGitHubSource("o/r", "bug", "open", true, "/tmp/repo")
	if !withComments.FetchComments {
		t.Error("--comments=true should set FetchComments")
	}
	withoutComments := ingestGitHubSource("o/r", "bug", "open", false, "/tmp/repo")
	if withoutComments.FetchComments {
		t.Error("--comments absent should leave FetchComments false")
	}
	// the other fields are threaded through unchanged.
	if withComments.Repo != "o/r" || withComments.Label != "bug" || withComments.State != "open" || withComments.Dir != "/tmp/repo" {
		t.Errorf("source fields wrong: %+v", withComments)
	}
}
