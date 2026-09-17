package spec

import (
	"fmt"
	"regexp"
	"strings"
)

// The promises a plan makes, in the shapes a later stage can be held to.
//
// A gummi plan already writes three kinds of commitment in prose, and
// until now all three were only prose:
//
//   - an INVARIANT — "every filter string valid today keeps parsing
//     exactly as it does today" — usually lifted from the card's own
//     description, and the thing a change must not break;
//   - a GOLDEN — "TestParse_Error[\"name eq c1)\"] = unbalanced
//     parentheses" — a named input with a stated expected result;
//   - an UNPROVEN file — verify's own admission that a changed file was
//     never exercised by any check that ran.
//
// Two full drives against a large repository showed what prose buys: a
// plan pinned a golden, the branch shipped without it, three critique
// rounds and verify passed, and the behaviour the golden described was
// wrong in the shipped code. Separately, a plan's stated invariant was
// contradicted by the diff, the critique labelled the contradiction
// "non-blocking", and the card finished verified. Nothing in the workflow
// ever compared a promise to the branch.
//
// These parsers are that comparison's input. They are deliberately
// forgiving about surrounding prose and strict about the one token that
// makes a line a promise, so an architect writing ordinary markdown
// produces checkable commitments without writing YAML.

// invariantRe matches an invariant line: a bullet or bare line whose
// first word is "invariant" followed by a colon.
var invariantRe = regexp.MustCompile(`(?im)^\s*(?:[-*+]\s+)?(?:\*\*)?invariant(?:\*\*)?\s*:\s*(.+)$`)

// goldenRe matches a golden line the same way, keyed on the word
// "golden". The rest of the line is kept verbatim: what makes a golden
// checkable is the literal it quotes, extracted separately.
var goldenRe = regexp.MustCompile(`(?im)^\s*(?:[-*+]\s+)?(?:\*\*)?golden(?:\*\*)?\s*:?\s*(.+)$`)

// goldenQuotedRe and goldenTickedRe pull the input out of a golden line:
// a double-quoted run first, a backticked run as the fallback. Go-style
// escaped quotes inside a markdown line appear in practice.
var (
	goldenQuotedRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	goldenTickedRe = regexp.MustCompile("`([^`]+)`")
	// goldenNameRe recognises a bare identifier — a golden's NAME, not an
	// input. A plan that labels its goldens (golden `divergence`: …) is
	// naming the case, and holding the branch to the presence of the word
	// "divergence" would be a floor made of nothing.
	goldenNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

// unprovenRe matches verify's admission line for a file no check covered.
var unprovenRe = regexp.MustCompile(`(?im)^\s*(?:[-*+]\s+)?(?:\*\*)?UNPROVEN(?:\*\*)?\s*:\s*([^\s—-]+)\s*(?:[—-]+\s*(.*))?$`)

// invariantVerdictRe matches verify's answer to one invariant: the id, a
// colon, and pass or fail.
var invariantVerdictRe = regexp.MustCompile(`(?im)^\s*(?:[-*+]\s+)?(?:\*\*)?(INV-\d+)(?:\*\*)?\s*:\s*(pass|fail|blocked)\b`)

// Invariant is one thing the change must not break, with the id verify
// answers it by.
type Invariant struct {
	// ID is positional — INV-1 is the first invariant in the artifact —
	// so an architect writes prose and gummi supplies the handle.
	ID string
	// Text is the invariant as written, minus the "invariant:" keyword.
	Text string
}

// Golden is one stated input/expected-result pair.
type Golden struct {
	// Literal is the quoted input the golden names: the string a test
	// must contain for this promise to be pinned. Empty when the golden
	// line quotes nothing, which makes it prose rather than a promise.
	Literal string
	// Text is the whole line, for the message that reports it missing.
	Text string
}

// UnprovenFile is one changed file verify says no check exercised.
type UnprovenFile struct {
	Path   string
	Reason string
}

// claimsHeadingRe matches the `Plan claims` subsection heading at any
// level, and claimsEndRe the next heading of any level after it.
var (
	claimsHeadingRe = regexp.MustCompile(`(?im)^#{2,6}\s*Plan claims\s*$`)
	anyHeadingRe    = regexp.MustCompile(`(?m)^#{1,6}\s`)
)

// claimsBlock returns the body of the artifact's `Plan claims`
// subsection, or "" when it has none.
//
// Promises are read from there and nowhere else, deliberately. The words
// "invariant" and "golden" are ordinary English that appears in a
// critique's own notes, in a Chosen approach's prose, and in the card
// description itself; minting a numbered, gate-blocking promise out of a
// reviewer's aside would make the floor arbitrary and teach everyone to
// avoid the words. The rubric asks for the table; the table is what
// binds.
func claimsBlock(content string) string {
	loc := claimsHeadingRe.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	rest := content[loc[1]:]
	if end := anyHeadingRe.FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return stripMarkerLines(rest)
}

// stripMarkerLines drops %% lines, so a resolved critique thread quoting
// a promise cannot mint a second one.
func stripMarkerLines(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if IsMarkerLine(l) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// Invariants returns the `Plan claims` table's invariant lines in
// document order, numbered INV-1, INV-2, … Duplicate text is kept: a plan
// that states the same invariant twice has made the promise twice, and
// renumbering around it would shift every id after it.
func Invariants(content string) []Invariant {
	var out []Invariant
	for _, m := range invariantRe.FindAllStringSubmatch(claimsBlock(content), -1) {
		text := strings.TrimSpace(m[1])
		if text == "" {
			continue
		}
		out = append(out, Invariant{ID: fmt.Sprintf("INV-%d", len(out)+1), Text: text})
	}
	return out
}

// Goldens returns the `Plan claims` table's golden lines in document
// order. A golden
// whose line quotes no literal is returned with an empty Literal — the
// caller reports those as unpinnable rather than silently dropping a
// promise it could not read.
func Goldens(content string) []Golden {
	var out []Golden
	for _, m := range goldenRe.FindAllStringSubmatch(claimsBlock(content), -1) {
		line := strings.TrimSpace(m[1])
		if line == "" {
			continue
		}
		out = append(out, Golden{Literal: goldenLiteral(line), Text: line})
	}
	return out
}

// goldenLiteral extracts the INPUT a golden names — the string a test
// file has to contain for the promise to be pinned.
//
// Two shapes appear in practice, and the same rule reads both:
//
//	golden `TestParse_Error["name eq c1)"] = "unbalanced parentheses"`
//	golden `emptyGroupError`: `Parse("()", op)` returns a non-nil error …
//
// In the first the backticked run is the whole assertion, which no test
// file contains verbatim — a table entry spells it `"name eq c1)":
// "unbalanced parentheses",`. In the second the backticked run is a
// LABEL. Both times the input is the first double-quoted string on the
// line, so that wins; a backticked run is the fallback for a golden that
// quotes nothing else, and a bare identifier is treated as a label and
// left unpinnable rather than held against the branch.
func goldenLiteral(line string) string {
	lit := ""
	if m := goldenQuotedRe.FindStringSubmatch(line); m != nil {
		lit = m[1]
	} else if m := goldenTickedRe.FindStringSubmatch(line); m != nil {
		lit = m[1]
	}
	// A golden written in Go source style carries escaped quotes; the
	// literal a test file contains is the unescaped one.
	lit = strings.ReplaceAll(lit, `\"`, `"`)
	lit = strings.ReplaceAll(lit, `\\`, `\`)
	lit = strings.TrimSpace(lit)
	if goldenNameRe.MatchString(lit) {
		// A label, not an input. Unpinnable rather than wrong.
		return ""
	}
	return lit
}

// InvariantVerdicts returns the id→verdict map verify wrote, lower-cased.
func InvariantVerdicts(content string) map[string]string {
	out := map[string]string{}
	for _, m := range invariantVerdictRe.FindAllStringSubmatch(content, -1) {
		out[strings.ToUpper(m[1])] = strings.ToLower(m[2])
	}
	return out
}

// UnprovenFiles returns the files verify declared unproven, in document
// order, de-duplicated by path (the last reason wins — a later line is a
// revision of an earlier one).
//
// A stage with nothing to declare answers the instruction rather than
// skipping it: "UNPROVEN: none" was the commonest shape on the lxd
// autopilot drive, and it reached `done`'s unproven_files as a
// one-element list whose element was the word "none" — a caller counting
// the list saw one unproven file on a card that had none. The words that
// mean "nothing here" are not paths and are dropped.
func UnprovenFiles(content string) []UnprovenFile {
	seen := map[string]int{}
	var out []UnprovenFile
	for _, m := range unprovenRe.FindAllStringSubmatch(content, -1) {
		path := strings.Trim(strings.TrimSpace(m[1]), "`\"'")
		if path == "" || isNothingWord(path) {
			continue
		}
		reason := strings.TrimSpace(m[2])
		if i, ok := seen[path]; ok {
			out[i].Reason = reason
			continue
		}
		seen[path] = len(out)
		out = append(out, UnprovenFile{Path: path, Reason: reason})
	}
	return out
}

// isNothingWord reports whether a declared UNPROVEN path is really the
// stage saying there were none. Matched on the WHOLE word, so a real file
// called none.go, or a path with "none" in it, is still a path.
func isNothingWord(path string) bool {
	switch strings.ToLower(strings.Trim(path, ".,;:()[]")) {
	case "none", "n/a", "na", "nil", "nothing", "-", "—":
		return true
	}
	return false
}
