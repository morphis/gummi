package main

import (
	"strings"
	"testing"

	"github.com/morphia/gummi/internal/domain"
)

func TestRenderProposalFlagsUnmapped(t *testing.T) {
	res := domain.IngestResult{
		Proposals: []domain.FeatureProposal{
			{Title: "Auth", OneLiner: "log in", SourceRefs: []string{"Security"}, Skip: domain.SkipFlags{Brainstorm: true}, Draft: domain.DraftSeed{OpenQuestions: []string{"sso?"}}},
			{Title: "Billing", DependsOn: []string{"Auth"}},
		},
		Coverage: []domain.CoverageEntry{
			{Requirement: "login", Status: domain.CoverageMapped},
			{Requirement: "analytics", Status: domain.CoverageOutOfScope, Note: "later"},
			{Requirement: "gdpr export", Status: domain.CoverageUnmapped, Note: "unclear owner"},
		},
	}
	var b strings.Builder
	renderProposal(&b, res)
	out := b.String()
	for _, want := range []string{
		"Proposed 2 feature(s)",
		"1. Auth",
		"from: Security",
		"needs: Auth",
		"skip brainstorm",
		"1 open question(s)",
		"Coverage: 1 mapped · 1 out-of-scope · 1 unmapped",
		"! UNMAPPED: gdpr export — unclear owner",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
}

func TestConfirmGate(t *testing.T) {
	cases := map[string]bool{"y\n": true, "yes\n": true, "Y\n": true, "n\n": false, "\n": false, "nope\n": false, "": false}
	for in, want := range cases {
		var out strings.Builder
		if got := confirm(strings.NewReader(in), &out, "go?"); got != want {
			t.Errorf("confirm(%q) = %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "go? [y/N]") {
			t.Errorf("confirm did not print the prompt for %q", in)
		}
	}
}
