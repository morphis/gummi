package main

import (
	"flag"
	"strings"
	"testing"
)

// An envelope is required (D6), same as `run`: missing --envelope with no
// GUMMI_ENVELOPE fails loud before any workspace is touched.
func TestResearchRequiresEnvelope(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "")
	err := runResearch([]string{"a research brief"})
	if err == nil || !strings.Contains(err.Error(), "envelope is required") {
		t.Fatalf("err = %v, want an envelope-required failure", err)
	}
}

// GUMMI_ENVELOPE supplies the envelope when --envelope is absent. Run from
// a directory with no .git, so the route fails on workspace resolution
// rather than ever touching real state — proving only that the failure is
// not the envelope check.
func TestResearchEnvelopeFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GUMMI_ENVELOPE", "250")
	err := runResearch([]string{"a research brief"})
	if err == nil || strings.Contains(err.Error(), "envelope is required") {
		t.Fatalf("err = %v, want a non-envelope failure (the fallback should have satisfied the requirement)", err)
	}
}

// --until is validated against RS's route (only "shape" is a legal stop)
// before any workspace work begins, exactly like runRun's until check.
func TestResearchUntilValidation(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "100")
	for _, until := range []string{"brainstorm", "spec", "plan", "banana"} {
		err := runResearch([]string{"--until", until, "a research brief"})
		if err == nil || !strings.Contains(err.Error(), "not a valid stop") || !strings.Contains(err.Error(), "shape") {
			t.Fatalf("--until %s: err = %v, want a rejection naming shape as the only valid stop", until, err)
		}
	}
}

// RS has no brainstorm/plan and no Verification-plan section to seed, so
// its flag surface deliberately omits --full and --acceptance.
func TestResearchRejectsFullAndAcceptance(t *testing.T) {
	fs := flag.NewFlagSet("research", flag.ContinueOnError)
	registerResearchFlags(fs)
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "full" || f.Name == "acceptance" {
			t.Errorf("registerResearchFlags binds --%s, want it absent", f.Name)
		}
	})
}

// research takes exactly one positional argument: the brief.
func TestResearchRequiresOnePositional(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "100")
	if err := runResearch(nil); err == nil {
		t.Fatal("no positional accepted")
	}
	if err := runResearch([]string{"one", "two"}); err == nil {
		t.Fatal("two positionals accepted")
	}
}
