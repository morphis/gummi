package engine

import (
	"strings"
	"testing"
)

func TestParseBodySections_FullTemplate(t *testing.T) {
	body := strings.Join([]string{
		"When logging in with SSO, the app shows a white screen instead of the dashboard.",
		"",
		"## Steps to reproduce",
		"1. Launch app",
		"2. Click login",
		"3. See error",
		"## Expected behavior",
		"Redirect to dashboard",
		"## Actual behavior",
		"White screen",
		"## Environment",
		"macOS 14, Chrome 120",
	}, "\n")
	r := parseBodySections(body)

	if !strings.Contains(r.Description, "shows a white screen") {
		t.Errorf("pre-heading prose should land in Description, got %q", r.Description)
	}
	if r.Reproduction != "1. Launch app\n2. Click login\n3. See error" {
		t.Errorf("Reproduction = %q", r.Reproduction)
	}
	if r.Expected != "Redirect to dashboard" {
		t.Errorf("Expected = %q", r.Expected)
	}
	if r.Actual != "White screen" {
		t.Errorf("Actual = %q", r.Actual)
	}
	if r.Environment != "macOS 14, Chrome 120" {
		t.Errorf("Environment = %q", r.Environment)
	}
	if r.Discussion != "" {
		t.Errorf("Discussion should be empty from the body parser, got %q", r.Discussion)
	}
}

func TestParseBodySections_NoHeadings(t *testing.T) {
	body := "just a paragraph with no headings whatsoever\n\nsecond paragraph"
	r := parseBodySections(body)
	if r.Description != body {
		t.Errorf("whole body should round-trip to Description, got %q", r.Description)
	}
	if r.Reproduction != "" || r.Expected != "" || r.Actual != "" || r.Environment != "" {
		t.Errorf("no headings should leave sections empty: %+v", r)
	}
}

func TestParseBodySections_PartialHeadings(t *testing.T) {
	body := "## Steps to reproduce\n1. foo\n## Actual behavior\ncrash"
	r := parseBodySections(body)
	if r.Reproduction != "1. foo" {
		t.Errorf("Reproduction = %q", r.Reproduction)
	}
	if r.Actual != "crash" {
		t.Errorf("Actual = %q", r.Actual)
	}
	if r.Expected != "" || r.Environment != "" {
		t.Errorf("missing headings should stay empty, got %+v", r)
	}
}

func TestParseBodySections_BoldLabels(t *testing.T) {
	body := "**Steps to reproduce:** 1. foo"
	r := parseBodySections(body)
	if r.Reproduction != "1. foo" {
		t.Errorf("bold label with same-line content: Reproduction = %q, want %q", r.Reproduction, "1. foo")
	}
}

func TestParseBodySections_DefListLabels(t *testing.T) {
	body := "Steps to reproduce:\n1. foo"
	r := parseBodySections(body)
	if r.Reproduction != "1. foo" {
		t.Errorf("def-list label: Reproduction = %q, want %q", r.Reproduction, "1. foo")
	}
}

func TestParseBodySections_CaseInsensitive(t *testing.T) {
	body := "## steps to reproduce\n1. foo"
	r := parseBodySections(body)
	if r.Reproduction != "1. foo" {
		t.Errorf("lowercase heading should match the whitelist: Reproduction = %q", r.Reproduction)
	}
}

func TestParseBodySections_EmptyBody(t *testing.T) {
	r := parseBodySections("")
	if r.Description != "" || r.Reproduction != "" || r.Expected != "" || r.Actual != "" || r.Environment != "" || r.Discussion != "" || len(r.OpenQuestions) != 0 {
		t.Errorf("empty body should return a zero BugReport, got %+v", r)
	}
}

func TestParseBodySections_ColonFalsePositives(t *testing.T) {
	// URLs and code spans that end in colons must not be read as headings.
	body := strings.Join([]string{
		"http://example.com/repro:",
		"see the `steps to reproduce:` command above",
		"Actual behavior: crash",
	}, "\n")
	r := parseBodySections(body)
	if r.Actual != "crash" {
		t.Errorf("Actual = %q, want %q", r.Actual, "crash")
	}
	if !strings.Contains(r.Description, "http://example.com/repro:") {
		t.Errorf("URL line should stay prose in Description, got %q", r.Description)
	}
	if !strings.Contains(r.Description, "steps to reproduce:`") {
		t.Errorf("backtick line should stay prose in Description, got %q", r.Description)
	}
}
