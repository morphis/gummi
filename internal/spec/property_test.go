package spec

// Property tests of the spec's fenced blocks, driven by hegel
// (https://hegel.dev). gummi writes each block canonically (RenderChecks,
// RenderFiles, SetDoneWhen) and reads it back from prose a person and
// agents edit, so what it writes must read back as what it wrote. Values
// are drawn from pieces of real commands and from the text that troubles
// a fenced YAML island: backtick fences, gummi's own %% markers and skip
// tags, colon-space, quotes, YAML indicators, tabs, newlines and
// non-ASCII.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
)

const propCases = 2000

func props(t *testing.T, fn func(*hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

var propPieces = []string{
	"go test ./...", "make ci", "grep -q", "x", "é", " ", "  ", "-", "- ", ": ", ":", "#", "'", `"`, "\\",
	"```", "``", "`", "%%", "%% @user: why?", "[ci-only]", "[env: docker]", "\t", "\n", "\n  ", "{", "}",
	"[", "]", "*", "&a", "!tag", "|", ">", "null", "true", "~", "@", "---", "...", "DW-1", "\r",
}

// drawText joins drawn pieces into one value.
func drawText(ht *hegel.T, minPieces int) string {
	ps := hegel.Draw(ht, hegel.Lists(hegel.SampledFrom(propPieces)).MinSize(minPieces).MaxSize(6))
	return strings.Join(ps, "")
}

func inSpec(block string) string {
	return "# A spec\n\n## Verification\n\nRun these:\n\n" + block + "\n\nThen read the diff.\n"
}

// TestPropertyRenderedChecksReadBackAsWritten: a checks block gummi
// renders reads back as the checks it rendered — every check with a
// command, its name defaulting to the command.
func TestPropertyRenderedChecksReadBackAsWritten(t *testing.T) {
	props(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 4))
		var checks, want []domain.Check
		for range n {
			c := domain.Check{Name: drawText(ht, 0), Cmd: drawText(ht, 1)}
			c.Timeout = hegel.Draw(ht, hegel.SampledFrom([]string{"", "10s", "5m", "30m"}))
			if b := hegel.Draw(ht, hegel.Integers(0, 2)); b > 0 {
				v := b == 1
				c.Baseline = &v
			}
			checks = append(checks, c)
			if strings.TrimSpace(c.Cmd) == "" {
				continue
			}
			if strings.TrimSpace(c.Name) == "" {
				c.Name = c.Cmd
			}
			want = append(want, c)
		}
		block := RenderChecks(checks)
		got, found, err := ParseChecks(inSpec(block))
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			ht.Fatalf("rendered:\n%s\nread back %#v (found %v, err %v)\nwant %#v", block, got, found, err, want)
		}
	})
}

// TestPropertyASkipTagGluedToAWrittenCommandNeverRuns: a [ci-only] or
// [env: …] tag an agent glues to a command it writes by hand, unquoted,
// is not part of the command read back — it would run as part of it.
// (A quoted command is taken as written; gummi quotes one that holds a
// tag as its own text, and TestPropertyRenderedChecksReadBackAsWritten
// holds that.)
func TestPropertyASkipTagGluedToAWrittenCommandNeverRuns(t *testing.T) {
	props(t, func(ht *hegel.T) {
		cmd := hegel.Draw(ht, hegel.SampledFrom([]string{"go test ./...", "make e2e", "-", "x: y", "#x", "a"}))
		tag := hegel.Draw(ht, hegel.SampledFrom([]string{"[ci-only]", "[CI-only]", "[env: docker]"}))
		sep := hegel.Draw(ht, hegel.SampledFrom([]string{"", " ", "  "}))
		block := "```gummi-checks\n- name: t\n  cmd: " + cmd + sep + tag + "\n```"
		got, _, err := ParseChecks(inSpec(block))
		if err != nil {
			return // a block that does not parse runs nothing
		}
		for _, g := range got {
			if skipTagRe.MatchString(g.Cmd) {
				ht.Fatalf("written:\n%s\nread back command %q", block, g.Cmd)
			}
		}
	})
}

// TestPropertyRenderedFilesReadBackAsWritten: a file manifest gummi
// renders reads back as written, for repo-relative paths each listed
// once and roles with no surrounding space.
func TestPropertyRenderedFilesReadBackAsWritten(t *testing.T) {
	props(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 4))
		var files []domain.PlannedFile
		seen := map[string]bool{}
		for i := range n {
			depth := hegel.Draw(ht, hegel.Integers(1, 3))
			var segs []string
			for range depth {
				seg := hegel.Draw(ht, hegel.SampledFrom([]string{"internal", "a b", "é", "x.go", "-x", "#y", "z:w"}))
				segs = append(segs, seg)
			}
			p := strings.Join(segs, "/") + fmt.Sprint(i)
			if seen[p] {
				continue
			}
			seen[p] = true
			isNew := hegel.Draw(ht, hegel.Booleans())
			files = append(files, domain.PlannedFile{Path: p, Role: strings.TrimSpace(drawText(ht, 0)), New: isNew})
		}
		block := RenderFiles(files)
		got, found, err := ParseFiles(inSpec(block))
		if len(files) == 0 {
			files = nil
		}
		if err != nil || !found || !reflect.DeepEqual(got, files) {
			ht.Fatalf("rendered:\n%s\nread back %#v (found %v, err %v)\nwant %#v", block, got, found, err, files)
		}
	})
}

// TestPropertyDoneWhenItemsSetReadBackAsSet: done-when items written into
// a session spec read back as written, says and check without surrounding
// space.
func TestPropertyDoneWhenItemsSetReadBackAsSet(t *testing.T) {
	props(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 4))
		var items []domain.DoneWhen
		for i := range n {
			d := domain.DoneWhen{ID: fmt.Sprintf("DW-%d", i+1), Says: strings.TrimSpace(drawText(ht, 1))}
			if d.Says == "" {
				d.Says = "it holds"
			}
			if hegel.Draw(ht, hegel.Booleans()) {
				d.Judge = true
			} else if d.Check = strings.TrimSpace(drawText(ht, 1)); d.Check == "" {
				d.Check = "true"
			}
			items = append(items, d)
		}
		doc, err := SetDoneWhen(SessionTemplate("t"), items)
		if err != nil {
			ht.Fatalf("SetDoneWhen: %v", err)
		}
		got, found, err := ParseDoneWhen(doc)
		if err != nil || !found || !reflect.DeepEqual(got, items) {
			ht.Fatalf("doc:\n%s\nread back %#v (found %v, err %v)\nwant %#v", doc, got, found, err, items)
		}
	})
}

// docLines are the lines a commented spec is made of: content, an
// indented continuation, a thread's markers, a blank.
var docLines = []string{"text", "  cont", "%% @a(d): q", "%% @b(d): resolved", "", "more text"}

func drawDoc(ht *hegel.T) []string {
	lines := hegel.Draw(ht, hegel.Lists(hegel.SampledFrom(docLines)).MinSize(1).MaxSize(12))
	for i, l := range lines {
		if l == "text" || l == "more text" {
			lines[i] = fmt.Sprintf("%s %d", l, i) // a content line is its own
		}
	}
	return lines
}

// TestPropertyANewCommentEndsItsThread: a comment added under a content
// line threads there, below every marker already threaded there — so a
// resolution further down the thread cannot close it as it is written.
func TestPropertyANewCommentEndsItsThread(t *testing.T) {
	props(t, func(ht *hegel.T) {
		lines := drawDoc(ht)
		var content []int
		for i, l := range lines {
			if strings.HasPrefix(l, "text") || strings.HasPrefix(l, "more") {
				content = append(content, i+1)
			}
		}
		if len(content) == 0 {
			return
		}
		at := hegel.Draw(ht, hegel.SampledFrom(content))
		next := at
		for next < len(lines) && lines[next] == "" {
			next++
		}
		if next < len(lines) && strings.HasPrefix(lines[next], " ") {
			// a known limit of the %% grammar, not of AddComment: a marker
			// put between a line and the indented line under it (blank
			// lines between or not) makes that line its continuation, and
			// whatever threaded there moves with it. No place to put the
			// comment avoids it.
			return
		}
		doc := strings.Join(lines, "\n")
		out, err := AddComment(doc, at, "user", "now", "the new one")
		if err != nil {
			ht.Fatalf("AddComment: %v", err)
		}
		var added Marker
		for _, m := range Parse(out).Markers {
			if m.Text == "the new one" {
				added = m
			}
		}
		if added.Anchor != at {
			ht.Fatalf("doc:\n%s\nthe comment on line %d threads on %d:\n%s", doc, at, added.Anchor, out)
		}
		for _, m := range Parse(out).Markers {
			if m.Anchor == at && m.Line > added.Line {
				ht.Fatalf("doc:\n%s\nthe comment on line %d sits above %q (line %d):\n%s", doc, at, m.Text, m.Line, out)
			}
		}
	})
}

// TestPropertyFindAnchorAgreesWithParse: a snippet only a marker holds
// finds that marker exactly when Parse gives it a line to thread on.
func TestPropertyFindAnchorAgreesWithParse(t *testing.T) {
	props(t, func(ht *hegel.T) {
		lines := drawDoc(ht)
		var markers []int
		for i, l := range lines {
			if IsMarkerLine(l) {
				markers = append(markers, i)
			}
		}
		if len(markers) == 0 {
			return
		}
		i := hegel.Draw(ht, hegel.SampledFrom(markers))
		lines[i] = "%% @a(d): the one to find"
		doc := strings.Join(lines, "\n")
		var anchor int
		for _, m := range Parse(doc).Markers {
			if m.Line == i+1 {
				anchor = m.Anchor
			}
		}
		line, ok := FindAnchor(doc, "the one to find")
		if ok != (anchor != 0) || (ok && line != i+1) {
			ht.Fatalf("doc:\n%s\nFindAnchor = %d, %v; Parse threads line %d on %d", doc, line, ok, i+1, anchor)
		}
	})
}

// TestPropertyAHeadingInACodeFenceIsNotASection: a `## ` line inside a
// fenced example is the example's text — not a heading, not the end of
// the section it sits in — and replacing a section with its own body
// leaves the document as it was.
func TestPropertyAHeadingInACodeFenceIsNotASection(t *testing.T) {
	props(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 3))
		var b strings.Builder
		var titles []string
		for i := range n {
			title := fmt.Sprintf("Part %d", i+1)
			titles = append(titles, title)
			fmt.Fprintf(&b, "## %s\n\nprose\n", title)
			if hegel.Draw(ht, hegel.Booleans()) {
				b.WriteString("```md\n## Not a section\n```\n")
			}
		}
		// newline-terminated: a body is (normalizeSectionBody, and its
		// round-trip promise is for one)
		doc := b.String()
		if got := Headings(doc); strings.Join(got, "|") != strings.Join(titles, "|") {
			ht.Fatalf("doc:\n%s\nheadings %q, want %q", doc, got, titles)
		}
		for _, title := range titles {
			body, ok := ViewSection(doc, title)
			if !ok {
				ht.Fatalf("no section %q", title)
			}
			again, _, err := ReplaceSection(doc, title, body)
			if err != nil || again != doc {
				ht.Fatalf("doc:\n%s\nreplacing %q with its own body: %v\n%s", doc, title, err, again)
			}
		}
	})
}

// TestPropertyDoneWhenReadsBackWithWindowsLineEndings: a spec saved with
// CRLF line endings reads its done-when items as the same items.
func TestPropertyDoneWhenReadsBackWithWindowsLineEndings(t *testing.T) {
	props(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 3))
		var items []domain.DoneWhen
		for i := range n {
			items = append(items, domain.DoneWhen{ID: fmt.Sprintf("DW-%d", i+1), Says: "it holds", Check: "go test ./..."})
		}
		doc, err := SetDoneWhen(SessionTemplate("t"), items)
		if err != nil {
			ht.Fatalf("SetDoneWhen: %v", err)
		}
		got, found, err := ParseDoneWhen(strings.ReplaceAll(doc, "\n", "\r\n"))
		if err != nil || !found || !reflect.DeepEqual(got, items) {
			ht.Fatalf("read back %#v (found %v, err %v), want %#v", got, found, err, items)
		}
	})
}
