package engine

import (
	"context"
	"fmt"
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

// DraftCommitMessage runs a best-effort, read-only scribe pass that
// drafts a squash-merge landing commit message for the feature: the spec,
// the branch's own commits, and its diffstat in, a candidate landing
// message out. The transient session is never tracked on the board.
//
// Best-effort only: any unusable reply, backend error, or the commitDraftTimeout bound
// returns an empty draft and no error, so a merge is never blocked or
// delayed by this pass. The draft is scrubbed for agent attribution
// before it returns; the human approving the merge remains the gate.
func (e *Engine) DraftCommitMessage(ctx context.Context, f domain.Feature) (string, error) {
	model, backend, _ := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return "", nil
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", nil
	}
	feed, err := e.cfg.Worktrees.BranchDraftFeed(ctx, &f)
	if err != nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, commitDraftTimeout)
	defer cancel()
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:      workDir,
		ArtifactPath: specPath,
		Role:         agent.RoleScribe,
		Model:        model,
		Permission:   e.cfg.Permission,
		SystemHints: []string{
			fmt.Sprintf("The feature's spec is at %s; read it first.", specPath),
			"You are composing a commit message read-only; do not modify any files.",
		},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return "", nil
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, commitmsgPrompt(feed)); err != nil {
		return "", nil
	}
	var text assistantText
	drain := func() string {
		draft, ok := parseGummiCommit(text.String())
		if !ok {
			return ""
		}
		// the deterministic pass: guarantee the shape regardless of model
		// compliance, so a diff-shaped or unwrapped draft never reaches
		// main's history on ctrl+s.
		if isDiffDump(draft) {
			return ""
		}
		if subject, body, ok := strings.Cut(draft, "\n"); ok {
			draft = subject + "\n" + wrapBodyLines(body, 72)
		}
		// the sharp edge: a generated message must never carry agent
		// attribution into main's history on ctrl+s.
		if worktree.MatchesAttribution(draft) != "" {
			return ""
		}
		return draft
	}
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return drain(), nil
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				return drain(), nil
			case agent.EventError:
				return "", nil
			}
		case <-ctx.Done():
			return "", nil
		}
	}
}
