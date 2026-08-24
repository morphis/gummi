package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// commitmsgPrompt builds the scribe prompt for a squash-merge landing
// commit message. The branch's own commits and its diffstat are the only
// inputs beyond the spec (passed as the artifact); everything else is a
// hard contract on the reply's shape.
func commitmsgPrompt(feed *worktree.DraftFeed) string {
	var b strings.Builder
	b.WriteString(`Compose the squash-merge landing commit for a feature about to land on
main. This is the only message that stays in main's history, so it must
carry the work's rationale, not a hurried summary.

Read the feature's spec first — it is the authoritative context. Also
below: the branch's own commit subjects and bodies, and a diffstat
against main.

Read-only: do not modify any files, run nothing, and do not invent a
merge. This branch is verified and awaiting a human landing.

Compose the landing commit message as:
- a Conventional Commits subject: type(scope): summary
- imperative mood, at most 72 characters, no trailing period
- a blank line
- a body of "- " bullets, each stating the change's rationale (the "why"),
  not a mechanical restatement of what the diff does

Every body line, including each "- " bullet, stays at or under 72
characters. If a bullet would run longer, continue it on an indented line
two spaces past the "- " (aligned under the bullet text) and keep that
line at or under 72 too.

Never enumerate the diff or implementation line-by-line: no per-file,
per-line, or per-hunk restatement of what changed. Each bullet must say
why the change is worth making — the reasoning a future reader needs —
not recite the mechanics.

Good:   - guarantee the landing message carries rationale, not a diff recap
Bad:    - edited commitmsg.go to add the wrapBodyLines and isDiffDump helpers

The subject must describe the CHANGE, not the process: no "address review
findings", no stage names, and no feature/bug id prefix (the id is
already on the branch).

Never copy from the input: no attribution trailers (e.g. Co-authored-by),
no "generated with/by" lines, no emoji markers, no model names, no
"VERDICT:" lines, and no "%%" role markers.

Reply with ONLY a fenced block and nothing else:

` + "```gummi-commit" + `
feat(scope): summary

- bullet one
- bullet two
` + "```" + `

## Branch commits
`)
	for _, c := range feed.Commits {
		fmt.Fprintf(&b, "%s\t%s\n", c.Hash, c.Body)
	}
	b.WriteString("\n## Diffstat against main\n")
	b.WriteString(feed.Diffstat)
	return b.String()
}

// parseGummiCommit extracts the landing-message draft from the scribe's
// reply. Only a reply that is entirely one fenced ```gummi-commit block
// parses; a chatty or unfenced reply yields ("", false). The fenced body
// is trimmed of its surrounding blank lines.
func parseGummiCommit(text string) (string, bool) {
	const fence = "```gummi-commit"
	open := strings.Index(text, fence)
	if open < 0 {
		return "", false
	}
	if strings.TrimSpace(text[:open]) != "" {
		return "", false // something meaningful precedes the fence
	}
	rest := text[open+len(fence):]
	close := strings.Index(rest, "```")
	if close < 0 {
		return "", false
	}
	if strings.TrimSpace(rest[close+3:]) != "" {
		return "", false // something meaningful follows the closing fence
	}
	draft := strings.TrimSpace(rest[:close])
	if draft == "" {
		return "", false
	}
	return draft, true
}

// isDiffDump reports whether s looks like a raw diff dump rather than a
// composed message: true if any trimmed line begins with a diff-marker
// prefix (`diff --git`, `+++ `, `--- `, or `@@ `). These never appear in
// a hand-written rationale and reliably signal a model that pasted the
// diff back instead of writing prose.
func isDiffDump(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "@@ ") {
			return true
		}
	}
	return false
}

// conventionalCommitTypes is the standard Conventional Commits type set a
// headless landing-message subject must begin with. It is the closed set
// recognized by the spec's `type(scope): summary` check — a subject that
// leads with anything else is refused before it can reach main's history.
var conventionalCommitTypes = map[string]struct{}{
	"build": {}, "chore": {}, "ci": {}, "docs": {}, "feat": {}, "fix": {},
	"perf": {}, "refactor": {}, "revert": {}, "style": {}, "test": {},
}

// ccSubjectRe matches a Conventional Commits `type(scope): summary` subject
// line: a known type (checked separately), an optional parenthesized scope
// (a single scope name or a comma-separated list of them), a colon+space
// separator, and a non-empty summary.
var ccSubjectRe = regexp.MustCompile(`^[a-z]+(\([a-z0-9-]+(,[a-z0-9-]+)*\))?: .+$`)

// ccSubjectScopeRe matches a subject that leads with a parenthesized scope
// group after the type and pulls out its raw contents. It exists only to
// report a malformed multi-scope subject as such instead of the generic
// type(scope): summary line; the contents are gated by validScopeList so a
// well-formed scope (even one with an empty summary) still falls through
// to the generic error rather than a misleading "malformed" line.
var ccSubjectScopeRe = regexp.MustCompile(`^[a-z]+\(([^)]*)\): `)

// scopeElRe matches a single valid scope name in a comma-separated list.
var scopeElRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// validScopeList reports whether a scope-group's raw contents form a
// non-empty comma-separated list of [a-z0-9-] names. A well-formed list
// passes; a trailing/leading/empty element or an empty scope does not.
func validScopeList(s string) bool {
	if s == "" {
		return false
	}
	for _, el := range strings.Split(s, ",") {
		if !scopeElRe.MatchString(el) {
			return false
		}
	}
	return true
}

// ValidateCommitMessage rejects a commit message that must not reach main's
// history through the headless landing path. It is deliberately stricter
// than the TUI's commit-message dialog: headless has no human at the form to
// catch a malformed message before it lands, so the machine enforces the
// shape. It rejects an empty message, a non-Conventional-Commits subject
// (unknown type, missing scope/summary form), a raw diff dump, and any agent
// attribution — each with a distinct reason. It never rewrites the message:
// the caller owns body formatting (the scribe's 72-column wrap is not run
// here), so a message that passes is landed byte-for-byte.
func ValidateCommitMessage(msg string) error {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return errors.New("commit message is empty")
	}
	first := trimmed
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		first = trimmed[:i]
	}
	if !ccSubjectRe.MatchString(first) {
		if m := ccSubjectScopeRe.FindStringSubmatch(first); m != nil && !validScopeList(m[1]) {
			return fmt.Errorf("commit message scope %q is malformed: scopes are comma-separated [a-z0-9-] names", m[1])
		}
		return fmt.Errorf("commit message subject %q is not Conventional Commits type(scope): summary", first)
	}
	typ, _, _ := strings.Cut(first, "(")
	typ, _, _ = strings.Cut(typ, ":")
	if _, ok := conventionalCommitTypes[strings.TrimSpace(typ)]; !ok {
		return fmt.Errorf("commit message type %q is not a conventional commits type", typ)
	}
	if isDiffDump(msg) {
		return errors.New("commit message looks like a raw diff dump")
	}
	if match := worktree.MatchesAttribution(msg); match != "" {
		return fmt.Errorf("commit message carries agent attribution (%q)", match)
	}
	return nil
}

// wrapBodyLines hard-wraps any body line longer than width at the last
// word boundary at or before width, continuing the remainder on an
// indented line (two spaces, aligned under a "- " bullet). Lines at or
// under width and blank lines pass through unchanged. The subject line is
// excluded by the caller, so the pass never rewrites it.
func wrapBodyLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" || len(line) <= width {
			continue
		}
		var wrapped []string
		for line != "" {
			line = strings.Trim(line, " ")
			if line == "" {
				break
			}
			if len(line) <= width {
				wrapped = append(wrapped, line)
				break
			}
			cut := strings.LastIndex(line[:width], " ")
			if cut < 0 {
				cut = width // no word boundary in the window; hard break
			}
			wrapped = append(wrapped, strings.TrimSpace(line[:cut]))
			line = strings.TrimSpace(line[cut:])
		}
		lines[i] = strings.Join(wrapped, "\n  ")
	}
	return strings.Join(lines, "\n")
}

// commitDraftTimeout bounds the landing-message pass so a wedged backend
// can never hang the merge dialog or delay a landing past its bound.
//
// The bound is sized to the measured latency of the configured scribe: a
// real local opencode model returns a correct draft in ~60s, so 30s was
// too tight (the reply arrived as a single end-of-stream message, so the
// accumulated text was still 0 bytes at the bound and the draft came back
// empty every time). 120s keeps the pass useful without ever blocking the
// merge: the dialog opens before the pass starts, esc cancels the
// in-flight context, and an arriving draft only fills an unmodified
// textarea — so a longer bound can never delay, block, or clobber a merge.
const commitDraftTimeout = 120 * time.Second

// CommitDraftGuardError marks a deliberate draft rejection — a
// correctness guard (diff dump, attribution) firing, not a fault. The
// dialog reads these differently from a backend/session/transport
// failure, because the right response is to regenerate or hand-write the
// message, not to fix the profile.
type CommitDraftGuardError struct{ reason string }

func (e *CommitDraftGuardError) Error() string { return e.reason }

// NewCommitDraftGuardError builds a guard rejection carrying reason, so
// callers outside the engine package can construct (or inspect) one.
func NewCommitDraftGuardError(reason string) *CommitDraftGuardError {
	return &CommitDraftGuardError{reason: reason}
}

// DraftCommitMessage runs a best-effort, read-only scribe pass that
// drafts a squash-merge landing commit message for the feature: the spec,
// the branch's own commits, and its diffstat in, a candidate landing
// message out. The transient session is never tracked on the board.
//
// Best-effort only: a failure never blocks or delays the merge, and the
// draft is scrubbed for agent attribution before it returns; the human
// approving the merge remains the gate. But a failure is never silent:
// every unusable reply, backend error, or the commitDraftTimeout bound
// returns a distinct non-empty reason alongside the empty draft, so the
// caller can tell a broken scribe config from a slow draft or a guard
// rejection instead of seeing a blank box forever. Deliberate rejections
// (diff dump / attribution) are returned as a *CommitDraftGuardError.
func (e *Engine) DraftCommitMessage(ctx context.Context, f domain.Feature) (string, error) {
	rc, backend := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return "", errors.New("no scribe agent is configured for the scribe backend")
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", fmt.Errorf("scribe could not locate the feature worktree: %w", err)
	}
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return "", fmt.Errorf("scribe could not resolve the feature's repository: %w", err)
	}
	feed, err := wt.BranchDraftFeed(ctx, &f)
	if err != nil {
		return "", fmt.Errorf("scribe could not gather the branch draft feed: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, commitDraftTimeout)
	defer cancel()
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:      workDir,
		ArtifactPath: specPath,
		Role:         agent.RoleScribe,
		Model:        rc.Model,
		Provider:     rc.Provider,
		Think:        rc.Think,
		Permission:   e.cfg.Permission,
		SystemHints: []string{
			fmt.Sprintf("The feature's spec is at %s; read it first.", specPath),
			"You are composing a commit message read-only; do not modify any files.",
		},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return "", fmt.Errorf("scribe session could not open: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, commitmsgPrompt(feed)); err != nil {
		return "", fmt.Errorf("scribe failed to start the draft: %w", err)
	}
	var text assistantText
	drain := func() (string, error) {
		draft, ok := parseGummiCommit(text.String())
		if !ok {
			return "", &CommitDraftGuardError{reason: "the scribe's reply was not a single fenced gummi-commit block"}
		}
		// the deterministic pass: guarantee the shape regardless of model
		// compliance, so a diff-shaped or unwrapped draft never reaches
		// main's history on ctrl+s.
		if isDiffDump(draft) {
			return "", &CommitDraftGuardError{reason: "the scribe pasted a diff instead of composing a message"}
		}
		if subject, body, ok := strings.Cut(draft, "\n"); ok {
			draft = subject + "\n" + wrapBodyLines(body, 72)
		}
		// the sharp edge: a generated message must never carry agent
		// attribution into main's history on ctrl+s.
		if worktree.MatchesAttribution(draft) != "" {
			return "", &CommitDraftGuardError{reason: "the draft carried agent attribution and was discarded"}
		}
		return draft, nil
	}
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return drain()
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				return drain()
			case agent.EventError:
				return "", fmt.Errorf("scribe refused or returned nothing: %w", ev.Err)
			}
		case <-ctx.Done():
			return "", errors.New("the scribe draft timed out")
		}
	}
}
