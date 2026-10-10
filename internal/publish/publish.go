// Package publish is the one seam through which gummi pushes a card's
// branch and opens, updates or readies its GitHub pull request (DESIGN §22,
// decision 25). Every face — the TUI, the web page and `gummi push` /
// `gummi pr` — calls it; none decides anything itself.
//
// It has the shape branchlog has: a pure part (Refusal, Floor, the Facts a
// person confirms and their Fingerprint) and an Env that runs the git and gh
// reads and writes. The writes run under the caller's card lock, with
// prompts disabled and no controlling terminal, so a credential that needs a
// person fails as a typed error instead of hanging a face.
//
// A person starts every act. No stage, autopilot step, goal, schedule, MCP
// tool or run/resume imports this package — TestOnlyPersonFacingCodeImportsPublish
// names the packages that must not — and the CLI verbs refuse inside an
// agent's session.
//
// Publishing is detected, never configured: it needs gh signed in and a push
// credential git can already use, and holds none of its own.
package publish

import (
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// Code names a refusal or failure, the same word on every face and the
// CLI's exit mapping.
type Code string

// Unavailable: the machine is not set up to publish (DESIGN §22.2).
const (
	CodeGHMissing       Code = "gh-missing"
	CodeGHNotSignedIn   Code = "gh-not-signed-in"
	CodeUnsupportedHost Code = "unsupported-host"
	CodeNoRemote        Code = "no-remote"
	CodeRemoteAmbiguous Code = "remote-ambiguous"
	CodeNoWriteAccess   Code = "no-write-access"
)

// Refused: the card is not one to publish right now (DESIGN §22.3).
const (
	CodeAgentSession     Code = "agent-session"
	CodeNoBranch         Code = "no-branch"
	CodeStacked          Code = "stacked"
	CodeGoalMember       Code = "goal-member"
	CodeLanded           Code = "landed"
	CodeContinued        Code = "continued"
	CodeBusy             Code = "agent-busy"
	CodeDirty            Code = "dirty"
	CodeRebasing         Code = "rebase-in-flight"
	CodeNothingToPublish Code = "nothing-to-publish"
	CodeForkNotYours     Code = "fork-not-yours"
	CodeOntoBase         Code = "onto-base"
)

// Failed: the act ran into the world (DESIGN §22.4).
const (
	CodeFactsChanged       Code = "facts-changed"
	CodeBranchMoved        Code = "branch-moved"
	CodeRemoteAhead        Code = "remote-ahead"
	CodeNameTaken          Code = "name-taken"
	CodeLeaseStale         Code = "lease-stale"
	CodeProtected          Code = "protected-branch"
	CodeHookRejected       Code = "hook-rejected"
	CodeNeedsInteraction   Code = "credential-needs-interaction"
	CodeAuthFailed         Code = "auth-failed"
	CodePRExists           Code = "pr-exists"
	CodePRMerged           Code = "pr-merged"
	CodePRClosed           Code = "pr-closed"
	CodeNoPR               Code = "no-pr"
	CodeNotVerified        Code = "not-verified"
	CodeUnresolved         Code = "unresolved-annotations"
	CodeHeadElsewhere      Code = "github-head-differs"
	CodeAlreadyReady       Code = "already-ready"
	CodeAlreadyDraft       Code = "already-draft"
	CodeLinkFailed         Code = "link-failed"
	CodeTimeout            Code = "timeout"
	CodeFailed             Code = "failed"
	CodeConfirmationNeeded Code = "confirmation-needed"
)

// Error is a typed refusal or failure: Code for machines, Text for the
// person, Fix for what would change the answer (may be empty).
type Error struct {
	Code Code
	Text string
	Fix  string
	// PR is the open pull request a CodePRExists names, so a face can
	// offer to link it.
	PR int
}

func (e *Error) Error() string {
	if e.Fix != "" {
		return e.Text + " — " + e.Fix
	}
	return e.Text
}

func fail(code Code, text, fix string) *Error { return &Error{Code: code, Text: text, Fix: fix} }

// AsError unwraps err to a publish Error, or wraps a plain one as CodeFailed.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return fail(CodeFailed, err.Error(), "")
}

// Act is one of the publish acts (DESIGN §22.1).
type Act string

const (
	// ActPush pushes the branch: a new branch, a fast-forward, or a
	// lease-pinned force after a rewrite of a branch gummi pushed.
	ActPush Act = "push"
	// ActCreate pushes if needed, opens the PR and links it to the card.
	ActCreate Act = "create"
	// ActUpdate pushes new commits to a linked PR's branch and, given a
	// title or body, edits them.
	ActUpdate Act = "update"
	// ActReady marks a linked draft PR ready for review.
	ActReady Act = "ready"
	// ActDraft turns a linked PR back into a draft.
	ActDraft Act = "draft"
)

// Card is what Refusal reads about a card: its record and the live facts
// about its tree a face already has.
type Card struct {
	F        *domain.Feature
	Busy     bool // an agent session holds it
	Dirty    bool // the worktree has uncommitted changes
	Rebasing bool // a rebase is in flight in its worktree
	Landed   bool // its commits are already on its base
}

// Refusal says why a card may not be published right now, or nil. It is
// pure: the faces ask it before they draw an act and the Env asks it again
// under the card lock before it runs one. It is deliberately not
// branchlog.Refusal, which answers whether history may be rewritten.
func Refusal(c Card) *Error {
	f := c.F
	switch {
	case f == nil:
		return fail(CodeNoBranch, "no card", "")
	case f.Kind == domain.KindResearch:
		return fail(CodeNoBranch, "a research card works in a scratch tree and has no branch to publish", "")
	case f.IsGoal():
		return fail(CodeNoBranch, "a goal lands its own branch through `gummi goal`; publishing a goal branch is not supported yet", "")
	case f.MainCheckout:
		return fail(CodeNoBranch, "this card works in the main checkout and has no branch of its own", "")
	case f.Stage == domain.StageTodo:
		return fail(CodeNoBranch, "this card has not started, so its branch is not cut yet", "")
	case f.StackID != "":
		return fail(CodeStacked, "a stacked card's PR needs the stack's order and a retarget on every replay, which gummi does not do yet (DESIGN §18.5)", "push the stack yourself with the printed --force-with-lease lines")
	case f.InGoal() && !f.GoalDropped():
		return fail(CodeGoalMember, "this card lands on its goal's branch; publishing it would go around the goal", "")
	case c.Landed:
		return fail(CodeLanded, "this card has landed: its commits are already on its base", "")
	case f.ContinuedAs != "":
		return fail(CodeContinued, fmt.Sprintf("this session continued as %s, which publishes the work", f.ContinuedAs), "")
	case c.Busy:
		return fail(CodeBusy, "an agent is working on this card", "wait for its turn to end")
	case c.Rebasing:
		return fail(CodeRebasing, "a rebase is in flight in this card's worktree", "finish or abort it first")
	case c.Dirty:
		return fail(CodeDirty, "this card's worktree has uncommitted changes; only commits are published", "commit them, or let the agent")
	}
	return nil
}

// Floor is the quality floor publishing protects (decision 25): whether the
// branch at tip may be offered as ready for review, which is whether it
// could land here. A workflow card needs its verify to have run on tip; a
// handed-off card meets MayLandAfterAll as well; a freeform card needs no
// unresolved review comments (openComments), its floor being the person's
// read of the diff (§19.1). nil means ready is allowed.
func Floor(f *domain.Feature, tip string, openComments int) *Error {
	if f.HandedOff() {
		// handed off at a verified branch: MayLand's stage test no longer
		// applies (the card is done), the revision it verified still does
		if err := f.MayLandAfterAll(); err != nil {
			return fail(CodeNotVerified, err.Error(), "")
		}
		if !f.IsFreeform() && (f.VerifiedRev == "" || f.VerifiedRev != tip) {
			return fail(CodeNotVerified, "the tip "+domain.ShortRev(tip)+" is not the revision this card was verified at", "")
		}
	} else if err := f.MayLandAt(tip); err != nil {
		// MayLandAt's errors all open with ErrNotVerified's words, which
		// this sentence has already said
		why := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(err.Error(), domain.ErrNotVerified.Error()), ":"))
		text := "the tip " + domain.ShortRev(tip) + " is not verified"
		switch {
		case strings.HasPrefix(why, "("):
			text += " " + why
		case why != "":
			text += ": " + why
		}
		return fail(CodeNotVerified, text, "verify the card again")
	}
	if openComments > 0 {
		return fail(CodeUnresolved, fmt.Sprintf("%d review comment(s) on the diff are unresolved", openComments), "resolve them first")
	}
	return nil
}
