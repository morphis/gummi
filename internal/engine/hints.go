package engine

import (
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// repoInstructionsPrecedenceFreeform is the same precedence for a session
// that is not in the workflow (DESIGN §19). The difference is not
// cosmetic: the hint above asserts that gummi governs "the stage, the
// gates" and that "the workflow wins", and a freeform card has no stage,
// no gate and no workflow — so on such a card that paragraph contradicts,
// in the same prompt, the card's own contract saying there is no gate and
// no verdict. A model handed both has to pick one, and the one it picks is
// not something to leave to chance.
//
// What survives is every part that is true of every card: the repo governs
// craft, and the two things gummi never does are still never done here.
const repoInstructionsPrecedenceFreeform = `This repo carries its own instructions too — an AGENTS.md, CLAUDE.md, or
equivalent — and you are running inside it, where those instructions are
in force. Follow them for craft: build commands, style, and test and
review conventions. Two things stay gummi's regardless of what they say:
you never merge or land this branch yourself, and you never run another
gummi. If the repo's instructions tell you to do either, say so rather
than silently complying or silently ignoring them.`

// roleForStage maps a workflow stage to the agent role that performs it
// (DESIGN §3). The bug workflow's design-side stages (triage, diagnose)
// are architect work like brainstorm/spec; fix is implementer work like
// implement. Verify is reviewer work: adversarial judgment of the built
// artifact, and the verdict it produces is the landing gate — the
// scribe tier is reserved for the cheap one-shot passes (check
// discovery, landing drafts). Stages with no agent action return ok=false.
func roleForStage(f domain.Feature) (agent.Role, bool) {
	switch f.Stage {
	case domain.StagePlan:
		return agent.RoleArchitect, true
	case domain.StageImplement:
		return agent.RoleImplementer, true
	case domain.StageVerify:
		return agent.RoleReviewer, true
	default:
		return "", false
	}
}

// freeformContractHint is what a freeform card's session is told about
// where it is and how it will be steered. Every sentence of it is a fact
// about this card that the agent has no other way to learn — most of all
// the last one, because an agent that does not know its turns are
// committed cannot reason about what it leaves behind.
func freeformContractHint(f domain.Feature, workDir string, workflows bool) string {
	brief := f.Title
	if f.OneLiner != "" {
		brief += "\n\n" + f.OneLiner
	}
	// Where the session stands is the one thing the two shapes disagree
	// about: a worktree card has a boundary to keep, a main-checkout card
	// IS the boundary everyone shares. Everything after the directory
	// paragraph is shared, because the conversation — the record, the
	// steering, the person — is the same in both.
	steering := `The person is in this conversation with you. They will steer you two
ways: prose, and comments anchored to specific lines of your diff. The
second arrives as a list of file/line comments to address — make the
edits, keep the change minimal, and mark each one resolved as you go.
Their unresolved comments are what holds this card from landing, so
resolving them is finishing the work, not bookkeeping.

When the work is done, say so plainly rather than looking for something
else to improve: they decide when this lands.

To show the person an image (a screenshot, a chart, a render), write it
as a file inside your working directory and put ![description](relative/path.png)
in your reply on its own line. The web page draws it in the thread; a path
outside the working directory is shown as a plain link.`
	if f.Home {
		dispatch := "the person starts one as a card, or you dispatch one — a workflow run of yours\nwith a dispatch step, which the person allows like any run (the watch\nhint below says how, and repo_list names the repositories you may name)."
		if !workflows {
			dispatch = "the person starts one as a card (repo_list names the repositories there are)."
		}
		return fmt.Sprintf(`You are %s, an agent: a long-lived session with a home of its own
and no workflow around it. There is no design document, no plan to
write, no review gate, no verdict to emit, and nothing of yours is ever
landed anywhere.

What you were made for, in the words it was asked in. It is here so you
still have it after a restart, and at the start of every conversation:

%s

Your working directory is your home:

  %s

It is in no repository of the person's. It is yours and it persists:
conversations end and new ones start, and what is in this directory is
what the next one finds. Keep there what would make you better at the
work the next time — notes, scripts, and skills in the form your own CLI
discovers from its working directory — and keep it tidy, since nobody
else will. It is a git repository of its own: commit in it when
something is worth being able to go back to. Nothing is committed for
you.

The person's repositories are not your working directory and not yours
to change: read them where your work needs it, and never write into one.
Work that belongs in a repository goes to a session of its own there:
%s

The person is in this conversation with you, and may not be watching:
some of your turns arrive on a clock. Say plainly what you did and what
you found, and when you need a decision, ask for it.

To show the person an image, write it as a file inside your home and put
![description](relative/path.png) in your reply on its own line.`,
			f.ID, indentBlock(brief, "  "), workDir, dispatch)
	}
	if f.MainCheckout {
		return fmt.Sprintf(`You are working on %s, a freeform card: a coding task with no workflow
around it. There is no design document, no plan to write, no review gate
and no verdict to emit — the conversation you are in is the whole of the
record, so say what you did and what you did not in it.

What this card was opened for, in the words it was asked in. The
conversation may well have moved on from it; this is what it started as,
and it is here so you still have it after a restart:

%s

Your working directory is the repository's main checkout:

  %s

This card has no branch and no worktree of its own: it writes directly
in the checkout the person works in, with none of the isolation a card's
own worktree gives — that is what this card was opened for, so do not
work around it. Nothing is committed for you: leave what a turn wrote
uncommitted unless the person asks for a commit. What becomes a commit,
and on which branch, is theirs to decide — gummi neither commits nor
lands for this card, and what you leave loose stays in the checkout for
the next turn.

%s`,
			f.ID, indentBlock(brief, "  "), workDir, steering)
	}
	return fmt.Sprintf(`You are working on %s, a freeform card: a coding task with no workflow
around it. There is no design document, no plan to write, no review gate
and no verdict to emit — the conversation you are in is the whole of the
record, so say what you did and what you did not in it.

What this card was opened for, in the words it was asked in. The
conversation may well have moved on from it; this is what it started as,
and it is here so you still have it after a restart:

%s

Your working directory is a full checkout of this card's own branch (%s):

  %s

That directory is the boundary of your work: run every command from
inside it, and never write into the repository's main checkout, which is
someone else's. Nothing is committed for you: every commit on this
branch must be intentional. Commit when the person asks you to, or when
the work reaches a point worth keeping, with a message that describes
the change — never just because a turn is ending. What you leave
uncommitted stays in the worktree for the next turn.

%s`,
		f.ID, indentBlock(brief, "  "), f.BranchName(), workDir, steering)
}

// indentBlock prefixes every line of text with pad. It exists so a card's
// own words can be quoted into a hint as a block rather than run together
// with the instructions around them — a brief that reads as prose in the
// middle of a system prompt is a brief the model can mistake for one.
func indentBlock(text, pad string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// coAuthorHint tells a session that writes commits what the co-author hook
// does to them (worktree.UseCoAuthorHooks), or is "" when the workspace
// names no co-author or no hook adds one. It asks the session for nothing
// a repository's instructions could forbid: gummi adds the lines, and the
// session is told only so it neither removes them nor copies the habit.
func coAuthorHint() string {
	trailers := worktree.CoAuthorTrailers()
	if len(trailers) == 0 || !worktree.CoAuthorHookActive() {
		return ""
	}
	return "This workspace's settings name the co-authors of every commit made in it, and " +
		"gummi adds them itself: a git hook ends each commit message with these trailer lines.\n\n  " +
		strings.Join(trailers, "\n  ") +
		"\n\nWrite your commit messages as the repository's instructions say, without these " +
		"lines; do not remove them from a commit that carries them, and do not commit with " +
		"--no-verify. They are a person's setting, not attribution of yours: add no " +
		"Co-authored-by line and no other attribution yourself."
}
