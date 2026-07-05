package domain

import "testing"

func TestIngestResultUnmapped(t *testing.T) {
	r := IngestResult{Coverage: []CoverageEntry{
		{Requirement: "login", Feature: "Auth", Status: CoverageMapped},
		{Requirement: "gdpr export", Status: CoverageUnmapped, Note: "unclear owner"},
		{Requirement: "analytics", Status: CoverageOutOfScope, Note: "later"},
		{Requirement: "sso", Status: CoverageUnmapped},
	}}
	got := r.Unmapped()
	if len(got) != 2 {
		t.Fatalf("Unmapped() = %d entries, want 2", len(got))
	}
	if got[0].Requirement != "gdpr export" || got[1].Requirement != "sso" {
		t.Errorf("Unmapped() returned wrong entries: %+v", got)
	}
}

func TestFeatureProposalSlug(t *testing.T) {
	if s, err := (FeatureProposal{Title: "Webhook Retries!"}).Slug(); err != nil || s != "webhook-retries" {
		t.Errorf("Slug() = %q, %v; want webhook-retries", s, err)
	}
	if _, err := (FeatureProposal{Title: "!!!"}).Slug(); err == nil {
		t.Error("Slug() of an unslugifiable title should error")
	}
}

func TestDraftProvenanceEmpty(t *testing.T) {
	if !(DraftProvenance{}).Empty() {
		t.Error("zero provenance should be Empty")
	}
	if (DraftProvenance{Refs: []string{"x"}}).Empty() {
		t.Error("provenance with refs is not Empty")
	}
}
