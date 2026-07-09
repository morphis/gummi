package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// maxDraftDiffBytes caps how much of the branch diff is fed to the
// scribe: past this a diff stops informing the summary and only crowds
// the model's context.
const maxDraftDiffBytes = 48 * 1024

// draftCommitPrompt is filled with feature ID (twice), title, and diff.
const draftCommitPrompt = `Draft the squash-merge commit message for landing this feature branch on main.
Format: a first line "%s: <imperative summary>" of at most 72 characters, then a
blank line, then a short body (2-6 lines or bullets) saying what changed and why.
Write it as a normal repository commit: describe the change itself, and never
mention gummi, its workflow stages or phases (spec, plan, implement, review,
verify), review rounds, or the spec/bug-report file.
Reply with the commit message text only — no preamble, no code fences.

Feature: %s — %s

Diff of the branch against main:
%s`

// DraftCommitMessage runs a one-shot scribe pass over the feature's
// branch diff and returns a proposed squash-commit message. The
// transient session is not tracked on the board. Unlike Estimate this
// returns errors: drafting is a convenience in front of a user-edited
// merge, and the caller substitutes a hand-written template when it
// fails, so it must know.
func (e *Engine) DraftCommitMessage(ctx context.Context, f domain.Feature) (string, error) {
	if e.cfg.Agent == nil {
		return "", errors.New("no agent configured")
	}
	diff, err := e.cfg.Worktrees.Diff(ctx, &f)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("%s has no changes to describe", f.ID)
	}
	if total := len(diff); total > maxDraftDiffBytes {
		diff = diff[:maxDraftDiffBytes] + fmt.Sprintf("\n[diff truncated — %d bytes total]", total)
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", err
	}
	model, provider := e.resolveRole(f.Profile, agent.RoleScribe)
	sess, err := e.cfg.Agent.NewSession(ctx, agent.SessionOpts{
		WorkDir:     workDir,
		Role:        agent.RoleScribe,
		Model:       model,
		Provider:    provider,
		Permission:  e.cfg.Permission,
		SystemHints: []string{fmt.Sprintf("The feature's spec is at %s.", specPath)},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, fmt.Sprintf(draftCommitPrompt, f.ID, f.ID, f.Title, diff)); err != nil {
		return "", err
	}
	var text assistantText
	finish := func() (string, error) {
		msg := strings.TrimSpace(stripFence(strings.TrimSpace(text.String())))
		if msg == "" {
			return "", errors.New("scribe returned no commit message")
		}
		return msg, nil
	}
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return finish()
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				return finish()
			case agent.EventError:
				return "", ev.Err
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// stripFence unwraps a reply the model fenced anyway (```lang\n...\n```),
// leaving anything else untouched.
func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") {
		return s
	}
	body := strings.TrimSuffix(s, "```")
	i := strings.IndexByte(body, '\n')
	if i < 0 { // one-line ``` blob — not a fenced message
		return s
	}
	return strings.TrimSpace(body[i+1:])
}
