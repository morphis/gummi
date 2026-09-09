package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// IssueRef is a GitHub issue named by a person: the owner/repo when the
// reference carried one, and the number. A bare `#N` leaves Owner and
// Repo empty — it is resolved against whichever repository the person
// has chosen, which is the caller's business, not this type's.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
}

// Bare reports whether the reference names no repository of its own.
func (r IssueRef) Bare() bool { return r.Owner == "" || r.Repo == "" }

// OwnerRepo returns "owner/repo", or "" for a bare reference.
func (r IssueRef) OwnerRepo() string {
	if r.Bare() {
		return ""
	}
	return r.Owner + "/" + r.Repo
}

// String renders the reference the short way people write it:
// "owner/repo#N", or "#N" when bare.
func (r IssueRef) String() string {
	if r.Bare() {
		return fmt.Sprintf("#%d", r.Number)
	}
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

var (
	issueURLRe   = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([\w.-]+)/([\w.-]+)/issues/(\d+)/?(?:[#?].*)?$`)
	issueShortRe = regexp.MustCompile(`^([\w.-]+)/([\w.-]+)#(\d+)$`)
	issueBareRe  = regexp.MustCompile(`^#(\d+)$`)
)

// ParseIssueRef recognises one line that is nothing but a GitHub issue
// reference, in the three shapes people paste or type: an issue URL,
// "owner/repo#N", or a bare "#N". Anything else — a sentence that
// happens to start with "#", a URL with trailing words — is not a
// reference, and ok is false. The whole line must be the reference:
// this is what lets a dialog offer an import without ever guessing.
func ParseIssueRef(line string) (ref IssueRef, ok bool) {
	line = strings.TrimSpace(line)
	if m := issueURLRe.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[3])
		return IssueRef{Owner: m[1], Repo: strings.TrimSuffix(m[2], ".git"), Number: n}, n > 0
	}
	if m := issueShortRe.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[3])
		return IssueRef{Owner: m[1], Repo: m[2], Number: n}, n > 0
	}
	if m := issueBareRe.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[1])
		return IssueRef{Number: n}, n > 0
	}
	return IssueRef{}, false
}

// FirstLine returns the first non-blank line of text, trimmed.
func FirstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

var acceptanceHeadingRe = regexp.MustCompile(`(?i)^#{1,3}\s+acceptance(?:\s+criteria)?\s*:?\s*$`)

// SplitAcceptance cuts an `## Acceptance` (or `## Acceptance criteria`)
// section out of a free-form feature description. rest is the text with
// that section removed, acceptance its body. The section runs to the
// next ATX heading or the end of the text. A description without the
// heading comes back untouched with an empty acceptance — the seeded
// Problem stays verbatim, which is the whole reason this is the one
// heading a feature description recognises and not a parser.
func SplitAcceptance(text string) (rest, acceptance string) {
	lines := strings.Split(text, "\n")
	var restLines, accLines []string
	inAcc := false
	for _, l := range lines {
		switch {
		case acceptanceHeadingRe.MatchString(strings.TrimSpace(l)):
			inAcc = true
			continue
		case inAcc && atxHeadingRe.MatchString(l):
			inAcc = false
		}
		if inAcc {
			accLines = append(accLines, l)
		} else {
			restLines = append(restLines, l)
		}
	}
	if len(accLines) == 0 {
		return text, ""
	}
	return strings.TrimRight(strings.Join(restLines, "\n"), "\n"), strings.TrimSpace(strings.Join(accLines, "\n"))
}
