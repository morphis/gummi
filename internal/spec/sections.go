package spec

import (
	"errors"
	"strings"
)

// Section-level access to a spec artifact: read one section's body or
// replace it, matched on the on-disk top-level `## ` heading. Section
// identity uses only top-level headings at column 0; `### ` subsections
// are freeform content inside their parent. Used by the client tools
// spec_view (read) and spec_replace_section (meditated write), which
// both operate on the current feature's artifact.

// ErrSectionUnknown is returned by ReplaceSection when name matches no
// top-level `## ` heading. There is deliberately no create-on-miss: the
// template's section list is fixed.
var ErrSectionUnknown = errors.New("unknown section")

// ErrSectionHeadingInBody is returned by ReplaceSection when body
// contains a line beginning with `## ` at column 0. The template's
// section list is fixed, so an injected top-level heading is always
// structural corruption.
var ErrSectionHeadingInBody = errors.New("body contains a top-level `## ` heading")

// ViewSection returns the body of the named section verbatim (heading
// line excluded, surrounding whitespace preserved byte-for-byte), or the
// whole document unchanged when name is empty or blank. section identity
// matches a top-level `## ` heading case-insensitively on its trimmed
// title text. ok is false when no section matches.
func ViewSection(content, name string) (body string, ok bool) {
	if strings.TrimSpace(name) == "" {
		return content, true
	}
	start, end, _, ok := sectionBounds(content, name)
	if !ok {
		return "", false
	}
	return content[start:end], true
}

// ReplaceSection replaces the named section's body — the lines between
// its `## ` heading and the next top-level `## ` heading or EOF — with
// body verbatim. The heading line itself is never touched. matchedTitle
// is the on-disk heading text (e.g. "Problem" for a call with
// name: "problem"), lifted from the shared sectionBounds helper so the
// caller can build a canonical activity note. body is a naive splice: %%
// marker lines are trusted as-is and any line beginning with `## ` at
// column 0 is rejected. On error the returned newContent is meaningless
// and the caller must leave the file on disk untouched.
func ReplaceSection(content, name, body string) (newContent, matchedTitle string, err error) {
	start, end, matchedTitle, ok := sectionBounds(content, name)
	if !ok {
		return "", "", ErrSectionUnknown
	}
	if hasHeadingAtCol0(body) {
		return "", "", ErrSectionHeadingInBody
	}
	return content[:start] + body + content[end:], matchedTitle, nil
}

// sectionBounds is the single source of truth for section identity and
// boundaries. It returns the byte range [bodyStart, bodyEnd) of the named
// section's body — excluding its heading line and the heading line's
// trailing newline — and the on-disk heading text. bodyEnd is the start
// of the next top-level heading or len(content) at EOF.
func sectionBounds(content, name string) (bodyStart, bodyEnd int, matchedTitle string, ok bool) {
	if strings.TrimSpace(name) == "" {
		return 0, 0, "", false
	}
	target := strings.ToLower(strings.TrimSpace(name))
	lines := strings.Split(content, "\n")
	pos := 0
	for _, line := range lines {
		if isHeading(line) {
			title := strings.TrimSpace(line[len("## "):])
			if strings.ToLower(title) == target {
				bodyStart = pos + len(line)
				if bodyStart < len(content) && content[bodyStart] == '\n' {
					bodyStart++ // past the heading line's trailing newline
				}
				// scan forward for the next top-level heading
				bodyEnd = len(content)
				j := bodyStart
				for {
					nl := strings.IndexByte(content[j:], '\n')
					if nl < 0 {
						break
					}
					lineStart := j
					lineEnd := j + nl
					if isHeading(content[lineStart:lineEnd]) {
						bodyEnd = lineStart
						break
					}
					j = lineEnd + 1
				}
				return bodyStart, bodyEnd, title, true
			}
		}
		pos += len(line) + 1
	}
	return 0, 0, "", false
}

// isHeading reports whether line is a top-level section heading (`## `
// at column 0). A line that merely starts with `##` without the space is
// not a heading.
func isHeading(line string) bool {
	return strings.HasPrefix(line, "## ")
}

// hasHeadingAtCol0 reports whether any line of body begins with `## ` at
// column 0. Indented `  ## …` lines (e.g. inside a code block) are not
// structural headings.
func hasHeadingAtCol0(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			return true
		}
	}
	return false
}
