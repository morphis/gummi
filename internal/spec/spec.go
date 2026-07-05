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
	"os"
	"path/filepath"
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

// FindAnchor locates the content line that uniquely contains snippet
// (trimmed, case-sensitive) and returns its 1-based line number. It
// requires exactly one match among non-marker, non-blank lines — zero or
// multiple matches return ok=false, so answer capture fails closed rather
// than annotating the wrong line. A marker line is never an anchor (its
// own anchor is the content above it).
func FindAnchor(content, snippet string) (line int, ok bool) {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return 0, false
	}
	found := 0
	for i, raw := range strings.Split(content, "\n") {
		if IsMarkerLine(raw) || strings.TrimSpace(raw) == "" {
			continue
		}
		if strings.Contains(raw, snippet) {
			found++
			line = i + 1
		}
	}
	if found != 1 {
		return 0, false
	}
	return line, true
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

// section prompts are the %% guidance a blank draft carries in each
// section; a seeded draft replaces the prompt with extracted content
// where ingestion supplied it (DESIGN §11.2).
const (
	promptProblem      = "%% @gummi: what hurts today? who feels it?"
	promptConsidered   = "%% @gummi: at least two candidates, with tradeoffs"
	promptChosen       = "%% @gummi: converge on one during the spec stage"
	promptProgress     = "%% @gummi: implement checkpoints here — what's done, what's left, where to resume"
	promptReview       = "%% @gummi: reviewer findings land here; the implementer resolves each one"
	promptVerification = "%% @gummi: repo checks always run; what feature-specific live checks prove this works?"
)

// Template renders the initial (blank) spec draft for a feature.
func Template(f *domain.Feature) string {
	return renderDraft(f, domain.DraftSeed{}, domain.DraftProvenance{})
}

// SeededTemplate renders a draft pre-populated from an ingested spec
// (DESIGN §11.2): the seed's extracted content fills the Problem,
// Implementation notes, and Verification plan sections; its open
// questions become %% markers under Problem (so they surface in the
// checklist); and the provenance header records the source and
// dependencies. Sections the seed leaves empty keep their %% prompt, and
// the considered/chosen approach sections are never seeded — that's
// brainstorm's work.
func SeededTemplate(f *domain.Feature, seed domain.DraftSeed, prov domain.DraftProvenance) string {
	return renderDraft(f, seed, prov)
}

// section renders "## Title" followed by body, falling back to prompt
// when body is blank. A section with neither (Implementation notes on a
// blank draft) is just its heading.
func section(b *strings.Builder, title, body, prompt string) {
	fmt.Fprintf(b, "## %s\n\n", title)
	if body = strings.TrimSpace(body); body == "" {
		body = prompt
	}
	if body != "" {
		b.WriteString(body + "\n\n")
	}
}

func renderDraft(f *domain.Feature, seed domain.DraftSeed, prov domain.DraftProvenance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	renderProvenance(&b, prov)

	// Problem, plus any ingested open questions. Each question is its own
	// content line (a bullet) with a %% flag threaded under it, so each is
	// an independent thread in the checklist: resolving one never closes
	// the others (adjacent %% lines with no content between would all share
	// one anchor and collapse into a single thread).
	section(&b, "Problem", neutralizeMarkers(seed.Problem), promptProblem)
	emitted := 0
	for _, q := range seed.OpenQuestions {
		if q = oneLine(q); q != "" {
			fmt.Fprintf(&b, "- %s\n%%%% @gummi: open question from the ingested spec\n", q)
			emitted++
		}
	}
	if emitted > 0 {
		b.WriteString("\n")
	}

	section(&b, "Considered approaches", "", promptConsidered)
	section(&b, "Chosen approach", "", promptChosen)
	section(&b, "Implementation notes", neutralizeMarkers(seed.Constraints), "")
	section(&b, "Progress", "", promptProgress)
	section(&b, "Review", "", promptReview)
	section(&b, "Verification plan", neutralizeMarkers(seed.Acceptance), promptVerification)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// oneLine flattens a seed value to a single line (open-question markers
// are one line each), mirroring AddComment's newline handling so ingested
// text can't inject extra content lines into the draft.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// neutralizeMarkers defuses any line of ingested body text that would
// otherwise parse as a %% marker — an injected marker could spawn a
// spurious checklist thread or, worse, silently resolve a real one. The
// %% channel is gummi's, not the source document's.
func neutralizeMarkers(body string) string {
	if !strings.Contains(body, "%%") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if IsMarkerLine(ln) {
			lines[i] = strings.Replace(ln, "%%", "% %", 1)
		}
	}
	return strings.Join(lines, "\n")
}

// renderProvenance writes the "Ingested from …" header for a seeded
// draft: source path, the source refs it was cut from, and its
// dependencies. Nothing is written for a blank (non-ingested) draft.
func renderProvenance(b *strings.Builder, p domain.DraftProvenance) {
	if p.Empty() {
		return
	}
	b.WriteString("> _Ingested")
	if p.Source != "" {
		fmt.Fprintf(b, " from `%s`", p.Source)
	}
	if len(p.Refs) > 0 {
		fmt.Fprintf(b, " · §%s", strings.Join(p.Refs, ", "))
	}
	b.WriteString("_\n")
	if len(p.DependsOn) > 0 {
		fmt.Fprintf(b, ">\n> Depends on: %s\n", strings.Join(p.DependsOn, ", "))
	}
	b.WriteString("\n")
}

// EnsureDraft materializes a feature's draft at path from the template
// if it does not exist yet. Both the engine (before an agent session
// spawns) and the spec view go through here — one creation path, so an
// agent never starts against a missing spec.
func EnsureDraft(path string, f *domain.Feature) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Template(f)), 0o600)
}

// DraftFilename is the draft's name under .gummi/state/drafts/.
func DraftFilename(f *domain.Feature) string {
	return string(f.ID) + "-" + f.Slug + ".md"
}
