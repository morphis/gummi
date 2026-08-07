package engine

import (
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// parseBodySections splits a GitHub issue body into the BugReport's
// symptom fields by recognizing common bug-report template headings. The
// issue body is the raw markdown a reporter pasted in; its structure is
// real signal a bug report should keep, so triage doesn't have to re-split
// it by hand. Everything before the first recognized heading (and any
// text that doesn't sit under a recognized heading) stays in Description,
// preserving the source's original framing. Comment threads are not the
// body's concern — Discussion is filled separately by fetchComments.
//
// Headings are matched case-insensitively in three shapes: ATX
// (`## Steps to reproduce`), bold labels (`**Steps to reproduce:** …`),
// and definition-list colons (`Steps to reproduce:`). See headingFields
// for the recognized labels and the field each populates.
func parseBodySections(body string) domain.BugReport {
	var r domain.BugReport
	writer := writeDescription
	for _, line := range strings.Split(body, "\n") {
		label, content, ok := matchHeading(line)
		if !ok {
			writer(&r, line)
			continue
		}
		writer = headingFields[label]
		if content != "" {
			writer(&r, content)
		}
	}
	return r
}

// headingFields maps a normalized heading label to the writer that
// appends prose into its BugReport field. Adding a recognized heading is
// one map entry, not a new switch branch.
var headingFields = map[string]func(*domain.BugReport, string){
	"steps to reproduce": writeReproduction,
	"step to reproduce":  writeReproduction,
	"reproduction":       writeReproduction,
	"repro":              writeReproduction,
	"expected behavior":  writeExpected,
	"expected behaviour": writeExpected,
	"expected":           writeExpected,
	"actual behavior":    writeActual,
	"actual behaviour":   writeActual,
	"actual":             writeActual,
	"environment":        writeEnvironment,
	"versions":           writeEnvironment,
	"env":                writeEnvironment,
}

// matchHeading reports whether line is a recognized heading and, if so,
// returns its normalized label plus any content that follows the label on
// the same line (e.g. `**Steps to reproduce:** 1. foo` yields content
// `1. foo`). Content on subsequent lines is captured by the caller via
// the active writer. Returns ok=false when the line is not a recognized
// heading — it is then treated as ordinary prose.
func matchHeading(line string) (label, content string, ok bool) {
	if m := boldLabelRe.FindStringSubmatch(line); m != nil {
		label = normalizeHeading(m[1])
		if _, ok := headingFields[label]; ok {
			return label, strings.TrimSpace(m[2]), true
		}
	}
	if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
		label = normalizeHeading(m[1])
		if _, ok := headingFields[label]; ok {
			return label, "", true
		}
	}
	if m := defListRe.FindStringSubmatch(line); m != nil && !colonFalsePositive(line) {
		label = normalizeHeading(m[1])
		if _, ok := headingFields[label]; ok {
			return label, strings.TrimSpace(m[2]), true
		}
	}
	return "", "", false
}

// atxHeadingRe matches `#`, `##`, or `###` ATX headings. The label is the
// rest of the line; content lives on following lines.
var atxHeadingRe = regexp.MustCompile(`^#{1,3}\s+(.+?)\s*$`)

// boldLabelRe matches `**Label:** content` or a bare `**Label**` — with
// an optional colon and optional same-line content captured in group 2.
var boldLabelRe = regexp.MustCompile(`^\*\*(.+?)\*\*\s*:?\s*(.*)$`)

// defListRe matches a colon-terminated label line of 1–6 words
// (`Steps to reproduce:`), capturing any same-line content after the
// colon. Longer prose lines and URLs are rejected by the pattern shape
// and, defensively, by colonFalsePositive.
var defListRe = regexp.MustCompile(`^((?:\w[\w-]*(?:\s+\w[\w-]*){0,5})\s*):\s*(.*)$`)

// colonFalsePositive skips colon-terminated lines that are almost
// certainly prose or code rather than headings: bare URLs and anything
// containing an inline backtick.
func colonFalsePositive(line string) bool {
	if strings.HasPrefix(line, "http") {
		return true
	}
	return strings.Contains(line, "`")
}

// normalizeHeading strips the marker syntax (ATX hashes, bold asterisks,
// trailing colons) and lowercases a heading label so the fixed whitelist
// lookup is case- and shape-insensitive.
func normalizeHeading(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "**")
	s = strings.TrimSuffix(s, "**")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ":")
	return strings.ToLower(strings.TrimSpace(s))
}

func writeDescription(r *domain.BugReport, line string)  { appendLine(&r.Description, line) }
func writeReproduction(r *domain.BugReport, line string) { appendLine(&r.Reproduction, line) }
func writeExpected(r *domain.BugReport, line string)     { appendLine(&r.Expected, line) }
func writeActual(r *domain.BugReport, line string)       { appendLine(&r.Actual, line) }
func writeEnvironment(r *domain.BugReport, line string)  { appendLine(&r.Environment, line) }

// appendLine adds one source line to a section, joining multi-line
// sections with a newline so the raw text round-trips.
func appendLine(dst *string, line string) {
	if *dst == "" {
		*dst = line
		return
	}
	*dst += "\n" + line
}
