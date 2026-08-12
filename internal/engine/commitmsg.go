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
- a short body of "- " bullets describing what changed and why

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
