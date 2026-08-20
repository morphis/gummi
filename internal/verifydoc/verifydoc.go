// Package verifydoc is the zero-token, deterministic Verify floor for a
// research document (FD-076): no open user `%%` threads, every inline
// `path:line` / `path:start-end` citation in Findings resolves against the
// managed repo checkout, and the brief's Questions reconcile against the
// Slices/Out of scope contract. No agent, model, or API is ever invoked —
// Check is a pure function over the artifact text and a caller-supplied
// view of the repo's files.
//
// Citation format: a backtick-quoted `path:line` or `path:start-end` token
// inside the Findings section, optionally followed (after only whitespace)
// by a fenced code block quoting the verbatim content the citation claims
// sits at that location:
//
//	The retry loop lives at `internal/foo.go:42`
//	```go
//	return retryLoop()
//	```
//
// Coverage contract: every bullet under `## Questions` must be answered —
// its trimmed text must appear verbatim either in some slice's
// `requirements` list (`## Slices`, fenced `yaml`) or as the key of an
// explicit `- key: prose` line under `## Out of scope`.
package verifydoc

import (
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/spec"
)

// Report is the structured verdict Check returns: one entry per broken
// citation and per unmapped coverage item, plus the open-thread count. It
// is read-only output — never written back into the artifact.
type Report struct {
	OpenThreads int
	Citations   []CitationIssue
	Coverage    []CoverageIssue
}

// Pass reports whether the document clears all three checks.
func (r Report) Pass() bool {
	return r.OpenThreads == 0 && len(r.Citations) == 0 && len(r.Coverage) == 0
}

// CitationIssue names one citation that failed to resolve and why.
type CitationIssue struct {
	Citation string // the raw "path:line" / "path:start-end" text
	Reason   string
}

// CoverageIssue names one brief question with neither a slice nor an
// out-of-scope line answering it.
type CoverageIssue struct {
	Item   string // the unmapped Questions bullet, verbatim
	Reason string
}

// Check runs the three deterministic checks against artifact and returns
// the aggregated report. files is the caller-resolved view of the managed
// repo: cited path -> its current lines (0-indexed slice, 1-based line
// numbers in citations). Checks run in order — open threads, citations,
// coverage — and every issue found is aggregated into one report.
func Check(artifact string, files map[string][]string) Report {
	doc := spec.Parse(artifact)
	return Report{
		OpenThreads: len(doc.UserOpenThreads()),
		Citations:   findCitations(artifact, files),
		Coverage:    findCoverage(artifact),
	}
}

// citationRe matches a backtick-quoted `path:line` or `path:start-end`
// token. Paths never contain a backtick, whitespace, or colon.
var citationRe = regexp.MustCompile("`([^`\\s:]+):(\\d+)(?:-(\\d+))?`")

// findCitations scans the artifact's Findings section for citation tokens,
// in document order, and resolves each against files.
func findCitations(artifact string, files map[string][]string) []CitationIssue {
	body, ok := spec.ViewSection(artifact, "Findings")
	if !ok {
		return nil
	}
	var issues []CitationIssue
	for _, m := range citationRe.FindAllStringSubmatchIndex(body, -1) {
		raw := body[m[0]:m[1]]
		path := body[m[2]:m[3]]
		start, _ := strconv.Atoi(body[m[4]:m[5]])
		end := start
		if m[6] >= 0 {
			end, _ = strconv.Atoi(body[m[6]:m[7]])
		}
		snippet, hasSnippet := findSnippet(body, m[1])
		if issue := resolveCitation(raw, path, start, end, snippet, hasSnippet, files); issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

// CitedPaths returns the distinct, document-order paths named by every
// citation token in the artifact's Findings section, whether or not the
// citation ultimately resolves. Callers use this to build the files map
// Check needs — reading only the paths a citation actually names, never
// the whole checkout.
func CitedPaths(artifact string) []string {
	body, ok := spec.ViewSection(artifact, "Findings")
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range citationRe.FindAllStringSubmatch(body, -1) {
		path := m[1]
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// findSnippet looks for a fenced code block starting at the first
// non-whitespace position from i onward, returning its content and
// whether one was found. Anything other than whitespace between the
// citation token and the fence means there is no snippet.
func findSnippet(body string, i int) (string, bool) {
	j := i
	for j < len(body) && isSpace(body[j]) {
		j++
	}
	if !strings.HasPrefix(body[j:], "```") {
		return "", false
	}
	j += 3
	nl := strings.IndexByte(body[j:], '\n')
	if nl < 0 {
		return "", false
	}
	bodyStart := j + nl + 1
	closeIdx := strings.Index(body[bodyStart:], "```")
	if closeIdx < 0 {
		return "", false
	}
	return body[bodyStart : bodyStart+closeIdx], true
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// resolveCitation applies the three containment/existence/snippet guards
// in order, returning the first failure. A path escaping the repo root is
// never read — files is trusted to hold only root-contained entries
// (fileMap's job at the engine layer), so containment is checked against
// the raw path text alone.
func resolveCitation(raw, path string, start, end int, snippet string, hasSnippet bool, files map[string][]string) *CitationIssue {
	if !contained(path) {
		return &CitationIssue{Citation: raw, Reason: "path escapes the repo root"}
	}
	lines, ok := files[path]
	if !ok {
		return &CitationIssue{Citation: raw, Reason: "file not found: " + path}
	}
	if start < 1 || end < start || end > len(lines) {
		return &CitationIssue{Citation: raw, Reason: "line out of range"}
	}
	if hasSnippet && !snippetPresent(lines, snippet) {
		return &CitationIssue{Citation: raw, Reason: "snippet no longer matches file content"}
	}
	return nil
}

// contained reports whether path stays under the repo root: no absolute
// form and no ".." path segment.
func contained(path string) bool {
	if strings.HasPrefix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// snippetPresent reports whether snippet's lines still appear as a
// contiguous run anywhere in lines — content-based, not line-number based,
// so a stanza shifted by a minor rebase still resolves while genuinely
// altered content is stale. Trailing whitespace is ignored per line.
func snippetPresent(lines []string, snippet string) bool {
	want := normalizeLines(strings.Split(snippet, "\n"))
	if len(want) == 0 {
		return true
	}
	got := normalizeLines(lines)
	if len(want) > len(got) {
		return false
	}
	for i := 0; i+len(want) <= len(got); i++ {
		if equalLines(got[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func normalizeLines(lines []string) []string {
	// A fenced block's content ends with a trailing newline before the
	// closing ``` — strip the resulting empty final element so it doesn't
	// force an impossible match.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " \t\r")
	}
	return out
}

func equalLines(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sliceEntry is one row of the `## Slices` fenced-YAML scaffold
// (internal/spec/research.go's slicesScaffold).
type sliceEntry struct {
	Title        string   `yaml:"title"`
	OneLiner     string   `yaml:"one-liner"`
	DependsOn    []string `yaml:"depends-on"`
	Requirements []string `yaml:"requirements"`
	ID           string   `yaml:"id"`
}

var yamlFenceRe = regexp.MustCompile("(?s)```ya?ml\\s*\\n(.*?)```")

// findCoverage reconciles every `## Questions` bullet against the union of
// every slice's `requirements` entries and every `## Out of scope` line's
// key, in document order.
func findCoverage(artifact string) []CoverageIssue {
	questions := bullets(artifact, "Questions")
	if len(questions) == 0 {
		return nil
	}
	answered := requirementSet(artifact)
	outOfScope := outOfScopeKeys(artifact)

	var issues []CoverageIssue
	for _, q := range questions {
		if answered[q] || outOfScope[q] {
			continue
		}
		issues = append(issues, CoverageIssue{Item: q, Reason: "no slice or out-of-scope line answers it"})
	}
	return issues
}

// bullets returns the trimmed text of each top-level "- " bullet in the
// named section, in document order.
func bullets(artifact, section string) []string {
	body, ok := spec.ViewSection(artifact, section)
	if !ok {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if s, ok := strings.CutPrefix(line, "- "); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// requirementSet unions every slice's requirements entries from the
// `## Slices` fenced YAML block.
func requirementSet(artifact string) map[string]bool {
	body, ok := spec.ViewSection(artifact, "Slices")
	out := map[string]bool{}
	if !ok {
		return out
	}
	m := yamlFenceRe.FindStringSubmatch(body)
	if m == nil {
		return out
	}
	var entries []sliceEntry
	if err := yaml.Unmarshal([]byte(m[1]), &entries); err != nil {
		return out
	}
	for _, e := range entries {
		for _, r := range e.Requirements {
			if r = strings.TrimSpace(r); r != "" {
				out[r] = true
			}
		}
	}
	return out
}

// outOfScopeKeys reads `## Out of scope` lines shaped `- key: prose` and
// returns the set of keys.
func outOfScopeKeys(artifact string) map[string]bool {
	body, ok := spec.ViewSection(artifact, "Out of scope")
	out := map[string]bool{}
	if !ok {
		return out
	}
	for _, line := range strings.Split(body, "\n") {
		s, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		key, _, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		if key = strings.TrimSpace(key); key != "" {
			out[key] = true
		}
	}
	return out
}
