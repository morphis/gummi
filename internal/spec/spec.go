// Package spec handles feature design docs: the draft template, and
// the %% marker mini-format (schipper convention) used for open
// questions, annotations, and their resolutions.
//
// Marker grammar, one line each, anywhere in the doc:
//
//	%% free-form question or note
//	%% @author: text
//	%% @author(date): text
//	%% @author: resolved — text        ← resolves its thread
//
// A resolution is a marker whose text is exactly "resolved" or starts
// with "resolved" followed by ":", "—", "–", or "-" (case-insensitive);
// prose that merely begins with the word ("resolved ordering still
// unclear?") does not resolve anything.
//
// Markers attach to the nearest preceding content line (their anchor);
// all markers under one anchor form a thread. A thread is open until
// any marker in it is a resolution.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/morphia/gummi/internal/domain"
)

// Marker is one parsed %% line.
type Marker struct {
	Line     int    // 1-based line number of the marker itself
	Anchor   int    // 1-based line number it annotates; 0 = doc-level
	Author   string // empty when unattributed
	Date     string // empty when absent; verbatim, not parsed
	Text     string
	Resolved bool
}

// Thread is a run of consecutive markers annotating one anchor line.
type Thread struct {
	Anchor   int
	Markers  []Marker
	Resolved bool
}

// Doc is a parsed spec document.
type Doc struct {
	Lines   []string
	Markers []Marker
}

var (
	markerRe   = regexp.MustCompile(`^\s*%%\s*(?:@([\w-]+)(?:\(([^)]*)\))?:\s*)?(.*)$`)
	resolvedRe = regexp.MustCompile(`(?i)^resolved\s*($|[:—–-])`)
)

// IsMarkerLine reports whether a raw line is a %% marker.
func IsMarkerLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "%%")
}

// Parse reads a spec document into lines + markers.
func Parse(content string) Doc {
	lines := strings.Split(content, "\n")
	d := Doc{Lines: lines}
	anchor := 0
	for i, raw := range lines {
		n := i + 1
		if !IsMarkerLine(raw) {
			if strings.TrimSpace(raw) != "" {
				anchor = n
			}
			continue
		}
		m := markerRe.FindStringSubmatch(raw)
		mk := Marker{Line: n, Anchor: anchor}
		if m != nil {
			mk.Author, mk.Date, mk.Text = m[1], m[2], strings.TrimSpace(m[3])
		}
		mk.Resolved = resolvedRe.MatchString(mk.Text)
		d.Markers = append(d.Markers, mk)
	}
	return d
}

// Threads groups markers by anchor line, in document order. All
// markers annotating one anchor are one conversation, even when blank
// lines separate them (hand-edited docs).
func (d Doc) Threads() []Thread {
	var out []Thread
	byAnchor := map[int]int{}
	for _, mk := range d.Markers {
		if i, ok := byAnchor[mk.Anchor]; ok {
			out[i].Markers = append(out[i].Markers, mk)
			out[i].Resolved = out[i].Resolved || mk.Resolved
			continue
		}
		byAnchor[mk.Anchor] = len(out)
		out = append(out, Thread{Anchor: mk.Anchor, Markers: []Marker{mk}, Resolved: mk.Resolved})
	}
	return out
}

// OpenQuestions returns the unresolved threads — the gate checklist.
func (d Doc) OpenQuestions() []Thread {
	var open []Thread
	for _, t := range d.Threads() {
		if !t.Resolved {
			open = append(open, t)
		}
	}
	return open
}

// MarkerLines lists the line numbers of all markers (for n/p jumps).
func (d Doc) MarkerLines() []int {
	out := make([]int, len(d.Markers))
	for i, m := range d.Markers {
		out[i] = m.Line
	}
	return out
}

// AddComment inserts `%% @author(date): text` into content after the
// given 1-based line, below any markers already threaded there, and
// returns the new content. Comment text must be a single line.
func AddComment(content string, line int, author, date, text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if text == "" {
		return "", fmt.Errorf("empty comment")
	}
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (1..%d)", line, len(lines))
	}
	// skip past the existing thread under this line
	insert := line
	for insert < len(lines) && IsMarkerLine(lines[insert]) {
		insert++
	}
	marker := fmt.Sprintf("%%%% @%s(%s): %s", author, date, text)
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insert]...)
	out = append(out, marker)
	out = append(out, lines[insert:]...)
	return strings.Join(out, "\n"), nil
}

// Template renders the initial spec draft for a feature.
func Template(f *domain.Feature) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	b.WriteString(`## Problem

%% @gummi: what hurts today? who feels it?

## Considered approaches

%% @gummi: at least two candidates, with tradeoffs

## Chosen approach

%% @gummi: converge on one during the spec stage

## Implementation notes

## Verification plan

%% @gummi: repo checks always run; what feature-specific live checks prove this works?
`)
	return b.String()
}

// DraftFilename is the draft's name under .gummi/state/drafts/.
func DraftFilename(f *domain.Feature) string {
	return string(f.ID) + "-" + f.Slug + ".md"
}
