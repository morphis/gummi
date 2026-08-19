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
	body = normalizeSectionBody(body)
	return content[:start] + body + content[end:], matchedTitle, nil
}

// normalizeSectionBody guarantees the spliced body is newline-terminated so
// the following `## ` heading (or the EOF boundary) stays at column 0. A body
// without a trailing newline would otherwise weld the next heading onto its
// last line, silently corrupting the document. A non-empty body already
// newline-terminated is preserved verbatim (never trimmed), so a
// ViewSection->ReplaceSection round-trip stays byte-identical; an empty body
// becomes a single newline so the section remains addressable.
func normalizeSectionBody(body string) string {
	if body == "" {
		return "\n"
	}
	if !strings.HasSuffix(body, "\n") {
		return body + "\n"
	}
	return body
}

// HealWeldedHeadings re-splits any top-level `## ` heading that a
// non-newline-terminated body splice welded mid-line (e.g. `...body## Next`)
// back onto its own line, restoring column-0 heading identity so the section
// becomes addressable by ViewSection/ReplaceSection again. It is idempotent: a
// document with no mid-line `## ` is returned byte-identical. Used to repair
// artifacts that were already corrupted before the splice-normalization fix
// shipped, so a subsequent mediated write heals the whole document.
//
// Only genuine boundary welds are repaired. A weld is a mid-line `## ` whose
// glued title is one of the template's fixed top-level section titles (see
// knownSectionTitles); a `## ` bearing any other title is prose or a snippet,
// never a welded heading. A `## ` inside a fenced (```) or indented code
// block, or inside an inline backtick span, is also legitimate content and is
// left untouched. The healer runs unconditionally ahead of every mediated
// write because, on a document that was never welded, its worst outcome is
// re-formatting a single prose line; on a genuinely welded document it
// recovers a whole section whose content would otherwise be absorbed and
// silently lost. It is idempotent.
//
// The discriminator has one accepted, deliberate residual: a prose line that
// is byte-identical to a genuine weld — `<text>## <known template title>` at
// end of line (or followed by another `## `), outside code blocks and backtick
// spans — cannot be distinguished from a welded heading, because the glued
// title runs to the end of the line (or the next `## `) and leaves nothing
// after it to inspect. Such a line is split back to column 0. This is
// unavoidable: any rule narrower than title-membership would also stop
// repairing genuine welds whose section headings are not preceded by a blank
// line (the bug-report template emits headings followed directly by content),
// and failing to heal those loses whole sections of data. The residual only
// ever re-formats one line of prose; it never loses data.
func HealWeldedHeadings(content string) string {
	lines := strings.Split(content, "\n")
	changed := false
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || isIndentedCode(line) {
			continue // fenced/indented code: verbatim, never a weld
		}
		var ok bool
		lines[i], ok = splitWelds(line)
		if ok {
			changed = true
		}
	}
	if !changed {
		return content
	}
	return strings.Join(lines, "\n")
}

// isIndentedCode reports whether line is an indented code block line (tab or
// four leading spaces), the standard Markdown signal for verbatim content.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

// knownSectionTitles are the fixed top-level `## ` section titles across the
// feature and bug templates (see Template/BugTemplate in spec.go). The
// template section list is fixed, so a mid-line `## ` whose title is not one
// of these cannot be a welded heading — it is prose or snippet content and
// must be left alone. Matching the weld's title against this list is what
// stops the healer from rewriting ordinary prose into column-0 headings.
var knownSectionTitles = map[string]struct{}{
	"problem":               {},
	"out of scope":          {},
	"considered approaches": {},
	"chosen approach":       {},
	"implementation notes":  {},
	"progress":              {},
	"review":                {},
	"verification plan":     {},
	"summary":               {},
	"reproduction":          {},
	"expected vs actual":    {},
	"environment":           {},
	"discussion":            {},
	"root cause":            {},
	"fix":                   {},
	"verification":          {},
	// research document sections (FD-076)
	"brief":       {},
	"questions":   {},
	"findings":    {},
	"constraints": {},
	"options":     {},
	"direction":   {},
	"slices":      {},
	"open risks":  {},
}

// splitWelds inserts a newline before every mid-line `## ` in line whose glued
// title is a known template section, returning the rewritten line and whether
// anything changed. It scans left to right so that a doubly-welded line (e.g.
// `body## Verification## Expected vs actual`) yields
// `body\n## Verification\n## Expected vs actual` in one pass: after a weld is
// split onto its own line, scanning continues just past that heading so the
// next `## ` on the line is handled without re-splitting the one already at
// column 0. A line that begins with a `## ` heading is left in place and only
// the welds that follow it are split.
func splitWelds(line string) (string, bool) {
	var b strings.Builder
	offset := 0
	changed := false
	for {
		rel := strings.Index(line[offset:], "## ")
		if rel < 0 {
			b.WriteString(line[offset:])
			break
		}
		idx := offset + rel
		if idx == 0 {
			// A `## ` already at column 0 is a real heading, not a weld;
			// scan past it so a further weld on the same line is found.
			b.WriteString(line[offset : idx+3])
			offset = idx + 3
			continue
		}
		if weldTitle(line, idx) != "" && !insideBackticks(line, idx) {
			b.WriteString(line[offset:idx])
			b.WriteString("\n")
			b.WriteString(line[idx : idx+3])
			offset = idx + 3
			changed = true
			continue
		}
		b.WriteString(line[offset : idx+3])
		offset = idx + 3
	}
	return b.String(), changed
}

// weldTitle returns the glued title of the `## ` heading at idx in line,
// lowercased, if it reads as a known template section title, or "" otherwise.
// The title runs from just after the `## ` up to the next `## ` (a second weld
// on the same line) or end of line, so a doubly-welded line like
// `body## Verification## Expected vs actual` yields "Verification".
func weldTitle(line string, idx int) string {
	rest := line[idx+3:]
	if end := strings.Index(rest, "## "); end >= 0 {
		rest = rest[:end]
	}
	title := strings.ToLower(strings.TrimSpace(rest))
	if title == "" {
		return ""
	}
	if _, ok := knownSectionTitles[title]; !ok {
		return ""
	}
	return title
}

// insideBackticks reports whether the byte offset idx of line (the start of a
// mid-line `## `) sits inside an inline backtick-delimited code span. A run of
// N backticks opens a span and a later run of exactly N closes it; runs of any
// other length leave the span state unchanged, matching common inline-code
// parsing. A `## ` that falls inside an open span is prose/snippet content, not
// a welded heading, and must never be split back to column 0.
func insideBackticks(line string, idx int) bool {
	delim := 0
	open := false
	i := 0
	for i < len(line) && i <= idx {
		if line[i] != '`' {
			if i == idx {
				break
			}
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		run := j - i
		if open && run == delim {
			open = false
		} else if !open {
			delim = run
			open = true
		}
		i = j
	}
	return open
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
