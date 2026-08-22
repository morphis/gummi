package main

import (
	"testing"
)

// TestResumeDoneRSDispatchesDecompose confirms --approve and
// --request-changes round-trip into ResumeInput exactly as they do for any
// other card — the FD-081 decompose checkpoint on a done research card
// needs no new CLI plumbing, only the driver-side dispatch on kind+stage
// (internal/driver/decompose_test.go covers that dispatch end-to-end).
func TestResumeDoneRSDispatchesDecompose(t *testing.T) {
	in, err := resumeInput("", true, "", false, "", false, false, false)
	if err != nil {
		t.Fatalf("--approve: %v", err)
	}
	if !in.Approve {
		t.Errorf("ResumeInput.Approve = false, want true")
	}

	note := "tighten the second slice's scope"
	in, err = resumeInput("", false, note, false, "", false, true, false)
	if err != nil {
		t.Fatalf("--request-changes: %v", err)
	}
	if in.RequestChanges == nil || *in.RequestChanges != note {
		t.Errorf("ResumeInput.RequestChanges = %v, want %q", in.RequestChanges, note)
	}
}
