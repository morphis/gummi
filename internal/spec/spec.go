// Package spec handles feature design docs: the draft template, and
// the %% marker mini-format (schipper convention) used for open
// questions, annotations, and their resolutions.
//
// Marker grammar, one line each, anywhere in the doc:
//
//	%% free-form question or note
//	%% @author: text
//	%% @author(date): text
//	%% @author: resolved — text        ← resolves that comment and the ones above it
//
// A resolution is a marker whose text is exactly "resolved" or starts
// with "resolved" followed by ":", "—", "–", or "-" (case-insensitive);
// prose that merely begins with the word ("resolved ordering still
// unclear?") does not resolve anything.
//
// Markers attach to the nearest preceding content line (their anchor);
// all markers under one anchor form a thread. A resolution closes only
// the markers ABOVE it in the run (and itself); a comment below every
// resolution stays open. Resolving one comment never closes the others
// sharing the anchor, and a later question reopens the thread.
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// fileLocks serializes read-modify-write cycles on a single spec artifact.
// The agent-pump annotate path (engine) and the UI comment path (ui) both
// do read → AddComment → write on the same file from different goroutines;
// without a shared lock the two clobber each other's %% markers, and a lost
// comment silently unblocks the approval gate it was meant to hold shut.
var fileLocks sync.Map // cleaned path -> *sync.Mutex

// LockFile acquires the per-path lock for a spec artifact and returns its
// release func. Wrap a full read → modify → write cycle with it:
//
//	defer spec.LockFile(path)()
func LockFile(path string) func() {
	m, _ := fileLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

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
	markerRe = regexp.MustCompile(`^\s*%%\s*(?:@([\w-]+)(?:\(([^)]*)\))?:\s*)?(.*)$`)
	// A resolution is exactly "resolved", or "resolved" followed by a colon
	// or em/en dash, or by a hyphen that is itself a separator (has
	// whitespace or the line end after it). The trailing-hyphen guard keeps
	// prose like "resolved-ish, still broken" from counting as a resolution
	// and wrongly closing an open thread.
	resolvedRe = regexp.MustCompile(`(?i)^resolved\s*(?:$|[:—–]|-(?:\s|$))`)
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
			continue
		}
		byAnchor[mk.Anchor] = len(out)
		out = append(out, Thread{Anchor: mk.Anchor, Markers: []Marker{mk}})
	}
	// A resolution closes only the markers ABOVE it in the run, plus
	// itself; a comment below every resolution stays open. So resolving
	// one comment never closes the others sharing the anchor, and a later
	// question reopens the thread. Scan bottom-up so a resolution
	// propagates upward and a thread is resolved only when every marker
	// in it is.
	for i := range out {
		t := &out[i]
		t.Resolved = true
		closed := false // a resolution seen below resolves everything above
		for j := len(t.Markers) - 1; j >= 0; j-- {
			if closed {
				t.Markers[j].Resolved = true
			}
			if t.Markers[j].Resolved {
				closed = true
			}
			if !t.Markers[j].Resolved {
				t.Resolved = false
			}
		}
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

// UserOpenThreads returns the open threads that carry an unresolved
// human (`@user`) comment — the review feedback that blocks a gate and
// that "request changes" sends to the agent. The template's own `@gummi`
// prompts and unattributed notes are scaffolding and are not included.
// Single source of truth for the gate-blocking check on both the UI and
// the engine (Advance) sides.
func (d Doc) UserOpenThreads() []Thread {
	var out []Thread
	for _, t := range d.OpenQuestions() {
		if UnresolvedUserMarker(t) != nil {
			out = append(out, t)
		}
	}
	return out
}

// UnresolvedUserMarker returns the last unresolved `@user` marker in a
// thread, or nil — the human's actual comment (which may thread under a
// template prompt, so Markers[0] is not necessarily it).
func UnresolvedUserMarker(t Thread) *Marker {
	var found *Marker
	for i := range t.Markers {
		if t.Markers[i].Author == "user" && !t.Markers[i].Resolved {
			found = &t.Markers[i]
		}
	}
	return found
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

// ResolveComment closes the marker at line by appending
// `%% @author(date): resolved` immediately after it, and returns the new
// content. The resolution flows through the normal %% grammar, so the
// gate's open-question count drops to zero. line is a 1-based marker
// line; it errors when out of range or not a marker line. Every other
// thread is left untouched.
//
// The resolution is spliced in after line itself — never after the
// thread's last marker — because a resolution closes only the markers
// above it in the run. That keeps any marker below the cursor open when a
// thread carries more than one (per-marker model); single-marker threads
// are unaffected.
func ResolveComment(content string, line int, author, date string) (string, error) {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (1..%d)", line, len(lines))
	}
	if !IsMarkerLine(lines[line-1]) {
		return "", fmt.Errorf("line %d is not a marker", line)
	}
	res := fmt.Sprintf("%%%% @%s(%s): resolved", author, date)
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:line]...)
	out = append(out, res)
	out = append(out, lines[line:]...)
	return strings.Join(out, "\n"), nil
}

// section prompts are the %% guidance a blank draft carries in each
// section; a seeded draft replaces the prompt with extracted content
// where ingestion supplied it (DESIGN §11.2).
const (
	promptProblem      = "%% @gummi: what hurts today? who feels it?"
	promptOutOfScope   = "%% @gummi: what this feature deliberately won't do — scope boundaries prevent gold-plating"
	promptConsidered   = "%% @gummi: at least two candidates, with tradeoffs"
	promptChosen       = "%% @gummi: converge on one during the spec stage"
	promptProgress     = "%% @gummi: implement checkpoints here — what's done, what's left, where to resume"
	promptReview       = "%% @gummi: reviewer findings land here; the implementer resolves each one"
	promptVerification = "%% @gummi: the repo's build/test/lint commands land here as a gummi-checks block at approval (auto-discovered and baselined); add the feature-specific live checks that prove this works — tag steps that can't run in the local worktree with [CI-only] or [env: <prereq>]"
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

	section(&b, "Out of scope", "", promptOutOfScope)
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

// Bug report prompts: the %% guidance a blank bug report carries. The
// root-cause and fix sections stay open until the diagnose/fix stages
// fill them — a source seeds symptoms, not the why or the how.
const (
	promptBugSummary   = "%% @gummi: what's broken, and who hits it?"
	promptBugRepro     = "%% @gummi: exact steps to reproduce — a minimal repro if you can"
	promptBugExpAct    = "%% @gummi: what should happen, vs what actually happens?"
	promptBugEnv       = "%% @gummi: versions, OS, config where it reproduces"
	promptBugRootCause = "%% @gummi: the diagnose stage records the root cause here — the why"
	promptBugFix       = "%% @gummi: the fix stage summarizes the change here"
	promptBugReview    = "%% @gummi: reviewer findings land here; the fix addresses each one"
	// The verify contract for a bug: the deterministic quality floor plus
	// the bug-specific proof (repro gone + regression test).
	promptBugVerify = "%% @gummi: the discovered gummi-checks commands always run. The reproduction " +
		"above must no longer reproduce, and a regression test must lock the fix in."
)

// BugTemplate renders the initial (blank) bug report for a bug.
func BugTemplate(f *domain.Feature) string {
	return renderBug(f, domain.BugReport{}, domain.BugProvenance{}, "")
}

// SeededBugTemplate renders a bug report pre-populated from a source
// (GitHub issue, manual entry): the symptoms fill Summary, Reproduction,
// Expected vs actual, and Environment; open questions become %% markers
// under Summary; the provenance header records the source and severity.
// Root cause and Fix stay open %% prompts — diagnose/fix work.
func SeededBugTemplate(f *domain.Feature, r domain.BugReport, prov domain.BugProvenance, sev domain.Severity) string {
	return renderBug(f, r, prov, sev)
}

func renderBug(f *domain.Feature, r domain.BugReport, prov domain.BugProvenance, sev domain.Severity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	renderBugProvenance(&b, prov, sev)

	// Summary, plus any open questions the source or triage flagged. Each
	// question is its own thread (a content line + %% flag), matching the
	// spec template so resolving one never closes the others.
	section(&b, "Summary", neutralizeMarkers(r.Description), promptBugSummary)
	emitted := 0
	for _, q := range r.OpenQuestions {
		if q = oneLine(q); q != "" {
			fmt.Fprintf(&b, "- %s\n%%%% @gummi: open question from triage\n", q)
			emitted++
		}
	}
	if emitted > 0 {
		b.WriteString("\n")
	}

	section(&b, "Reproduction", neutralizeMarkers(r.Reproduction), promptBugRepro)
	section(&b, "Expected vs actual", expectedVsActual(r), promptBugExpAct)
	section(&b, "Environment", neutralizeMarkers(r.Environment), promptBugEnv)
	if r.Discussion != "" {
		section(&b, "Discussion", neutralizeMarkers(r.Discussion), "")
	}
	section(&b, "Root cause", "", promptBugRootCause)
	section(&b, "Fix", "", promptBugFix)
	section(&b, "Review", "", promptBugReview)
	section(&b, "Verification", "", promptBugVerify)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// expectedVsActual renders the seeded expected/actual pair as a labelled
// block; empty when the source supplied neither (the section then keeps
// its %% prompt).
func expectedVsActual(r domain.BugReport) string {
	exp, act := strings.TrimSpace(r.Expected), strings.TrimSpace(r.Actual)
	if exp == "" && act == "" {
		return ""
	}
	var b strings.Builder
	if exp != "" {
		fmt.Fprintf(&b, "**Expected:** %s\n\n", neutralizeMarkers(exp))
	}
	if act != "" {
		fmt.Fprintf(&b, "**Actual:** %s", neutralizeMarkers(act))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderBugProvenance writes the "Reported via …" header for a bug: the
// source, its external reference, and the severity. Nothing is written
// when there is neither provenance nor severity.
func renderBugProvenance(b *strings.Builder, p domain.BugProvenance, sev domain.Severity) {
	if p.Empty() && sev == "" {
		return
	}
	if !p.Empty() {
		b.WriteString("> _Reported")
		if p.Source != "" {
			fmt.Fprintf(b, " via %s", p.Source)
		}
		if p.ExternalRef != "" {
			fmt.Fprintf(b, " · %s", p.ExternalRef)
		}
		b.WriteString("_\n")
	}
	if sev != "" {
		if p.Empty() {
			fmt.Fprintf(b, "> Severity: %s\n", sev)
		} else {
			fmt.Fprintf(b, ">\n> Severity: %s\n", sev)
		}
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
	return atomicfile.Write(path, []byte(blankTemplate(f)), 0o600)
}

// blankTemplate is the initial artifact for a work item: a bug report for
// bugs, a research document for research cards, a spec draft for features.
func blankTemplate(f *domain.Feature) string {
	if f.Kind == domain.KindBug {
		return BugTemplate(f)
	}
	if f.Kind == domain.KindResearch {
		return ResearchTemplate(f)
	}
	return Template(f)
}

// DraftFilename is the draft's name under .gummi/state/drafts/.
func DraftFilename(f *domain.Feature) string {
	return string(f.ID) + "-" + f.Slug + ".md"
}

// LocateArtifact returns the first candidate path that exists on disk, or
// "" when none does. Callers pass the artifact's possible homes in
// precedence order (workspace home, then draft, then a mid-flight worktree
// copy); it is the single source of truth for "where does this item's
// design artifact live right now?" — shared by the engine (Advance's gate
// checks) and the read-only CLI commands, so both resolve identically.
func LocateArtifact(candidates ...string) string {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
