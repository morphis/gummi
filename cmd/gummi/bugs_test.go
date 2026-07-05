package main

import (
	"strings"
	"testing"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
)

func TestRenderBugProposals(t *testing.T) {
	res := engine.BugIngestResult{
		Source: "github",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", OneLiner: "SSO bounce", Severity: domain.SeverityHigh, ExternalRef: "https://x/42"},
			{Title: "Typo in footer"},
		},
		Skipped: []domain.BugProposal{{Title: "old one", ExternalRef: "https://x/1"}},
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
		"Skipped 1 already on the board.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
}
