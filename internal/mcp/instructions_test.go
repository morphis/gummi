package mcp

import (
	"strings"
	"testing"
)

func TestWorkspaceInstructionsMentionsRunAndResume(t *testing.T) {
	s := WorkspaceInstructions()
	for _, want := range []string{"gummi run", "gummi resume"} {
		if !strings.Contains(s, want) {
			t.Errorf("WorkspaceInstructions() missing %q:\n%s", want, s)
		}
	}
}

func TestFeatureInstructionsMentionsRunResumeAndID(t *testing.T) {
	s := FeatureInstructions("FD-019")
	for _, want := range []string{"gummi run", "gummi resume", "FD-019"} {
		if !strings.Contains(s, want) {
			t.Errorf("FeatureInstructions() missing %q:\n%s", want, s)
		}
	}
}
